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
  hives,
}: {
  userCode?: string
  invalid: boolean
  name: string
  /**
   * Every hive the person belongs to. With one, that is where the machine
   * goes. With several the daemon asks in the terminal, because approving
   * here only says who the machine belongs to.
   */
  hives: string[]
}) {
  const only = hives.length === 1 ? hives[0] : null
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
        {done === "denied"
          ? "Denied. Nothing was connected."
          : only
            ? `Approved. This machine is joining ${only}. You can close this tab; the daemon will finish on its own.`
            : "Approved. Go back to the terminal: the daemon asks there which hive this machine should serve, unless you already named one."}
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
        A HiveWorker daemon is asking to add this machine
        {only ? (
          <>
            {" "}
            to <strong className="font-medium text-foreground">{only}</strong>
          </>
        ) : null}
        , as <strong className="font-medium text-foreground">{name}</strong>.
        Approve only if this code matches the one it is showing.
      </p>
      {only ? null : (
        <div className="flex flex-col gap-2 text-sm">
          <p className="text-muted-foreground">
            You are in {hives.length} hives. Approving does not choose one: the
            daemon asks in the terminal which of these the machine should serve,
            unless you already named one there.
          </p>
          <ul className="flex flex-col gap-1.5 font-mono">
            {hives.map((hive) => (
              <li key={hive} className="flex items-center gap-2.5">
                <span
                  aria-hidden="true"
                  className="h-3 w-2.5 shrink-0 bg-primary hex"
                />
                {hive}
              </li>
            ))}
          </ul>
        </div>
      )}
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
