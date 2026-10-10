"use client"
import { useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { Field, FieldError, FieldLabel } from "@workspace/ui/components/field"
import { Input } from "@workspace/ui/components/input"

import { post } from "@/components/action-button"
import { fieldHeight } from "@/components/form-controls"

export function DeviceForm({
  userCode,
  invalid,
  name,
}: {
  userCode?: string
  invalid: boolean
  name: string
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [done, setDone] = useState<"approved" | "denied" | null>(null)

  async function decide(decision: "approve" | "deny") {
    setBusy(true)
    setError("")
    try {
      await post(`/api/auth/device/${decision}`, { userCode })
      setDone(decision === "approve" ? "approved" : "denied")
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Please try again")
      setBusy(false)
    }
  }

  if (done)
    return (
      <p role="status">
        {done === "approved"
          ? "Approved. You can close this tab; the daemon will finish on its own."
          : "Denied. Nothing was connected."}
      </p>
    )

  if (!userCode)
    return (
      <form className="flex flex-col gap-4" action="/device" method="get">
        {invalid && (
          <FieldError>
            That code is not valid or has expired. Start again from the daemon.
          </FieldError>
        )}
        <Field>
          <FieldLabel htmlFor="user_code">Code shown by the daemon</FieldLabel>
          <Input
            id="user_code"
            name="user_code"
            className={fieldHeight}
            autoComplete="off"
            autoCapitalize="characters"
            required
          />
        </Field>
        <Button>Continue</Button>
      </form>
    )

  return (
    <div className="flex flex-col gap-5">
      <p className="text-muted-foreground">
        A HiveWorker daemon is asking to add this machine to your hive as{" "}
        <strong className="font-medium text-foreground">{name}</strong>. Approve
        only if this code matches the one it is showing.
      </p>
      <p className="text-center font-mono text-3xl tracking-widest">
        {userCode}
      </p>
      <div className="flex gap-3">
        <Button disabled={busy} onClick={() => decide("approve")}>
          Approve
        </Button>
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => decide("deny")}
        >
          Deny
        </Button>
      </div>
      {error && <FieldError>{error}</FieldError>}
    </div>
  )
}
