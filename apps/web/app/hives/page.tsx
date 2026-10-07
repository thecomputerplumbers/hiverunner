import { listOrganizations } from "@workspace/auth"
import { redirect } from "next/navigation"

import { getDb } from "@/db"
import { requireServerSession } from "@/lib/session"
import { SelectOrganization } from "./select-organization"

export const metadata = { title: "Switch hive" }

export default async function PoolsPage() {
  const session = await requireServerSession()
  const pools = await listOrganizations(getDb(), session.user.id)
  if (!pools.length) redirect("/dashboard")
  return (
    <main className="flex min-h-screen items-center justify-center p-6">
      <SelectOrganization
        organizations={pools.map(({ id, name }) => ({ id, name }))}
      />
    </main>
  )
}
