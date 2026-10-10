import { env } from "cloudflare:workers"
import Link from "next/link"
import { and, eq, isNull, desc } from "drizzle-orm"
import { agentGrant, oauthClient, organization } from "@workspace/auth/schema"
import { AccountPage } from "@/components/account-page"
import { ActionButton } from "@/components/action-button"
import { Panel, Snippet } from "@/components/console/panel"
import { mcpClients } from "@/components/marketing/site"
import { requireServerSession } from "@/lib/session"
import { getDb } from "@/db"

export const metadata = { title: "Agents" }

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
  const url = `${env.APP_URL}/mcp`
  return (
    <AccountPage title="agents">
      <Panel title="connect an agent" bodyClassName="flex flex-col gap-3">
        <p className="max-w-2xl text-sm text-muted-foreground">
          Point Claude Code, Cursor, Codex or any MCP client at this URL. It
          asks you to sign in with GitHub and pick a hive; there is no API key.
        </p>
        <Snippet>{url}</Snippet>
        <p className="text-sm text-muted-foreground">In Claude Code:</p>
        <Snippet prompt>{mcpClients(url).claudeCode}</Snippet>
        <p className="text-sm text-muted-foreground">
          <Link className="text-foreground underline" href="/docs/agents">
            Other clients, the tools and their permissions
          </Link>
        </p>
      </Panel>
      <p className="text-muted-foreground">
        Each connection is limited to the hive and permissions you approved.
      </p>
      {!grants.length && <p>No connected agents.</p>}
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
