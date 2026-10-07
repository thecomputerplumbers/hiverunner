"use client"

import { useId, useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { FieldError } from "@workspace/ui/components/field"

/**
 * Starts a card payment for one of the amounts on offer. `offerAuto` shows
 * the box that also saves the card and keeps the balance topped up; it is
 * hidden once that is already on.
 */
export function AddFunds({
  amounts,
  below,
  offerAuto,
}: {
  amounts: readonly number[]
  below: number
  offerAuto: boolean
}) {
  const [pending, setPending] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [auto, setAuto] = useState(offerAuto)
  const id = useId()

  async function pay(amount: number) {
    setPending(amount)
    setError(null)
    try {
      const response = await fetch("/api/wallet/top-up", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ amount, auto: offerAuto && auto }),
      })
      const body = (await response.json().catch(() => ({}))) as {
        url?: string
        error?: string
      }
      if (!response.ok || !body.url)
        throw new Error(body.error ?? "Could not start the payment.")
      window.location.assign(body.url)
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "Could not start the payment."
      )
      setPending(null)
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap gap-2">
        {amounts.map((amount) => (
          <Button
            key={amount}
            variant={amount === amounts[1] ? "default" : "outline"}
            disabled={pending !== null}
            onClick={() => pay(amount)}
          >
            {pending === amount ? "Opening checkout…" : `Add $${amount}`}
          </Button>
        ))}
      </div>
      {offerAuto && (
        <label htmlFor={id} className="flex items-start gap-2 text-sm">
          <input
            id={id}
            type="checkbox"
            className="mt-0.5 size-4 accent-primary"
            checked={auto}
            onChange={(event) => setAuto(event.target.checked)}
          />
          <span>
            Keep it topped up. Save this card and add the same amount again
            whenever the balance falls below ${below}. You can turn this off at
            any time.
          </span>
        </label>
      )}
      {error && <FieldError>{error}</FieldError>}
    </div>
  )
}
