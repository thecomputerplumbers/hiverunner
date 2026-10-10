"use client"

import { useState } from "react"
import { authClient } from "@workspace/auth/client"
import { Button } from "@workspace/ui/components/button"
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@workspace/ui/components/card"
import { FieldError } from "@workspace/ui/components/field"

export function SelectOrganization({
  organizations,
}: {
  organizations: Array<{ id: string; name: string }>
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function select(organizationId: string) {
    setPending(true)
    setError(null)
    try {
      const result = await authClient.organization.setActive({ organizationId })
      if (result.error)
        throw new Error(
          result.error.message ?? "Could not select the organization."
        )
      window.location.assign("/dashboard")
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "Could not select the organization."
      )
      setPending(false)
    }
  }

  return (
    <Card className="w-full max-w-sm">
      <CardHeader>
        <CardTitle className="font-normal">Choose a hive</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {organizations.map((organization) => (
          <Button
            key={organization.id}
            disabled={pending}
            onClick={() => select(organization.id)}
          >
            {organization.name}
          </Button>
        ))}
        {error && <FieldError>{error}</FieldError>}
      </CardContent>
    </Card>
  )
}
