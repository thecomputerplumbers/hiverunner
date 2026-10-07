"use client"

import * as React from "react"
import type { LiveJob } from "@workspace/db/live"
import { cn } from "@workspace/ui/lib/utils"

import { JobTable } from "@/components/console/jobs"
import { useLive } from "@/components/console/live"
import { Panel } from "@/components/console/panel"

const filters = {
  all: () => true,
  running: (job: LiveJob) => job.completedAt === null,
  failed: (job: LiveJob) => job.result === "failed",
} as const

/**
 * The pool's jobs, newest first. The page's own list is the history; jobs
 * the poller has seen since are laid over it, so a job appears when it
 * starts and settles when it ends without a reload.
 */
export function JobList({ jobs }: { jobs: LiveJob[] }) {
  const { snapshot } = useLive()
  const [filter, setFilter] = React.useState<keyof typeof filters>("all")
  const fresh = new Map(
    [...(snapshot?.recent ?? []), ...(snapshot?.running ?? [])].map((job) => [
      job.id,
      job,
    ])
  )
  const known = new Set(jobs.map((job) => job.id))
  const merged = [
    ...[...fresh.values()].filter((job) => !known.has(job.id)),
    ...jobs.map((job) => fresh.get(job.id) ?? job),
  ].sort((a, b) => b.startedAt - a.startedAt)
  const shown = merged.filter(filters[filter])
  return (
    <Panel
      title="jobs"
      aside={
        <div role="group" aria-label="Show" className="flex gap-1">
          {(Object.keys(filters) as Array<keyof typeof filters>).map((key) => (
            <button
              key={key}
              type="button"
              aria-pressed={filter === key}
              onClick={() => setFilter(key)}
              className={cn(
                "px-2 py-1 outline-none focus-visible:ring-2 focus-visible:ring-ring",
                filter === key
                  ? "bg-primary text-primary-foreground"
                  : "hover:text-foreground"
              )}
            >
              {key}{" "}
              <span className="tabular-nums opacity-70">
                {merged.filter(filters[key]).length}
              </span>
            </button>
          ))}
        </div>
      }
    >
      <JobTable
        jobs={shown}
        earnsBack={snapshot?.earnsBack ?? true}
        empty={
          merged.length ? (
            `No ${filter} jobs among the last ${merged.length}.`
          ) : (
            <>
              No jobs yet. Set <code>runs-on: hive</code> in a workflow to send
              one.
            </>
          )
        }
      />
    </Panel>
  )
}
