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
  /** The sizes it has lately refused. See `refusing`. */
  declined?: Declines
}

/**
 * A machine's refusals of jobs, by the vCPUs asked for: how many in a row,
 * and when the last was. The daemon refuses a job it cannot take now, for
 * want of memory most often; a size refused stands for every larger one.
 */
export type Declines = Record<string, { at: number; count: number }>

/**
 * How long a machine is not offered a size again after refusing it: five
 * seconds, twice as long for each refusal in a row, and a minute at most.
 * Offered at once, a machine short of memory refused the same job once a
 * second for as long as it was queued.
 */
export function offerBackoff(count: number) {
  return Math.min(5000 * 2 ** Math.max(count - 1, 0), 60000)
}

/**
 * How long a machine that keeps refusing a size is not counted as free for
 * it when a workflow asks where to run: ten minutes from the last refusal,
 * once it has refused twice in a row. Counted, it took a job it would not
 * run, and the job waited at GitHub with nowhere else to go.
 */
export function routeBackoff(count: number) {
  return count >= 2 ? 10 * 60000 : 0
}

/**
 * Whether a refusal says only that the machine is full right now, which
 * another job ending changes, rather than that this job does not fit it.
 * These are the daemon's own words, `ReasonBusy` and `ReasonMacOSVMs` in
 * `daemon/internal/jobs`; its tests hold them to this.
 */
export function full(reason: string) {
  return / is running as many /.test(reason)
}

/** Whether a machine refused this size, or a smaller one, within its backoff. */
export function refusing(
  candidate: Pick<Candidate, "declined">,
  vcpus: number,
  now: number,
  backoff: (count: number) => number = offerBackoff
) {
  return Object.entries(candidate.declined ?? {}).some(
    ([size, { at, count }]) =>
      Number(size) <= vcpus && now - at < backoff(count)
  )
}

/** The refusals left after a machine refused, or took, a job of this size. */
export function declined(
  declines: Declines | undefined,
  vcpus: number,
  refused: boolean,
  now: number
): Declines {
  const left: Declines = {}
  for (const [size, entry] of Object.entries(declines ?? {}))
    // Taking a job says the machine has room for any no larger.
    if (refused || Number(size) > vcpus) left[size] = entry
  if (refused) left[vcpus] = { at: now, count: (left[vcpus]?.count ?? 0) + 1 }
  return left
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
 *
 * A machine that has just refused a job this large is passed over for a
 * while (`offerBackoff`), so another machine gets it.
 */
export function pickMachine(
  candidates: Candidate[],
  size: JobSize,
  policy: RoutingPolicy = "fastest",
  turns: Record<string, number> = {},
  now = Date.now()
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
    if (
      candidate.presence !== "online" ||
      !room(candidate, size) ||
      refusing(candidate, size.vcpus, now)
    )
      continue
    // Negative when the candidate goes before the best so far.
    const before = best
      ? (order.map((by) => by(candidate, best!)).find(Boolean) ?? 0)
      : -1
    if (before < 0) best = candidate
  }
  return best
}

const load = (candidate: Candidate) => Object.keys(candidate.jobs ?? {}).length

/** GitHub's counts for a scale set, as its messages carry them. */
export type Counts = {
  totalAssignedJobs: number
  totalRunningJobs: number
  totalAvailableJobs?: number
}

/**
 * The demand a listener acts on: GitHub's counts, but never fewer jobs than
 * the listener itself has been told of and not yet seen start or end.
 *
 * GitHub's counts are a snapshot of when a message was made. Jobs queued
 * together arrive in one batch whose counts can name only the first, and
 * nothing new is sent until something else happens, so counts alone left
 * the rest of the batch waiting on a free machine for minutes. A job the
 * listener holds a claim for is assigned to this set; one it was offered
 * and has not claimed is still on offer. Each leaves when GitHub says the
 * job started or completed, or when another set acquires it.
 */
export function demand(
  counts: Counts | undefined,
  known: Record<string, unknown>,
  claims: Record<string, unknown>
) {
  const claimed = Object.keys(claims).length
  const unclaimed = Object.keys(known).filter((key) => !(key in claims)).length
  return {
    assigned: Math.max(counts?.totalAssignedJobs ?? 0, claimed),
    running: counts?.totalRunningJobs ?? 0,
    available: Math.max(counts?.totalAvailableJobs ?? 0, unclaimed),
  }
}
