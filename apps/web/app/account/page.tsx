import { headers } from "next/headers"
import { AccountPage } from "@/components/account-page"
import { Panel } from "@/components/console/panel"
import { ActionButton } from "@/components/action-button"
import { requireServerSession } from "@/lib/session"
import { auth } from "@/lib/auth"
import { AccountForm } from "./account-form"
export default async function Page() {
  const current = await requireServerSession()
  const sessions = await auth.api.listSessions({ headers: await headers() })
  return (
    <AccountPage title="account">
      <p className="font-mono text-sm text-muted-foreground">
        {current.user.email} · signed in with GitHub
      </p>
      <Panel title="profile" className="max-w-3xl">
        <AccountForm name={current.user.name} />
      </Panel>
      <Panel title="signed-in devices" className="max-w-3xl" flush>
        <ul className="divide-y divide-border">
          {sessions.map((session) => (
            <li
              className="flex flex-wrap items-center justify-between gap-3 p-4 sm:px-6"
              key={session.id}
            >
              <div className="min-w-0 flex-1">
                <p className="font-mono text-xs break-words">
                  {session.userAgent ?? "Unknown device"}
                </p>
                <p className="mt-1 text-sm text-muted-foreground">
                  Signed in {session.createdAt.toLocaleDateString("en-US")}
                  {session.id === current.session.id ? " · this device" : ""}
                </p>
              </div>
              <ActionButton
                label="Sign out device"
                path="/api/account/sessions"
                body={{ id: session.id }}
                redirectTo={
                  session.id === current.session.id ? "/sign-in" : undefined
                }
              />
            </li>
          ))}
        </ul>
      </Panel>
    </AccountPage>
  )
}
