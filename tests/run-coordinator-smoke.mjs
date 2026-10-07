// Starts the coordinator Worker on a fresh local D1 and drives it as a
// daemon would. Nothing here reaches the network.
import { spawn, spawnSync } from "node:child_process"
import {
  mkdtempSync,
  openSync,
  closeSync,
  rmSync,
  writeFileSync,
  readFileSync,
} from "node:fs"
import { createHash, randomBytes } from "node:crypto"
import { createServer } from "node:net"
import { once } from "node:events"
import { request as httpRequest } from "node:http"
import assert from "node:assert/strict"
import { setTimeout as delay } from "node:timers/promises"
import { resolve } from "node:path"
import { parse } from "jsonc-parser"
import { actionsToken, oidcPublicJwk } from "./oidc-fixture.mjs"

const storage = mkdtempSync("/tmp/hiveworker-coordinator.")
const reservation = createServer()
reservation.listen(0, "127.0.0.1")
await once(reservation, "listening")
const port = reservation.address().port
await new Promise((resolve) => reservation.close(resolve))
const origin = `http://localhost:${port}`
const log = openSync(`${storage}/worker.log`, "w")

function wrangler(app, args, options = {}) {
  return spawnSync("pnpm", ["--filter", app, "exec", "wrangler", ...args], {
    stdio: ["ignore", log, log],
    ...options,
  })
}
/** Runs SQL against the same local D1 the Worker is using. */
async function sql(command) {
  if (worker) {
    const path = /^SELECT\b/i.test(command) ? "sql" : "mutate"
    const response = await fetch(`${origin}/__test/${path}`, {
      method: "POST",
      body: JSON.stringify({ sql: command }),
    })
    assert.equal(response.status, 200)
    return response.json()
  }
  const result = wrangler(
    "web",
    [
      "d1",
      "execute",
      "DB",
      "--local",
      "--persist-to",
      storage,
      "--json",
      "--command",
      command,
    ],
    { stdio: ["ignore", "pipe", log], encoding: "utf8" }
  )
  if (result.status !== 0) throw new Error(`SQL failed: ${command}`)
  return JSON.parse(result.stdout)[0].results
}
const hash = (token) => createHash("sha256").update(token).digest("hex")
const tokens = { a: "hwm_machine-a", b: "hwm_machine-b" }

let worker
let passed = false
try {
  if (
    wrangler("web", [
      "d1",
      "migrations",
      "apply",
      "DB",
      "--local",
      "--persist-to",
      storage,
    ]).status !== 0
  )
    throw new Error("Local migrations failed")
  const seed = `${storage}/seed.sql`
  writeFileSync(
    seed,
    [
      "INSERT INTO organization (id,name,slug,created_at) VALUES ('gh-501','acme','gh-501',0);",
      "INSERT INTO github_installation (account_id,installation_id,organization_id,account_login,account_type,repository_selection,status,created_at,updated_at) VALUES (501,9001,'gh-501','acme','Organization','all','active',0,0);",
      "INSERT INTO user (id,name,email,email_verified,created_at,updated_at) VALUES ('ada','Ada','ada@example.test',1,0,0);",
      "INSERT INTO organization (id,name,slug,created_at) VALUES ('gh-502','globex','gh-502',0);",
      "INSERT INTO github_installation (account_id,installation_id,organization_id,account_login,account_type,repository_selection,status,created_at,updated_at) VALUES (502,9002,'gh-502','globex','Organization','all','active',0,0);",
      ...Object.entries(tokens).flatMap(([id, token], i) => [
        `INSERT INTO machine (id,user_id,name,model,os,arch,cores,memory_bytes,speed_score,daemon_version,token_hash,created_at) VALUES ('${id}','ada','Mac ${id}','Mac15,9','darwin','arm64',12,1,${1 + i},'0.0.0','${hash(token)}',0);`,
        `INSERT INTO machine_pool (machine_id,organization_id,created_at) VALUES ('${id}','gh-501',0);`,
      ]),
      // Machine a also serves a second organization.
      "INSERT INTO machine_pool (machine_id,organization_id,created_at) VALUES ('a','gh-502',0);",
      "INSERT INTO ledger_entry (id,account_type,account_id,kind,amount,created_at) VALUES ('grant:gh-501','organization','gh-501','grant',5000000,0);",
    ].join("\n")
  )
  if (
    wrangler("web", [
      "d1",
      "execute",
      "DB",
      "--local",
      "--persist-to",
      storage,
      "--file",
      seed,
    ]).status !== 0
  )
    throw new Error("Seeding failed")

  const config = `${storage}/wrangler.json`
  const local = parse(readFileSync("apps/coordinator/wrangler.jsonc", "utf8"))
  local.main = resolve("tests/coordinator-harness.mjs")
  delete local.routes
  delete local.account_id
  delete local.workers_dev
  local.vars = {
    ...local.vars,
    FIXTURE_OIDC_PUBLIC_KEY: JSON.stringify(oidcPublicJwk),
  }
  writeFileSync(config, JSON.stringify(local))
  worker = spawn(
    "pnpm",
    [
      "--filter",
      "coordinator",
      "exec",
      "wrangler",
      "dev",
      "--config",
      config,
      "--port",
      String(port),
      "--var",
      "SWEEP_SECONDS:1",
      "--local-upstream",
      `localhost:${port}`,
      "--persist-to",
      storage,
    ],
    { stdio: ["ignore", log, log], detached: true }
  )
  let ready = false
  for (let attempt = 0; attempt < 150; attempt++) {
    if (worker.exitCode !== null)
      throw new Error("Worker exited before startup")
    try {
      if ((await fetch(`${origin}/health`)).ok) {
        ready = true
        break
      }
    } catch {
      /* Starting. */
    }
    await delay(100)
  }
  if (!ready) throw new Error("Coordinator did not become healthy")

  /** A daemon's end of the tunnel, with its frames queued for the test. */
  async function connect(token, pool = "gh-501") {
    const socket = new WebSocket(
      `ws://localhost:${port}/v1/tunnel?pool=${pool}`,
      {
        headers: { Authorization: `Bearer ${token}` },
      }
    )
    const frames = []
    const waiting = []
    let closed
    socket.addEventListener("message", (event) => {
      const next = waiting.shift()
      if (next) next(event.data)
      else frames.push(event.data)
    })
    socket.addEventListener("close", (event) => (closed = event.code))
    await once(socket, "open")
    return {
      send: (message) =>
        socket.send(
          typeof message === "string" ? message : JSON.stringify(message)
        ),
      next: async () => {
        const raw =
          frames.shift() ??
          (await Promise.race([
            new Promise((resolve) => waiting.push(resolve)),
            delay(5000).then(() => {
              throw new Error("No frame within 5s")
            }),
          ]))
        return raw === "pong" ? raw : JSON.parse(raw)
      },
      closedWith: async () => {
        for (let i = 0; i < 100 && closed === undefined; i++) await delay(50)
        return closed
      },
      close: () => socket.close(),
    }
  }
  const presence = async (id, pool = "gh-501") =>
    (
      await sql(
        `select presence from machine_pool where machine_id='${id}' and organization_id='${pool}'`
      )
    )[0].presence
  async function eventually(check, message) {
    for (let i = 0; i < 60; i++) {
      if (await check()) return
      await delay(100)
    }
    throw new Error(message)
  }
  const hello = (state = "online") => ({
    type: "hello",
    protocol: 3,
    daemonVersion: "0.1.0",
    presence: state,
  })
  const capacity = (overrides = {}, label = "hive-4cpu") =>
    fetch(`${origin}/v1/capacity?label=${label}`, {
      headers: { Authorization: `Bearer ${actionsToken(origin, overrides)}` },
    })
  const available = async () => {
    const response = await capacity()
    assert.equal(response.status, 200)
    assert.equal(response.headers.get("cache-control"), "no-store")
    return (await response.json()).available
  }
  assert.equal(
    (await fetch(`${origin}/v1/capacity?label=hive-4cpu`)).status,
    401
  )
  assert.equal((await capacity({ aud: "wrong-audience" })).status, 401)
  assert.equal((await capacity({ repository_owner_id: "999" })).status, 403)
  assert.equal((await capacity({}, "unsupported")).status, 400)
  assert.equal(await available(), false, "empty pool is unavailable")

  /** The HTTP status a tunnel request gets, without completing an upgrade. */
  const tunnelStatus = (headers, pool = "gh-501") =>
    new Promise((resolve, reject) => {
      const request = httpRequest({
        port,
        host: "localhost",
        path: `/v1/tunnel?pool=${pool}`,
        headers: {
          Connection: "Upgrade",
          Upgrade: "websocket",
          "Sec-WebSocket-Version": "13",
          "Sec-WebSocket-Key": randomBytes(16).toString("base64"),
          ...headers,
        },
      })
      request.on("response", (response) => resolve(response.statusCode))
      request.on("upgrade", (response, socket) => {
        socket.destroy()
        resolve(response.statusCode)
      })
      request.on("error", reject)
      request.end()
    })

  // Only an enrolled machine gets a tunnel.
  assert.equal((await fetch(`${origin}/v1/tunnel`)).status, 426)
  assert.equal(await tunnelStatus({}), 401)
  assert.equal(await tunnelStatus({ Authorization: "Bearer hwm_nope" }), 401)
  // A valid machine, but not one that serves the pool it asks for.
  assert.equal(
    await tunnelStatus({ Authorization: `Bearer ${tokens.b}` }, "gh-502"),
    401
  )

  const a = await connect(tokens.a)
  assert.deepEqual(await a.next(), {
    type: "welcome",
    machineId: "a",
    protocol: 3,
  })
  // Nothing is believed before hello, and junk does not kill the tunnel.
  a.send({ type: "presence", presence: "online" })
  assert.equal((await a.next()).type, "error")
  a.send("not json")
  assert.equal((await a.next()).type, "error")
  assert.equal(await presence("a"), "offline")
  assert.equal(await available(), false, "socket before hello is unavailable")
  a.send(hello())
  await eventually(
    async () => (await presence("a")) === "online",
    "hello sets presence"
  )
  assert.equal(await available(), true, "idle enrolled machine is available")
  await fetch(`${origin}/__test/offer`, {
    method: "POST",
    body: JSON.stringify({ release: false }),
  })
  assert.equal((await a.next()).type, "job.assign")
  assert.equal(
    await available(),
    false,
    "a reserved slot for the same label is unavailable"
  )
  await fetch(`${origin}/__test/offer`, {
    method: "POST",
    body: JSON.stringify({ release: true }),
  })
  assert.equal((await a.next()).type, "job.release")
  assert.equal(await available(), true)

  // An owner who allows two jobs lends two slots, and no more.
  const offer = (assignmentId, release = false) =>
    fetch(`${origin}/__test/offer`, {
      method: "POST",
      body: JSON.stringify({ assignmentId, release }),
    })
  a.send({ ...hello(), maxJobs: 2 })
  await offer("first")
  assert.equal((await a.next()).assignmentId, "first")
  await eventually(available, "a machine with one job of two has room")
  assert.equal(await presence("a"), "online")
  a.send({ type: "job.accepted", assignmentId: "first" })
  await eventually(
    async () => (await presence("a")) === "busy",
    "a machine at work shows as busy"
  )
  assert.equal(await available(), true, "and still has room for a second")
  await offer("second")
  assert.equal((await a.next()).assignmentId, "second")
  assert.equal(await available(), false, "two jobs of two is full")
  await offer("third")
  await offer("second", true)
  assert.deepEqual(await a.next(), {
    type: "job.release",
    assignmentId: "second",
  })
  assert.equal(await available(), true, "a released offer frees its slot")
  a.send({ type: "job.rejected", assignmentId: "first", reason: "test" })
  await eventually(
    async () => (await presence("a")) === "online",
    "an idle machine is online again"
  )
  a.send(hello())
  await offer("alone")
  assert.equal((await a.next()).assignmentId, "alone")
  assert.equal(await available(), false, "without the setting, one job")
  await offer("alone", true)
  assert.equal((await a.next()).type, "job.release")
  assert.equal(await available(), true)
  await sql("update ledger_entry set amount=0 where id='grant:gh-501'")
  assert.equal(await available(), false, "zero credit cannot acquire jobs")
  await sql("update ledger_entry set amount=5000000 where id='grant:gh-501'")
  await sql("update machine set status='retired' where id='a'")
  assert.equal(
    await available(),
    false,
    "retired machine is refused before the sweep"
  )
  await sql("update machine set status='active' where id='a'")
  assert.equal(
    (await sql("select daemon_version v from machine where id='a'"))[0].v,
    "0.1.0"
  )
  a.send({ type: "presence", presence: "paused" })
  await eventually(
    async () => (await presence("a")) === "paused",
    "presence is written through"
  )
  assert.equal(await available(), false, "paused machine is unavailable")
  a.send("ping")
  assert.equal(await a.next(), "pong")
  console.log("Tunnel authentication, hello and presence passed.")

  // A reconnect replaces the old socket without flapping to offline.
  const a2 = await connect(tokens.a)
  assert.equal((await a2.next()).type, "welcome")
  assert.equal(await a.closedWith(), 4000)
  a2.send(hello())
  await eventually(
    async () => (await presence("a")) === "online",
    "the new socket is the machine"
  )
  await delay(300)
  assert.equal(
    await presence("a"),
    "online",
    "the old socket closing changed nothing"
  )

  // An old daemon is told to update rather than half-working.
  const b = await connect(tokens.b)
  await b.next()
  b.send({ ...hello(), protocol: 1 })
  assert.equal(await b.closedWith(), 4002)

  // Revocation is a row in D1; the pool notices on its own.
  const b2 = await connect(tokens.b)
  await b2.next()
  b2.send(hello())
  await eventually(
    async () => (await presence("b")) === "online",
    "b is online"
  )
  await sql("update machine_pool set status='revoked' where machine_id='b'")
  assert.equal(await b2.closedWith(), 4001)
  await eventually(
    async () => (await presence("b")) === "offline",
    "revoked machines go offline"
  )
  assert.equal(await presence("a"), "online")
  assert.equal(await tunnelStatus({ Authorization: `Bearer ${tokens.b}` }), 401)
  console.log("Reconnect, version refusal and revocation passed.")

  // One machine, two organizations: a tunnel and a presence for each.
  const g = await connect(tokens.a, "gh-502")
  assert.equal((await g.next()).type, "welcome")
  g.send(hello("busy"))
  await eventually(
    async () => (await presence("a", "gh-502")) === "busy",
    "globex sees it busy"
  )
  assert.equal(await presence("a"), "online", "acme's view is its own")
  assert.equal(
    await available(),
    false,
    "busy in another pool cannot accept work here"
  )
  // Removed from one pool: that tunnel closes, the other is untouched.
  await sql(
    "update machine_pool set status='revoked' where machine_id='a' and organization_id='gh-502'"
  )
  assert.equal(await g.closedWith(), 4001)
  await delay(1500)
  assert.equal(await presence("a"), "online")
  assert.equal(await available(), true)
  console.log("One machine serving two pools passed.")

  // Uninstalling the App silences the whole pool.
  await sql(
    "update github_installation set status='removed' where account_id=501"
  )
  assert.equal(
    (await capacity()).status,
    403,
    "removed installation is immediately refused"
  )
  assert.equal(await a2.closedWith(), 4001)
  await eventually(
    async () => (await presence("a")) === "offline",
    "the pool empties"
  )
  console.log("Installation removal passed.")
  passed = true
} finally {
  if (worker?.pid) {
    try {
      process.kill(-worker.pid, "SIGTERM")
    } catch {
      /* Already exited. */
    }
    if (worker.exitCode === null) await once(worker, "exit")
  }
  closeSync(log)
  if (passed) rmSync(storage, { recursive: true, force: true })
  else console.error(`Local diagnostics retained at ${storage}`)
}
