import { and, desc, eq } from "drizzle-orm"

import type { AuthDb } from "@workspace/auth/db"
import { member, organization, session, user } from "@workspace/auth/schema"
import type { GitHubApp } from "@workspace/github"

import { canAddMachines } from "@workspace/db/membership"
import { githubInstallation, machine, machinePool } from "@workspace/db/schema"

/** Enough for a laptop, a desktop and a few build boxes; not for abuse. */
export const MAX_MACHINES_PER_PERSON = 10

export type MachineFacts = {
  name: string
  model: string
  os: string
  arch: string
  cores: number
  memoryBytes: number
  daemonVersion: string
  /** The daemon's own benchmark. A claim, clamped here, corrected later. */
  speedScore?: number
}

export type Refusal = {
  code: "INVALID_INPUT" | "FORBIDDEN" | "CONFLICT" | "NOT_FOUND"
  message: string
  fields?: Record<string, string>
}

export type EnrollResult =
  | {
      ok: true
      machineId: string
      token: string
      /** Pools the machine was authorized for. */
      joined: string[]
      /** Pools it asked for and was refused, each with the reason. */
      refused: Array<{ organizationId: string; message: string }>
    }
  | ({ ok: false } & Refusal)

type GitHubChecks = Pick<GitHubApp, "isTeamMember" | "getOrgRole" | "getLogin">

async function sha256Hex(value: string) {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(value)
  )
  return [...new Uint8Array(digest)]
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("")
}

function newToken() {
  const bytes = crypto.getRandomValues(new Uint8Array(32))
  let binary = ""
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return `hwm_${btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "")}`
}

const text = (value: unknown, max: number) =>
  typeof value === "string" && value.trim().length > 0 && value.length <= max

export function validateMachineFacts(facts: MachineFacts) {
  const fields: Record<string, string> = {}
  if (!text(facts.name, 80)) fields.name = "Give the machine a name"
  if (!text(facts.model, 120)) fields.model = "Missing hardware model"
  if (!text(facts.daemonVersion, 40))
    fields.daemonVersion = "Missing daemon version"
  // v1 hosts. Widening this is a product decision, not a validation tweak.
  if (facts.os !== "darwin" || facts.arch !== "arm64")
    fields.os = "HiveWorker runs on Apple Silicon Macs for now"
  if (!Number.isInteger(facts.cores) || facts.cores < 1 || facts.cores > 512)
    fields.cores = "Invalid core count"
  if (!Number.isSafeInteger(facts.memoryBytes) || facts.memoryBytes < 2 ** 30)
    fields.memoryBytes = "Invalid memory size"
  if (
    facts.speedScore !== undefined &&
    !(Number.isFinite(facts.speedScore) && facts.speedScore > 0)
  )
    fields.speedScore = "Invalid benchmark result"
  return Object.keys(fields).length ? fields : null
}

/**
 * Whether this person may put a machine in this pool right now, or why not.
 *
 * `github` is null only where no GitHub App is configured (local
 * development), in which case cached membership is all there is to check.
 */
async function authorizePool(
  db: AuthDb,
  github: GitHubChecks | null,
  input: { userId: string; organizationId: string; exceptMachineId?: string }
): Promise<Refusal | null> {
  const [membership] = await db
    .select({ id: member.id })
    .from(member)
    .where(
      and(
        eq(member.organizationId, input.organizationId),
        eq(member.userId, input.userId)
      )
    )
    .limit(1)
  const allowed =
    membership &&
    (github
      ? await canAddMachines(db, github, input)
      : await poolIsActive(db, input.organizationId))
  if (!allowed)
    return {
      code: "FORBIDDEN",
      message:
        "You cannot add machines to this hive. Ask an owner whether it is limited to certain teams.",
    }
  const mine = await db
    .select({ id: machine.id })
    .from(machinePool)
    .innerJoin(machine, eq(machine.id, machinePool.machineId))
    .where(
      and(
        eq(machinePool.organizationId, input.organizationId),
        eq(machinePool.status, "active"),
        eq(machine.userId, input.userId),
        eq(machine.status, "active")
      )
    )
  if (
    mine.filter((row) => row.id !== input.exceptMachineId).length >=
    MAX_MACHINES_PER_PERSON
  )
    return {
      code: "CONFLICT",
      message: `You already have ${MAX_MACHINES_PER_PERSON} machines in this hive. Remove one first.`,
    }
  return null
}

/**
 * Enrolls a machine and authorizes it for each pool asked for that its owner
 * may add machines to. Returns the machine token, once: it is not stored and
 * cannot be shown again, so a lost one means enrolling again.
 *
 * Each pool is decided separately, because each organization sets its own
 * rules. The machine is created if at least one says yes.
 */
export async function enrollMachine(
  db: AuthDb,
  github: GitHubChecks | null,
  input: { userId: string; organizationIds: string[]; facts: MachineFacts },
  now = new Date()
): Promise<EnrollResult> {
  const fields = validateMachineFacts(input.facts)
  if (fields)
    return {
      ok: false,
      code: "INVALID_INPUT",
      message: "This machine cannot be enrolled",
      fields,
    }
  const requested = [...new Set(input.organizationIds)]
  if (!requested.length || requested.length > 25)
    return { ok: false, code: "INVALID_INPUT", message: "Choose a hive" }

  const joined: string[] = []
  const refused: Array<{ organizationId: string; message: string }> = []
  let firstRefusal: Refusal | undefined
  for (const organizationId of requested) {
    const refusal = await authorizePool(db, github, {
      userId: input.userId,
      organizationId,
    })
    if (refusal) {
      firstRefusal ??= refusal
      refused.push({ organizationId, message: refusal.message })
    } else joined.push(organizationId)
  }
  if (!joined.length) return { ok: false, ...firstRefusal! }

  const token = newToken()
  const machineId = crypto.randomUUID()
  await db.insert(machine).values({
    id: machineId,
    userId: input.userId,
    name: input.facts.name.trim(),
    model: input.facts.model.trim(),
    os: input.facts.os,
    arch: input.facts.arch,
    cores: input.facts.cores,
    memoryBytes: input.facts.memoryBytes,
    speedScore: Math.min(Math.max(input.facts.speedScore ?? 1, 0.25), 8),
    daemonVersion: input.facts.daemonVersion.trim(),
    tokenHash: await sha256Hex(token),
    createdAt: now,
  })
  await db.insert(machinePool).values(
    joined.map((organizationId) => ({
      machineId,
      organizationId,
      createdAt: now,
    }))
  )
  return { ok: true, machineId, token, joined, refused }
}

/**
 * Authorizes a machine its owner already enrolled for another pool. The
 * running daemon picks the new pool up on its own; nothing is installed.
 */
export async function addMachineToPool(
  db: AuthDb,
  github: GitHubChecks | null,
  input: { userId: string; machineId: string; organizationId: string },
  now = new Date()
): Promise<Refusal | null> {
  const row = await getMachine(db, input.machineId)
  // The same answer whether it does not exist or is someone else's.
  if (!row || row.userId !== input.userId || row.status !== "active")
    return { code: "NOT_FOUND", message: "No such machine" }
  const refusal = await authorizePool(db, github, {
    userId: input.userId,
    organizationId: input.organizationId,
    exceptMachineId: row.id,
  })
  if (refusal) return refusal
  await db
    .insert(machinePool)
    .values({
      machineId: row.id,
      organizationId: input.organizationId,
      createdAt: now,
    })
    .onConflictDoUpdate({
      target: [machinePool.machineId, machinePool.organizationId],
      set: { status: "active", presence: "offline", revokedAt: null },
    })
  return null
}

async function poolIsActive(db: AuthDb, organizationId: string) {
  const [row] = await db
    .select({ status: githubInstallation.status })
    .from(githubInstallation)
    .where(eq(githubInstallation.organizationId, organizationId))
    .limit(1)
  return row?.status === "active"
}

/** Machines serving one pool, with their state as that pool last saw it. */
export async function listMachines(db: AuthDb, organizationId: string) {
  return db
    .select({
      id: machine.id,
      name: machine.name,
      model: machine.model,
      cores: machine.cores,
      memoryBytes: machine.memoryBytes,
      speedScore: machine.speedScore,
      presence: machinePool.presence,
      lastSeenAt: machinePool.lastSeenAt,
      createdAt: machinePool.createdAt,
      userId: machine.userId,
      owner: user.name,
    })
    .from(machinePool)
    .innerJoin(machine, eq(machine.id, machinePool.machineId))
    .innerJoin(user, eq(user.id, machine.userId))
    .where(
      and(
        eq(machinePool.organizationId, organizationId),
        eq(machinePool.status, "active"),
        eq(machine.status, "active")
      )
    )
    .orderBy(desc(machinePool.createdAt))
    .limit(500)
}

/** A person's own machines, wherever they serve. */
export async function listUserMachines(db: AuthDb, userId: string) {
  return db
    .select({ id: machine.id, name: machine.name, model: machine.model })
    .from(machine)
    .where(and(eq(machine.userId, userId), eq(machine.status, "active")))
    .orderBy(desc(machine.createdAt))
    .limit(100)
}

/**
 * The pools a machine should hold a tunnel to. The daemon asks for this on a
 * timer, which is how it learns it was added to a pool or dropped from one.
 */
export async function listMachinePools(db: AuthDb, machineId: string) {
  return db
    .select({ id: organization.id, name: organization.name })
    .from(machinePool)
    .innerJoin(organization, eq(organization.id, machinePool.organizationId))
    .innerJoin(
      githubInstallation,
      eq(githubInstallation.organizationId, machinePool.organizationId)
    )
    .where(
      and(
        eq(machinePool.machineId, machineId),
        eq(machinePool.status, "active"),
        eq(githubInstallation.status, "active")
      )
    )
}

export async function getMachine(db: AuthDb, machineId: string) {
  const [row] = await db
    .select()
    .from(machine)
    .where(eq(machine.id, machineId))
    .limit(1)
  return row ?? null
}

/** Stops a machine serving one pool. It keeps serving its others. */
export async function removeMachineFromPool(
  db: AuthDb,
  input: { machineId: string; organizationId: string },
  now = new Date()
) {
  const changed = await db
    .update(machinePool)
    .set({ status: "revoked", presence: "offline", revokedAt: now })
    .where(
      and(
        eq(machinePool.machineId, input.machineId),
        eq(machinePool.organizationId, input.organizationId),
        eq(machinePool.status, "active")
      )
    )
    .returning({ machineId: machinePool.machineId })
  return changed.length > 0
}

/** Retires the machine and its token everywhere. */
export async function revokeMachine(
  db: AuthDb,
  machineId: string,
  now = new Date()
) {
  await db
    .update(machine)
    .set({ status: "revoked", revokedAt: now })
    .where(and(eq(machine.id, machineId), eq(machine.status, "active")))
  await db
    .update(machinePool)
    .set({ status: "revoked", presence: "offline", revokedAt: now })
    .where(
      and(
        eq(machinePool.machineId, machineId),
        eq(machinePool.status, "active")
      )
    )
}

/** Who a machine token belongs to, or null unless the machine is active. */
export async function authenticateMachine(db: AuthDb, token: string) {
  if (!token.startsWith("hwm_")) return null
  const [row] = await db
    .select()
    .from(machine)
    .where(eq(machine.tokenHash, await sha256Hex(token)))
    .limit(1)
  return row?.status === "active" ? row : null
}

/**
 * The same, for one pool's tunnel: the machine must also be authorized for
 * that pool, and the pool's installation must be active.
 */
export async function authenticateMachineForPool(
  db: AuthDb,
  token: string,
  organizationId: string
) {
  const found = await authenticateMachine(db, token)
  if (!found) return null
  const [row] = await db
    .select({ installation: githubInstallation.status })
    .from(machinePool)
    .innerJoin(
      githubInstallation,
      eq(githubInstallation.organizationId, machinePool.organizationId)
    )
    .where(
      and(
        eq(machinePool.machineId, found.id),
        eq(machinePool.organizationId, organizationId),
        eq(machinePool.status, "active")
      )
    )
    .limit(1)
  return row?.installation === "active" ? found : null
}

/** The daemon's sign-in session has done its one job. */
export async function endSession(db: AuthDb, sessionId: string) {
  await db.delete(session).where(eq(session.id, sessionId))
}
