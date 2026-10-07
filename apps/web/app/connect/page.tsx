import Link from "next/link"
import { eq } from "drizzle-orm"
import { listOrganizations } from "@workspace/auth"
import { oauthClient } from "@workspace/auth/schema"
import { AuthPage } from "@/components/auth-page"
import { requireServerSession } from "@/lib/session"
import { getDb } from "@/db"
import { ConsentForm } from "./consent-form"
export default async function Page({
  searchParams,
}: {
  searchParams: Promise<{ client_id?: string; scope?: string }>
}) {
  const session = await requireServerSession(),
    params = await searchParams,
    db = getDb()
  const [client] = params.client_id
    ? await db
        .select({ name: oauthClient.name, id: oauthClient.clientId })
        .from(oauthClient)
        .where(eq(oauthClient.clientId, params.client_id))
        .limit(1)
    : []
  const organizations = await listOrganizations(db, session.user.id)
  if (!client)
    return (
      <AuthPage title="Connection unavailable">
        <p>Restart the connection from your client.</p>
      </AuthPage>
    )
  return (
    <AuthPage title={`Connect ${client.name ?? "application"}`}>
      <p className="mb-4 text-sm break-all text-muted-foreground">
        Client: {client.id}
      </p>
      {!organizations.length && (
        <p className="mb-4">
          First{" "}
          <Link className="underline" href="/hives">
            create an organization
          </Link>
          , then restart the connection.
        </p>
      )}
      <ConsentForm
        organizations={organizations.map(({ id, name }) => ({ id, name }))}
        scopes={(params.scope ?? "").split(" ").filter(Boolean)}
      />
    </AuthPage>
  )
}
