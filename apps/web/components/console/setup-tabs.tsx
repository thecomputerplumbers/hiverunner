"use client"

import * as React from "react"
import { cn } from "@workspace/ui/lib/utils"

const TABS = [
  ["hand", "By hand"],
  ["agent", "With your agent"],
] as const

type Tab = (typeof TABS)[number][0]

/**
 * The two ways through setup, in one pane: the steps to do yourself, and the
 * prompt that has a coding agent do them. Both stay in the page, so a step
 * that is ticked off while the other tab is open is there on return.
 */
export function SetupTabs({
  byHand,
  withAgent,
}: {
  byHand: React.ReactNode
  withAgent: React.ReactNode
}) {
  const [tab, setTab] = React.useState<Tab>("hand")
  const id = React.useId()
  const buttons = React.useRef<Partial<Record<Tab, HTMLButtonElement | null>>>(
    {}
  )

  function onKeyDown(event: React.KeyboardEvent) {
    if (event.key !== "ArrowRight" && event.key !== "ArrowLeft") return
    event.preventDefault()
    const next: Tab = tab === "hand" ? "agent" : "hand"
    setTab(next)
    buttons.current[next]?.focus()
  }

  return (
    <>
      <div
        role="tablist"
        aria-label="How to set up"
        onKeyDown={onKeyDown}
        className="flex gap-1 border-b border-border px-2 font-mono text-xs sm:px-4"
      >
        {TABS.map(([key, label]) => (
          <button
            key={key}
            ref={(node) => {
              buttons.current[key] = node
            }}
            type="button"
            role="tab"
            id={`${id}-${key}-tab`}
            aria-selected={tab === key}
            aria-controls={`${id}-${key}`}
            tabIndex={tab === key ? 0 : -1}
            onClick={() => setTab(key)}
            className={cn(
              "-mb-px border-b-2 px-3 py-3 outline-none focus-visible:bg-muted",
              tab === key
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            )}
          >
            {label}
          </button>
        ))}
      </div>
      <div
        role="tabpanel"
        id={`${id}-hand`}
        aria-labelledby={`${id}-hand-tab`}
        hidden={tab !== "hand"}
      >
        {byHand}
      </div>
      <div
        role="tabpanel"
        id={`${id}-agent`}
        aria-labelledby={`${id}-agent-tab`}
        hidden={tab !== "agent"}
        className="p-4 sm:p-6"
      >
        {withAgent}
      </div>
    </>
  )
}
