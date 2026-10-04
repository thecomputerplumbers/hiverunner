import { drizzle } from "drizzle-orm/d1"
import { authenticateMachineForPool } from "@workspace/db/machines"

export { Pool } from "./pool"
export { ScaleListener } from "./listener"

/**
 * The coordinator's front door. It authenticates a machine token for the
 * pool it asks for and hands the connection to that organization's object; everything after that happens
 * inside the pool's Durable Object.
 *
 * A separate Worker from the web app on purpose: deploying a Worker drops
 * the WebSockets its objects hold, and a copy change on the marketing site
 * should not disconnect every machine.
 */
export default {
  async fetch(request: Request, env: CoordinatorEnv) {
    const url = new URL(request.url)
    if (url.pathname === "/health") return Response.json({ ok: true })
    if (url.pathname !== "/v1/tunnel")
      return new Response("Not found", { status: 404 })
    if (request.headers.get("upgrade") !== "websocket")
      return new Response("Expected a WebSocket", { status: 426 })

    // One tunnel per pool: a machine serving three organizations holds
    // three, each to that organization's own object.
    const pool = url.searchParams.get("pool")
    const token = request.headers.get("authorization")?.match(/^Bearer (.+)$/)
    const machine =
      token && pool
        ? await authenticateMachineForPool(drizzle(env.DB), token[1]!, pool)
        : null
    if (!machine || !pool) return new Response("Unauthorized", { status: 401 })

    // The pool trusts these because only this Worker can reach it.
    const headers = new Headers(request.headers)
    headers.delete("authorization")
    headers.set("x-hive-machine", machine.id)
    headers.set("x-hive-pool", pool)
    headers.set("x-hive-cores", String(machine.cores))
    headers.set("x-hive-speed", String(machine.speedScore))
    return env.POOL.getByName(pool).fetch(new Request(request, { headers }))
  },
} satisfies ExportedHandler<CoordinatorEnv>
