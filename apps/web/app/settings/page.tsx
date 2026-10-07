import { roleAtLeast } from "@workspace/auth"
import { getPoolSettings } from "@workspace/db/settings"

import { AccountPage } from "@/components/account-page"
import { Panel } from "@/components/console/panel"
import { getDb } from "@/db"
import { requirePool } from "@/lib/session"
import { SettingsForm } from "./settings-form"

export const metadata = { title: "Hive settings" }

export default async function SettingsPage() {
  const { organizationId, installation, membership } = await requirePool()
  const settings = await getPoolSettings(getDb(), organizationId)
  const owner = roleAtLeast(membership.role, "owner")
  return (
    <AccountPage title="settings">
      <p className="max-w-3xl text-sm text-muted-foreground">
        Membership comes from GitHub. Anyone who leaves{" "}
        {installation.accountLogin} loses access here and their machines stop
        taking jobs.
      </p>
      {settings && (
        <Panel title="who can join, and who gets paid" className="max-w-3xl">
          <SettingsForm
            organizationId={organizationId}
            login={installation.accountLogin}
            allowedTeams={settings.allowedTeams}
            earningsDestination={settings.earningsDestination}
            version={settings.version}
            editable={owner}
          />
        </Panel>
      )}
    </AccountPage>
  )
}
