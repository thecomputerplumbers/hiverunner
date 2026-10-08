"use client"
import { useState } from "react"
import { Button } from "@workspace/ui/components/button"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@workspace/ui/components/field"
import { Input } from "@workspace/ui/components/input"

import { post } from "@/components/action-button"

export function SettingsForm({
  organizationId,
  login,
  allowedTeams,
  earningsDestination,
  macosJobs,
  version,
  editable,
}: {
  organizationId: string
  login: string
  allowedTeams: string[]
  earningsDestination: "organization" | "developer"
  macosJobs: boolean
  version: number
  editable: boolean
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("")
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError("")
    const data = new FormData(event.currentTarget)
    try {
      await post("/api/pool/settings", {
        organizationId,
        version,
        earningsDestination: data.get("earningsDestination"),
        macosJobs: data.get("macosJobs") === "on",
        allowedTeams: String(data.get("allowedTeams") ?? "")
          .split(/[\s,]+/)
          .filter(Boolean),
      })
      window.location.reload()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Please try again")
      setBusy(false)
    }
  }
  return (
    <form className="flex flex-col gap-6" onSubmit={submit}>
      <Field>
        <FieldLabel htmlFor="allowedTeams">
          Teams that can add machines
        </FieldLabel>
        <Input
          id="allowedTeams"
          name="allowedTeams"
          defaultValue={allowedTeams.join(", ")}
          placeholder="Everyone in the organization"
          disabled={!editable || busy}
        />
        <FieldDescription>
          Team slugs from {login} on GitHub, separated by commas. Leave empty to
          let every member add machines. Owners always can.
        </FieldDescription>
      </Field>
      <Field>
        <FieldLabel htmlFor="earningsDestination">
          Who is paid when a machine runs a job
        </FieldLabel>
        <select
          id="earningsDestination"
          name="earningsDestination"
          className="h-10 rounded-md border bg-background px-3"
          defaultValue={earningsDestination}
          disabled={!editable || busy}
        >
          <option value="organization">The organization</option>
          <option value="developer">The developer whose machine it is</option>
        </select>
        <FieldDescription>
          Paying the organization returns most of each job&apos;s price to your
          own balance.
        </FieldDescription>
      </Field>
      <Field>
        <label className="flex items-start gap-3 text-sm">
          <input
            type="checkbox"
            name="macosJobs"
            className="mt-1 size-4"
            defaultChecked={macosJobs}
            disabled={!editable || busy}
          />
          <span>
            <span className="font-medium">
              Allow macOS jobs (<code>runs-on: hive-macos</code>)
            </span>
            <FieldDescription>
              For Xcode and iOS builds. Unlike other jobs, a macOS job runs on a
              member&apos;s Mac itself, as a separate user, not in a VM. It sees
              that Mac&apos;s installed software and network. Turn this on only
              for code you trust, and only Macs whose owners opted in with{" "}
              <code>hived config set macos-jobs on</code> will take them.
            </FieldDescription>
          </span>
        </label>
      </Field>
      {editable ? (
        <div>
          <Button disabled={busy}>Save settings</Button>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">
          Only owners of {login} on GitHub can change these.
        </p>
      )}
      {error && <FieldError>{error}</FieldError>}
    </form>
  )
}
