"use client"

import { useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { FieldError } from "@workspace/ui/components/field"

/** The standing instruction to keep the balance up, and the way to change it. */
export function AutoTopUp({
  enabled,
  amount,
  amounts,
  below,
  card,
}: {
  enabled: boolean
  amount: number
  amounts: readonly number[]
  below: number
  card: string
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function save(next: { enabled: boolean; amount?: number }) {
    setPending(true)
    setError(null)
    try {
      const response = await fetch("/api/wallet/auto-top-up", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(next),
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
    <div className="flex flex-col gap-3">
      <p>
        {enabled ? (
          <>
            <strong>On.</strong> When the balance falls below ${below}, $
            {amount} is charged to {card}.
          </>
        ) : (
          <>
            <strong>Off.</strong> Nothing is charged unless you add funds
            yourself. {card} is saved.
          </>
        )}
      </p>
      <div className="flex flex-wrap items-center gap-2">
        {enabled &&
          amounts.map((offered) => (
            <Button
              key={offered}
              size="sm"
              variant={offered === amount ? "secondary" : "outline"}
              disabled={pending || offered === amount}
              onClick={() => save({ enabled: true, amount: offered })}
            >
              ${offered}
            </Button>
          ))}
        <Button
          size="sm"
          variant={enabled ? "outline" : "default"}
          disabled={pending}
          onClick={() => save({ enabled: !enabled })}
        >
          {enabled ? "Turn off" : `Turn on, $${amount} at a time`}
        </Button>
      </div>
      {error && <FieldError>{error}</FieldError>}
    </div>
  )
}
