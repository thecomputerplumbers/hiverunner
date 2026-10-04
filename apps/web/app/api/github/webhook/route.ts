import { applyGitHubEvent, type GitHubEvent } from "@workspace/db/installations"

import { getDb } from "@/db"
import { discardUnreadBody, logError, logEvent } from "@/lib/diagnostics"
import { getGitHubApp } from "@/lib/github"

/**
 * GitHub's side of the installation. This, not the browser's return from
 * GitHub, is what provisions a pool: it arrives whether or not anyone is
 * looking at a page.
 */
export async function POST(request: Request) {
  const app = getGitHubApp()
  if (!app) {
    await discardUnreadBody(request)
    return Response.json({ error: "Not configured" }, { status: 503 })
  }

  // Verify the exact bytes GitHub signed, before parsing any of it.
  const body = await request.text()
  if (
    !(await app.verifyWebhook(body, request.headers.get("x-hub-signature-256")))
  )
    return Response.json({ error: "Invalid signature" }, { status: 401 })

  const deliveryId = request.headers.get("x-github-delivery")
  const event = request.headers.get("x-github-event")
  if (!deliveryId || !event)
    return Response.json({ error: "Missing delivery headers" }, { status: 400 })

  try {
    const payload = JSON.parse(body) as GitHubEvent["payload"]
    const outcome = await applyGitHubEvent(getDb(), {
      deliveryId,
      event,
      payload,
    })
    logEvent("github.webhook", { operation: `${event}:${outcome}` })
    return Response.json({ ok: true, outcome })
  } catch (error) {
    // A 500 makes the delivery show as failed in GitHub, where it can be
    // redelivered. Handlers are idempotent, so that is always safe.
    logError(error, `github.webhook.${event}`)
    return Response.json({ error: "Could not apply event" }, { status: 500 })
  }
}
