"use client"
import { useState } from "react"
import { Button } from "@workspace/ui/components/button"
import { Input } from "@workspace/ui/components/input"
import { Textarea } from "@workspace/ui/components/textarea"
import { Field, FieldLabel, FieldError } from "@workspace/ui/components/field"
import { fieldHeight, selectClass } from "@/components/form-controls"
export function ProjectForm({
  organizationId,
  project,
}: {
  organizationId: string
  project?: {
    id: string
    name: string
    description: string
    status: string
    version: number
  }
}) {
  const [id] = useState(() => project?.id ?? crypto.randomUUID()),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [fields, setFields] = useState<Record<string, string>>({})
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError("")
    setFields({})
    const form = new FormData(event.currentTarget)
    try {
      const response = await fetch("/api/projects", {
        method: project ? "PATCH" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          organizationId,
          id,
          version: project?.version,
          name: form.get("name"),
          description: form.get("description"),
          status: form.get("status") ?? "active",
        }),
      })
      const result = (await response.json()) as {
        id: string
        fields?: Record<string, string>
        error?: string
        requestId?: string
      }
      if (!response.ok) {
        setFields(result.fields ?? {})
        throw new Error(
          `${result.error ?? "Could not save project"}${result.requestId ? ` (Reference: ${result.requestId})` : ""}`
        )
      }
      window.location.assign(`/projects/${result.id}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Please try again")
      setBusy(false)
    }
  }
  return (
    <form className="flex max-w-xl flex-col gap-5" onSubmit={submit}>
      <Field>
        <FieldLabel htmlFor="project-name">Project name</FieldLabel>
        <Input
          id="project-name"
          name="name"
          className={fieldHeight}
          defaultValue={project?.name}
          maxLength={100}
          required
          aria-invalid={!!fields.name}
        />
        {fields.name && <FieldError>{fields.name}</FieldError>}
      </Field>
      <Field>
        <FieldLabel htmlFor="project-description">Description</FieldLabel>
        <Textarea
          id="project-description"
          name="description"
          defaultValue={project?.description}
          maxLength={2000}
          aria-invalid={!!fields.description}
        />
        {fields.description && <FieldError>{fields.description}</FieldError>}
      </Field>
      {project && (
        <Field>
          <FieldLabel htmlFor="project-status">Status</FieldLabel>
          <select
            id="project-status"
            name="status"
            defaultValue={project.status}
            className={selectClass}
          >
            <option value="active">Active</option>
            <option value="archived">Archived</option>
          </select>
        </Field>
      )}
      {error && <FieldError role="alert">{error}</FieldError>}
      <Button disabled={busy} type="submit">
        {busy ? "Saving…" : project ? "Save project" : "Create project"}
      </Button>
    </form>
  )
}
