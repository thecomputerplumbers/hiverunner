import { env } from "cloudflare:workers"
import Link from "next/link"
import { RUNNER_SIZES, TRIAL_GRANT_USD } from "@workspace/pricing"
import { getLiveSnapshot } from "@workspace/db/live"
import { getPoolSettings } from "@workspace/db/settings"
import { roleAtLeast } from "@workspace/auth"

import { AccountPage } from "@/components/account-page"
import { Notice, Panel, Snippet } from "@/components/console/panel"
import { getDb } from "@/db"
import { NoHiveSteps } from "@/components/no-hive-steps"
import { DAEMON_RELEASED, INSTALL_COMMAND } from "@/components/marketing/site"
import { getPool } from "@/lib/session"
import { LiveDashboard } from "./live-dashboard"

export const metadata = { title: "Dashboard" }

export default async function DashboardPage({
  searchParams,
}: {
  searchParams: Promise<{ installed?: string }>
}) {
  const { pool } = await getPool()
  if (!pool) return <NoPool />

  const { installed } = await searchParams
  const { organizationId, installation, membership } = pool
  const db = getDb()
  const [live, settings] = await Promise.all([
    getLiveSnapshot(db, organizationId),
    getPoolSettings(db, organizationId),
  ])
  const online = live.machines.filter(
    (machine) => machine.presence === "online" || machine.presence === "busy"
  ).length
  // Set up means a job has run here. Until then the page leads with a checklist.
  const ready = live.recent.length > 0
  const owner = roleAtLeast(membership.role, "owner")

  return (
    <AccountPage title={installation.accountLogin} live={live}>
      {installed && (
        <Notice>
          HiveWorker is installed on{" "}
          <strong>{installation.accountLogin}</strong>, with ${TRIAL_GRANT_USD}{" "}
          of credit to start. Two steps to your first job.
        </Notice>
      )}
      {installation.status !== "active" && (
        <Notice tone="alert">
          {installation.status === "suspended"
            ? "The GitHub App is suspended on this organization. Jobs will not be picked up until an owner unsuspends it on GitHub."
            : "The GitHub App was uninstalled from this organization. Reinstall it to run jobs; your balance and history are kept."}
        </Notice>
      )}
      {live.balance <= 0 && (
        <Notice tone="alert">
          The balance has run out, so new jobs are waiting in GitHub&apos;s
          queue.{" "}
          <Link className="underline" href="/wallet">
            Add funds
          </Link>{" "}
          to start it moving again.
        </Notice>
      )}

      {!ready && (
        <FirstJob
          machines={live.machines.length}
          online={online}
          jobs={live.recent.length}
          organization={installation.accountLogin}
        />
      )}

      <LiveDashboard
        invite={{
          organization: installation.accountLogin,
          url: `${env.APP_URL}/join/${installation.accountLogin}`,
          teams: settings?.allowedTeams ?? [],
        }}
      />

      <div className="flex flex-col gap-1 text-sm text-muted-foreground">
        {installation.repositorySelection === "selected" && (
          <p>
            The App is installed on selected repositories only. Workflows in
            other repositories will not reach this hive.
          </p>
        )}
        {owner && (
          <p>
            Any member of {installation.accountLogin} on GitHub can add a
            machine.{" "}
            <Link className="text-foreground underline" href="/settings">
              Narrow it to teams
            </Link>
            .
          </p>
        )}
      </div>
    </AccountPage>
  )
}

/**
 * The path to a first job, with each step ticked as it actually happens.
 * It is the whole page's purpose until a job has run, and gone after.
 */
function FirstJob({
  machines,
  online,
  jobs,
  organization,
}: {
  machines: number
  online: number
  jobs: number
  organization: string
}) {
  const steps = [
    {
      done: true,
      title: `HiveWorker is installed on ${organization}, with $${TRIAL_GRANT_USD} of credit and no card needed`,
      body: null,
    },
    {
      done: machines > 0,
      title:
        machines > 0
          ? online > 0
            ? `${machines === 1 ? "A Mac is" : `${machines} Macs are`} in the hive`
            : "A Mac is in the hive, but not connected right now"
          : "Add a Mac",
      body:
        machines > 0 ? (
          online > 0 ? null : (
            <p>
              Start it with <code>hived run</code>, or open its lid. Jobs wait
              until a Mac is connected.
            </p>
          )
        ) : (
          <>
            {DAEMON_RELEASED ? (
              <p>
                On any Apple Silicon Mac or Linux machine with KVM, paste this
                into a terminal and sign in with GitHub when the browser opens.
                Anyone in {organization} can. It asks for no password.
              </p>
            ) : (
              <p>
                The installer is not released yet. This is the command that will
                add a Mac, and it does not work today.
              </p>
            )}
            <Snippet prompt>{INSTALL_COMMAND}</Snippet>
            <p>
              <Link className="underline" href="/download">
                {DAEMON_RELEASED
                  ? "What it installs, and what the Mac needs"
                  : "Status and requirements"}
              </Link>
            </p>
          </>
        ),
    },
    {
      done: jobs > 0,
      title: "Change one line in a workflow",
      body: (
        <>
          <Snippet>{`jobs:\n  test:\n    runs-on: ${RUNNER_SIZES[0].label}`}</Snippet>
          <p>
            Push it, and the job runs on your Mac. This page updates when it
            does.
          </p>
        </>
      ),
    },
  ]
  const done = steps.filter((step) => step.done).length
  return (
    <Panel
      title="Get your first job running"
      aside={
        <span className="tabular-nums">
          {done} of {steps.length} done
        </span>
      }
      bodyClassName="p-0"
    >
      <ol className="divide-y divide-border">
        {steps.map((step, index) => (
          <li key={step.title} className="flex gap-3 p-4">
            <span
              aria-hidden="true"
              className={
                step.done
                  ? "flex size-6 shrink-0 items-center justify-center bg-primary font-mono text-xs font-semibold text-primary-foreground"
                  : "flex size-6 shrink-0 items-center justify-center border border-border font-mono text-xs text-muted-foreground"
              }
            >
              {step.done ? "✓" : index + 1}
            </span>
            <div className="flex min-w-0 flex-1 flex-col gap-2">
              <p
                className={step.done ? "text-muted-foreground" : "font-medium"}
              >
                <span className="sr-only">
                  {step.done ? "Done: " : "To do: "}
                </span>
                {step.title}
              </p>
              {step.body && (!step.done || (index === 1 && online === 0)) ? (
                <div className="flex max-w-2xl flex-col gap-2 text-sm text-muted-foreground">
                  {step.body}
                </div>
              ) : null}
            </div>
          </li>
        ))}
      </ol>
    </Panel>
  )
}

function NoPool() {
  return (
    <AccountPage title="No hive yet">
      <div className="max-w-2xl">
        <NoHiveSteps />
      </div>
    </AccountPage>
  )
}
