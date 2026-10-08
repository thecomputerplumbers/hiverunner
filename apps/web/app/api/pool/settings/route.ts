import { getMembership, roleAtLeast } from "@workspace/auth"
import {
  updatePoolSettings,
  validatePoolSettings,
} from "@workspace/db/settings"

import { getDb } from "@/db"
import { browserSession } from "@/lib/browser-request"
import { logError, requestId } from "@/lib/diagnostics"
import { AppError, errorResponse } from "@/lib/errors"

export async function POST(request: Request) {
  try {
    const { user } = await browserSession(request)
    const input = (await request.json()) as Record<string, unknown>
    if (
      typeof input.organizationId !== "string" ||
      !Array.isArray(input.allowedTeams) ||
      !input.allowedTeams.every((team) => typeof team === "string") ||
      typeof input.earningsDestination !== "string" ||
      typeof input.macosJobs !== "boolean" ||
      typeof input.version !== "number"
    )
      throw new AppError("INVALID_INPUT", "Invalid input")
    const db = getDb()
    // From the database, not the session: see `requireMembership`.
    const membership = await getMembership(db, {
      organizationId: input.organizationId,
      userId: user.id,
    })
    if (!membership || !roleAtLeast(membership.role, "owner"))
      throw new AppError(
        "FORBIDDEN",
        "Only owners of the GitHub organization can change hive settings"
      )
    const settings = {
      allowedTeams: input.allowedTeams.map((team) => team.trim().toLowerCase()),
      earningsDestination: input.earningsDestination as
        | "organization"
        | "developer",
      macosJobs: input.macosJobs,
      version: input.version,
    }
    const fields = validatePoolSettings(settings)
    if (fields) throw new AppError("INVALID_INPUT", "Check the form", fields)
    if (!(await updatePoolSettings(db, input.organizationId, settings)))
      throw new AppError(
        "CONFLICT",
        "These settings changed since you loaded them. Reload and try again."
      )
    return Response.json({ ok: true })
  } catch (error) {
    logError(error, "pool.settings")
    return errorResponse(error, requestId())
  }
}
