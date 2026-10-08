import { and, desc, eq, gte, isNotNull, isNull, sql } from "drizzle-orm"

import type { AuthDb } from "@workspace/auth/db"
import { competitorUsdPerMinuteForLabel } from "@workspace/pricing"

import { getBalance } from "@workspace/db/ledger"
import { listMachines } from "@workspace/db/machines"
import { job, ledgerEntry, machine, poolSettings } from "@workspace/db/schema"

const HOUR = 3_600_000
/** How far back the console's charts look. */
export const LIVE_DAYS = 14
const HOURS = LIVE_DAYS * 24

/**
 * What a job took out of the organization's balance: the charge, less the
 * part its own machines earned straight back. This is the one price the
 * console shows; the receipt has the full split.
 */
const price = sql<number>`${job.amount} - case when ${job.payeeType} = 'organization' then coalesce(${job.earning}, 0) else 0 end`

const jobColumns = {
  id: job.id,
  repository: job.repository,
  jobName: job.jobName,
  label: job.label,
  vcpus: job.vcpus,
  speedScore: job.speedScore,
  rate: job.rate,
  machineId: job.machineId,
  machine: machine.name,
  startedAt: job.startedAt,
  completedAt: job.completedAt,
  result: job.result,
  price: price.as("price"),
}

export type LiveJob = {
  id: string
  repository: string
  jobName: string
  label: string
  vcpus: number
  speedScore: number
  /** Micro-tokens per compute unit, locked when the job was assigned. */
  rate: number
  machineId: string
  machine: string
  /** Milliseconds since the epoch, like every time in a snapshot. */
  startedAt: number
  completedAt: number | null
  result: string | null
  /** Micro-tokens; null while the job is running. */
  price: number | null
}

export type LiveMachine = {
  id: string
  name: string
  model: string
  cores: number
  memoryBytes: number
  speedScore: number
  /** Whether it takes `hive-macos` jobs, run on the Mac itself. */
  macos: boolean
  /** What its daemon said it was running, at its last hello. */
  daemonVersion: string
  presence: string
  lastSeenAt: number | null
  userId: string
  owner: string
  /** Over the chart window. */
  jobs: number
  seconds: number
}

/** One hour of the pool's work, oldest first; `at` is the hour's start. */
export type LiveHour = {
  at: number
  jobs: number
  failed: number
  seconds: number
  /** Micro-tokens that left the balance. */
  price: number
  /** What the same minutes list for on GitHub-hosted runners, in USD. */
  githubUsd: number
}

export type LiveSnapshot = {
  /** The server's clock when this was taken. */
  now: number
  balance: number
  /**
   * Whether what a job pays its machine comes back to this balance, which it
   * does unless the pool pays its developers directly.
   */
  earnsBack: boolean
  /** Every job this pool has finished, since its first. */
  lifetime: {
    jobs: number
    seconds: number
    /** Micro-tokens that left the balance. */
    price: number
    /** What the same minutes list for on GitHub-hosted runners, in USD. */
    githubUsd: number
  }
  machines: LiveMachine[]
  running: LiveJob[]
  recent: LiveJob[]
  hours: LiveHour[]
}

const toJob = (row: {
  startedAt: Date
  completedAt: Date | null
  price: number | null
  [key: string]: unknown
}) =>
  ({
    ...row,
    startedAt: row.startedAt.getTime(),
    completedAt: row.completedAt?.getTime() ?? null,
    price: row.completedAt ? Number(row.price ?? 0) : null,
  }) as LiveJob

export async function listLiveJobs(
  db: AuthDb,
  organizationId: string,
  limit = 200
) {
  const rows = await db
    .select(jobColumns)
    .from(job)
    .innerJoin(machine, eq(machine.id, job.machineId))
    .where(eq(job.organizationId, organizationId))
    .orderBy(desc(job.startedAt))
    .limit(Math.min(Math.max(limit, 1), 500))
  return rows.map(toJob)
}

/**
 * Everything the console draws, in one read. It is polled, so it is a handful of
 * indexed queries and no more; the charts are grouped in SQL, never by
 * fetching the jobs.
 */
export async function getLiveSnapshot(
  db: AuthDb,
  organizationId: string,
  now = new Date()
): Promise<LiveSnapshot> {
  const thisHour = Math.floor(now.getTime() / HOUR)
  const since = new Date((thisHour - HOURS + 1) * HOUR)
  const hour = sql<number>`cast(${job.startedAt} / ${HOUR} as integer)`
  const seconds = sql<number>`coalesce(sum((${job.completedAt} - ${job.startedAt}) / 1000.0), 0)`
  const inWindow = and(
    eq(job.organizationId, organizationId),
    gte(job.startedAt, since)
  )
  const [
    balance,
    machines,
    running,
    recent,
    buckets,
    perMachine,
    settings,
    sizes,
  ] = await Promise.all([
    getBalance(db, {
      accountType: "organization",
      accountId: organizationId,
    }),
    listMachines(db, organizationId),
    db
      .select(jobColumns)
      .from(job)
      .innerJoin(machine, eq(machine.id, job.machineId))
      .where(
        and(eq(job.organizationId, organizationId), isNull(job.completedAt))
      )
      .orderBy(desc(job.startedAt))
      .limit(50),
    db
      .select(jobColumns)
      .from(job)
      .innerJoin(machine, eq(machine.id, job.machineId))
      .where(eq(job.organizationId, organizationId))
      .orderBy(desc(job.startedAt))
      .limit(12),
    db
      .select({
        hour: hour.as("hour"),
        label: job.label,
        jobs: sql<number>`count(*)`.as("jobs"),
        failed: sql<number>`coalesce(sum(${job.result} = 'failed'), 0)`.as(
          "failed"
        ),
        seconds: seconds.as("seconds"),
        price: sql<number>`coalesce(sum(${price}), 0)`.as("total"),
      })
      .from(job)
      .where(inWindow)
      .groupBy(sql`1`, job.label),
    db
      .select({
        machineId: job.machineId,
        jobs: sql<number>`count(*)`.as("jobs"),
        seconds: seconds.as("seconds"),
      })
      .from(job)
      .where(inWindow)
      .groupBy(job.machineId),
    db
      .select({ destination: poolSettings.earningsDestination })
      .from(poolSettings)
      .where(eq(poolSettings.organizationId, organizationId))
      .limit(1),
    db
      .select({
        label: job.label,
        jobs: sql<number>`count(*)`.as("jobs"),
        seconds: seconds.as("seconds"),
        price: sql<number>`coalesce(sum(${price}), 0)`.as("total"),
      })
      .from(job)
      .where(
        and(eq(job.organizationId, organizationId), isNotNull(job.completedAt))
      )
      .groupBy(job.label),
  ])

  const hours: LiveHour[] = Array.from({ length: HOURS }, (_, index) => ({
    at: (thisHour - HOURS + 1 + index) * HOUR,
    jobs: 0,
    failed: 0,
    seconds: 0,
    price: 0,
    githubUsd: 0,
  }))
  // A job's label says its size and its architecture, and GitHub lists a
  // price for each: an x64 minute is not compared at the arm64 rate. A label
  // GitHub has no price for is left out rather than guessed at.
  const github = (label: string) =>
    competitorUsdPerMinuteForLabel("github", label) ?? 0
  for (const bucket of buckets) {
    const slot = hours[Number(bucket.hour) - (thisHour - HOURS + 1)]
    if (!slot) continue
    slot.jobs += Number(bucket.jobs)
    slot.failed += Number(bucket.failed)
    slot.seconds += Number(bucket.seconds)
    slot.price += Number(bucket.price)
    slot.githubUsd += (Number(bucket.seconds) / 60) * github(bucket.label)
  }
  const lifetime = { jobs: 0, seconds: 0, price: 0, githubUsd: 0 }
  for (const size of sizes) {
    lifetime.jobs += Number(size.jobs)
    lifetime.seconds += Number(size.seconds)
    lifetime.price += Number(size.price)
    lifetime.githubUsd += (Number(size.seconds) / 60) * github(size.label)
  }
  const work = new Map(perMachine.map((row) => [row.machineId, row]))
  return {
    now: now.getTime(),
    balance,
    earnsBack: settings[0]?.destination !== "developer",
    lifetime,
    machines: machines.map((row) => ({
      id: row.id,
      name: row.name,
      model: row.model,
      cores: row.cores,
      memoryBytes: row.memoryBytes,
      speedScore: row.speedScore,
      macos: row.macos,
      daemonVersion: row.daemonVersion,
      presence: row.presence,
      lastSeenAt: row.lastSeenAt?.getTime() ?? null,
      userId: row.userId,
      owner: row.owner,
      jobs: Number(work.get(row.id)?.jobs ?? 0),
      seconds: Number(work.get(row.id)?.seconds ?? 0),
    })),
    running: running.map(toJob),
    recent: recent.map(toJob),
    hours,
  }
}

/**
 * The balance at the end of each of the last `days` UTC days, oldest first,
 * walked back from today's balance through the ledger.
 */
export async function getBalanceHistory(
  db: AuthDb,
  organizationId: string,
  days = 30,
  now = new Date()
) {
  const DAY = 24 * HOUR
  const today = Math.floor(now.getTime() / DAY)
  const day = sql<number>`cast(${ledgerEntry.createdAt} / ${DAY} as integer)`
  const account = and(
    eq(ledgerEntry.accountType, "organization"),
    eq(ledgerEntry.accountId, organizationId)
  )
  const [balance, moved] = await Promise.all([
    getBalance(db, { accountType: "organization", accountId: organizationId }),
    db
      .select({
        day: day.as("day"),
        net: sql<number>`sum(${ledgerEntry.amount})`.as("net"),
        added:
          sql<number>`coalesce(sum(case when ${ledgerEntry.kind} in ('grant', 'purchase') then ${ledgerEntry.amount} else 0 end), 0)`.as(
            "added"
          ),
      })
      .from(ledgerEntry)
      .where(
        and(
          account,
          gte(ledgerEntry.createdAt, new Date((today - days + 1) * DAY))
        )
      )
      .groupBy(sql`1`),
  ])
  const byDay = new Map(moved.map((row) => [Number(row.day), row]))
  const history: Array<{ at: number; balance: number; added: number }> = []
  let closing = balance
  for (let index = today; index > today - days; index--) {
    const row = byDay.get(index)
    history.unshift({
      at: index * DAY,
      balance: closing,
      added: Number(row?.added ?? 0),
    })
    closing -= Number(row?.net ?? 0)
  }
  return history
}
