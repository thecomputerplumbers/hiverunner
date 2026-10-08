import { env } from "cloudflare:workers"
import Link from "next/link"
import { roleAtLeast } from "@workspace/auth"
import { getLiveSnapshot } from "@workspace/db/live"
import { listUserMachines } from "@workspace/db/machines"
import { getPoolSettings } from "@workspace/db/settings"

import { AccountPage } from "@/components/account-page"
import { ActionButton } from "@/components/action-button"
import { InvitePanel } from "@/components/console/invite"
import { Panel, Snippet } from "@/components/console/panel"
import {
  INSTALL_COMMAND,
  releasedDaemonVersion,
} from "@/components/marketing/site"
import { getDb } from "@/db"
import { requirePool } from "@/lib/session"
import { MachineGrid } from "./machine-grid"

export const metadata = { title: "Machines" }

export default async function MachinesPage() {
  const { organizationId, installation, membership, session } =
    await requirePool()
  const db = getDb()
  const [live, own, settings, released] = await Promise.all([
    getLiveSnapshot(db, organizationId),
    listUserMachines(db, session.user.id),
    getPoolSettings(db, organizationId),
    releasedDaemonVersion(),
  ])
  const machines = live.machines
  // The caller's machines that serve other pools but not this one.
  const addable = own.filter(
    (mine) => !machines.some((machine) => machine.id === mine.id)
  )
  const owner = roleAtLeast(membership.role, "owner")
  return (
    <AccountPage title="machines" live={live}>
      <p className="max-w-3xl text-sm text-muted-foreground">
        Computers serving the {installation.accountLogin} hive. Each belongs to
        the person who enrolled it and leaves this hive when they leave the
        organization. One machine can serve several organizations.
      </p>
      <MachineGrid
        organizationId={organizationId}
        userId={session.user.id}
        owner={owner}
        released={released}
      />
      <InvitePanel
        machines={machines}
        organization={installation.accountLogin}
        url={`${env.APP_URL}/join/${installation.accountLogin}`}
        teams={settings?.allowedTeams ?? []}
      />
      <Panel
        title={machines.length ? "add another Mac of yours" : "add a Mac"}
        bodyClassName="flex max-w-3xl flex-col gap-3"
      >
        <p className="text-sm text-muted-foreground">
          On any Apple Silicon Mac or Linux machine with KVM, paste this into a
          terminal and sign in with GitHub when the browser opens. It appears
          here as soon as it connects.{" "}
          <Link className="text-foreground underline" href="/download">
            What it installs
          </Link>
        </p>
        <Snippet prompt>{INSTALL_COMMAND}</Snippet>
      </Panel>
      {addable.length > 0 && (
        <Panel
          title="add one of your machines"
          bodyClassName="flex flex-col gap-3"
        >
          <p className="max-w-3xl text-sm text-muted-foreground">
            These already run HiveWorker for another organization. Adding one
            here needs nothing installed; its daemon picks this hive up within a
            minute.
          </p>
          <ul className="divide-y divide-border border-y border-border">
            {addable.map((machine) => (
              <li
                key={machine.id}
                className="flex flex-wrap items-center justify-between gap-3 py-3"
              >
                <p className="font-mono text-sm">
                  {machine.name}{" "}
                  <span className="text-muted-foreground">{machine.model}</span>
                </p>
                <ActionButton
                  label={`Add to ${installation.accountLogin}`}
                  path="/api/machines/pools"
                  body={{ machineId: machine.id, organizationId }}
                />
              </li>
            ))}
          </ul>
        </Panel>
      )}
    </AccountPage>
  )
}
