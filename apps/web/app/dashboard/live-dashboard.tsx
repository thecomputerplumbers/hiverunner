"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { RUNNER_SIZES, privatePoolUsdPerMinute } from "@workspace/pricing"

import { CombLegend, PoolComb } from "@/components/console/comb"
import {
  formatMinutes,
  formatMoney,
  formatUsd,
} from "@/components/console/format"
import { InvitePanel } from "@/components/console/invite"
import { JobTable, RunningJobs } from "@/components/console/jobs"
import { useLive } from "@/components/console/live"
import { Panel, Snippet } from "@/components/console/panel"

/**
 * The pool, now. Everything here redraws from the polled snapshot; the
 * checklist above it is the server's, so the page is refreshed when the
 * things it counts change.
 */
export function LiveDashboard({
  invite,
}: {
  invite: { organization: string; url: string; teams: string[] }
}) {
  const { snapshot } = useLive()
  const router = useRouter()
  const counted = snapshot
    ? `${snapshot.machines.length}:${snapshot.machines.some((machine) => machine.presence !== "offline")}:${snapshot.recent.length > 0}`
    : ""
  const before = React.useRef(counted)
  React.useEffect(() => {
    if (before.current !== counted) router.refresh()
    before.current = counted
  }, [counted, router])

  if (!snapshot) return null
  const { machines, running, recent, balance, earnsBack, lifetime, hours } =
    snapshot
  const weekSpend =
    hours.slice(-24 * 7).reduce((total, hour) => total + hour.price, 0) / 7
  const runway = weekSpend > 0 ? balance / weekSpend : null
  const size = RUNNER_SIZES[0]
  const minutesLeft = Math.max(
    0,
    Math.floor(balance / 1_000_000 / privatePoolUsdPerMinute(size.vcpus).net)
  )
  const worked = lifetime.jobs > 0

  return (
    <>
      <Savings lifetime={lifetime} />

      <div className="grid gap-6 lg:grid-cols-5">
        <Panel
          title="hive"
          aside={
            <Link href="/machines" className="hover:text-foreground">
              {machines.length} {machines.length === 1 ? "Mac" : "Macs"}
            </Link>
          }
          className="lg:col-span-3"
          bodyClassName="flex flex-col gap-5"
        >
          <PoolComb
            machines={machines}
            running={running}
            seat={{ label: "+ invite", href: "#invite" }}
          />
          <CombLegend machines={machines} />
        </Panel>
        <Panel
          title="running now"
          aside={<span className="tabular-nums">{running.length}</span>}
          className="lg:col-span-2"
        >
          <RunningJobs jobs={running} earnsBack={earnsBack} />
        </Panel>
      </div>

      <InvitePanel machines={machines} {...invite} />

      {worked ? (
        <Panel
          title="latest jobs"
          aside={
            <Link href="/jobs" className="hover:text-foreground">
              all jobs
            </Link>
          }
        >
          <JobTable jobs={recent.slice(0, 6)} earnsBack={earnsBack} />
        </Panel>
      ) : null}

      <p className="text-sm text-muted-foreground">
        <span className="font-mono text-foreground tabular-nums">
          {formatMoney(balance)}
        </span>{" "}
        of credit left, about{" "}
        {runway === null
          ? `${minutesLeft.toLocaleString("en-US")} minutes on a ${size.vcpus}-vCPU runner`
          : `${runway >= 365 ? "a year or more" : `${Math.max(1, Math.round(runway))} days`} at this week's pace`}
        .{" "}
        <Link className="text-foreground underline" href="/wallet">
          {balance > 0 ? "Billing" : "Add funds"}
        </Link>
      </p>

      {worked ? null : <Workflow />}
    </>
  )
}

/**
 * The one number the page is for: what this pool has kept, against the list
 * price of the same minutes on GitHub-hosted runners. Two bars to the same
 * scale say the rest.
 */
function Savings({
  lifetime,
}: {
  lifetime: NonNullable<ReturnType<typeof useLive>["snapshot"]>["lifetime"]
}) {
  const spent = lifetime.price / 1_000_000
  const saved = Math.max(0, lifetime.githubUsd - spent)
  const bars: Array<[string, number, string]> = [
    ["GitHub-hosted", lifetime.githubUsd, "bg-foreground/25"],
    ["this hive", spent, "bg-primary"],
  ]
  return (
    <section
      aria-label="Savings"
      className="grid items-end gap-x-12 gap-y-6 py-4 lg:grid-cols-2"
    >
      <div className="flex flex-col gap-2">
        <p className="font-mono text-sm text-muted-foreground">saved so far</p>
        <p className="font-mono text-6xl font-medium tracking-tight text-primary tabular-nums sm:text-7xl">
          {formatUsd(saved)}
        </p>
        <p className="max-w-md text-muted-foreground">
          {lifetime.jobs && spent > 0 ? (
            <>
              <span className="text-foreground">
                {(lifetime.githubUsd / spent).toFixed(1)}× cheaper
              </span>{" "}
              than GitHub-hosted runners, over {formatMinutes(lifetime.seconds)}{" "}
              minutes and {lifetime.jobs.toLocaleString("en-US")} jobs.
            </>
          ) : (
            "Counts from your first job, against GitHub-hosted list price for the same minutes."
          )}
        </p>
      </div>
      {lifetime.jobs ? (
        <dl className="flex flex-col gap-4 font-mono text-xs">
          {bars.map(([label, usd, tone]) => (
            <div key={label} className="flex flex-col gap-1.5">
              <div className="flex items-baseline justify-between">
                <dt className="text-muted-foreground">{label}</dt>
                <dd className="text-sm tabular-nums">{formatUsd(usd)}</dd>
              </div>
              <span className="block h-3 bg-foreground/[0.04]">
                <span
                  className={`block h-full ${tone}`}
                  style={{
                    width: `${Math.max(1, (usd / Math.max(lifetime.githubUsd, 0.0001)) * 100)}%`,
                  }}
                />
              </span>
            </div>
          ))}
        </dl>
      ) : null}
    </section>
  )
}

function Workflow() {
  const size = RUNNER_SIZES[0]
  return (
    <Panel
      title="use it in a workflow"
      bodyClassName="grid gap-6 md:grid-cols-2"
    >
      <div className="flex flex-col gap-3">
        <Snippet>{`jobs:\n  test:\n    runs-on: ${size.label}`}</Snippet>
        <p className="text-sm text-muted-foreground">
          Change one line. Jobs wait in GitHub&apos;s queue until a Mac in your
          hive is free.
        </p>
      </div>
      <table className="h-fit w-full font-mono text-xs">
        <thead>
          <tr className="text-left text-muted-foreground [&>th]:pb-2 [&>th]:font-normal">
            <th>runs-on</th>
            <th>vCPUs</th>
            <th className="text-right">per minute</th>
          </tr>
        </thead>
        <tbody>
          {RUNNER_SIZES.map((runner) => (
            <tr
              key={runner.label}
              className="border-t border-border [&>td]:py-2"
            >
              <td className="text-primary">{runner.label}</td>
              <td>{runner.vcpus}</td>
              <td className="text-right tabular-nums">
                ${privatePoolUsdPerMinute(runner.vcpus).net.toFixed(5)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Panel>
  )
}
