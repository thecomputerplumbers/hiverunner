"use client"
import { useState } from "react"
import { authClient } from "@workspace/auth/client"
import { Button } from "@workspace/ui/components/button"
import { Input } from "@workspace/ui/components/input"
import { Field, FieldLabel, FieldError } from "@workspace/ui/components/field"
export function AccountForm({ name }: { name: string }) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [message, setMessage] = useState("")
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError("")
    setMessage("")
    const data = new FormData(event.currentTarget)
    try {
      const result = await authClient.updateUser({
        name: String(data.get("name")),
      })
      if (result.error)
        throw new Error(result.error.message ?? "Please try again")
      setMessage("Changes saved.")
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Please try again")
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <Field>
          <FieldLabel htmlFor="name">Name</FieldLabel>
          <Input
            id="name"
            name="name"
            defaultValue={name}
            required
            maxLength={100}
          />
        </Field>
        <Button disabled={busy}>Save name</Button>
      </form>
      {error && <FieldError>{error}</FieldError>}
      {message && <p role="status">{message}</p>}
    </>
  )
}
