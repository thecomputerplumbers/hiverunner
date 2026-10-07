import type { Presence } from "@workspace/protocol"

/** Cores every job leaves for the machine's owner. */
export const OWNER_CORES = 2

export type Candidate = {
  machineId: string
  presence: Presence
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
 * Whether a machine may ever be given a job of this size: it must leave
 * cores for its owner, and stay within the limit its owner set.
 */
export function fits(candidate: Candidate, vcpus: number) {
  if (candidate.cores - vcpus < OWNER_CORES) return false
  return candidate.maxVcpus === undefined || vcpus <= candidate.maxVcpus
}

/**
 * How many more jobs of this size a machine has room for right now, beside
 * the ones it already holds. Whether it is willing is its presence, which
 * this does not look at.
 */
export function room(candidate: Candidate, vcpus: number) {
  if (!fits(candidate, vcpus)) return 0
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
export function pickMachine(candidates: Candidate[], vcpus: number) {
  let best: Candidate | null = null
  for (const candidate of candidates) {
    if (candidate.presence !== "online" || !room(candidate, vcpus)) continue
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
