// Local integration-test entry only. Never used by deployment configuration.
// Query D1 through the live binding instead of starting competing SQLite
// processes, and age a pending slot without sleeping for three minutes.
import coordinator, {
  Pool,
  ScaleListener as Listener,
} from "../apps/coordinator/src/index.ts"
import { capacityResponse } from "../apps/coordinator/src/capacity.ts"
import { verifyActionsIdentity } from "../apps/coordinator/src/oidc.ts"

export { Pool }
export class ScaleListener extends Listener {
  async ageUnstartedOnNextAlarm() {
    await this.ctx.storage.put("test:age-unstarted", true)
    await this.ctx.storage.setAlarm(Date.now() + 50)
  }
  async alarm() {
    if (await this.ctx.storage.get("test:age-unstarted")) {
      const state = await this.ctx.storage.get("state")
      let aged = false
      for (const slot of Object.values(state?.slots ?? {})) {
        if (slot.phase === "running" && !slot.githubStarted) {
          slot.createdAt = Date.now() - 180001
          aged = true
        }
      }
      if (aged) {
        await this.ctx.storage.put("state", state)
        await this.ctx.storage.delete("test:age-unstarted")
      }
    }
    return super.alarm()
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
      const { release, assignmentId = "capacity-fixture" } =
        await request.json()
      const pool = env.POOL.getByName("gh-501")
      if (release) await pool.release(assignmentId)
      else
        await pool.offer({
          assignmentId,
          label: "hive-4cpu",
          vcpus: 4,
          timeoutMinutes: 1,
        })
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
    if (path === "/__test/age-unstarted" && request.method === "POST") {
      await env.LISTENER.getByName("gh-501:hive").ageUnstartedOnNextAlarm()
      return new Response(null, { status: 204 })
    }
    return coordinator.fetch(request, env)
  },
}
