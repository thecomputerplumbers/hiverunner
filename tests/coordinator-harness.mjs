// Local integration-test entry only. Never used by deployment configuration.
// Query D1 through the live binding instead of starting competing SQLite
// processes.
import coordinator, {
  Pool,
  ScaleListener as Retired,
} from "../apps/coordinator/src/index.ts"
import { capacityResponse } from "../apps/coordinator/src/capacity.ts"
import { verifyActionsIdentity } from "../apps/coordinator/src/oidc.ts"

export { Pool }
export class ScaleListener extends Retired {
  // What a listener held before it moved out: an alarm, and its state.
  async seedForHandover() {
    await this.ctx.storage.put("settings", {
      organizationId: "gh-retired",
      label: "hive",
      vcpus: 2,
    })
    await this.ctx.storage.put("state", { cursor: 7, slots: {} })
    await this.ctx.storage.setAlarm(Date.now() + 50)
  }
}
export default {
  async fetch(request, env) {
    const path = new URL(request.url).pathname
    if (path === "/v1/capacity" && env.FIXTURE_OIDC_PUBLIC_KEY) {
      const key = await crypto.subtle.importKey(
        "jwk",
        JSON.parse(env.FIXTURE_OIDC_PUBLIC_KEY),
        { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
        false,
        ["verify"]
      )
      return capacityResponse(
        request,
        {
          ...env,
          GITHUB_APP_ID: "fixture",
          GITHUB_APP_PRIVATE_KEY: "fixture",
          RECEIPT_SIGNING_KEY: "fixture",
        },
        (token, audience) =>
          verifyActionsIdentity(token, audience, async () => key)
      )
    }
    if (path === "/__test/offer" && request.method === "POST") {
      const {
        release,
        assignmentId = "capacity-fixture",
        label = "hive-4cpu",
        arch = "arm64",
      } = await request.json()
      const pool = env.POOL.getByName("gh-501")
      if (release) await pool.release(assignmentId)
      else {
        const chosen = await pool.offer({
          assignmentId,
          label,
          arch,
          vcpus: 4,
          timeoutMinutes: 1,
        })
        // Which machine was offered the job, for tests that ask.
        return Response.json({ machineId: chosen?.machineId ?? null })
      }
      return new Response(null, { status: 204 })
    }
    if (
      (path === "/__test/sql" || path === "/__test/mutate") &&
      request.method === "POST"
    ) {
      const { sql } = await request.json()
      const allowed = path === "/__test/sql" ? /^SELECT\b/i : /^UPDATE\b/i
      if (typeof sql !== "string" || !allowed.test(sql))
        return new Response("Unsupported fixture query", { status: 400 })
      return Response.json((await env.DB.prepare(sql).all()).results)
    }
    if (path === "/__test/retired-listener" && request.method === "POST") {
      const stub = env.LISTENER.getByName("gh-retired:hive")
      await stub.seedForHandover()
      return new Response(null, { status: 204 })
    }
    return coordinator.fetch(request, env)
  },
}
