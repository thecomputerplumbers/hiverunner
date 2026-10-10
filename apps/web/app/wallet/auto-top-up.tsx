"use client"

import { useId, useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { FieldError } from "@workspace/ui/components/field"

import { selectClass } from "@/components/form-controls"

/**
 * The standing instruction to keep the balance up, written as the sentence
 * it is, with each part of it a choice.
 */
export function AutoTopUp({
  enabled,
  amount,
  below,
  cap,
  amounts,
  belows,
  caps,
  card,
  chargedThisMonth,
}: {
  enabled: boolean
  amount: number
  below: number
  /** Null is no monthly limit. */
  cap: number | null
  amounts: readonly number[]
  belows: readonly number[]
  caps: readonly number[]
  card: string
  chargedThisMonth: number
}) {
  const [draft, setDraft] = useState({ amount, below, cap })
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const id = useId()
  const changed =
    draft.amount !== amount || draft.below !== below || draft.cap !== cap
  const capTooLow = draft.cap !== null && draft.cap < draft.amount

  async function save(next: { enabled: boolean }) {
    setPending(true)
    setError(null)
    try {
      const response = await fetch("/api/wallet/auto-top-up", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...next, ...draft }),
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

  const reachedCap = enabled && cap !== null && chargedThisMonth + amount > cap

  return (
    <div className="flex flex-col gap-4">
      <p>
        <strong className="font-medium">{enabled ? "On." : "Off."}</strong>{" "}
        {enabled
          ? `Charges ${card}.`
          : `Nothing is charged unless you add funds yourself. ${card[0]!.toUpperCase()}${card.slice(1)} is saved.`}
      </p>
      <div className="flex flex-col gap-3 text-sm">
        <label
          htmlFor={`${id}-below`}
          className="flex flex-wrap items-center gap-2"
        >
          When the balance falls below
          <select
            id={`${id}-below`}
            className={selectClass}
            value={draft.below}
            disabled={pending}
            onChange={(event) =>
              setDraft({ ...draft, below: Number(event.target.value) })
            }
          >
            {belows.map((option) => (
              <option key={option} value={option}>
                ${option}
              </option>
            ))}
          </select>
        </label>
        <label
          htmlFor={`${id}-amount`}
          className="flex flex-wrap items-center gap-2"
        >
          add
          <select
            id={`${id}-amount`}
            className={selectClass}
            value={draft.amount}
            disabled={pending}
            onChange={(event) =>
              setDraft({ ...draft, amount: Number(event.target.value) })
            }
          >
            {amounts.map((option) => (
              <option key={option} value={option}>
                ${option}
              </option>
            ))}
          </select>
        </label>
        <label
          htmlFor={`${id}-cap`}
          className="flex flex-wrap items-center gap-2"
        >
          and charge at most
          <select
            id={`${id}-cap`}
            className={selectClass}
            value={draft.cap ?? "none"}
            disabled={pending}
            onChange={(event) =>
              setDraft({
                ...draft,
                cap:
                  event.target.value === "none"
                    ? null
                    : Number(event.target.value),
              })
            }
          >
            <option value="none">any amount</option>
            {caps.map((option) => (
              <option key={option} value={option}>
                ${option}
              </option>
            ))}
          </select>
          a month.
        </label>
      </div>
      <p className="text-sm text-muted-foreground">
        {reachedCap
          ? `This month's limit is reached: $${chargedThisMonth} of $${cap} charged. Automatic top-up resumes on the 1st, and you can still add funds yourself.`
          : `Charged automatically so far this month: $${chargedThisMonth}${cap === null ? "" : ` of $${cap}`}. Payments you make yourself do not count.`}
      </p>
      {capTooLow && (
        <FieldError>
          The monthly limit has to cover at least one top-up of ${draft.amount}.
        </FieldError>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {enabled ? (
          <>
            <Button
              size="sm"
              disabled={pending || !changed || capTooLow}
              onClick={() => save({ enabled: true })}
            >
              Save changes
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={pending}
              onClick={() => save({ enabled: false })}
            >
              Turn off
            </Button>
          </>
        ) : (
          <Button
            size="sm"
            disabled={pending || capTooLow}
            onClick={() => save({ enabled: true })}
          >
            Turn on
          </Button>
        )}
      </div>
      {error && <FieldError>{error}</FieldError>}
    </div>
  )
}
