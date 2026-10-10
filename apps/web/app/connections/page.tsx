import { and, eq, isNull, desc } from "drizzle-orm"
import { agentGrant, oauthClient, organization } from "@workspace/auth/schema"
import { AccountPage } from "@/components/account-page"
import { ActionButton } from "@/components/action-button"
import { Panel } from "@/components/console/panel"
import { requireServerSession } from "@/lib/session"
import { getDb } from "@/db"
export default async function Page() {
  const { user } = await requireServerSession()
  const rows = await getDb()
    .select({
      id: agentGrant.id,
      approvedAt: agentGrant.approvedAt,
      scopes: agentGrant.scopes,
      client: oauthClient.name,
      organization: organization.name,
    })
    .from(agentGrant)
    .leftJoin(oauthClient, eq(oauthClient.clientId, agentGrant.clientId))
    .innerJoin(organization, eq(organization.id, agentGrant.organizationId))
    .where(and(eq(agentGrant.userId, user.id), isNull(agentGrant.revokedAt)))
    .orderBy(desc(agentGrant.createdAt))
  const grants = rows.filter((row) => row.approvedAt)
  return (
    <AccountPage title="Connected apps">
      <p className="text-muted-foreground">
        Each connection is limited to the organization and permissions you
        approved.
      </p>
      {!grants.length && <p>No connected apps.</p>}
      {grants.map((grant) => (
        <Panel
          key={grant.id}
          title={grant.client ?? "Application"}
          className="max-w-3xl"
          bodyClassName="flex flex-col gap-3"
        >
          <p>{grant.organization}</p>
          <p className="font-mono text-xs text-muted-foreground">
            {grant.scopes.join(", ")}
          </p>
          <ActionButton
            label="Revoke access"
            path="/api/connections"
            body={{ id: grant.id }}
          />
        </Panel>
      ))}
    </AccountPage>
  )
}
