"use client"

import * as React from "react"
import type { LiveMachine } from "@workspace/db/live"
import { Button } from "@workspace/ui/components/button"

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

/**
 * The ask to bring teammates in: why, the link, and who it works for. One
 * Mac is a pool that sleeps when its owner does, so the reason is said in
 * terms of that Mac; it shrinks to a line once the comb has filled.
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
  const more = Math.max(1, 5 - count)
  const message = `Add your Mac to the ${organization} hive. It runs our CI in a VM on your spare cores. One command, sign in with GitHub, about two minutes: ${url}`

  if (count >= 5)
    return (
      <div
        id="invite"
        className="flex scroll-mt-24 flex-wrap items-center justify-between gap-3 border border-border bg-card/50 px-4 py-4 sm:px-6"
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
      title="invite teammates"
      aside={`${count} ${lonely ? "Mac" : "Macs"} so far`}
      className="scroll-mt-24"
      bodyClassName="flex flex-col gap-5"
    >
      <p className="max-w-2xl">
        {lonely ? (
          <>
            <strong>{machines[0]!.name}</strong> is the whole hive. When its lid
            closes, every job waits.
          </>
        ) : (
          <>
            {count} Macs share the work. Every one more is another laptop that
            can be asleep without a job waiting.
          </>
        )}{" "}
        With {more} more like {lonely ? "it" : "these"},{" "}
        <span className="text-primary">{now + more * typical} jobs</span> could
        run at once instead of {now}.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <code className="min-w-0 flex-1 basis-72 truncate border border-border bg-background px-3 py-2 font-mono text-xs">
          {url}
        </code>
        <CopyButton text={url}>Copy invite link</CopyButton>
        <CopyButton text={message} variant="outline">
          Copy a message for Slack
        </CopyButton>
      </div>
      <p className="max-w-2xl text-sm text-muted-foreground">
        The link works for{" "}
        {teams.length
          ? `members of ${teams.join(", ")} in ${organization}`
          : `anyone in ${organization} on GitHub`}
        , and nobody else. They sign in and paste one command. It costs them
        nothing, and nobody has to approve it.
      </p>
    </Panel>
  )
}
