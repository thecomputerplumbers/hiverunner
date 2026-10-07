"use client"

import Link from "next/link"
import type { LiveJob } from "@workspace/db/live"
import { JOB_TAX_RATE } from "@workspace/pricing"
import { cn } from "@workspace/ui/lib/utils"

import {
  formatAgo,
  formatDuration,
  formatElapsed,
  formatMoney,
  formatUtc,
} from "./format"
import { useNow } from "./live"

const SPINNER = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"]

export function ResultGlyph({
  result,
  className,
}: {
  result: string | null
  className?: string
}) {
  const now = useNow()
  if (result === null)
    return (
      <span
        role="img"
        aria-label="Running"
        className={cn("font-mono text-foreground", className)}
      >
        {SPINNER[Math.floor(now / 1000) % SPINNER.length]}
      </span>
    )
  const [glyph, tone] =
    result === "succeeded"
      ? ["✓", "text-muted-foreground"]
      : result === "failed"
        ? ["✗", "text-destructive"]
        : ["⊘", "text-muted-foreground"]
  return (
    <span
      role="img"
      aria-label={result}
      className={cn("font-mono", tone, className)}
    >
      {glyph}
    </span>
  )
}

/**
 * What a running job has cost so far, to the second. An estimate until
 * GitHub reports the job finished and the price is settled.
 */
export function runningPrice(job: LiveJob, now: number, earnsBack: boolean) {
  const minutes = Math.max(0, now - job.startedAt) / 60_000
  const charge = minutes * job.vcpus * job.speedScore * job.rate
  return charge * (earnsBack ? JOB_TAX_RATE : 1)
}

const repo = (repository: string) => repository.split("/").pop() ?? repository

/** The jobs on a machine right now, each with a clock and a running price. */
export function RunningJobs({
  jobs,
  earnsBack,
}: {
  jobs: LiveJob[]
  earnsBack: boolean
}) {
  const now = useNow()
  if (!jobs.length)
    return (
      <p className="font-mono text-sm text-muted-foreground">
        <span aria-hidden="true" className="text-primary">
          ▍
        </span>{" "}
        idle. Nothing is running; the next job starts on a ready Mac.
      </p>
    )
  return (
    <ul className="flex flex-col divide-y divide-border">
      {jobs.map((job) => (
        <li key={job.id}>
          <Link
            href={`/jobs/${encodeURIComponent(job.id)}`}
            className="grid grid-cols-[auto_1fr_auto] items-center gap-x-3 gap-y-1 py-2.5 font-mono text-xs outline-none first:pt-0 hover:bg-muted/60 focus-visible:bg-muted/60"
          >
            <ResultGlyph result={null} className="text-base" />
            <span className="min-w-0 truncate text-sm">
              <span className="text-muted-foreground">
                {repo(job.repository)}/
              </span>
              {job.jobName || job.label}
            </span>
            <span className="text-right text-sm tabular-nums">
              {formatElapsed(now - job.startedAt)}
            </span>
            <span />
            <span className="min-w-0 truncate text-muted-foreground">
              {job.label} on {job.machine}
            </span>
            <span className="text-right text-muted-foreground tabular-nums">
              ≈{formatMoney(runningPrice(job, now, earnsBack), "fine")}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  )
}

/**
 * Jobs as a terminal would list them: one line each, the result first, the
 * price last, and a bar for how long it ran against the longest in view.
 */
export function JobTable({
  jobs,
  earnsBack,
  empty,
}: {
  jobs: LiveJob[]
  earnsBack: boolean
  empty?: React.ReactNode
}) {
  const now = useNow()
  if (!jobs.length)
    return <div className="text-sm text-muted-foreground">{empty}</div>
  const took = (job: LiveJob) => (job.completedAt ?? now) - job.startedAt
  const longest = Math.max(1, ...jobs.map(took))
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[34rem] border-collapse font-mono text-xs">
        <thead>
          <tr className="text-left whitespace-nowrap text-muted-foreground [&>th]:pb-2 [&>th]:font-normal">
            <th className="w-6">
              <span className="sr-only">Result</span>
            </th>
            <th>job</th>
            <th className="hidden md:table-cell">runs-on</th>
            <th className="hidden sm:table-cell">mac</th>
            <th className="hidden lg:table-cell">started</th>
            <th className="w-40">took</th>
            <th className="text-right">price</th>
          </tr>
        </thead>
        <tbody>
          {jobs.map((job) => {
            const href = `/jobs/${encodeURIComponent(job.id)}`
            const running = job.completedAt === null
            return (
              <tr
                key={job.id}
                className="group border-t border-border [&>td]:py-2 [&>td]:pr-3 last:[&>td]:pr-0"
              >
                <td>
                  <ResultGlyph result={job.result} className="text-sm" />
                </td>
                <td className="w-[40%] max-w-0 truncate">
                  <Link
                    href={href}
                    className="outline-none group-hover:text-primary focus-visible:text-primary"
                  >
                    <span className="text-muted-foreground group-hover:text-primary/70">
                      {repo(job.repository)}/
                    </span>
                    {job.jobName || job.label}
                  </Link>
                </td>
                <td className="hidden whitespace-nowrap text-muted-foreground md:table-cell">
                  {job.label}
                </td>
                <td className="hidden max-w-32 truncate text-muted-foreground sm:table-cell">
                  {job.machine}
                </td>
                <td
                  className="hidden whitespace-nowrap text-muted-foreground lg:table-cell"
                  title={`${formatUtc(job.startedAt)} UTC`}
                >
                  {formatAgo(job.startedAt, now)}
                </td>
                <td>
                  <span className="flex items-center gap-2">
                    <span className="w-14 shrink-0 tabular-nums">
                      {running
                        ? formatElapsed(took(job))
                        : formatDuration(took(job))}
                    </span>
                    <span className="h-1 flex-1 bg-foreground/[0.07]">
                      <span
                        className={cn(
                          "block h-full",
                          running
                            ? "bg-primary"
                            : job.result === "failed"
                              ? "bg-destructive/70"
                              : "bg-foreground/40"
                        )}
                        style={{
                          width: `${Math.max(2, (took(job) / longest) * 100)}%`,
                        }}
                      />
                    </span>
                  </span>
                </td>
                <td className="text-right whitespace-nowrap tabular-nums">
                  {running
                    ? `≈${formatMoney(runningPrice(job, now, earnsBack), "fine")}`
                    : formatMoney(job.price ?? 0, "fine")}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
