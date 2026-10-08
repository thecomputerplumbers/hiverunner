import type { RunnerArch } from "@workspace/pricing"
import type { Presence } from "@workspace/protocol"

/** Cores every job leaves for the machine's owner. */
export const OWNER_CORES = 2

/** What a label asks of a machine: so many cores, of one kind of machine. */
export type JobSize = { vcpus: number; arch: RunnerArch }

export type Candidate = {
  machineId: string
  presence: Presence
  /** Absent means arm64, which is all there was before there were two. */
  arch?: RunnerArch
  /** Whether this Mac takes macOS jobs, run on the Mac itself. */
  macos?: boolean
  cores: number
  /** The owner's limit on job size, from the daemon. Absent means none. */
  maxVcpus?: number
  /** How many jobs the owner lets it run at once. Absent means one. */
  maxJobs?: number
  speedScore: number
  /**
   * The jobs this pool has placed on the machine and not yet seen the end
   * of, by assignment: offered, running, or being torn down.
   */
  jobs?: Record<string, { vcpus: number }>
}

/**
 * Whether a machine is the kind the job's label names. An x64 job needs an
 * x64 machine; a macOS job needs an arm64 Mac whose owner opted in to running
 * jobs on the Mac itself. This is checked here and nowhere else, so
 * everything that asks what a machine can take (an offer, capacity, which
 * listeners to wake) gets the same answer.
 */
export function serves(
  candidate: Pick<Candidate, "arch" | "macos">,
  arch: RunnerArch
) {
  const machine = candidate.arch ?? "arm64"
  if (arch === "macos") return machine === "arm64" && candidate.macos === true
  return machine === arch
}

/**
 * Whether a machine may ever be given a job of this size: it must be of the
 * kind the job's label names, leave cores for its owner, and stay within the
 * limit its owner set.
 */
export function fits(candidate: Candidate, { vcpus, arch }: JobSize) {
  if (!serves(candidate, arch)) return false
  if (candidate.cores - vcpus < OWNER_CORES) return false
  return candidate.maxVcpus === undefined || vcpus <= candidate.maxVcpus
}

/**
 * How many more jobs of this size a machine has room for right now, beside
 * the ones it already holds. Whether it is willing is its presence, which
 * this does not look at.
 */
export function room(candidate: Candidate, size: JobSize) {
  if (!fits(candidate, size)) return 0
  const { vcpus } = size
  const held = Object.values(candidate.jobs ?? {})
  const slots = (candidate.maxJobs ?? 1) - held.length
  const used = held.reduce((sum, job) => sum + job.vcpus, 0)
  const cores = candidate.cores - OWNER_CORES - used
  return Math.max(0, Math.min(slots, Math.floor(cores / vcpus)))
}

/**
 * The machine a job of this size should go to, or null if none can take it.
 *
 * Among machines with room, the fastest: the job finishes sooner, and the
 * speed score already prices the difference. Between equals, the one doing
 * less, so work spreads before it stacks.
 */
export function pickMachine(candidates: Candidate[], size: JobSize) {
  let best: Candidate | null = null
  for (const candidate of candidates) {
    if (candidate.presence !== "online" || !room(candidate, size)) continue
    if (
      !best ||
      candidate.speedScore > best.speedScore ||
      (candidate.speedScore === best.speedScore && load(candidate) < load(best))
    )
      best = candidate
  }
  return best
}

const load = (candidate: Candidate) => Object.keys(candidate.jobs ?? {}).length
