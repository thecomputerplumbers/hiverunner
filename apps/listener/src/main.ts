import { createServer, type IncomingMessage } from "node:http"
import { hostname } from "node:os"
import { timingSafeEqual } from "node:crypto"
import { runnerSize } from "@workspace/pricing"
import { createCoordinatorClient } from "./coordinator"
import { ScaleListener, waiting } from "./listener"
import {
  LeaseLost,
  memoryStore,
  postgresStore,
  type ListenerStore,
} from "./store"

const LEASE_MS = 90000
const RENEW_MS = 20000
const SCAN_MS = 500

function required(name: string) {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required`)
  return value
}

const token = required("LISTENER_API_TOKEN")
const local = process.env.LISTENER_STORE === "memory"
// A fake GitHub and throwaway state are for local fixtures, never a pod.
const apiUrl = local ? process.env.GITHUB_API_URL : undefined
const store: ListenerStore = local
  ? memoryStore()
  : await postgresStore(required("DATABASE_URL"))
const coordinator = createCoordinatorClient({
  url: required("COORDINATOR_URL"),
  token,
})
const pod = process.env.POD_NAME ?? hostname()

const listeners = new Map<string, ScaleListener>()
const running = new Set<string>()
const ageing = new Set<string>()
let stopping = false

async function tick(name: string) {
  try {
    if (ageing.delete(name)) await ageUnstarted(name)
    let listener = listeners.get(name)
    if (!listener) {
      listener = new ScaleListener(
        name,
        store.storage(name, pod),
        coordinator,
        apiUrl
      )
      listeners.set(name, listener)
    }
    await listener.alarm()
    await store.finish(name, pod)
  } catch (error) {
    if (!(error instanceof LeaseLost))
      console.error("Listener tick", {
        kind: error instanceof Error ? error.name : "unknown",
      })
    // Left claimed: its lease runs out and a pod takes it from where it saved.
  } finally {
    running.delete(name)
  }
}

async function scan() {
  while (!stopping) {
    try {
      for (const name of await store.claim(pod, [...running], LEASE_MS)) {
        running.add(name)
        void tick(name)
      }
    } catch (error) {
      console.error("Listener scan", {
        kind: error instanceof Error ? error.name : "unknown",
      })
    }
    await new Promise((resolve) => setTimeout(resolve, SCAN_MS))
  }
}

const renewal = setInterval(() => {
  store
    .renew(pod, [...running], LEASE_MS)
    .catch(() => console.error("Listener lease renewal failed"))
}, RENEW_MS)

/** Fixture only: makes a slot look three minutes old on its next tick. */
async function ageUnstarted(name: string) {
  const storage = store.storage(name, pod)
  const state = await storage.get<{
    slots: Record<
      string,
      { phase: string; githubStarted?: boolean; createdAt: number }
    >
  }>("state")
  let aged = false
  for (const slot of Object.values(state?.slots ?? {}))
    if (slot.phase === "running" && !slot.githubStarted) {
      slot.createdAt = Date.now() - 180001
      aged = true
    }
  if (aged) await storage.put("state", state)
  else ageing.add(name)
}

function authorized(request: IncomingMessage) {
  const given = Buffer.from(request.headers.authorization ?? "")
  const wanted = Buffer.from(`Bearer ${token}`)
  return given.length === wanted.length && timingSafeEqual(given, wanted)
}

async function body(request: IncomingMessage) {
  let text = ""
  for await (const chunk of request) text += chunk
  return JSON.parse(text) as Record<string, unknown>
}

const server = createServer(async (request, response) => {
  const respond = (status: number, value?: unknown) => {
    response.writeHead(status, { "Content-Type": "application/json" })
    response.end(value === undefined ? undefined : JSON.stringify(value))
  }
  try {
    const url = new URL(request.url ?? "/", "http://listener")
    if (url.pathname === "/health") return respond(200, { ok: !stopping })
    if (!authorized(request)) return respond(401, { error: "Unauthorized" })
    if (request.method === "POST" && url.pathname === "/v1/activate") {
      const { organizationId, labels } = (await body(request)) as {
        organizationId: string
        labels: string[]
      }
      for (const label of labels) {
        const size = runnerSize(label)
        if (!size) return respond(400, { error: "Unknown runner size" })
        await store.activate(`${organizationId}:${label}`, {
          organizationId,
          label,
          vcpus: size.vcpus,
        })
      }
      return respond(200, {})
    }
    if (request.method === "GET" && url.pathname === "/v1/waiting") {
      const name = url.searchParams.get("name") ?? ""
      return respond(200, { waiting: waiting(await store.read(name, "state")) })
    }
    if (request.method === "POST" && url.pathname === "/v1/adopt") {
      const { name, data } = (await body(request)) as {
        name: string
        data: Record<string, unknown>
      }
      await store.adopt(name, data)
      return respond(200, {})
    }
    if (local && url.pathname === "/__test/age-unstarted") {
      ageing.add(url.searchParams.get("name") ?? "")
      return respond(200, {})
    }
    if (local && url.pathname === "/__test/read") {
      return respond(200, {
        value:
          (await store.read(
            url.searchParams.get("name") ?? "",
            url.searchParams.get("key") ?? ""
          )) ?? null,
      })
    }
    respond(404, { error: "Not found" })
  } catch (error) {
    console.error("Listener request", {
      kind: error instanceof Error ? error.name : "unknown",
    })
    if (!response.headersSent) respond(500, { error: "Listener error" })
    else response.end()
  }
})

async function shutdown() {
  if (stopping) return
  stopping = true
  clearInterval(renewal)
  // Hand everything over now rather than leave it waiting out a lease.
  await store.release(pod, [...running]).catch(() => {})
  server.close()
  await store.close().catch(() => {})
  process.exit(0)
}
process.on("SIGTERM", () => void shutdown())
process.on("SIGINT", () => void shutdown())

server.listen(Number(process.env.PORT ?? 8080), "0.0.0.0")
void scan()
console.info("Listener started", { pod, store: local ? "memory" : "postgres" })
