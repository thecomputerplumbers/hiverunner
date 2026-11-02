import { Pool as PostgresPool } from "pg"

/** Thrown by a write after another pod has taken this listener over. */
export class LeaseLost extends Error {
  constructor() {
    super("Listener lease lost")
    this.name = "LeaseLost"
  }
}

/** What one listener reads and writes while it holds its lease. */
export type ListenerStorage = {
  get<T>(key: string): Promise<T | undefined>
  put(key: string, value: unknown): Promise<void>
  delete(key: string): Promise<void>
  setAlarm(at: number): Promise<void>
}

export type ListenerSettings = {
  organizationId: string
  label: string
  vcpus: number
}

/**
 * Every listener's state, and who is running it. Either pod may run any
 * listener: a pod claims one whose alarm is due, holds a lease on it for as
 * long as it runs, and every write is fenced on that lease, so a pod that
 * stalled past its lease cannot overwrite the pod that took over.
 */
export type ListenerStore = {
  /** Due listeners this pod now owns, each until `finish` or its lease ends. */
  claim(pod: string, running: string[], leaseMs: number): Promise<string[]>
  renew(pod: string, running: string[], leaseMs: number): Promise<void>
  finish(name: string, pod: string): Promise<void>
  /** Gives running listeners up at once, for the other pod, on shutdown. */
  release(pod: string, running: string[]): Promise<void>
  /** Wakes a listener, creating it if need be, without moving a due alarm. */
  activate(name: string, settings: ListenerSettings): Promise<void>
  /** Takes everything a retired Durable Object held; it wins over any tick. */
  adopt(name: string, data: Record<string, unknown>): Promise<void>
  read<T>(name: string, key: string): Promise<T | undefined>
  storage(name: string, pod: string): ListenerStorage
  close(): Promise<void>
}

const identityError = () => new Error("Listener identity mismatch")

const SCHEMA = `
  CREATE TABLE IF NOT EXISTS hive_listener (
    name text PRIMARY KEY,
    data jsonb NOT NULL DEFAULT '{}',
    alarm_at timestamptz,
    owner text,
    lease_until timestamptz,
    ticking boolean NOT NULL DEFAULT false
  );
  CREATE INDEX IF NOT EXISTS hive_listener_alarm ON hive_listener (alarm_at)
    WHERE alarm_at IS NOT NULL;
`

export async function postgresStore(url: string): Promise<ListenerStore> {
  const db = new PostgresPool({ connectionString: url, max: 8 })
  // An idle client losing its connection must not take the process down.
  db.on("error", (error) =>
    console.error("Listener store connection", { kind: error.name })
  )
  await db.query(SCHEMA)
  const fenced = async (query: string, values: unknown[]) => {
    if (!(await db.query(query, values)).rowCount) throw new LeaseLost()
  }
  return {
    async claim(pod, running, leaseMs) {
      const { rows } = await db.query<{ name: string }>(
        `UPDATE hive_listener
            SET owner = $1, lease_until = now() + $3 * interval '1 millisecond',
                alarm_at = NULL, ticking = true
          WHERE name IN (
            SELECT name FROM hive_listener
             WHERE name <> ALL($2::text[])
               AND (alarm_at <= now() OR ticking)
               AND (lease_until IS NULL OR lease_until < now())
             FOR UPDATE SKIP LOCKED)
          RETURNING name`,
        [pod, running, leaseMs]
      )
      return rows.map((row) => row.name)
    },
    async renew(pod, running, leaseMs) {
      await db.query(
        `UPDATE hive_listener
            SET lease_until = now() + $3 * interval '1 millisecond'
          WHERE owner = $1 AND name = ANY($2::text[])`,
        [pod, running, leaseMs]
      )
    },
    async finish(name, pod) {
      await db.query(
        `UPDATE hive_listener
            SET owner = NULL, lease_until = NULL, ticking = false
          WHERE name = $1 AND owner = $2`,
        [name, pod]
      )
    },
    async release(pod, running) {
      await db.query(
        `UPDATE hive_listener
            SET owner = NULL, lease_until = NULL, ticking = false,
                alarm_at = now()
          WHERE owner = $1 AND name = ANY($2::text[])`,
        [pod, running]
      )
    },
    async activate(name, settings) {
      const { rows } = await db.query<{ settings: ListenerSettings }>(
        `INSERT INTO hive_listener (name, data, alarm_at)
              VALUES ($1, jsonb_build_object('settings', $2::jsonb),
                      now() + interval '1 second')
         ON CONFLICT (name) DO UPDATE
                 SET alarm_at = CASE
                       WHEN hive_listener.alarm_at IS NULL
                       THEN now() + interval '1 second'
                       ELSE hive_listener.alarm_at END
         RETURNING data->'settings' AS settings`,
        [name, JSON.stringify(settings)]
      )
      const stored = rows[0]?.settings
      if (
        stored?.organizationId !== settings.organizationId ||
        stored?.label !== settings.label
      )
        throw identityError()
    },
    async adopt(name, data) {
      await db.query(
        `INSERT INTO hive_listener (name, data, alarm_at)
              VALUES ($1, $2::jsonb, now())
         ON CONFLICT (name) DO UPDATE
                 SET data = EXCLUDED.data, alarm_at = now(), owner = NULL,
                     lease_until = NULL, ticking = false`,
        [name, JSON.stringify(data)]
      )
    },
    async read<T>(name: string, key: string) {
      const { rows } = await db.query<{ value: T | null }>(
        "SELECT data->$2 AS value FROM hive_listener WHERE name = $1",
        [name, key]
      )
      return rows[0]?.value ?? undefined
    },
    storage(name, pod) {
      return {
        async get<T>(key: string) {
          const { rows } = await db.query<{ value: T | null }>(
            "SELECT data->$2 AS value FROM hive_listener WHERE name = $1",
            [name, key]
          )
          return rows[0]?.value ?? undefined
        },
        put: (key, value) =>
          fenced(
            `UPDATE hive_listener
                SET data = jsonb_set(data, ARRAY[$3], $4::jsonb)
              WHERE name = $1 AND owner = $2`,
            [name, pod, key, JSON.stringify(value)]
          ),
        delete: (key) =>
          fenced(
            `UPDATE hive_listener SET data = data - $3
              WHERE name = $1 AND owner = $2`,
            [name, pod, key]
          ),
        setAlarm: (at) =>
          fenced(
            `UPDATE hive_listener
                SET alarm_at = to_timestamp($3::double precision / 1000)
              WHERE name = $1 AND owner = $2`,
            [name, pod, at]
          ),
      }
    },
    close: () => db.end(),
  }
}

type Row = {
  data: Record<string, unknown>
  alarmAt: number | null
  owner: string | null
  leaseUntil: number
  ticking: boolean
}

/** Local fixtures only: the same rules, gone when the process ends. */
export function memoryStore(): ListenerStore {
  const rows = new Map<string, Row>()
  const owned = (name: string, pod: string) => {
    const row = rows.get(name)
    if (!row || row.owner !== pod) throw new LeaseLost()
    return row
  }
  return {
    async claim(pod, running, leaseMs) {
      const now = Date.now()
      const claimed: string[] = []
      for (const [name, row] of rows) {
        if (running.includes(name)) continue
        const due = row.ticking || (row.alarmAt !== null && row.alarmAt <= now)
        if (!due || row.leaseUntil >= now) continue
        Object.assign(row, {
          owner: pod,
          leaseUntil: now + leaseMs,
          alarmAt: null,
          ticking: true,
        })
        claimed.push(name)
      }
      return claimed
    },
    async renew(pod, running, leaseMs) {
      for (const name of running) {
        const row = rows.get(name)
        if (row?.owner === pod) row.leaseUntil = Date.now() + leaseMs
      }
    },
    async finish(name, pod) {
      const row = rows.get(name)
      if (row?.owner === pod)
        Object.assign(row, { owner: null, leaseUntil: 0, ticking: false })
    },
    async release(pod, running) {
      for (const name of running) {
        const row = rows.get(name)
        if (row?.owner === pod)
          Object.assign(row, {
            owner: null,
            leaseUntil: 0,
            ticking: false,
            alarmAt: Date.now(),
          })
      }
    },
    async activate(name, settings) {
      let row = rows.get(name)
      if (!row) {
        row = {
          data: { settings },
          alarmAt: null,
          owner: null,
          leaseUntil: 0,
          ticking: false,
        }
        rows.set(name, row)
      }
      const stored = row.data.settings as ListenerSettings | undefined
      if (
        stored?.organizationId !== settings.organizationId ||
        stored?.label !== settings.label
      )
        throw identityError()
      row.alarmAt ??= Date.now() + 1000
    },
    async adopt(name, data) {
      rows.set(name, {
        data: structuredClone(data),
        alarmAt: Date.now(),
        owner: null,
        leaseUntil: 0,
        ticking: false,
      })
    },
    async read<T>(name: string, key: string) {
      return rows.get(name)?.data[key] as T | undefined
    },
    storage(name, pod) {
      return {
        async get<T>(key: string) {
          const value = rows.get(name)?.data[key]
          return value === undefined ? undefined : (structuredClone(value) as T)
        },
        async put(key, value) {
          owned(name, pod).data[key] = structuredClone(value)
        },
        async delete(key) {
          delete owned(name, pod).data[key]
        },
        async setAlarm(at) {
          owned(name, pod).alarmAt = at
        },
      }
    },
    async close() {},
  }
}
