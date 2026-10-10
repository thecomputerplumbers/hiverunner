"use client"

import { cn } from "@workspace/ui/lib/utils"

import { CopyButton } from "@/components/console/invite"

/**
 * The other way through setup: one prompt for the person's coding agent.
 * The prompt is passed in because it names this deployment's own origin,
 * which only the server knows.
 */
export function AgentPrompt({
  prompt,
  quiet,
  className,
}: {
  prompt: string
  /** Beside a page's own main button, this one steps back to an outline. */
  quiet?: boolean
  className?: string
}) {
  return (
    <section
      aria-label="Set up with your agent"
      className={cn(
        "flex flex-col gap-4 border border-border bg-card/50 p-4 sm:p-6",
        className
      )}
    >
      <div className="flex flex-col gap-2">
        <h2 className="flex items-center gap-2.5 font-mono text-[0.8125rem]">
          <span aria-hidden="true" className="h-2.5 w-2 bg-primary hex" />
          or let your agent set it up
        </h2>
        <p className="text-sm leading-6 text-muted-foreground">
          Paste one prompt into Claude Code, Cursor or Codex. It connects to
          HiveWorker, tells you the few things only you can do, and makes the
          workflow change.
        </p>
      </div>
      <div>
        <CopyButton text={prompt} variant={quiet ? "outline" : "default"}>
          Copy the prompt
        </CopyButton>
      </div>
      <details className="group text-sm">
        <summary className="cursor-pointer list-none font-mono text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:underline [&::-webkit-details-marker]:hidden">
          <span className="group-open:hidden">Read the prompt</span>
          <span className="hidden group-open:inline">Hide the prompt</span>
        </summary>
        <pre className="mt-3 max-h-80 overflow-auto bg-code px-4 py-3 font-mono text-xs leading-6 whitespace-pre-wrap text-code-foreground">
          {prompt}
        </pre>
      </details>
    </section>
  )
}
