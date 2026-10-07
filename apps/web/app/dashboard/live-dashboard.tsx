"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { RUNNER_SIZES, privatePoolUsdPerMinute } from "@workspace/pricing"

import {
  CostLines,
  HourHeat,
  JobsPerDay,
  Spark,
  byDay,
} from "@/components/console/charts"
import { CombLegend, PoolComb } from "@/components/console/comb"
import {
  formatMinutes,
  formatMoney,
  formatUsd,
} from "@/components/console/format"
import { InvitePanel } from "@/components/console/invite"
import { JobTable, RunningJobs } from "@/components/console/jobs"
import { useLive } from "@/components/console/live"
import { Panel, Snippet, Stat } from "@/components/console/panel"

/** Cores a Mac keeps for the person using it, as the coordinator does. */
const OWNER_CORES = 2

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
  const { machines, running, recent, hours, balance, earnsBack } = snapshot
  const days = byDay(hours)
  const last24 = hours.slice(-24)
  const sum = (
    rows: typeof hours,
    key: "jobs" | "failed" | "seconds" | "price"
  ) => rows.reduce((total, row) => total + row[key], 0)
  const jobs24 = sum(last24, "jobs")
  const failed24 = sum(last24, "failed")
  const seconds = sum(hours, "seconds")
  const spent = sum(hours, "price") / 1_000_000
  const github = hours.reduce((total, hour) => total + hour.githubUsd, 0)
  const weekSpend = sum(hours.slice(-24 * 7), "price") / 7
  const runway = weekSpend > 0 ? balance / weekSpend : null
  const awake = machines.filter(
    (machine) => machine.presence === "online" || machine.presence === "busy"
  )
  const capacity = awake.reduce(
    (total, machine) => total + Math.max(0, machine.cores - OWNER_CORES),
    0
  )
  const inUse = running.reduce((total, job) => total + job.vcpus, 0)
  const size = RUNNER_SIZES[0]
  const minutesLeft = Math.max(
    0,
    Math.floor(balance / 1_000_000 / privatePoolUsdPerMinute(size.vcpus).net)
  )
  const worked = seconds > 0

  return (
    <>
      <div className="grid gap-4 lg:grid-cols-5">
        <Panel
          title="pool"
          aside={
            <Link href="/machines" className="hover:text-foreground">
              {machines.length} {machines.length === 1 ? "Mac" : "Macs"}
            </Link>
          }
          className="lg:col-span-3"
          bodyClassName="flex flex-col gap-4"
        >
          <PoolComb
            machines={machines}
            running={running}
            seat={{ label: "+ invite", href: "#invite" }}
          />
          <div className="flex flex-col gap-2">
            <div className="flex items-baseline justify-between font-mono text-xs">
              <span className="text-muted-foreground">vCPUs running jobs</span>
              <span className="tabular-nums">
                {inUse} of {capacity}
              </span>
            </div>
            <div
              className="flex h-2 gap-px"
              role="img"
              aria-label={`${inUse} of ${capacity} vCPUs are running jobs`}
            >
              {capacity ? (
                Array.from({ length: Math.min(capacity, 96) }, (_, index) => (
                  <span
                    key={index}
                    className={
                      index <
                      Math.round((inUse / capacity) * Math.min(capacity, 96))
                        ? "flex-1 bg-primary"
                        : "flex-1 bg-foreground/10"
                    }
                  />
                ))
              ) : (
                <span className="flex-1 bg-foreground/10" />
              )}
            </div>
            <CombLegend machines={machines} />
          </div>
        </Panel>
        <Panel
          title="running now"
          aside={<span className="tabular-nums">{running.length}</span>}
          className="lg:col-span-2"
        >
          <RunningJobs jobs={running} earnsBack={earnsBack} />
        </Panel>
      </div>

      {machines.length < 5 ? (
        <InvitePanel machines={machines} {...invite} />
      ) : null}

      <section
        aria-label="Totals"
        className="grid border border-border bg-card/50 sm:grid-cols-2 lg:grid-cols-4 [&>div]:border-border lg:[&>div+div]:border-l sm:[&>div:nth-child(even)]:border-l max-sm:[&>div:nth-child(n+2)]:border-t max-lg:[&>div:nth-child(n+3)]:border-t"
      >
        <Stat label="balance" value={formatMoney(balance)}>
          {runway === null
            ? `About ${minutesLeft.toLocaleString("en-US")} minutes on a ${size.vcpus}-vCPU runner.`
            : `About ${runway >= 365 ? "a year or more" : `${Math.max(1, Math.round(runway))} days`} at this week's pace.`}{" "}
          <Link className="text-foreground underline" href="/wallet">
            {balance > 0 ? "Billing" : "Add funds"}
          </Link>
        </Stat>
        <Stat label="jobs, last 24 h" value={jobs24.toLocaleString("en-US")}>
          <Spark values={last24.map((hour) => hour.jobs)} className="mb-1.5" />
          {jobs24
            ? `${Math.round(((jobs24 - failed24) / jobs24) * 100)}% passed`
            : "None yet today."}
        </Stat>
        <Stat label="runner minutes, 14 days" value={formatMinutes(seconds)}>
          <Spark values={days.map((day) => day.seconds)} className="mb-1.5" />
          {formatUsd(spent)} spent
        </Stat>
        <Stat
          label="saved against GitHub-hosted"
          value={formatUsd(Math.max(0, github - spent))}
        >
          {worked && spent > 0
            ? `${(github / spent).toFixed(1)}× cheaper for the same minutes, at list price.`
            : "Counts from your first job."}
        </Stat>
      </section>

      {worked ? (
        <>
          <div className="grid gap-4 lg:grid-cols-2">
            <Panel title="jobs per day" aside="14 days, UTC">
              <JobsPerDay days={days} />
            </Panel>
            <Panel title="cost of the same minutes" aside="running total">
              <CostLines days={days} />
            </Panel>
          </div>
          <div className="grid gap-4 lg:grid-cols-5">
            <Panel
              title="latest jobs"
              aside={
                <Link href="/jobs" className="hover:text-foreground">
                  all jobs
                </Link>
              }
              className="lg:col-span-3"
            >
              <JobTable jobs={recent} earnsBack={earnsBack} />
            </Panel>
            <Panel title="when the pool works" className="lg:col-span-2">
              <HourHeat hours={hours} />
            </Panel>
          </div>
        </>
      ) : null}

      {machines.length >= 5 ? (
        <InvitePanel machines={machines} {...invite} />
      ) : null}

      <Panel
        title="use it in a workflow"
        bodyClassName="grid gap-4 md:grid-cols-2"
      >
        <div className="flex flex-col gap-3">
          <Snippet>{`jobs:\n  test:\n    runs-on: ${size.label}`}</Snippet>
          <p className="text-sm text-muted-foreground">
            Change one line. Jobs wait in GitHub&apos;s queue until a Mac in
            your pool is free.
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
    </>
  )
}
