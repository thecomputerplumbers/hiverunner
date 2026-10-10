import { eq } from "drizzle-orm"
import { listOrganizations } from "@workspace/auth"
import { oauthClient } from "@workspace/auth/schema"
import { Button } from "@workspace/ui/components/button"
import { AuthPage } from "@/components/auth-page"
import { RouteLink } from "@/components/route-link"
import { requireServerSession } from "@/lib/session"
import { getDb } from "@/db"
import { ConsentForm } from "./consent-form"

type Query = Record<string, string | string[] | undefined>

/** This page's own address, to come back to after a detour through GitHub. */
function here(params: Query) {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(params))
    for (const each of [value].flat())
      if (each !== undefined) query.append(key, each)
  return encodeURIComponent(`/connect?${query}`)
}

export default async function Page({
  searchParams,
}: {
  searchParams: Promise<Query>
}) {
  const session = await requireServerSession(),
    params = await searchParams,
    db = getDb()
  const clientId =
    typeof params.client_id === "string" ? params.client_id : undefined
  const [client] = clientId
    ? await db
        .select({ name: oauthClient.name, id: oauthClient.clientId })
        .from(oauthClient)
        .where(eq(oauthClient.clientId, clientId))
        .limit(1)
    : []
  const organizations = await listOrganizations(db, session.user.id)
  if (!client)
    return (
      <AuthPage title="Connection unavailable">
        <p>Restart the connection from your client.</p>
      </AuthPage>
    )
  const scope = typeof params.scope === "string" ? params.scope : ""
  return (
    <AuthPage
      title={`Connect ${client.name ?? "application"}`}
      wide={!organizations.length}
    >
      <p className="mb-6 text-sm text-muted-foreground">
        Signed in as{" "}
        <span className="text-foreground">{session.user.name}</span>.{" "}
        <span className="break-all">Client: {client.id}</span>
      </p>
      {!organizations.length && <NoHive back={here(params)} />}
      <ConsentForm
        organizations={organizations.map(({ id, name }) => ({ id, name }))}
        scopes={scope.split(" ").filter(Boolean)}
        current={session.session.activeOrganizationId}
      />
    </AuthPage>
  )
}

/**
 * Signed in, but in no hive, so there is nothing to connect to yet. The
 * install is a detour: GitHub brings the person back to this same page, with
 * the hive listed, and the connection carries on.
 */
function NoHive({ back }: { back: string }) {
  return (
    <div className="mb-6 flex flex-col gap-4">
      <p className="text-muted-foreground">
        You are not in a hive yet, so there is nothing to connect to. A hive
        belongs to a GitHub organization with HiveWorker installed on it.
      </p>
      <ol className="flex list-decimal flex-col gap-4 pl-5 text-muted-foreground marker:font-medium marker:text-foreground">
        <li>
          <strong className="text-foreground">
            Install HiveWorker on a GitHub organization.
          </strong>{" "}
          On GitHub&apos;s install screen, choose the organization, not your
          personal account. If you are not an owner of it, GitHub sends the
          owners a request, and you come back here once one of them approves. If
          you have no organization,{" "}
          <a
            className="underline"
            href="https://github.com/organizations/plan"
            rel="noreferrer"
            target="_blank"
          >
            create a free one on GitHub
          </a>
          .
          <span className="mt-3 block">
            <Button render={<RouteLink to={`/install?next=${back}`} />}>
              Install on GitHub
            </Button>
          </span>
        </li>
        <li>
          <strong className="text-foreground">You come back here</strong> with
          the hive listed. Allow the connection.
        </li>
        <li>
          <strong className="text-foreground">
            Already in an organization that has HiveWorker?
          </strong>{" "}
          <RouteLink className="underline" to={`/github/sync?next=${back}`}>
            Check my organizations again
          </RouteLink>
          .
        </li>
      </ol>
      <p className="text-sm text-muted-foreground">
        If this page has been open a while, the connection may have expired.
        Restart it from your client and sign in again.
      </p>
    </div>
  )
}
