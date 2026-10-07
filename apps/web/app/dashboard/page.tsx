import Link from "next/link"
import {
  RUNNER_SIZES,
  TRIAL_GRANT_USD,
  privatePoolUsdPerMinute,
} from "@workspace/pricing"
import { listJobs } from "@workspace/db/jobs"
import { getBalance } from "@workspace/db/ledger"
import { listMachines } from "@workspace/db/machines"
import { roleAtLeast } from "@workspace/auth"
import { Button } from "@workspace/ui/components/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@workspace/ui/components/card"

import { AccountPage } from "@/components/account-page"
import { RouteLink } from "@/components/route-link"
import { getDb } from "@/db"
import { formatMoney } from "@/lib/format"
import { DAEMON_RELEASED, INSTALL_COMMAND } from "@/components/marketing/site"
import { getPool } from "@/lib/session"

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
  const [balance, machines, jobs] = await Promise.all([
    getBalance(db, { accountType: "organization", accountId: organizationId }),
    listMachines(db, organizationId),
    listJobs(db, organizationId, 1),
  ])
  const online = machines.filter((m) => m.presence === "online").length
  // Set up means a job has run here. Until then the page is a checklist.
  const ready = jobs.length > 0
  const owner = roleAtLeast(membership.role, "owner")
  const perMinute = privatePoolUsdPerMinute(RUNNER_SIZES[0].vcpus)

  return (
    <AccountPage title={installation.accountLogin}>
      {installed && (
        <p role="status" className="rounded-lg border p-4">
          HiveWorker is installed on{" "}
          <strong>{installation.accountLogin}</strong>, with ${TRIAL_GRANT_USD}{" "}
          of credit to start. Two steps to your first job.
        </p>
      )}
      {installation.status !== "active" && (
        <p role="alert" className="rounded-lg border border-destructive p-4">
          {installation.status === "suspended"
            ? "The GitHub App is suspended on this organization. Jobs will not be picked up until an owner unsuspends it on GitHub."
            : "The GitHub App was uninstalled from this organization. Reinstall it to run jobs; your balance and history are kept."}
        </p>
      )}

      {!ready && (
        <FirstJob
          machines={machines.length}
          online={online}
          jobs={jobs.length}
          organization={installation.accountLogin}
        />
      )}

      <div className="grid gap-4 sm:grid-cols-2">
        <Card>
          <CardHeader>
            <CardDescription>Balance</CardDescription>
            <CardTitle className="text-3xl tabular-nums">
              {formatMoney(balance)}
            </CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            About{" "}
            {Math.max(
              0,
              Math.floor(balance / 1_000_000 / perMinute.net)
            ).toLocaleString("en-US")}{" "}
            minutes on a {RUNNER_SIZES[0].vcpus}-vCPU runner.{" "}
            <Link className="underline" href="/wallet">
              {balance > 0 ? "Billing" : "Add funds"}
            </Link>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardDescription>Machines</CardDescription>
            <CardTitle className="text-3xl tabular-nums">
              {machines.length}
            </CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            {machines.length ? (
              <>
                {online} ready for jobs,{" "}
                {machines.filter((m) => m.presence === "busy").length} running
                one.{" "}
                <Link className="underline" href="/machines">
                  See the pool
                </Link>
              </>
            ) : (
              <>No Macs in this pool yet. Adding one is step 2 above.</>
            )}
          </CardContent>
        </Card>
      </div>

      {ready && (
        <section className="space-y-3">
          <h2 className="text-lg font-semibold">Use it in a workflow</h2>
          <p className="text-muted-foreground">
            Change one line. Jobs wait in GitHub&apos;s queue until a machine in
            your pool is free.
          </p>
          <pre className="overflow-x-auto rounded-lg border bg-muted p-4 text-sm">
            <code>{`jobs:\n  test:\n    runs-on: ${RUNNER_SIZES[0].label}`}</code>
          </pre>
          <ul className="text-sm text-muted-foreground">
            {RUNNER_SIZES.map((size) => (
              <li key={size.label}>
                <code>{size.label}</code>: {size.vcpus} vCPUs
              </li>
            ))}
          </ul>
        </section>
      )}
      {installation.repositorySelection === "selected" && (
        <p className="text-sm text-muted-foreground">
          The App is installed on selected repositories only. Workflows in other
          repositories will not reach this pool.
        </p>
      )}

      {owner && (
        <p className="text-sm text-muted-foreground">
          Any member of {installation.accountLogin} on GitHub can add a machine.{" "}
          <Link className="underline" href="/settings">
            Narrow it to teams
          </Link>
          .
        </p>
      )}
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
            ? `${machines === 1 ? "A Mac is" : `${machines} Macs are`} in the pool`
            : "A Mac is in the pool, but not connected right now"
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
                On any Apple Silicon Mac, paste this into Terminal and sign in
                with GitHub when the browser opens. Anyone in {organization}{" "}
                can. It asks for no password.
              </p>
            ) : (
              <p>
                The installer is not released yet. This is the command that will
                add a Mac, and it does not work today.
              </p>
            )}
            <pre className="overflow-x-auto rounded-lg border bg-muted p-3 text-sm">
              <code>{INSTALL_COMMAND}</code>
            </pre>
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
          <pre className="overflow-x-auto rounded-lg border bg-muted p-3 text-sm">
            <code>{`jobs:\n  test:\n    runs-on: ${RUNNER_SIZES[0].label}`}</code>
          </pre>
          <p>
            Push it, and the job runs on your Mac. This page updates when it
            does.
          </p>
        </>
      ),
    },
  ]
  return (
    <section className="rounded-lg border">
      <h2 className="border-b p-4 text-lg font-semibold">
        Get your first job running
      </h2>
      <ol className="divide-y">
        {steps.map((step, index) => (
          <li key={step.title} className="flex gap-3 p-4">
            <span
              aria-hidden="true"
              className={
                step.done
                  ? "flex size-6 shrink-0 items-center justify-center rounded-full bg-primary text-sm font-semibold text-primary-foreground"
                  : "flex size-6 shrink-0 items-center justify-center rounded-full border text-sm text-muted-foreground"
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
                <div className="flex flex-col gap-2 text-sm text-muted-foreground">
                  {step.body}
                </div>
              ) : null}
            </div>
          </li>
        ))}
      </ol>
    </section>
  )
}

function NoPool() {
  return (
    <AccountPage title="No pool yet">
      <p className="text-muted-foreground">
        A pool belongs to a GitHub organization. Install HiveWorker on yours, or
        ask an owner to: once it is installed, every member can sign in and add
        a machine.
      </p>
      <div className="flex flex-wrap gap-3">
        <Button render={<RouteLink to="/install" />}>Install on GitHub</Button>
        <Button variant="outline" render={<RouteLink to="/github/sync" />}>
          Check my organizations again
        </Button>
      </div>
    </AccountPage>
  )
}
