import { listOrganizations } from "@workspace/auth"
import {
  endSession,
  enrollMachine,
  type MachineFacts,
} from "@workspace/db/machines"

import { getDb } from "@/db"
import { apiSession } from "@/lib/browser-request"
import { logError, requestId } from "@/lib/diagnostics"
import { AppError, errorResponse } from "@/lib/errors"
import { getGitHubApp } from "@/lib/github"

/**
 * Enrolls the caller's machine and returns its token, once.
 *
 * Called by the daemon right after device sign-in. A machine can serve
 * several of its owner's organizations; each one asked for is authorized
 * separately. The sign-in session is ended here: from now on the machine
 * token is the daemon's only credential, so a person's session expiring
 * never takes their machine offline.
 */
export async function POST(request: Request) {
  try {
    const { user, session, bearer } = await apiSession(request)
    const input = (await request.json()) as Record<string, unknown>
    const db = getDb()
    const pools = await listOrganizations(db, user.id)

    let organizationIds = input.organizationIds
    if (organizationIds === undefined) {
      if (pools.length !== 1)
        // The daemon shows this list and asks again with a choice.
        return Response.json(
          {
            error: pools.length
              ? "Choose which hives this machine should serve"
              : "You are not a member of any hive",
            code: pools.length ? "CHOOSE_POOL" : "FORBIDDEN",
            pools: pools.map(({ id, name }) => ({ id, name })),
          },
          { status: pools.length ? 409 : 403 }
        )
      organizationIds = [pools[0]!.id]
    }
    if (
      !Array.isArray(organizationIds) ||
      !organizationIds.every((id) => typeof id === "string")
    )
      throw new AppError("INVALID_INPUT", "Invalid input")

    const result = await enrollMachine(db, getGitHubApp(), {
      userId: user.id,
      organizationIds,
      facts: input as unknown as MachineFacts,
    })
    if (!result.ok)
      throw new AppError(result.code, result.message, result.fields)
    if (bearer) await endSession(db, session.id)
    const named = (id: string) => ({
      id,
      name: pools.find((pool) => pool.id === id)?.name ?? id,
    })
    return Response.json(
      {
        machineId: result.machineId,
        token: result.token,
        pools: result.joined.map(named),
        refused: result.refused.map((refusal) => ({
          ...named(refusal.organizationId),
          message: refusal.message,
        })),
      },
      { status: 201, headers: { "Cache-Control": "no-store" } }
    )
  } catch (error) {
    logError(error, "machine.enroll")
    return errorResponse(error, requestId())
  }
}
