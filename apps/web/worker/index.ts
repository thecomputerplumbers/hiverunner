import product from "../config/product.json"
import { observeRequest } from "../lib/diagnostics"
import vinextWorker from "vinext/server/app-router-entry"

export { AppDatabase } from "./database"

import { auth } from "../lib/auth"
import { handleMcp } from "../mcp/handler"

const ORGANIZATION_ENDPOINTS = new Set([
  "set-active",
  "list",
  "get-full-organization",
  "get-active-member",
])

export default {
  async fetch(request: Request, env: CloudflareEnv, ctx: ExecutionContext) {
    return observeRequest(request, async () => {
      const path = new URL(request.url).pathname
      if (
        (!product.features.mcp &&
          (path === "/mcp" ||
            path.startsWith("/.well-known/") ||
            path.startsWith("/api/auth/oauth2") ||
            path === "/connect" ||
            path === "/api/connect" ||
            path === "/connections" ||
            path === "/api/connections")) ||
        (!product.features.billing &&
          (path === "/billing" ||
            (path.startsWith("/api/stripe/") &&
              // Card top-ups arrive on the same webhook as subscriptions.
              !(product.features.funding && path === "/api/stripe/webhook"))))
      )
        return new Response("Not found", { status: 404 })
      // Pools and their members come from GitHub. Better Auth's organization
      // plugin is kept for sessions and roles, but nobody may create, invite,
      // promote or remove through it.
      if (
        path.startsWith("/api/auth/organization/") &&
        !ORGANIZATION_ENDPOINTS.has(
          path.slice("/api/auth/organization/".length)
        )
      )
        return new Response("Not found", { status: 404 })
      if (path === "/mcp") return handleMcp(request)
      if (path.startsWith("/.well-known/")) return auth.handler(request)
      return vinextWorker.fetch(request, env, ctx)
    })
  },
} satisfies ExportedHandler<CloudflareEnv>
