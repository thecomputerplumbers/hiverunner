import { and, desc, eq, inArray } from "drizzle-orm"

import type { AuthDb } from "@workspace/auth/db"
import { member, organization, session, user } from "@workspace/auth/schema"
import type { GitHubApp } from "@workspace/github"
import { runnerArch } from "@workspace/pricing"

import { type SyncResult, whyNoMachines } from "@workspace/db/membership"
import {
  githubInstallation,
  machine,
  machinePool,
  poolSettings,
} from "@workspace/db/schema"

/** Enough for a laptop, a desktop and a few build boxes; not for abuse. */
export const MAX_MACHINES_PER_PERSON = 10

/** How many machines one request may ask to add to a pool. */
export const MAX_MACHINES_PER_REQUEST = 25

/**
 * How long after a person joins a hive a page may still tell them what that
 * did to their machines. Past it the link that carried the news says nothing.
 */
export const JOINED_NOTICE_MS = 15 * 60_000

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

export async function sha256Hex(value: string) {
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
  // The hosts a daemon exists for. Widening this is a product decision, not
  // a validation tweak.
  if (!hostArch(facts))
    fields.os =
      "Hive Runner runs on Apple Silicon Macs, and on Linux on arm64 or x64"
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
 * The architecture of the jobs a machine can run, or null for a machine no
 * daemon runs on. The daemon reports Go's names for what it is, and exactly
 * three are hosts: darwin/arm64, linux/arm64 and linux/amd64. What is stored
 * is the product's word, so Go's amd64 becomes the labels' x64.
 */
function hostArch(facts: Pick<MachineFacts, "os" | "arch">) {
  const known =
    facts.arch === "arm64"
      ? facts.os === "darwin" || facts.os === "linux"
      : facts.arch === "amd64" && facts.os === "linux"
  return known ? runnerArch(facts.arch) : null
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
  const [row] = await db
    .select({ name: organization.name, member: member.id })
    .from(organization)
    .leftJoin(
      member,
      and(
        eq(member.organizationId, organization.id),
        eq(member.userId, input.userId)
      )
    )
    .where(eq(organization.id, input.organizationId))
    .limit(1)
  // The same answer whether the hive does not exist or is not theirs.
  if (!row?.member)
    return {
      code: "NOT_FOUND",
      message: "You are not a member of this hive.",
    }
  const why = github
    ? await whyNoMachines(db, github, input)
    : (await poolIsActive(db, input.organizationId))
      ? null
      : "inactive"
  if (why === "inactive")
    return {
      code: "FORBIDDEN",
      message: `${row.name} is not taking machines: Hive Runner is suspended or uninstalled on it. Ask an owner to restore it.`,
    }
  if (why === "not-member")
    return {
      code: "FORBIDDEN",
      message: `GitHub does not list you as a member of ${row.name}. Accept its invitation on GitHub, then try again.`,
    }
  if (why === "teams")
    return {
      code: "FORBIDDEN",
      message: `${row.name} limits machines to certain teams, and you are on none of them. Ask an owner to add you to one, or to change the hive's settings.`,
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
    arch: hostArch(input.facts)!,
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

/**
 * The same for several of the caller's machines, each decided separately.
 * Says which were added and why each of the others was not.
 */
export async function addMachinesToPool(
  db: AuthDb,
  github: GitHubChecks | null,
  input: { userId: string; machineIds: string[]; organizationId: string },
  now = new Date()
) {
  const added: string[] = []
  const refused: Array<{ machineId: string } & Refusal> = []
  for (const machineId of new Set(input.machineIds)) {
    const refusal = await addMachineToPool(
      db,
      github,
      { userId: input.userId, machineId, organizationId: input.organizationId },
      now
    )
    if (refusal) refused.push({ machineId, ...refusal })
    else added.push(machineId)
  }
  return { added, refused }
}

/**
 * Whether a machine joins hives its owner newly becomes an owner of. Only its
 * owner may say, and only from a browser: the route that calls this refuses
 * every other credential.
 */
export async function setMachineAutoJoin(
  db: AuthDb,
  input: { userId: string; machineId: string; autoJoin: boolean }
): Promise<Refusal | null> {
  const changed = await db
    .update(machine)
    .set({ autoJoin: input.autoJoin })
    .where(
      and(
        eq(machine.id, input.machineId),
        eq(machine.userId, input.userId),
        eq(machine.status, "active")
      )
    )
    .returning({ id: machine.id })
  // The same answer whether it does not exist or is someone else's.
  return changed.length
    ? null
    : { code: "NOT_FOUND", message: "No such machine" }
}

/**
 * Adds a person's auto-join machines to the hives a sync has just made them
 * an owner of, and says which it added.
 *
 * It acts on new member rows only. That is what keeps a machine its owner
 * later takes out of a hive from coming back, and what keeps turning the
 * setting on from reaching hives they were already in. Hives they are only a
 * member of are never joined this way.
 *
 * Every add is authorized like any other. One that throws, because GitHub
 * could not be asked, is handed to `onError` and is neither a yes nor a no.
 */
export async function autoJoinMachines(
  db: AuthDb,
  github: GitHubChecks | null,
  input: {
    userId: string
    joined: SyncResult["joined"]
    onError?: (error: unknown) => void
  },
  now = new Date()
) {
  const added: Array<{
    machineId: string
    name: string
    organizationId: string
  }> = []
  const owned = input.joined.filter((hive) => hive.role === "owner")
  if (!owned.length) return added
  const machines = (await listUserMachines(db, input.userId)).filter(
    (row) => row.autoJoin
  )
  if (!machines.length) return added
  const serving = await listUserMachineHives(db, input.userId)
  for (const { organizationId } of owned)
    for (const row of machines) {
      if (
        serving.some(
          (pool) =>
            pool.machineId === row.id && pool.organizationId === organizationId
        )
      )
        continue
      try {
        const refusal = await addMachineToPool(
          db,
          github,
          { userId: input.userId, machineId: row.id, organizationId },
          now
        )
        if (!refusal)
          added.push({ machineId: row.id, name: row.name, organizationId })
      } catch (error) {
        input.onError?.(error)
      }
    }
  return added
}

/**
 * What joining these hives meant for the person's machines, for the page
 * they land on to say: which machines auto-join put in each, and how many of
 * theirs do not serve it yet.
 *
 * The hive ids come from a URL, so they choose only which hives are spoken
 * of. Everything said is read here, and only for a hive joined within
 * `JOINED_NOTICE_MS`: a link kept in a tab or a bookmark goes quiet.
 */
export async function joinedHives(
  db: AuthDb,
  input: { userId: string; organizationIds: string[] },
  now = new Date()
) {
  const asked = [...new Set(input.organizationIds)].slice(0, 10)
  if (!asked.length) return []
  const hives = (
    await db
      .select({
        organizationId: member.organizationId,
        login: githubInstallation.accountLogin,
        role: member.role,
        createdAt: member.createdAt,
      })
      .from(member)
      .innerJoin(
        githubInstallation,
        eq(githubInstallation.organizationId, member.organizationId)
      )
      .where(
        and(
          eq(member.userId, input.userId),
          inArray(member.organizationId, asked),
          eq(githubInstallation.status, "active")
        )
      )
  )
    .filter(
      (hive) => now.getTime() - hive.createdAt.getTime() <= JOINED_NOTICE_MS
    )
    // In the order asked for, so the page reads the same on a reload.
    .sort(
      (a, b) =>
        asked.indexOf(a.organizationId) - asked.indexOf(b.organizationId)
    )
  if (!hives.length) return []
  const [machines, serving] = await Promise.all([
    listUserMachines(db, input.userId),
    listUserMachineHives(db, input.userId),
  ])
  return hives.map((hive) => {
    const here = new Set(
      serving
        .filter((pool) => pool.organizationId === hive.organizationId)
        .map((pool) => pool.machineId)
    )
    return {
      organizationId: hive.organizationId,
      login: hive.login,
      /** Machines auto-join put here. Only an owner's hive is auto-joined. */
      autoJoined:
        hive.role === "owner"
          ? machines
              .filter((row) => row.autoJoin && here.has(row.id))
              .map((row) => row.name)
          : [],
      /** How many of their machines do not serve this hive. */
      missing: machines.filter((row) => !here.has(row.id)).length,
    }
  })
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
      os: machine.os,
      arch: machine.arch,
      cores: machine.cores,
      memoryBytes: machine.memoryBytes,
      speedScore: machine.speedScore,
      macos: machine.macos,
      macosProblem: machine.macosProblem,
      daemonVersion: machine.daemonVersion,
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
    .select({
      id: machine.id,
      name: machine.name,
      model: machine.model,
      autoJoin: machine.autoJoin,
    })
    .from(machine)
    .where(and(eq(machine.userId, userId), eq(machine.status, "active")))
    .orderBy(desc(machine.createdAt))
    .limit(100)
}

/** Which hives each of a person's machines serves. */
export async function listUserMachineHives(db: AuthDb, userId: string) {
  return db
    .select({
      machineId: machinePool.machineId,
      organizationId: machinePool.organizationId,
      login: githubInstallation.accountLogin,
    })
    .from(machinePool)
    .innerJoin(machine, eq(machine.id, machinePool.machineId))
    .innerJoin(
      githubInstallation,
      eq(githubInstallation.organizationId, machinePool.organizationId)
    )
    .where(
      and(
        eq(machine.userId, userId),
        eq(machine.status, "active"),
        eq(machinePool.status, "active")
      )
    )
    .orderBy(githubInstallation.accountLogin)
    .limit(1000)
}

/**
 * The pools a machine should hold a tunnel to. The daemon asks for this on a
 * timer, which is how it learns it was added to a pool or dropped from one.
 * Each says whether it allows macOS jobs, so a Mac set up for them can tell
 * its owner which hives will actually send any.
 */
export async function listMachinePools(db: AuthDb, machineId: string) {
  const rows = await db
    .select({
      id: organization.id,
      name: organization.name,
      macosJobs: poolSettings.macosJobs,
    })
    .from(machinePool)
    .innerJoin(organization, eq(organization.id, machinePool.organizationId))
    .innerJoin(
      githubInstallation,
      eq(githubInstallation.organizationId, machinePool.organizationId)
    )
    .leftJoin(
      poolSettings,
      eq(poolSettings.organizationId, machinePool.organizationId)
    )
    .where(
      and(
        eq(machinePool.machineId, machineId),
        eq(machinePool.status, "active"),
        eq(githubInstallation.status, "active")
      )
    )
  return rows.map((row) => ({ ...row, macosJobs: row.macosJobs === true }))
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
