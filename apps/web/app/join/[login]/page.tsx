import Link from "next/link"
import { redirect } from "next/navigation"
import { listOrganizations } from "@workspace/auth"
import { getInstallationByOrganization } from "@workspace/db/installations"
import { getLiveSnapshot } from "@workspace/db/live"
import { listUserMachines } from "@workspace/db/machines"
import { Button } from "@workspace/ui/components/button"

import { LiveProvider } from "@/components/console/live"
import { HiveMark } from "@/components/marketing/hive-mark"
import { INSTALL_COMMAND } from "@/components/marketing/site"
import { getDb } from "@/db"
import { getServerSession } from "@/lib/session"
import { JoinPool } from "./join-pool"

export const metadata = { title: "Add your Mac" }

const LOGIN = /^[A-Za-z0-9-]{1,39}$/

/**
 * Where a teammate's invite link lands. It asks one thing, for this person's
 * Mac, and nothing about setting up an organization: that was done by whoever
 * sent the link. Only a member of the organization on GitHub gets past
 * sign-in, and anyone else is told the same thing whether the pool exists or
 * not.
 */
export default async function JoinPage({
  params,
}: {
  params: Promise<{ login: string }>
}) {
  const { login } = await params
  if (!LOGIN.test(login)) redirect("/dashboard")
  const here = `/join/${login}`
  const session = await getServerSession()
  if (!session) redirect(`/sign-in?next=${encodeURIComponent(here)}`)

  const db = getDb()
  // Membership from the database, which sign-in has just synced with GitHub.
  const pools = await listOrganizations(db, session.user.id)
  const installations = await Promise.all(
    pools.map((pool) => getInstallationByOrganization(db, pool.id))
  )
  const installation = installations.find(
    (found) => found?.accountLogin.toLowerCase() === login.toLowerCase()
  )

  return (
    <div className="flex min-h-svh flex-col">
      <header className="border-b border-border">
        <div className="mx-auto flex h-12 w-full max-w-5xl items-center justify-between px-4 font-mono text-sm sm:px-6">
          <span className="flex items-center gap-2 font-medium">
            <HiveMark className="size-5" />
            HiveWorker
          </span>
          <span className="truncate text-xs text-muted-foreground">
            {session.user.name}
          </span>
        </div>
      </header>
      <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-6 px-4 py-10 sm:px-6">
        {installation ? (
          <Joining
            organizationId={installation.organizationId}
            login={installation.accountLogin}
            userId={session.user.id}
          />
        ) : (
          <div className="flex max-w-xl flex-col gap-4">
            <h1 className="font-mono text-2xl font-medium tracking-tight">
              We can&apos;t see you in {login}
            </h1>
            <p className="text-muted-foreground">
              This link is for members of {login} on GitHub. If you joined it
              recently, or your membership is private and you have not approved
              HiveWorker for it yet, check again. Otherwise ask whoever sent the
              link.
            </p>
            <div>
              <Button
                render={
                  <a href={`/github/sync?next=${encodeURIComponent(here)}`} />
                }
              >
                Check my organizations again
              </Button>
            </div>
          </div>
        )}
      </main>
    </div>
  )
}

async function Joining({
  organizationId,
  login,
  userId,
}: {
  organizationId: string
  login: string
  userId: string
}) {
  const db = getDb()
  const [live, own] = await Promise.all([
    getLiveSnapshot(db, organizationId),
    listUserMachines(db, userId),
  ])
  return (
    <LiveProvider initial={live} organizationId={organizationId}>
      <JoinPool
        organizationId={organizationId}
        login={login}
        userId={userId}
        command={INSTALL_COMMAND}
        // Macs of theirs that already run HiveWorker for someone else.
        elsewhere={own.filter(
          (mine) => !live.machines.some((machine) => machine.id === mine.id)
        )}
      />
      <p className="text-sm text-muted-foreground">
        <Link className="underline" href="/download">
          What the installer puts on your Mac
        </Link>
        {" · "}
        <Link className="underline" href="/security">
          How jobs are kept away from your files
        </Link>
      </p>
    </LiveProvider>
  )
}
