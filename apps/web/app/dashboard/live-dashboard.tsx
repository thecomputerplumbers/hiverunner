"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { RUNNER_SIZES, privatePoolUsdPerMinute } from "@workspace/pricing"
import { cn } from "@workspace/ui/lib/utils"

import { CombLegend, CombVersions, PoolComb } from "@/components/console/comb"
import {
  behind,
  formatMinutes,
  formatMoney,
  formatUsd,
} from "@/components/console/format"
import { InvitePanel } from "@/components/console/invite"
import { JobTable, RunningJobs } from "@/components/console/jobs"
import { useLive } from "@/components/console/live"
import { Panel } from "@/components/console/panel"

/**
 * The pool, now. Everything here redraws from the polled snapshot; the
 * checklist above it is the server's, so the page is refreshed when the
 * things it counts change.
 */
export function LiveDashboard({
  invite,
  released,
}: {
  invite: { organization: string; url: string; teams: string[] }
  /** The daemon version currently released, to mark machines behind it. */
  released?: string | null
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
  // Until a job has started the checklist above is the page, so there is no
  // total to show yet, no pane of running jobs to keep empty beside the hive,
  // and no empty comb before the first Mac.
  const started = worked || running.length > 0
  const stale = machines.some((machine) =>
    behind(machine.daemonVersion, released)
  )

  return (
    <>
      {worked ? <Savings lifetime={lifetime} /> : null}

      {machines.length || started ? (
        <div className={cn("grid gap-8", started && "lg:grid-cols-5")}>
          <Panel
            title="your hive"
            aside={
              <Link href="/machines" className="hover:text-foreground">
                {machines.length} {machines.length === 1 ? "Mac" : "Macs"}
              </Link>
            }
            className={started ? "lg:col-span-3" : undefined}
            bodyClassName="flex flex-col gap-6"
          >
            <PoolComb
              machines={machines}
              running={running}
              seat={
                machines.length
                  ? { label: "+ invite", href: "#invite" }
                  : { label: "your Mac" }
              }
              released={released}
            />
            <CombLegend machines={machines} running={running} />
            {stale ? (
              <CombVersions machines={machines} released={released} />
            ) : null}
          </Panel>
          {started ? (
            <Panel
              title="running now"
              aside={<span className="tabular-nums">{running.length}</span>}
              className="lg:col-span-2"
            >
              <RunningJobs jobs={running} earnsBack={earnsBack} />
            </Panel>
          ) : null}
        </div>
      ) : null}

      {worked ? (
        <Panel
          title="latest jobs"
          aside={
            <>
              <Link
                href="/docs/labels-and-sizes"
                className="hover:text-foreground"
              >
                runs-on labels
              </Link>
              <Link href="/jobs" className="hover:text-foreground">
                all jobs
              </Link>
            </>
          }
        >
          <JobTable jobs={recent.slice(0, 6)} earnsBack={earnsBack} />
        </Panel>
      ) : null}

      <InvitePanel machines={machines} {...invite} />

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
    </>
  )
}

/**
 * The one number the page is for once jobs have run: what this pool has
 * kept, against the list price of the same minutes on GitHub-hosted runners.
 * Two bars to the same scale say the rest.
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
      className="grid items-end gap-x-12 gap-y-6 py-2 lg:grid-cols-2"
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
