"use client"
import { useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { Field, FieldLabel, FieldError } from "@workspace/ui/components/field"
import { post } from "@/components/action-button"
import { selectClass } from "@/components/form-controls"
const labels: Record<string, string> = {
  openid: "Identify your account",
  profile: "Read your name",
  email: "Read your email address",
  offline_access: "Stay connected until revoked",
  "hive:read":
    "Read the selected hive: machines, jobs, balance, settings and receipts",
  "hive:write":
    "Change the selected hive's settings, budget and machines (owners and admins only)",
}
export function ConsentForm({
  organizations,
  scopes,
}: {
  organizations: Array<{ id: string; name: string }>
  scopes: string[]
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("")
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError("")
    const data = new FormData(event.currentTarget)
    const accept =
      (event.nativeEvent as SubmitEvent).submitter?.getAttribute("value") !==
      "deny"
    try {
      const result = await post("/api/connect", {
        accept,
        organizationId: data.get("organizationId"),
        oauthQuery: window.location.search.slice(1),
      })
      if (!result.url) throw new Error("No redirect returned")
      window.location.assign(result.url)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Please try again")
      setBusy(false)
    }
  }
  return (
    <form className="flex flex-col gap-5" onSubmit={submit}>
      <ul className="list-disc space-y-2 pl-5 marker:text-primary">
        {scopes.map((scope) => (
          <li key={scope}>{labels[scope] ?? scope}</li>
        ))}
      </ul>
      <Field>
        <FieldLabel htmlFor="organization">Hive</FieldLabel>
        <select
          className={selectClass}
          id="organization"
          name="organizationId"
          required
          disabled={busy}
        >
          {organizations.map((org) => (
            <option key={org.id} value={org.id}>
              {org.name}
            </option>
          ))}
        </select>
      </Field>
      <p className="text-sm text-muted-foreground">
        This connection can access only the hive you select. You can revoke it
        from Agents.
      </p>
      {error && <FieldError>{error}</FieldError>}
      <div className="flex gap-3">
        <Button
          disabled={busy || !organizations.length}
          type="submit"
          value="allow"
        >
          Allow access
        </Button>
        <Button
          variant="outline"
          disabled={busy}
          formNoValidate
          type="submit"
          value="deny"
        >
          Deny
        </Button>
      </div>
    </form>
  )
}
