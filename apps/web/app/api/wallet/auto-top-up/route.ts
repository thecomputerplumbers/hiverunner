import { requireMembership } from "@workspace/auth"
import { setAutoTopUp } from "@workspace/db/top-up"
import { TOP_UP_AMOUNTS_USD } from "@workspace/pricing"

import { getDb } from "@/db"
import { getServerSession } from "@/lib/session"
import { canTakeCards } from "@/lib/stripe"

/**
 * Turns automatic top-up on or off for the active organization, or changes
 * what it adds. The card itself is only ever saved by paying through
 * Checkout; nothing here touches it.
 */
export async function POST(request: Request) {
  if (!canTakeCards())
    return Response.json(
      { error: "Card payments are not set up yet." },
      { status: 503 }
    )
  const session = await getServerSession()
  if (!session)
    return Response.json({ error: "Not signed in" }, { status: 401 })
  const organizationId = session.session.activeOrganizationId
  if (!organizationId)
    return Response.json({ error: "No active organization" }, { status: 400 })

  const body = (await request.json().catch(() => ({}))) as {
    enabled?: unknown
    amount?: unknown
  }
  if (typeof body.enabled !== "boolean")
    return Response.json({ error: "Say on or off." }, { status: 400 })
  const dollars =
    body.amount === undefined
      ? undefined
      : TOP_UP_AMOUNTS_USD.find((offered) => offered === body.amount)
  if (body.amount !== undefined && !dollars)
    return Response.json(
      { error: "Choose one of the amounts offered." },
      { status: 400 }
    )

  const db = getDb()
  try {
    await requireMembership(db, {
      organizationId,
      userId: session.user.id,
      role: "admin",
    })
  } catch {
    return Response.json(
      { error: "Only an owner or admin can change this." },
      { status: 403 }
    )
  }
  try {
    const saved = await setAutoTopUp(db, {
      organizationId,
      enabled: body.enabled,
      amountCents: dollars === undefined ? undefined : dollars * 100,
    })
    return Response.json({ enabled: saved?.enabled ?? false })
  } catch (error) {
    return Response.json(
      { error: error instanceof Error ? error.message : "Could not save." },
      { status: 400 }
    )
  }
}
