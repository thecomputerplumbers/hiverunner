"use client"

import { useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { FieldError } from "@workspace/ui/components/field"

/** Starts a card payment for one of the amounts on offer. */
export function AddFunds({ amounts }: { amounts: readonly number[] }) {
  const [pending, setPending] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function pay(amount: number) {
    setPending(amount)
    setError(null)
    try {
      const response = await fetch("/api/wallet/top-up", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ amount }),
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
      {error && <FieldError>{error}</FieldError>}
    </div>
  )
}
