import type { RoutingPolicy } from "@workspace/db/settings"
import { machineServes, OWNER_CORES, type RunnerArch } from "@workspace/pricing"
import type { Presence } from "@workspace/protocol"

export { OWNER_CORES }

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
 * Whether a machine is the kind the job's label names. The rule itself is
 * `machineServes` in `@workspace/pricing`, shared with the web app, so
 * everything that asks what a machine can take (an offer, capacity, which
 * listeners to wake, what an agent is told) gets the same answer.
 */
export function serves(
  candidate: Pick<Candidate, "arch" | "macos">,
  arch: RunnerArch
) {
  return machineServes(candidate, arch)
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
 * Every policy chooses among the same machines, the ones with room; they
 * differ only in which of those goes first.
 *
 * - `fastest`: the fastest. The job finishes sooner, and the speed score
 *   already prices the difference. Between equals, the one doing less, so
 *   work spreads before it stacks.
 * - `spread`: the one doing the least, so an idle machine is used before a
 *   second job lands on a working one. Between equals, the fastest.
 * - `round-robin`: the one that has waited longest since it was last given a
 *   job, by `turns` (larger is more recent), so every machine gets work whatever its speed. One never
 *   given a job goes first; between equals, the fastest.
 */
export function pickMachine(
  candidates: Candidate[],
  size: JobSize,
  policy: RoutingPolicy = "fastest",
  turns: Record<string, number> = {}
) {
  const faster = (a: Candidate, b: Candidate) => b.speedScore - a.speedScore
  const idler = (a: Candidate, b: Candidate) => load(a) - load(b)
  const waited = (a: Candidate, b: Candidate) =>
    (turns[a.machineId] ?? 0) - (turns[b.machineId] ?? 0)
  const order = {
    fastest: [faster, idler],
    spread: [idler, faster],
    "round-robin": [waited, faster],
  }[policy]
  let best: Candidate | null = null
  for (const candidate of candidates) {
    if (candidate.presence !== "online" || !room(candidate, size)) continue
    // Negative when the candidate goes before the best so far.
    const before = best
      ? (order.map((by) => by(candidate, best!)).find(Boolean) ?? 0)
      : -1
    if (before < 0) best = candidate
  }
  return best
}

const load = (candidate: Candidate) => Object.keys(candidate.jobs ?? {}).length
