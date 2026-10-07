import { getMembership } from "@workspace/auth"
import { getLiveSnapshot } from "@workspace/db/live"

import { getDb } from "@/db"
import { browserSession } from "@/lib/browser-request"
import { logError, requestId } from "@/lib/diagnostics"
import { AppError, errorResponse } from "@/lib/errors"

/** The console polls this. It reads; it never changes anything. */
export async function GET(request: Request) {
  try {
    const { user, session } = await browserSession(request)
    // A person being asked to join a pool reads it before it is their
    // active one. Either way membership is what decides.
    const organizationId =
      new URL(request.url).searchParams.get("organization") ??
      session.activeOrganizationId
    if (!organizationId) throw new AppError("NOT_FOUND", "No hive selected")
    const db = getDb()
    // From the database, not the session: see `requireMembership`.
    if (!(await getMembership(db, { organizationId, userId: user.id })))
      throw new AppError("FORBIDDEN", "You are not a member of this hive")
    return Response.json(await getLiveSnapshot(db, organizationId), {
      headers: { "Cache-Control": "no-store" },
    })
  } catch (error) {
    logError(error, "pool.live")
    return errorResponse(error, requestId())
  }
}
