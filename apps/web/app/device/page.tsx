import { headers } from "next/headers"
import { redirect } from "next/navigation"
import { listOrganizations } from "@workspace/auth"

import { AuthPage } from "@/components/auth-page"
import { NoHiveSteps } from "@/components/no-hive-steps"
import { getDb } from "@/db"
import { auth } from "@/lib/auth"
import { getServerSession } from "@/lib/session"
import { DeviceForm } from "./device-form"

export const metadata = { title: "Connect a machine" }

/**
 * Where the daemon sends the person to approve it. They are already signed
 * in with GitHub, or are about to be; the code ties this browser to that
 * daemon.
 */
export default async function DevicePage({
  searchParams,
}: {
  searchParams: Promise<{ user_code?: string }>
}) {
  const { user_code: userCode } = await searchParams
  const here = `/device${userCode ? `?user_code=${encodeURIComponent(userCode)}` : ""}`
  const session = await getServerSession()
  if (!session) redirect(`/sign-in?next=${encodeURIComponent(here)}`)

  // Sign-in just synced memberships with GitHub. Approving with none would
  // only move the dead end into the terminal, so it is explained here.
  const pools = await listOrganizations(getDb(), session.user.id)
  if (!pools.length) return <NoHive here={here} />

  let pending = false
  if (userCode) {
    try {
      const result = await auth.api.deviceVerify({
        query: { user_code: userCode },
        headers: await headers(),
      })
      pending = result.status === "pending"
    } catch {
      pending = false
    }
  }

  return (
    <AuthPage title="Connect a machine">
      <DeviceForm
        userCode={pending ? userCode : undefined}
        invalid={Boolean(userCode) && !pending}
        name={session.user.name}
        hives={pools.map((pool) => pool.name)}
      />
    </AuthPage>
  )
}

function NoHive({ here }: { here: string }) {
  return (
    <AuthPage title="You are not in a hive yet" wide>
      <NoHiveSteps syncTo={here} />
    </AuthPage>
  )
}
