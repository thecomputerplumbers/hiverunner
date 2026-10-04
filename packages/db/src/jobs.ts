import { and, desc, eq, isNull } from "drizzle-orm"

import type { AuthDb } from "@workspace/auth/db"
import { FLOOR_RATE, priceJob } from "@workspace/pricing"

import { getBalance } from "@workspace/db/ledger"
import {
  receiptText,
  type ReceiptFacts,
  type ReceiptSigner,
} from "@workspace/db/receipts"
import {
  job,
  ledgerEntry,
  machine,
  machinePool,
  poolSettings,
} from "@workspace/db/schema"

export const jobIdFor = (organizationId: string, runnerRequestId: number) =>
  `${organizationId}:${runnerRequestId}`

/**
 * Whether the pool may take another job. An organization that has spent its
 * balance gets no new jobs acquired: they wait in GitHub's queue, and the
 * dashboard says why. A job already running is never cut off.
 */
export async function canAcquireJobs(db: AuthDb, organizationId: string) {
  return (
    (await getBalance(db, {
      accountType: "organization",
      accountId: organizationId,
    })) > 0
  )
}

export type JobStart = {
  organizationId: string
  runnerRequestId: number
  machineId: string
  label: string
  vcpus: number
  repository: string
  jobName?: string
  workflowRunId?: number
  githubJobId?: number
  /** The quote locked by the coordinator when the machine accepted. */
  speedScore?: number
  /** GitHub's time, not the machine's. */
  startedAt: Date
}

/**
 * Records that GitHub started a job on a machine, and locks its price: the
 * machine's speed score and the rate as they are now. Idempotent.
 */
export async function startJob(db: AuthDb, input: JobStart) {
  if (!Number.isFinite(input.startedAt.getTime()))
    throw new Error("Invalid GitHub job start time")
  if (
    input.speedScore !== undefined &&
    (!Number.isFinite(input.speedScore) || input.speedScore <= 0)
  )
    throw new Error("Invalid assignment speed score")
  // A machine runs jobs only for pools it is authorized for.
  const [runner] = await db
    .select({ speedScore: machine.speedScore })
    .from(machinePool)
    .innerJoin(machine, eq(machine.id, machinePool.machineId))
    .where(
      and(
        eq(machinePool.machineId, input.machineId),
        eq(machinePool.organizationId, input.organizationId),
        // A verified assignment remains billable if its pool is revoked
        // while the runner is finishing. New assignments never use this path.
        input.speedScore === undefined
          ? eq(machinePool.status, "active")
          : undefined
      )
    )
    .limit(1)
  if (!runner) return null
  const id = jobIdFor(input.organizationId, input.runnerRequestId)
  await db
    .insert(job)
    .values({
      id,
      organizationId: input.organizationId,
      machineId: input.machineId,
      label: input.label,
      repository: input.repository,
      jobName: input.jobName ?? "",
      workflowRunId: input.workflowRunId,
      githubJobId: input.githubJobId,
      vcpus: input.vcpus,
      speedScore: input.speedScore ?? runner.speedScore,
      rate: FLOOR_RATE,
      startedAt: input.startedAt,
    })
    .onConflictDoNothing()
  return id
}

export type SettleResult =
  | { settled: true; amount: number; tax: number; earning: number }
  | { settled: false; reason: "unknown-job" | "already-settled" }

/**
 * Prices a finished job, moves the tokens, and signs the receipt.
 *
 * The job row is claimed first with a conditional update, so of two
 * deliveries of the same completion only one prices it. The three ledger
 * entries have ids derived from the job, so even a crash between the claim
 * and the entries cannot double-charge on retry: `settleLedger` is safe to
 * run again and is what a repair job would call.
 *
 * A job is charged whatever its conclusion. A failed test run used the
 * machine as much as a passing one.
 */
export async function settleJob(
  db: AuthDb,
  signer: ReceiptSigner,
  input: { jobId: string; completedAt: Date; result: string }
): Promise<SettleResult> {
  const [row] = await db
    .select()
    .from(job)
    .where(eq(job.id, input.jobId))
    .limit(1)
  if (!row) return { settled: false, reason: "unknown-job" }
  if (row.completedAt) {
    // A previous delivery may have committed the receipt before crashing.
    await settleLedger(db, row.id)
    return { settled: false, reason: "already-settled" }
  }
  if (!Number.isFinite(input.completedAt.getTime()))
    throw new Error("Invalid GitHub job completion time")

  const seconds = Math.max(
    0,
    (input.completedAt.getTime() - row.startedAt.getTime()) / 1000
  )
  const price = priceJob({
    seconds,
    vcpus: row.vcpus,
    speedScore: row.speedScore,
    rate: row.rate,
  })

  const [settings] = await db
    .select({ destination: poolSettings.earningsDestination })
    .from(poolSettings)
    .where(eq(poolSettings.organizationId, row.organizationId))
    .limit(1)
  const [runner] = await db
    .select({ userId: machine.userId })
    .from(machine)
    .where(eq(machine.id, row.machineId))
    .limit(1)
  const payee =
    settings?.destination === "developer" && runner
      ? { payeeType: "user" as const, payeeId: runner.userId }
      : { payeeType: "organization" as const, payeeId: row.organizationId }

  const facts: ReceiptFacts = {
    jobId: row.id,
    organizationId: row.organizationId,
    machineId: row.machineId,
    repository: row.repository,
    label: row.label,
    vcpus: row.vcpus,
    speedScore: row.speedScore,
    startedAt: row.startedAt.toISOString(),
    completedAt: input.completedAt.toISOString(),
    result: input.result,
    computeUnits: price.units,
    rate: price.rate,
    amount: price.amount,
    tax: price.tax,
    earning: price.earning,
    payee: `${payee.payeeType}:${payee.payeeId}`,
  }
  const receipt = receiptText(facts)

  const claimed = await db
    .update(job)
    .set({
      completedAt: input.completedAt,
      result: input.result,
      computeUnits: price.units,
      amount: price.amount,
      tax: price.tax,
      earning: price.earning,
      ...payee,
      receipt,
      receiptSignature: await signer.sign(receipt),
      receiptKeyId: signer.keyId,
    })
    .where(and(eq(job.id, row.id), isNull(job.completedAt)))
    .returning({ id: job.id })
  if (!claimed.length) {
    await settleLedger(db, row.id)
    return { settled: false, reason: "already-settled" }
  }

  await settleLedger(db, row.id)
  return {
    settled: true,
    amount: price.amount,
    tax: price.tax,
    earning: price.earning,
  }
}

/**
 * Writes a settled job's three ledger entries if they are not there yet.
 * They always sum to zero: the organization pays, the payee and the treasury
 * receive, and no token appears or vanishes.
 */
export async function settleLedger(db: AuthDb, jobId: string) {
  const [row] = await db.select().from(job).where(eq(job.id, jobId)).limit(1)
  if (
    !row?.completedAt ||
    row.amount === null ||
    row.tax === null ||
    row.earning === null ||
    !row.payeeType ||
    !row.payeeId
  )
    return false
  const createdAt = row.completedAt
  await db
    .insert(ledgerEntry)
    .values([
      {
        id: `charge:${row.id}`,
        accountType: "organization",
        accountId: row.organizationId,
        kind: "charge",
        amount: -row.amount,
        jobId: row.id,
        createdAt,
      },
      {
        id: `earning:${row.id}`,
        accountType: row.payeeType,
        accountId: row.payeeId,
        kind: "earning",
        amount: row.earning,
        jobId: row.id,
        createdAt,
      },
      {
        id: `tax:${row.id}`,
        accountType: "treasury",
        accountId: "treasury",
        kind: "tax",
        amount: row.tax,
        jobId: row.id,
        createdAt,
      },
    ])
    .onConflictDoNothing()
  return true
}

export async function listJobs(db: AuthDb, organizationId: string, limit = 50) {
  return db
    .select({
      id: job.id,
      repository: job.repository,
      jobName: job.jobName,
      label: job.label,
      machine: machine.name,
      startedAt: job.startedAt,
      completedAt: job.completedAt,
      result: job.result,
      amount: job.amount,
    })
    .from(job)
    .innerJoin(machine, eq(machine.id, job.machineId))
    .where(eq(job.organizationId, organizationId))
    .orderBy(desc(job.startedAt))
    .limit(Math.min(Math.max(limit, 1), 200))
}

export async function getJob(
  db: AuthDb,
  organizationId: string,
  jobId: string
) {
  const [row] = await db
    .select()
    .from(job)
    .where(and(eq(job.id, jobId), eq(job.organizationId, organizationId)))
    .limit(1)
  return row ?? null
}
