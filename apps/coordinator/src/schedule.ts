import type { Presence } from "@workspace/protocol"

export type Candidate = {
  machineId: string
  presence: Presence
  cores: number
  /** The owner's limit on job size, from the daemon. Absent means none. */
  maxVcpus?: number
  speedScore: number
  /** Set while a job is assigned and not yet finished. */
  assignmentId?: string
}

/**
 * Whether a machine may be given a job of this size: it must leave cores for
 * its owner, and stay within the limit its owner set.
 */
export function fits(candidate: Candidate, vcpus: number) {
  if (candidate.cores - vcpus < 2) return false
  return candidate.maxVcpus === undefined || vcpus <= candidate.maxVcpus
}

/**
 * The machine a job of this size should go to, or null if none can take it.
 *
 * Among machines that can, the fastest: the job finishes sooner, and the
 * speed score already prices the difference.
 */
export function pickMachine(candidates: Candidate[], vcpus: number) {
  let best: Candidate | null = null
  for (const candidate of candidates) {
    if (candidate.presence !== "online" || candidate.assignmentId) continue
    if (!fits(candidate, vcpus)) continue
    if (!best || candidate.speedScore > best.speedScore) best = candidate
  }
  return best
}
