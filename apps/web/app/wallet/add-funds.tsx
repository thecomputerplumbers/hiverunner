"use client"

import { useId, useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { FieldError } from "@workspace/ui/components/field"
import { Input } from "@workspace/ui/components/input"

import { checkboxClass, fieldHeight } from "@/components/form-controls"

/**
 * Starts a card payment: one click for an amount on offer, or any whole
 * number of dollars from `min` to `max`. `offerAuto` shows the box that also
 * saves the card and keeps the balance topped up; it is hidden once that is
 * already on.
 */
export function AddFunds({
  amounts,
  min,
  max,
  below,
  offerAuto,
}: {
  amounts: readonly number[]
  min: number
  max: number
  below: number
  offerAuto: boolean
}) {
  /** What is being paid, and whether it was typed rather than clicked. */
  const [pending, setPending] = useState<{
    amount: number
    typed: boolean
  } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [auto, setAuto] = useState(offerAuto)
  const [typed, setTyped] = useState("")
  const id = useId()

  const own = /^\d+$/.test(typed) ? Number(typed) : null
  const ownAllowed = own !== null && own >= min && own <= max
  const limits = `$${min} to $${max.toLocaleString("en-US")}`

  async function pay(amount: number, typed = false) {
    setPending({ amount, typed })
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
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <p className="font-mono text-xs text-muted-foreground">quick add</p>
        <div className="flex flex-wrap gap-2">
          {amounts.map((amount) => (
            <Button
              key={amount}
              variant={amount === amounts[1] ? "default" : "outline"}
              disabled={pending !== null}
              onClick={() => pay(amount)}
            >
              {pending?.amount === amount && !pending.typed
                ? "Opening checkout…"
                : `Add $${amount}`}
            </Button>
          ))}
        </div>
      </div>
      <form
        className="flex flex-col gap-1.5"
        onSubmit={(event) => {
          event.preventDefault()
          if (ownAllowed && pending === null) void pay(own, true)
        }}
      >
        <label
          htmlFor={`${id}-own`}
          className="font-mono text-xs text-muted-foreground"
        >
          or a specific amount
        </label>
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative w-32">
            <span
              aria-hidden="true"
              className="pointer-events-none absolute inset-y-0 left-2.5 flex items-center text-sm text-muted-foreground"
            >
              $
            </span>
            <Input
              id={`${id}-own`}
              className={`${fieldHeight} pl-6 tabular-nums`}
              inputMode="numeric"
              autoComplete="off"
              placeholder={String(min)}
              maxLength={String(max).length}
              value={typed}
              aria-invalid={typed !== "" && !ownAllowed}
              aria-describedby={`${id}-own-hint`}
              disabled={pending !== null}
              onChange={(event) =>
                setTyped(event.target.value.replace(/\D/g, ""))
              }
            />
          </div>
          <Button
            type="submit"
            variant="outline"
            className={fieldHeight}
            disabled={!ownAllowed || pending !== null}
          >
            {pending?.typed
              ? "Opening checkout…"
              : ownAllowed
                ? `Add $${own}`
                : "Add"}
          </Button>
        </div>
        <p id={`${id}-own-hint`} className="text-xs text-muted-foreground">
          Whole dollars, {limits}.
          {offerAuto && auto
            ? " A specific amount is paid once and does not turn on automatic top-up."
            : ""}
        </p>
      </form>
      {offerAuto && (
        <label htmlFor={id} className="flex items-start gap-3 text-sm">
          <input
            id={id}
            type="checkbox"
            className={`mt-0.5 ${checkboxClass}`}
            checked={auto}
            onChange={(event) => setAuto(event.target.checked)}
          />
          <span>
            Keep it topped up. With a quick add, save this card and add the same
            amount again whenever the balance falls below ${below}. You can turn
            this off at any time.
          </span>
        </label>
      )}
      {error && <FieldError>{error}</FieldError>}
    </div>
  )
}
