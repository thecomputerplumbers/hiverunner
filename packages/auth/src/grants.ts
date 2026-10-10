import { AsyncLocalStorage } from "node:async_hooks"
import { and, eq } from "drizzle-orm"
import type { AuthDb } from "@workspace/auth/db"
import { agentGrant, oauthConsent, user } from "@workspace/auth/schema"
import { requireMembership } from "@workspace/auth/organization"

export const MCP_SCOPES = [
  "openid",
  "profile",
  "email",
  "offline_access",
  "hive:read",
  "hive:write",
] as const

// A consent submission carries its own selection; another browser tab cannot
// change it by changing session.activeOrganizationId. No module-global identity.
const selection = new AsyncLocalStorage<{
  id: string
  userId: string
  organizationId: string
  clientId: string
}>()
export const hasGrantSelection = () => selection.getStore() !== undefined
export const withGrantSelection = <T>(
  value: NonNullable<ReturnType<typeof selection.getStore>>,
  callback: () => T
) => selection.run(value, callback)

export async function consentGrant(
  db: AuthDb,
  userId: string,
  scopes: string[]
) {
  const chosen = selection.getStore()
  if (!chosen || chosen.userId !== userId)
    throw new Error("Choose an organization to authorize")
  const membership = await requireMembership(db, {
    userId,
    organizationId: chosen.organizationId,
    role: scopes.some((scope) => scope.endsWith(":write")) ? "admin" : "member",
  })
  await db
    .insert(agentGrant)
    .values({ ...chosen, scopes, createdAt: new Date() })
    .onConflictDoNothing()
  return { id: chosen.id, membership }
}

export async function requireGrant(
  db: AuthDb,
  id: string,
  userId: string,
  clientId?: string
) {
  const [grant] = await db
    .select()
    .from(agentGrant)
    .where(and(eq(agentGrant.id, id), eq(agentGrant.userId, userId)))
    .limit(1)
  if (
    !grant ||
    !grant.approvedAt ||
    grant.revokedAt ||
    (clientId && grant.clientId !== clientId)
  )
    throw new Error("Connection revoked or unavailable")
  const [account] = await db
    .select()
    .from(user)
    .where(eq(user.id, userId))
    .limit(1)
  if (!account?.emailVerified) throw new Error("Verified account required")
  const [consent] = await db
    .select({ id: oauthConsent.id })
    .from(oauthConsent)
    .where(
      and(
        eq(oauthConsent.referenceId, id),
        eq(oauthConsent.userId, userId),
        eq(oauthConsent.clientId, grant.clientId)
      )
    )
    .limit(1)
  if (!consent) throw new Error("Connection consent revoked")
  await requireMembership(db, { userId, organizationId: grant.organizationId })
  return grant
}
