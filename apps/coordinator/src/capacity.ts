import { drizzle } from "drizzle-orm/d1"
import { getInstallationByOrganization } from "@workspace/db/installations"
import { canAcquireJobs } from "@workspace/db/jobs"
import { macosJobsAllowed } from "@workspace/db/settings"
import { createGitHubApp } from "@workspace/github"
import { runnerSize } from "@workspace/pricing"
import { githubTestAPI } from "./config"
import { verifyActionsIdentity } from "./oidc"

/** Read-only pool routing; this does not reserve a machine or dispatch a job. */
export async function capacityResponse(
  request: Request,
  env: CoordinatorEnv,
  verify: typeof verifyActionsIdentity = verifyActionsIdentity
) {
  const respond = (body: unknown, status = 200) =>
    Response.json(body, { status, headers: { "Cache-Control": "no-store" } })
  if (request.method !== "GET")
    return respond({ error: "Method not allowed" }, 405)
  const url = new URL(request.url)
  const label = url.searchParams.get("label")
  const size = runnerSize(label)
  if (!size) return respond({ error: "Unsupported runner label" }, 400)
  const token = request.headers
    .get("authorization")
    ?.match(/^Bearer (\S+)$/)?.[1]
  if (!token) return respond({ error: "Unauthorized" }, 401)
  try {
    const identity = await verify(token, url.origin)
    if (!identity) return respond({ error: "Unauthorized" }, 401)
    if (
      !env.GITHUB_APP_ID ||
      !env.GITHUB_APP_PRIVATE_KEY ||
      !env.RECEIPT_SIGNING_KEY
    )
      return respond({ error: "Execution is not configured" }, 503)
    const db = drizzle(env.DB)
    const installation = await getInstallationByOrganization(
      db,
      identity.organizationId
    )
    if (
      !installation ||
      installation.status !== "active" ||
      installation.accountType !== "Organization"
    )
      return respond({ error: "No active installation" }, 403)
    if (installation.repositorySelection === "selected") {
      const github = createGitHubApp({
        appId: env.GITHUB_APP_ID,
        privateKey: env.GITHUB_APP_PRIVATE_KEY,
        webhookSecret: "",
        apiUrl: githubTestAPI(env),
      })
      if (
        !(await github.canAccessRepository(
          installation.installationId,
          identity.repository,
          identity.repositoryId
        ))
      )
        return respond({ error: "Repository is not installed" }, 403)
    }
    const free =
      (await canAcquireJobs(db, identity.organizationId)) &&
      // A macOS job runs on a member's Mac itself; the hive must allow it.
      (size.arch !== "macos" ||
        (await macosJobsAllowed(db, identity.organizationId)))
        ? await env.POOL.getByName(identity.organizationId).availableCapacity(
            identity.organizationId,
            { vcpus: size.vcpus, arch: size.arch }
          )
        : 0
    // A free machine goes to the jobs already queued for it first. Counted
    // free while they wait, it took a workflow's jobs behind them: three
    // pushes at once queued nine 4-core jobs behind one free Mac.
    const waiting = free
      ? await env.LISTENER.getByName(
          `${identity.organizationId}:${size.label}`
        ).waiting()
      : 0
    return respond({ available: free > waiting, label: size.label })
  } catch {
    // Neither credentials nor upstream responses belong in diagnostics.
    return respond({ error: "Capacity temporarily unavailable" }, 503)
  }
}
