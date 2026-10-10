"use client"

import { useId, useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { FieldError } from "@workspace/ui/components/field"
import { Input } from "@workspace/ui/components/input"

import { fieldHeight, selectClass } from "@/components/form-controls"

const money = (dollars: number) =>
  dollars.toLocaleString("en-US", { style: "currency", currency: "USD" })

/**
 * What the hive has spent on jobs this month, against the budget it set for
 * itself if it has one, and the form that sets it.
 */
export function Budget({
  spent,
  budget,
  atLimit,
  max,
  mayChange,
  hive,
}: {
  /** Dollars spent on jobs so far this calendar month. */
  spent: number
  /** The monthly budget in whole dollars; null is none. */
  budget: number | null
  atLimit: "warn" | "pause"
  max: number
  mayChange: boolean
  hive: string
}) {
  const [typed, setTyped] = useState(budget === null ? "" : String(budget))
  const [limit, setLimit] = useState(atLimit)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const id = useId()

  const amount = /^\d+$/.test(typed) ? Number(typed) : null
  const allowed = amount !== null && amount >= 1 && amount <= max
  const changed = amount !== budget || limit !== atLimit
  const share = budget ? Math.min(1, spent / budget) : 0
  const over = budget !== null && spent >= budget

  async function save(next: number | null) {
    setPending(true)
    setError(null)
    try {
      const response = await fetch("/api/wallet/budget", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ budget: next, atLimit: limit }),
      })
      const body = (await response.json().catch(() => ({}))) as {
        error?: string
      }
      if (!response.ok) throw new Error(body.error ?? "Could not save.")
      window.location.reload()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not save.")
      setPending(false)
    }
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,18rem)_1fr]">
      <div className="flex flex-col gap-2">
        <p className="font-mono text-2xl font-medium tracking-tight tabular-nums">
          {money(spent)}
          {budget !== null && (
            <span className="text-base text-muted-foreground">
              {" "}
              of {money(budget)}
            </span>
          )}
        </p>
        {budget !== null && (
          <div
            role="progressbar"
            aria-label="Share of this month's budget spent"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(share * 100)}
            className="h-2.5 w-full bg-muted"
          >
            <div
              className={over ? "h-full bg-destructive" : "h-full bee-stripes"}
              style={{ width: `${share * 100}%` }}
            />
          </div>
        )}
        <p className="text-sm text-muted-foreground">
          Spent on jobs this month: what {hive} was charged, less what its own
          machines earned back. Adding funds is not spending.
        </p>
      </div>

      {mayChange ? (
        <form
          className="flex flex-col gap-4 text-sm"
          onSubmit={(event) => {
            event.preventDefault()
            if (allowed && changed && !pending) void save(amount)
          }}
        >
          <label
            htmlFor={`${id}-amount`}
            className="flex flex-wrap items-center gap-2"
          >
            Spend no more than
            <span className="relative w-28">
              <span
                aria-hidden="true"
                className="pointer-events-none absolute inset-y-0 left-2.5 flex items-center text-muted-foreground"
              >
                $
              </span>
              <Input
                id={`${id}-amount`}
                className={`${fieldHeight} pl-6 tabular-nums`}
                inputMode="numeric"
                autoComplete="off"
                placeholder="none"
                maxLength={String(max).length}
                value={typed}
                aria-invalid={typed !== "" && !allowed}
                disabled={pending}
                onChange={(event) =>
                  setTyped(event.target.value.replace(/\D/g, ""))
                }
              />
            </span>
            a month on jobs.
          </label>
          <label
            htmlFor={`${id}-limit`}
            className="flex flex-wrap items-center gap-2"
          >
            At the limit,
            <select
              id={`${id}-limit`}
              className={selectClass}
              value={limit}
              disabled={pending}
              onChange={(event) =>
                setLimit(event.target.value === "pause" ? "pause" : "warn")
              }
            >
              <option value="warn">email us and keep running jobs</option>
              <option value="pause">
                email us and hold new jobs until next month
              </option>
            </select>
          </label>
          <p className="text-muted-foreground">
            Owners and admins are emailed at 80% and again at the limit. A job
            already running is never cut off, so the month can end a little
            over.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button type="submit" disabled={!allowed || !changed || pending}>
              {pending
                ? "Saving…"
                : budget === null
                  ? "Set budget"
                  : "Save budget"}
            </Button>
            {budget !== null && (
              <Button
                type="button"
                variant="outline"
                disabled={pending}
                onClick={() => save(null)}
              >
                Remove budget
              </Button>
            )}
          </div>
          {error && <FieldError>{error}</FieldError>}
        </form>
      ) : (
        <p className="text-muted-foreground">
          {budget === null
            ? `No budget is set. An owner or admin of ${hive} can set one.`
            : atLimit === "pause"
              ? "At the limit, new jobs are held until next month."
              : "At the limit, owners and admins are emailed and jobs keep running."}
        </p>
      )}
    </div>
  )
}
