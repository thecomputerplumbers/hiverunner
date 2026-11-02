import { eq } from "drizzle-orm"
import { drizzle } from "drizzle-orm/d1"
import {
  canAcquireJobs,
  jobIdFor,
  settleJob,
  startJob,
} from "@workspace/db/jobs"
import { createReceiptSigner } from "@workspace/db/receipts"
import { githubInstallation, runnerScaleSet } from "@workspace/db/schema"
import { macosJobsAllowed } from "@workspace/db/settings"
import { createGitHubApp } from "@workspace/github"
import { githubTestAPI } from "./config"

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Input = any

const POOL_METHODS = new Set([
  "capacity",
  "offer",
  "offerStatus",
  "start",
  "release",
])

async function authorized(request: Request, env: CoordinatorEnv) {
  if (!env.LISTENER_API_TOKEN) return false
  const digest = async (value: string) =>
    new Uint8Array(
      await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value))
    )
  const given = await digest(request.headers.get("authorization") ?? "")
  const wanted = await digest(`Bearer ${env.LISTENER_API_TOKEN}`)
  return given.every((byte, index) => byte === wanted[index])
}

/**
 * What the listener service may ask of Cloudflare. The keys stay here: it
 * gets installation tokens that expire, and asks for jobs to be recorded and
 * signed rather than signing them itself.
 */
export async function listenerApiResponse(
  request: Request,
  env: CoordinatorEnv,
  operation: string
) {
  if (request.method !== "POST")
    return new Response("Method not allowed", { status: 405 })
  if (!(await authorized(request, env)))
    return new Response("Unauthorized", { status: 401 })
  if (
    !env.GITHUB_APP_ID ||
    !env.GITHUB_APP_PRIVATE_KEY ||
    !env.RECEIPT_SIGNING_KEY
  )
    return new Response("Execution is not configured", { status: 503 })
  const input: Input = await request.json()
  const db = drizzle(env.DB)
  switch (operation) {
    case "installation": {
      const [row] = await db
        .select({
          installationId: githubInstallation.installationId,
          accountLogin: githubInstallation.accountLogin,
          accountType: githubInstallation.accountType,
          status: githubInstallation.status,
        })
        .from(githubInstallation)
        .where(eq(githubInstallation.organizationId, input.organizationId))
        .limit(1)
      return Response.json(row ?? null)
    }
    case "installation-token": {
      const app = createGitHubApp({
        appId: env.GITHUB_APP_ID,
        privateKey: env.GITHUB_APP_PRIVATE_KEY,
        webhookSecret: "",
        apiUrl: githubTestAPI(env),
      })
      return Response.json({
        token: await app.installationToken(input.installationId),
        // GitHub's tokens last an hour; this one is treated as lasting less.
        expiresAt: Date.now() + 30 * 60000,
      })
    }
    case "macos-jobs-allowed":
      return Response.json(await macosJobsAllowed(db, input.organizationId))
    case "can-acquire-jobs":
      return Response.json(await canAcquireJobs(db, input.organizationId))
    case "save-scale-set": {
      const values = {
        installationId: input.installationId,
        scaleSetId: input.scaleSetId,
        runnerGroupId: input.runnerGroupId,
        updatedAt: new Date(),
      }
      await db
        .insert(runnerScaleSet)
        .values({
          organizationId: input.organizationId,
          label: input.label,
          ...values,
        })
        .onConflictDoUpdate({
          target: [runnerScaleSet.organizationId, runnerScaleSet.label],
          set: values,
        })
      return Response.json(null)
    }
    case "start-job":
      return Response.json(
        (await startJob(db, {
          ...input,
          startedAt: new Date(input.startedAt),
        })) ?? null
      )
    case "settle-job": {
      const signer = await createReceiptSigner(env.RECEIPT_SIGNING_KEY)
      return Response.json(
        await settleJob(db, signer, {
          jobId: jobIdFor(input.organizationId, input.runnerRequestId),
          completedAt: new Date(input.completedAt),
          result: input.result,
        })
      )
    }
    case "pool": {
      if (!POOL_METHODS.has(input.method))
        return new Response("Unknown pool method", { status: 400 })
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const pool: any = env.POOL.getByName(input.organizationId)
      return Response.json(
        (await pool[input.method](
          ...input.args.map((arg: unknown) => arg ?? undefined)
        )) ?? null
      )
    }
    default:
      return new Response("Not found", { status: 404 })
  }
}
