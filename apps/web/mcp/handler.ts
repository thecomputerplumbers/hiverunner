import { env } from "cloudflare:workers"
import { createMcpEndpoint } from "@workspace/mcp"
import { authenticateMcp, mcpAuthError } from "@workspace/auth/mcp"
import product from "@/config/product.json"
import { auth } from "@/lib/auth"
import { getDb } from "@/db"
import web from "../package.json"
import { journeySteps } from "./journey"
import { registerHiveTools, serverInstructions } from "./tools"

export async function handleMcp(request: Request) {
  const resource = `${env.APP_URL}/mcp`
  const origin = request.headers.get("origin")
  if (origin && origin !== env.APP_URL)
    return new Response("Origin not allowed", { status: 403 })
  const db = getDb()
  try {
    const actor = await authenticateMcp(request, auth, db, resource)
    const budget = await env.APP_DATABASE.getByName(
      `mcp:${actor.userId}`
    ).consumeRateLimit("requests", 120, 60_000)
    if (!budget.allowed)
      return new Response("Too many requests", {
        status: 429,
        headers: { "Retry-After": "60" },
      })
    const context = { db, actor }
    return createMcpEndpoint(
      (server) => registerHiveTools(server, context),
      { name: product.slug, version: web.version },
      { instructions: serverInstructions(env.APP_URL) }
    )(request)
  } catch (error) {
    return mcpAuthError(error, resource, journeySteps(env.APP_URL))
  }
}
