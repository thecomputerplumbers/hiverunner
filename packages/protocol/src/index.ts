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
export const PROTOCOL_VERSION = 3

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

/**
 * The architecture of a machine, and of the jobs it can run. The same two
 * words as `RunnerArch` in `@workspace/pricing`, said again here because the
 * protocol stands alone: the coordinator passes one where the other is
 * wanted, so the two cannot drift without failing its typecheck.
 */
export const ARCHES = ["arm64", "x64"] as const
export type Arch = (typeof ARCHES)[number]

export type DaemonMessage =
  | {
      type: "hello"
      protocol: number
      daemonVersion: string
      presence: Presence
      /**
       * What the machine is. Absent from daemons that predate a second
       * architecture, every one of which is an arm64 Mac; the coordinator
       * then goes by what the machine enrolled as.
       */
      arch?: Arch
      /**
       * The jobs this machine is holding or running for this pool, if any.
       * A daemon that reconnects mid-job says so here, so the pool counts
       * them again instead of offering cores that are already at work.
       */
      holding?: string[]
      /**
       * The largest job the machine's owner lets it take. Absent means as
       * large as the machine allows.
       */
      maxVcpus?: number
      /**
       * How many jobs the machine's owner lets it run at once. Absent means
       * one.
       */
      maxJobs?: number
    }
  | { type: "presence"; presence: Presence }
  /** The machine has reserved a job slot for this offer. */
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
      /**
       * The architecture the label names. The coordinator only offers a job
       * to a machine of that architecture; this lets the machine refuse one
       * offered by mistake rather than run it on the wrong kind of guest.
       */
      arch?: Arch
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

const isArch = (value: unknown): value is Arch => ARCHES.includes(value as Arch)

/** The most jobs one machine may run at once, whatever its owner asks for. */
export const MAX_JOBS = 8

function isCount(value: unknown, most: number) {
  return (
    Number.isInteger(value) &&
    (value as number) >= 1 &&
    (value as number) <= most
  )
}

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
        (value.arch !== undefined && !isArch(value.arch)) ||
        (value.holding !== undefined &&
          !(
            Array.isArray(value.holding) &&
            value.holding.length <= MAX_JOBS &&
            value.holding.every((id) => isString(id))
          )) ||
        (value.maxVcpus !== undefined && !isCount(value.maxVcpus, 256)) ||
        (value.maxJobs !== undefined && !isCount(value.maxJobs, MAX_JOBS))
      )
        return null
      return {
        type: "hello",
        protocol: value.protocol as number,
        daemonVersion: value.daemonVersion,
        presence: value.presence,
        ...(value.arch === undefined ? {} : { arch: value.arch as Arch }),
        ...(value.holding === undefined
          ? {}
          : { holding: value.holding as string[] }),
        ...(value.maxVcpus === undefined
          ? {}
          : { maxVcpus: value.maxVcpus as number }),
        ...(value.maxJobs === undefined
          ? {}
          : { maxJobs: value.maxJobs as number }),
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
