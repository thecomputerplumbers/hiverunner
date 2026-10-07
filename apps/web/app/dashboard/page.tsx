import Link from "next/link"
import { RUNNER_SIZES, privatePoolUsdPerMinute } from "@workspace/pricing"
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
import { formatTokens, formatUsdEstimate } from "@/lib/format"
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
  const [balance, machines] = await Promise.all([
    getBalance(db, { accountType: "organization", accountId: organizationId }),
    listMachines(db, organizationId),
  ])
  const owner = roleAtLeast(membership.role, "owner")
  const perMinute = privatePoolUsdPerMinute(RUNNER_SIZES[0].vcpus)

  return (
    <AccountPage title={installation.accountLogin}>
      {installed && (
        <p role="status" className="rounded-lg border p-4">
          HiveWorker is installed on{" "}
          <strong>{installation.accountLogin}</strong>. Add a machine to start
          running jobs.
        </p>
      )}
      {installation.status !== "active" && (
        <p role="alert" className="rounded-lg border border-destructive p-4">
          {installation.status === "suspended"
            ? "The GitHub App is suspended on this organization. Jobs will not be picked up until an owner unsuspends it on GitHub."
            : "The GitHub App was uninstalled from this organization. Reinstall it to run jobs; your balance and history are kept."}
        </p>
      )}

      <div className="grid gap-4 sm:grid-cols-2">
        <Card>
          <CardHeader>
            <CardDescription>Balance</CardDescription>
            <CardTitle className="text-3xl tabular-nums">
              {formatTokens(balance)}
            </CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            About {formatUsdEstimate(balance)}, or{" "}
            {Math.floor(balance / 1_000_000 / perMinute.net).toLocaleString(
              "en-US"
            )}{" "}
            minutes on a {RUNNER_SIZES[0].vcpus}-vCPU runner.{" "}
            <Link className="underline" href="/wallet">
              Wallet
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
                {machines.filter((m) => m.presence === "online").length} ready
                for jobs, {machines.filter((m) => m.presence === "busy").length}{" "}
                running one.{" "}
                <Link className="underline" href="/machines">
                  See the pool
                </Link>
              </>
            ) : (
              <>
                No machines in this pool yet.{" "}
                <Link className="underline" href="/download">
                  Install the daemon
                </Link>{" "}
                on an Apple Silicon Mac and sign in with GitHub.
              </>
            )}
          </CardContent>
        </Card>
      </div>

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
        {installation.repositorySelection === "selected" && (
          <p className="text-sm text-muted-foreground">
            The App is installed on selected repositories only. Workflows in
            other repositories will not reach this pool.
          </p>
        )}
      </section>

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
