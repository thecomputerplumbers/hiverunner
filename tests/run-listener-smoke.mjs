// Real workerd/D1/DOs with a local GitHub Actions service and a scripted daemon.
// Exercises the entire scheduling and receipt path without external credentials.
import assert from "node:assert/strict"
import { spawn, spawnSync } from "node:child_process"
import { createHash, generateKeyPairSync } from "node:crypto"
import { once } from "node:events"
import {
  mkdtempSync,
  writeFileSync,
  readFileSync,
  rmSync,
  openSync,
  closeSync,
} from "node:fs"
import { createServer } from "node:http"
import { setTimeout as delay } from "node:timers/promises"
import { resolve } from "node:path"
import { verifyReceipt } from "../packages/db/src/receipts.ts"

const storage = mkdtempSync("/tmp/hiveworker-listener.")
const log = openSync(`${storage}/worker.log`, "w")
const signer = generateKeyPairSync("ed25519")
const rsa = generateKeyPairSync("rsa", { modulusLength: 2048 })
const scenario = process.env.HIVE_LISTENER_CASE ?? "offer"
const direct = scenario !== "offer"
const jobIdentity = direct ? "9b60c775-161b-430b-a441-c34e54ae3f55" : 100
const queue = [
  {
    messageId: 1,
    jobs: [
      {
        messageType: direct ? "JobAssigned" : "JobAvailable",
        runnerRequestId: direct ? 0 : 100,
        jobId: direct ? jobIdentity : undefined,
        ownerName: "acme",
        repositoryName: "app",
      },
    ],
  },
]
if (scenario === "initial") queue.length = 0
let acquisitionCount = 0,
  jitCount = 0,
  ackCount = 0
let runnerName,
  acquireFailure = false
const apiPaths = []
const fake = createServer(async (req, res) => {
  let body = ""
  for await (const chunk of req) body += chunk
  const url = new URL(req.url, `http://${req.headers.host}`)
  apiPaths.push(url.pathname)
  const json = (value) => {
    res.setHeader("Content-Type", "application/json")
    res.end(JSON.stringify(value))
  }
  if (url.pathname.endsWith("access_tokens"))
    return json({
      token: "installation",
      expires_at: new Date(Date.now() + 3600000).toISOString(),
    })
  if (url.pathname.endsWith("registration-token"))
    return json({ token: "registration" })
  if (url.pathname === "/actions/runner-registration")
    return json({ url: `${api}/tenant`, token: "admin" })
  if (url.pathname.endsWith("runnergroups/"))
    return json({ value: [{ id: 1 }] })
  if (url.pathname.endsWith("runnerscalesets"))
    return json({ value: [{ id: 6, name: "hive", runnerGroupId: 1 }] })
  if (url.pathname.endsWith("sessions"))
    return json({
      sessionId: "session",
      messageQueueUrl: `${api}/queue`,
      messageQueueAccessToken: "session",
      statistics: { totalAssignedJobs: direct ? 1 : 0, totalRunningJobs: 0 },
    })
  if (url.pathname.endsWith("sessions/session")) {
    res.statusCode = 204
    return res.end()
  }
  if (url.pathname.endsWith("acquirejobs")) {
    acquisitionCount++
    if (acquireFailure) {
      acquireFailure = false
      res.statusCode = 503
      return res.end()
    }
    return json({ value: JSON.parse(body) })
  }
  if (url.pathname.endsWith("generatejitconfig")) {
    jitCount++
    runnerName = JSON.parse(body).name
    return json({
      runner: { id: 77, name: runnerName },
      encodedJITConfig: "one-time-jit",
    })
  }
  if (url.pathname.endsWith("/agents")) return json({ value: [] })
  if (url.pathname.endsWith("/agents/77")) {
    res.statusCode = 204
    return res.end()
  }
  if (url.pathname === "/queue") {
    const next = queue[0]
    if (next)
      return json({
        messageId: next.messageId,
        statistics: {
          totalAssignedJobs: next.jobs.every(
            (job) => job.messageType === "JobCompleted"
          )
            ? 0
            : 1,
          totalRunningJobs: 0,
        },
        body: JSON.stringify(next.jobs),
      })
    res.statusCode = 202
    return res.end()
  }
  if (url.pathname.startsWith("/queue/") && req.method === "DELETE") {
    if (queue[0]?.messageId === Number(url.pathname.split("/").at(-1)))
      queue.shift()
    ackCount++
    res.statusCode = 204
    return res.end()
  }
  res.statusCode = 404
  res.end()
})
fake.listen(0, "127.0.0.1")
await once(fake, "listening")
const api = `http://127.0.0.1:${fake.address().port}`
async function freePort() {
  const server = createServer()
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
  const port = server.address().port
  await new Promise((resolve) => server.close(resolve))
  return port
}
const port = await freePort()
const origin = `http://localhost:${port}`
function wrangler(args, options = {}) {
  const result = spawnSync(
    "pnpm",
    ["--filter", "web", "exec", "wrangler", ...args],
    { stdio: ["ignore", log, log], ...options }
  )
  if (result.status !== 0)
    throw new Error(`Wrangler failed (${args[0]}): see ${storage}/worker.log`)
  return result
}
function sql(command) {
  return JSON.parse(
    wrangler(
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
      { encoding: "utf8", stdio: ["ignore", "pipe", log] }
    ).stdout
  )[0].results
}
async function eventually(check, message) {
  for (let i = 0; i < 200; i++) {
    if (await check()) return
    await delay(100)
  }
  throw new Error(message)
}
let worker,
  socket,
  passed = false
try {
  wrangler([
    "d1",
    "migrations",
    "apply",
    "DB",
    "--local",
    "--persist-to",
    storage,
  ])
  sql(`INSERT INTO organization (id,name,slug,created_at) VALUES ('gh-501','acme','gh-501',0);
    INSERT INTO github_installation (account_id,installation_id,organization_id,account_login,account_type,repository_selection,created_at,updated_at) VALUES (501,9001,'gh-501','acme','Organization','all',0,0);
    INSERT INTO user (id,name,email,email_verified,created_at,updated_at) VALUES ('ada','Ada','ada@example.test',1,0,0);
    INSERT INTO machine (id,user_id,name,model,os,arch,cores,memory_bytes,speed_score,daemon_version,token_hash,created_at) VALUES ('mac','ada','Mac','Mac15,9','darwin','arm64',4,1,1.5,'0.1.0','${createHash("sha256").update("hwm_test").digest("hex")}',0);
    INSERT INTO machine_pool (machine_id,organization_id,created_at) VALUES ('mac','gh-501',0);
    INSERT INTO ledger_entry (id,account_type,account_id,kind,amount,created_at) VALUES ('grant:gh-501','organization','gh-501','grant',5000000,0);`)
  const config = `${storage}/wrangler.json`
  writeFileSync(
    config,
    JSON.stringify({
      name: "hiveworker-coordinator",
      main: resolve("apps/coordinator/src/index.ts"),
      compatibility_date: "2026-09-17",
      compatibility_flags: ["nodejs_compat"],
      d1_databases: [
        {
          binding: "DB",
          database_name: "hiveworker",
          database_id: "baef55aa-e1c6-4ddc-b9f9-0de634c15a45",
        },
      ],
      durable_objects: {
        bindings: [
          { name: "POOL", class_name: "Pool" },
          { name: "LISTENER", class_name: "ScaleListener" },
        ],
      },
      exports: {
        Pool: { type: "durable-object", storage: "sqlite" },
        ScaleListener: { type: "durable-object", storage: "sqlite" },
      },
      vars: {
        SWEEP_SECONDS: "1",
        GITHUB_API_URL: api,
        GITHUB_APP_ID: "1",
        GITHUB_APP_PRIVATE_KEY: rsa.privateKey.export({
          type: "pkcs8",
          format: "pem",
        }),
        RECEIPT_SIGNING_KEY: signer.privateKey
          .export({ type: "pkcs8", format: "der" })
          .toString("base64"),
      },
    })
  )
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
      "--inspector-port",
      String(await freePort()),
      "--local-upstream",
      `localhost:${port}`,
      "--persist-to",
      storage,
    ],
    { stdio: ["ignore", log, log], detached: true }
  )
  await eventually(async () => {
    if (worker.exitCode !== null) throw new Error("Worker exited")
    try {
      return (await fetch(`${origin}/health`)).ok
    } catch {
      return false
    }
  }, "Worker startup")
  const frames = []
  async function connect(assignmentId) {
    const ws = new WebSocket(`ws://localhost:${port}/v1/tunnel?pool=gh-501`, {
      headers: { Authorization: "Bearer hwm_test" },
    })
    ws.addEventListener("message", (event) =>
      frames.push(JSON.parse(event.data))
    )
    await once(ws, "open")
    await next("welcome")
    ws.send(
      JSON.stringify({
        type: "hello",
        protocol: 2,
        daemonVersion: "0.1.0",
        presence: assignmentId ? "busy" : "online",
        assignmentId,
      })
    )
    return ws
  }
  async function next(type) {
    await eventually(
      () => frames.some((frame) => frame.type === type),
      `Expected ${type}`
    )
    return frames.splice(
      frames.findIndex((frame) => frame.type === type),
      1
    )[0]
  }
  const send = (message) => socket.send(JSON.stringify(message))
  socket = await connect()
  const rejected = await next("job.assign")
  assert.equal(rejected.jitConfig, undefined)
  assert.equal(acquisitionCount, 0, "Never acquire before acceptance")
  send({
    type: "job.rejected",
    assignmentId: rejected.assignmentId,
    reason: "busy in another pool",
  })
  send({ type: "presence", presence: "online" })
  const offer = await next("job.assign")
  assert.notEqual(offer.assignmentId, rejected.assignmentId)
  assert.equal(acquisitionCount, 0, "Rejection must not acquire a job")
  // An API retry occurs while the daemon holds its slot.
  acquireFailure = !direct
  send({ type: "job.accepted", assignmentId: offer.assignmentId })
  const start = await next("job.start")
  assert.equal(start.assignmentId, offer.assignmentId)
  assert.equal(start.jitConfig, "one-time-jit")
  assert.equal(jitCount, 1)
  assert.equal(acquisitionCount, direct ? 0 : 2)
  assert.equal(runnerName, start.runnerName)
  // Reconnect carries the held assignment instead of advertising a free slot.
  socket = await connect(offer.assignmentId)
  send({ type: "presence", presence: "online" })
  await delay(200)
  assert.equal(sql("SELECT presence FROM machine_pool")[0].presence, "busy")
  const event = {
    runnerRequestId: direct ? 0 : 100,
    jobId: direct ? jobIdentity : undefined,
    ownerName: "acme",
    repositoryName: "app",
    runnerName,
    runnerAssignTime: "2026-10-03T12:00:00Z",
    jobDisplayName: "test",
  }
  queue.push({
    messageId: 2,
    jobs: [
      {
        ...event,
        messageType: "JobStarted",
        runnerAssignTime: "0001-01-01T00:00:00Z",
      },
      { ...event, messageType: "JobStarted" },
    ],
  })
  await eventually(
    () => sql("SELECT count(*) n FROM job")[0].n === 1,
    "GitHub start recorded"
  )
  // Machine exit alone cannot charge anything.
  send({ type: "job.exited", assignmentId: offer.assignmentId, exitCode: 0 })
  send({ type: "presence", presence: "online" })
  assert.equal(
    sql("SELECT count(*) n FROM ledger_entry WHERE job_id IS NOT NULL")[0].n,
    0
  )
  const completed = {
    ...event,
    messageType: "JobCompleted",
    finishTime: "2026-10-03T12:01:00Z",
    result: "succeeded",
  }
  queue.push({ messageId: 3, jobs: [completed] })
  await eventually(
    () =>
      sql("SELECT count(*) n FROM ledger_entry WHERE job_id IS NOT NULL")[0]
        .n === 3,
    "GitHub completion settled"
  )
  const row = sql("SELECT * FROM job")[0]
  assert.equal(row.id, `gh-501:${jobIdentity}`)
  assert.equal(row.amount, 1500)
  assert.equal(row.speed_score, 1.5)
  const publicKey = await (await fetch(`${origin}/v1/receipt-key`)).json()
  assert.equal(publicKey.keyId, row.receipt_key_id)
  assert.equal(
    await verifyReceipt(
      publicKey.publicKey,
      row.receipt,
      row.receipt_signature
    ),
    true
  )
  assert.equal(
    sql(
      "SELECT sum(amount) total FROM ledger_entry WHERE job_id IS NOT NULL"
    )[0].total,
    0
  )
  queue.push({ messageId: 4, jobs: [completed] })
  await eventually(
    () => ackCount >= (scenario === "initial" ? 3 : 4),
    "Duplicate completion acknowledged"
  )
  assert.equal(
    sql("SELECT count(*) n FROM ledger_entry WHERE job_id IS NOT NULL")[0].n,
    3
  )
  assert.equal(
    sql("SELECT scale_set_id FROM runner_scale_set")[0].scale_set_id,
    6
  )
  console.log(
    `Listener ${scenario}: rejection, JIT, reconnect, stable identity and signed idempotent settlement passed.`
  )
  passed = true
} finally {
  socket?.close()
  if (worker?.pid) {
    try {
      process.kill(-worker.pid, "SIGTERM")
    } catch {}
    if (worker.exitCode === null) await once(worker, "exit")
  }
  fake.closeAllConnections()
  await new Promise((resolve) => fake.close(resolve))
  closeSync(log)
  if (passed) rmSync(storage, { recursive: true, force: true })
  else {
    console.error("Fake API paths", apiPaths)
    console.error(readFileSync(`${storage}/worker.log`, "utf8").slice(-6000))
    console.error(`Diagnostics retained: ${storage}`)
  }
}

if (!process.env.HIVE_LISTENER_CASE) {
  for (const scenario of ["direct", "initial"]) {
    const child = spawnSync(process.execPath, [import.meta.filename], {
      env: { ...process.env, HIVE_LISTENER_CASE: scenario },
      stdio: "inherit",
    })
    assert.equal(child.status, 0, `Listener ${scenario} scenario failed`)
  }
}
