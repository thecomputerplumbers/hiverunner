"use client"

import type { LiveMachine } from "@workspace/db/live"
import { OWNER_CORES } from "@workspace/pricing"
import { cn } from "@workspace/ui/lib/utils"

import { ActionButton } from "@/components/action-button"
import { PRESENCE } from "@/components/console/comb"
import {
  behind,
  formatAgo,
  formatMinutes,
  gib,
} from "@/components/console/format"
import { ResultGlyph } from "@/components/console/jobs"
import { useLive, useNow } from "@/components/console/live"

const dot: Record<string, string> = {
  busy: "bg-primary motion-safe:animate-pulse",
  online: "border border-primary bg-primary/20",
  paused: "border border-dashed border-foreground/60",
  offline: "bg-foreground/20",
}

/**
 * One box for each Mac in the pool: its cores drawn one square apiece, the
 * job it is running, and what it has done lately.
 */

export function MachineGrid({
  organizationId,
  userId,
  owner,
  released,
}: {
  organizationId: string
  userId: string
  owner: boolean
  /** The daemon version currently released, to mark machines behind it. */
  released?: string | null
}) {
  const { snapshot } = useLive()
  const now = useNow()
  if (!snapshot) return null
  const fastest = Math.max(1, ...snapshot.machines.map((m) => m.speedScore))
  return (
    <ul className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      {snapshot.machines.map((machine) => {
        const jobs = snapshot.running.filter(
          (job) => job.machineId === machine.id
        )
        const inUse = jobs.reduce((sum, job) => sum + job.vcpus, 0)
        const presence = jobs.length ? "busy" : machine.presence
        return (
          <li
            key={machine.id}
            id={machine.id}
            // Read by the daemon's end-to-end test; keep it on the card.
            data-presence={presence}
            className="flex scroll-mt-20 flex-col border border-border bg-card/50 target:border-primary"
          >
            <header className="flex min-h-12 items-center justify-between gap-3 border-b border-border px-4 font-mono text-xs sm:px-6">
              <h2 className="flex min-w-0 items-center gap-2.5 text-[0.8125rem] font-normal">
                <span
                  aria-hidden="true"
                  className={cn("size-2 shrink-0 rounded-full", dot[presence])}
                />
                <span className="truncate">{machine.name}</span>
              </h2>
              <span
                className={
                  presence === "busy" ? "text-primary" : "text-muted-foreground"
                }
              >
                {PRESENCE[presence] ?? presence}
              </span>
            </header>
            <div className="flex flex-1 flex-col gap-5 p-4 sm:p-6">
              <Cores machine={machine} inUse={inUse} />
              <dl className="grid grid-cols-3 gap-3 font-mono text-xs [&_dd]:text-sm [&_dd]:tabular-nums [&_dt]:text-muted-foreground">
                <div>
                  <dt>speed</dt>
                  <dd>{machine.speedScore.toFixed(2)}×</dd>
                  <span className="mt-1.5 block h-1 bg-foreground/10">
                    <span
                      className="block h-full bg-foreground/50"
                      style={{
                        width: `${(machine.speedScore / fastest) * 100}%`,
                      }}
                    />
                  </span>
                </div>
                <div>
                  <dt>jobs, 14 d</dt>
                  <dd>{machine.jobs.toLocaleString("en-US")}</dd>
                </div>
                <div>
                  <dt>minutes, 14 d</dt>
                  <dd>{formatMinutes(machine.seconds)}</dd>
                </div>
              </dl>
              {jobs.length ? (
                <ul className="flex flex-col gap-1 font-mono text-xs">
                  {jobs.map((job) => (
                    <li key={job.id} className="flex items-center gap-2">
                      <ResultGlyph result={null} />
                      <span className="min-w-0 flex-1 truncate">
                        {job.repository.split("/").pop()}/
                        {job.jobName || job.label}
                      </span>
                      <span className="text-muted-foreground">
                        {job.vcpus} vcpu
                      </span>
                    </li>
                  ))}
                </ul>
              ) : null}
              <div className="mt-auto flex flex-wrap items-end justify-between gap-3">
                <p className="font-mono text-xs leading-5 text-muted-foreground">
                  {machine.model} · {gib(machine.memoryBytes)}
                  {machine.macos && (
                    <>
                      {" "}
                      ·{" "}
                      <span
                        className="text-foreground"
                        title="Its owner opted in to hive-macos jobs, which run on the Mac itself"
                      >
                        macOS jobs
                      </span>
                    </>
                  )}
                  {!machine.macos && machine.macosProblem && (
                    <>
                      {" "}
                      ·{" "}
                      <span
                        className="text-destructive"
                        title={machine.macosProblem}
                      >
                        macOS jobs off: needs its owner
                      </span>
                    </>
                  )}
                  <br />
                  {machine.owner} ·{" "}
                  {machine.lastSeenAt
                    ? presence === "offline"
                      ? `seen ${formatAgo(machine.lastSeenAt, now)}`
                      : "connected"
                    : "not connected yet"}
                  {" · "}
                  <span
                    title={
                      behind(machine.daemonVersion, released)
                        ? `${released} is released. The daemon updates itself when idle unless its owner turned that off; or they run: hived update`
                        : "The daemon's version"
                    }
                  >
                    hived {machine.daemonVersion}
                    {behind(machine.daemonVersion, released) && (
                      <span className="text-foreground">
                        {" "}
                        (update available)
                      </span>
                    )}
                  </span>
                </p>
                {(owner || machine.userId === userId) && (
                  <ActionButton
                    label="Remove from this hive"
                    path="/api/machines/revoke"
                    body={{ machineId: machine.id, organizationId }}
                  />
                )}
              </div>
            </div>
          </li>
        )
      })}
    </ul>
  )
}

function Cores({ machine, inUse }: { machine: LiveMachine; inUse: number }) {
  const forJobs = Math.max(0, machine.cores - OWNER_CORES)
  const awake = machine.presence === "online" || machine.presence === "busy"
  return (
    <div className="flex flex-col gap-1.5">
      <div
        role="img"
        aria-label={`${machine.cores} cores: ${inUse} running jobs, ${forJobs - inUse} free for jobs, ${machine.cores - forJobs} kept for its owner`}
        className="flex flex-wrap gap-1"
      >
        {Array.from({ length: machine.cores }, (_, index) => (
          <span
            key={index}
            className={cn(
              "size-3.5",
              index < inUse
                ? "bee-stripes"
                : index < forJobs
                  ? awake
                    ? "border border-primary/60 bg-primary/10"
                    : "border border-foreground/20"
                  : "bg-[repeating-linear-gradient(135deg,var(--muted-foreground)_0_1px,transparent_1px_4px)] opacity-60"
            )}
          />
        ))}
      </div>
      <p className="font-mono text-xs text-muted-foreground">
        <span className="text-foreground">{inUse}</span> of {forJobs} cores on
        jobs; {machine.cores - forJobs} stay with its owner
      </p>
    </div>
  )
}
