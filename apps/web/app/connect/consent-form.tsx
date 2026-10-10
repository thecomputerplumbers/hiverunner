"use client"
import { useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { FieldError } from "@workspace/ui/components/field"
import { post } from "@/components/action-button"
/** What each permission allows, naming the hive it is being given for. */
const labels = (hive: string): Record<string, string> => ({
  openid: "Identify your account",
  profile: "Read your name",
  email: "Read your email address",
  offline_access: "Stay connected until revoked",
  "hive:read": `Read ${hive}: machines, jobs, balance, settings and receipts`,
  "hive:write": `Change ${hive}'s settings, budget and machines (owners and admins only)`,
})
export function ConsentForm({
  organizations,
  scopes,
  current,
}: {
  organizations: Array<{ id: string; name: string }>
  scopes: string[]
  /** The hive the person is working in now, offered first. */
  current?: string | null
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("")
  const [selected, setSelected] = useState(
    organizations.find((org) => org.id === current)?.id ?? organizations[0]?.id
  )
  const hive = organizations.find((org) => org.id === selected)
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError("")
    const accept =
      (event.nativeEvent as SubmitEvent).submitter?.getAttribute("value") !==
      "deny"
    try {
      const result = await post("/api/connect", {
        accept,
        organizationId: selected,
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
    <form className="flex flex-col gap-6" onSubmit={submit}>
      {organizations.length > 0 && (
        <fieldset className="flex flex-col gap-2" disabled={busy}>
          <legend className="mb-2 text-sm font-medium">
            {organizations.length === 1
              ? "It will be connected to this hive"
              : "Which hive should it be connected to?"}
          </legend>
          {organizations.map((org) => (
            <label
              key={org.id}
              className="flex cursor-pointer items-center gap-3 border border-border bg-background px-4 py-3 has-checked:border-primary has-checked:bg-primary/10 has-focus-visible:ring-3 has-focus-visible:ring-ring/50"
            >
              <input
                type="radio"
                name="organizationId"
                value={org.id}
                checked={org.id === selected}
                onChange={() => setSelected(org.id)}
                className="sr-only"
              />
              <span
                aria-hidden="true"
                className={
                  org.id === selected
                    ? "h-4 w-3.5 shrink-0 bg-primary hex"
                    : "h-4 w-3.5 shrink-0 bg-muted hex"
                }
              />
              <span className="min-w-0 flex-1 truncate font-mono text-sm">
                {org.name}
              </span>
              {org.id === current && (
                <span className="shrink-0 text-xs text-muted-foreground">
                  open in your console
                </span>
              )}
            </label>
          ))}
        </fieldset>
      )}
      {hive && (
        <div className="flex flex-col gap-2">
          <p className="text-sm font-medium">It will be able to</p>
          <ul className="list-disc space-y-2 pl-5 text-sm marker:text-primary">
            {scopes.map((scope) => (
              <li key={scope}>{labels(hive.name)[scope] ?? scope}</li>
            ))}
          </ul>
        </div>
      )}
      <p className="text-sm text-muted-foreground">
        This connection can reach only{" "}
        {hive ? hive.name : "the hive you select"}, and nothing in your other
        organizations. You can revoke it from Agents.
      </p>
      {error && <FieldError>{error}</FieldError>}
      <div className="flex gap-3">
        <Button
          disabled={busy || !organizations.length}
          type="submit"
          value="allow"
        >
          {hive ? `Allow access to ${hive.name}` : "Allow access"}
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
