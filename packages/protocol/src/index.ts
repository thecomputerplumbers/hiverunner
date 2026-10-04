/**
 * The tunnel between a daemon and the coordinator: JSON text frames over one
 * outbound WebSocket.
 *
 * The Go daemon has its own copy of these shapes in `daemon/internal/protocol`.
 * `fixtures/*.json` is the contract between the two: both sides' tests must
 * decode every message in it, so a change here that Go cannot read fails Go's
 * tests, and the other way round.
 */

/** Bumped on any change an older peer could not safely ignore. */
export const PROTOCOL_VERSION = 2

/** Sent and answered without waking the coordinator's object. */
export const PING = "ping"
export const PONG = "pong"

export const PRESENCE = [
  "offline",
  "paused",
  "online",
  "busy",
  "draining",
] as const
export type Presence = (typeof PRESENCE)[number]

/** Close codes the daemon acts on. 4000-4999 is the application range. */
export const CLOSE = {
  /** Another connection for the same machine took over. Do not reconnect. */
  REPLACED: 4000,
  /**
   * The machine no longer serves this pool. Do not reconnect to it; ask the
   * server which pools remain.
   */
  REVOKED: 4001,
  /** The daemon is too old to speak to this coordinator. */
  UNSUPPORTED: 4002,
} as const

export type DaemonMessage =
  | {
      type: "hello"
      protocol: number
      daemonVersion: string
      presence: Presence
      /**
       * The job this machine is holding or running, if any. A daemon that
       * reconnects mid-job says so here, so the pool restores `busy` instead
       * of offering a machine that is already at work.
       */
      assignmentId?: string
    }
  | { type: "presence"; presence: Presence }
  /** The machine has reserved its one job slot for this offer. */
  | { type: "job.accepted"; assignmentId: string }
  | { type: "job.rejected"; assignmentId: string; reason: string }
  /** The runner has ended and the VM is gone. `exitCode` is the runner's. */
  | { type: "job.exited"; assignmentId: string; exitCode: number }

/**
 * A job goes to a machine in two steps, because GitHub hands an acquired job
 * to any idle runner in the scale set and an acquired job with no runner
 * hangs. So nothing is acquired, and no credential is minted, until a
 * machine has said yes:
 *
 *   job.assign  ->  job.accepted  ->  (acquire, mint JIT)  ->  job.start
 *
 * `job.release` backs out of an accepted offer at any point after that.
 */
export type CoordinatorMessage =
  | { type: "welcome"; machineId: string; protocol: number }
  | {
      /** An offer. It carries no credential. */
      type: "job.assign"
      assignmentId: string
      /** The `runs-on` label, which is also the scale set. */
      label: string
      vcpus: number
      timeoutMinutes: number
    }
  | {
      type: "job.start"
      assignmentId: string
      /** GitHub's one-time runner configuration, passed to the runner as is. */
      jitConfig: string
      /** The name the JIT config registers; GitHub reports it back. */
      runnerName: string
    }
  /** Free the slot and destroy the VM: the job is not coming, or is over. */
  | { type: "job.release"; assignmentId: string }
  | { type: "drain" }
  | { type: "error"; message: string }

/**
 * How long a machine holds its slot for an accepted offer before giving up
 * on `job.start` and freeing itself. The coordinator must start or release
 * within this.
 */
export const START_TIMEOUT_SECONDS = 30

const MAX_FRAME = 64 * 1024

const isString = (value: unknown, max = 200): value is string =>
  typeof value === "string" && value.length > 0 && value.length <= max

const isPresence = (value: unknown): value is Presence =>
  PRESENCE.includes(value as Presence)

/**
 * A frame from a daemon, or null if it is not one. Daemons run on machines
 * we do not control, so nothing about a frame is assumed.
 */
export function parseDaemonMessage(raw: string): DaemonMessage | null {
  if (raw.length > MAX_FRAME) return null
  let value: Record<string, unknown>
  try {
    value = JSON.parse(raw)
  } catch {
    return null
  }
  if (typeof value !== "object" || value === null) return null
  switch (value.type) {
    case "hello":
      if (
        !Number.isInteger(value.protocol) ||
        !isString(value.daemonVersion, 40) ||
        !isPresence(value.presence) ||
        (value.assignmentId !== undefined && !isString(value.assignmentId))
      )
        return null
      return {
        type: "hello",
        protocol: value.protocol as number,
        daemonVersion: value.daemonVersion,
        presence: value.presence,
        ...(value.assignmentId === undefined
          ? {}
          : { assignmentId: value.assignmentId }),
      }
    case "presence":
      return isPresence(value.presence)
        ? { type: "presence", presence: value.presence }
        : null
    case "job.accepted":
      return isString(value.assignmentId)
        ? { type: "job.accepted", assignmentId: value.assignmentId }
        : null
    case "job.rejected":
      return isString(value.assignmentId) && isString(value.reason, 500)
        ? {
            type: "job.rejected",
            assignmentId: value.assignmentId,
            reason: value.reason,
          }
        : null
    case "job.exited":
      return isString(value.assignmentId) && Number.isInteger(value.exitCode)
        ? {
            type: "job.exited",
            assignmentId: value.assignmentId,
            exitCode: value.exitCode as number,
          }
        : null
    default:
      return null
  }
}
