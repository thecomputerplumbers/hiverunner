"use client"

import * as React from "react"
import type { LiveMachine } from "@workspace/db/live"
import { Button } from "@workspace/ui/components/button"
import { cn } from "@workspace/ui/lib/utils"

import { Panel } from "./panel"

/** Cores a Mac keeps for its owner, and the most jobs one Mac takes. */
const OWNER_CORES = 2
const MAX_JOBS = 8

/** How many 2-vCPU jobs these Macs can run side by side. */
export const parallelJobs = (machines: LiveMachine[]) =>
  machines.reduce(
    (sum, machine) =>
      sum + Math.min(MAX_JOBS, Math.floor((machine.cores - OWNER_CORES) / 2)),
    0
  )

export function CopyButton({
  text,
  children,
  variant = "default",
  className,
}: {
  text: string
  children: React.ReactNode
  variant?: "default" | "outline"
  className?: string
}) {
  const [copied, setCopied] = React.useState(false)
  return (
    <Button
      type="button"
      variant={variant}
      className={className}
      onClick={async () => {
        await navigator.clipboard.writeText(text)
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1800)
      }}
    >
      <span aria-live="polite">{copied ? "Copied" : children}</span>
    </Button>
  )
}

const HEX = "M12 1.2 21.35 6.6v10.8L12 22.8 2.65 17.4V6.6z"

/**
 * The ask to bring teammates in. One Mac is a pool that sleeps when its
 * owner does, so with one this is the loudest thing on the page; it quietens
 * as the comb fills.
 */
export function InvitePanel({
  machines,
  organization,
  url,
  teams,
}: {
  machines: LiveMachine[]
  organization: string
  /** The join link. Only members of the organization on GitHub can use it. */
  url: string
  teams: string[]
}) {
  const count = machines.length
  if (!count) return null
  const now = parallelJobs(machines)
  const typical = Math.max(2, Math.round(now / count))
  const lonely = count === 1
  const empty = Math.max(1, 5 - count)
  const message = `Add your Mac to the ${organization} hive. It runs our CI in a VM on your spare cores. One command, sign in with GitHub, about two minutes: ${url}`

  if (count >= 5)
    return (
      <div
        id="invite"
        className="flex flex-wrap items-center justify-between gap-3 border border-border bg-card/50 px-4 py-3"
      >
        <p className="text-sm text-muted-foreground">
          <span className="text-foreground">{count} Macs in the comb.</span>{" "}
          Anyone in {organization} on GitHub can add theirs with this link.
        </p>
        <CopyButton text={url} variant="outline">
          Copy invite link
        </CopyButton>
      </div>
    )

  return (
    <Panel
      id="invite"
      title={lonely ? "it's lonely in here" : "room in the comb"}
      aside={`${count} of ${organization}'s Macs`}
      className={cn(lonely && "border-primary/60")}
      bodyClassName="grid items-center gap-6 md:grid-cols-[auto_1fr]"
    >
      <div
        aria-hidden="true"
        className="flex items-center justify-center gap-1"
      >
        {Array.from({ length: count + empty }, (_, index) => (
          <svg key={index} viewBox="0 0 24 24" className="size-12 sm:size-14">
            <path
              d={HEX}
              strokeWidth={0.8}
              strokeDasharray={index < count ? undefined : "2 1.6"}
              className={
                index < count
                  ? "fill-primary stroke-primary"
                  : "fill-primary/5 stroke-primary/70 motion-safe:animate-pulse"
              }
              style={{ animationDelay: `${(index - count) * 350}ms` }}
            />
            {index >= count ? (
              <text
                x="12"
                y="12.5"
                textAnchor="middle"
                dominantBaseline="central"
                className="fill-primary font-mono text-[9px]"
              >
                ?
              </text>
            ) : null}
          </svg>
        ))}
      </div>
      <div className="flex flex-col gap-3">
        <p className="max-w-2xl text-base">
          {lonely ? (
            <>
              <strong>{machines[0]!.name}</strong> is the whole hive. When its
              lid closes, every job waits. Each teammate who joins keeps the
              queue moving and adds jobs that run side by side.
            </>
          ) : (
            <>
              {count} Macs share the work. Every one more is another laptop that
              can be asleep without a job waiting.
            </>
          )}
        </p>
        <dl className="flex flex-wrap gap-x-8 gap-y-2 font-mono text-xs [&_dd]:text-lg [&_dd]:tabular-nums [&_dt]:text-muted-foreground">
          <div>
            <dt>jobs at once, now</dt>
            <dd>{now}</dd>
          </div>
          <div>
            <dt>
              with {empty} more like {lonely ? "it" : "these"}
            </dt>
            <dd className="text-primary">{now + empty * typical}</dd>
          </div>
          <div>
            <dt>cost to them</dt>
            <dd>$0</dd>
          </div>
        </dl>
        <div className="flex flex-wrap items-center gap-2">
          <CopyButton text={url}>Copy invite link</CopyButton>
          <CopyButton text={message} variant="outline">
            Copy a message for Slack
          </CopyButton>
        </div>
        <p className="font-mono text-xs break-all text-muted-foreground">
          {url}
        </p>
        <p className="max-w-2xl text-sm text-muted-foreground">
          The link works for{" "}
          {teams.length
            ? `members of ${teams.join(", ")} in ${organization}`
            : `anyone in ${organization} on GitHub`}
          , and nobody else. They sign in, paste one command, and their Mac
          takes a cell. No card, and nothing for you to approve.
        </p>
      </div>
    </Panel>
  )
}
