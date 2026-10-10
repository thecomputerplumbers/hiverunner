import { provisionInstallation } from "@workspace/db/installations"
import {
  repairActiveOrganization,
  syncMemberships,
} from "@workspace/db/membership"

import { getDb } from "@/db"
import { auth } from "@/lib/auth"
import { logError } from "@/lib/diagnostics"
import { consentReturnPath, getGitHubApp, githubUserToken } from "@/lib/github"
import { seeOther } from "@/lib/redirect"

const status = (state: string) => seeOther(`/github/status?state=${state}`)

/**
 * Where GitHub sends the browser after the App is installed.
 *
 * The `installation_id` in this URL is whatever the visitor typed. It is
 * trusted only after GitHub confirms, with the visitor's own token, that
 * they can see that installation.
 */
export async function GET(request: Request) {
  const url = new URL(request.url)
  const session = await auth.api.getSession({ headers: request.headers })
  if (!session)
    return seeOther(
      `/sign-in?next=${encodeURIComponent(url.pathname + url.search)}`
    )

  // Set by `/install?next=` when the install began on an agent's consent
  // page. It has been through GitHub and the address bar: checked again here.
  const consent = consentReturnPath(url.searchParams.get("state"))

  // A non-owner can only ask; an owner approves it on GitHub later.
  if (url.searchParams.get("setup_action") === "request")
    return status(consent ? "requested-connection" : "requested")

  const installationId = Number(url.searchParams.get("installation_id"))
  if (!Number.isSafeInteger(installationId) || installationId <= 0)
    return status("invalid")

  const app = getGitHubApp()
  if (!app) return status("unconfigured")
  const userToken = await githubUserToken(session.user.id)
  if (!userToken) return status("github-required")

  try {
    const visible = await app.listUserInstallationIds(userToken)
    if (!visible.includes(installationId)) return status("denied")
    const installation = await app.getInstallation(installationId)
    if (!installation) return status("denied")

    // The webhook usually got here first. If it has not, do its work now
    // rather than showing an empty dashboard; both paths are idempotent.
    const db = getDb()
    const provisioned = await provisionInstallation(db, installation)
    if (!provisioned.supported) return status("personal")

    const { organizationIds } = await syncMemberships(db, app, {
      userId: session.user.id,
      userToken,
    })
    if (!organizationIds.includes(provisioned.organizationId))
      return status("not-member")
    await repairActiveOrganization(db, {
      userId: session.user.id,
      organizationIds,
      prefer: provisioned.organizationId,
    })
    return seeOther(consent ?? "/dashboard?installed=1")
  } catch (error) {
    logError(error, "github.setup")
    return status("unavailable")
  }
}
