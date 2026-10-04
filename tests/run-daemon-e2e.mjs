// The real daemon binary against the real web and coordinator Workers, on a
// fresh local D1: sign in, enroll, connect, serve a second organization
// without being touched, leave it, and be retired.
// macOS only, because that is the only host the daemon supports.
import { spawn, spawnSync } from "node:child_process"
import {
  existsSync,
  readdirSync,
  mkdtempSync,
  openSync,
  closeSync,
  rmSync,
  writeFileSync,
} from "node:fs"
import { createServer } from "node:net"
import { once } from "node:events"
import assert from "node:assert/strict"
import { setTimeout as delay } from "node:timers/promises"
import { hashPassword } from "../packages/auth/node_modules/better-auth/dist/crypto/index.mjs"

if (process.platform !== "darwin") {
  console.log("Skipped: the daemon runs on macOS only.")
  process.exit(0)
}

const storage = mkdtempSync("/tmp/hiveworker-daemon.")
async function freePort() {
  const server = createServer()
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
  const { port } = server.address()
  await new Promise((resolve) => server.close(resolve))
  return port
}
const webPort = await freePort()
const coordinatorPort = await freePort()
const inspectorPorts = [await freePort(), await freePort()]
const origin = `http://localhost:${webPort}`
const log = openSync(`${storage}/workers.log`, "w")
const password = crypto.randomUUID()
const children = []
let passed = false

function wrangler(app, args, options = {}) {
  return spawnSync("pnpm", ["--filter", app, "exec", "wrangler", ...args], {
    stdio: ["ignore", log, log],
    ...options,
  })
}
function sql(command) {
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
function start(app, args) {
  const child = spawn(
    "pnpm",
    [
      "--filter",
      app,
      "exec",
      "wrangler",
      "dev",
      ...args,
      // Each Worker gets its own debugger port; the default collides when
      // two run at once, here or in another checkout.
      "--inspector-port",
      String(inspectorPorts.pop()),
      "--persist-to",
      storage,
    ],
    {
      stdio: ["ignore", log, log],
      detached: true,
    }
  )
  children.push(child)
  return child
}
async function healthy(url, child) {
  for (let attempt = 0; attempt < 150; attempt++) {
    if (child.exitCode !== null) throw new Error(`Worker for ${url} exited`)
    try {
      if ((await fetch(url)).ok) return
    } catch {
      /* Starting. */
    }
    await delay(100)
  }
  throw new Error(`${url} did not become healthy`)
}
async function eventually(check, message) {
  for (let i = 0; i < 100; i++) {
    if (await check()) return
    await delay(200)
  }
  throw new Error(message)
}

try {
  // Signed, so it may create virtual machines.
  const built = spawnSync("daemon/build.sh", [`${storage}/hived`], {
    stdio: ["ignore", log, log],
  })
  if (built.status !== 0) throw new Error("The daemon did not build")

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
      "INSERT INTO pool_settings (organization_id,allowed_teams,earnings_destination,version,updated_at) VALUES ('gh-501','[]','organization',1,0);",
      "INSERT INTO user (id,name,email,email_verified,created_at,updated_at) VALUES ('owner','Owner','owner@example.test',1,0,0);",
      `INSERT INTO account (id,account_id,provider_id,user_id,password,created_at,updated_at) VALUES ('pw','owner','credential','owner','${await hashPassword(password)}',0,0);`,
      "INSERT INTO member (id,organization_id,user_id,role,created_at) VALUES ('m','gh-501','owner','owner',0);",
      // A second organization the same person belongs to.
      "INSERT INTO organization (id,name,slug,created_at) VALUES ('gh-502','globex','gh-502',0);",
      "INSERT INTO github_installation (account_id,installation_id,organization_id,account_login,account_type,repository_selection,status,created_at,updated_at) VALUES (502,9002,'gh-502','globex','Organization','all','active',0,0);",
      "INSERT INTO pool_settings (organization_id,allowed_teams,earnings_destination,version,updated_at) VALUES ('gh-502','[]','organization',1,0);",
      "INSERT INTO member (id,organization_id,user_id,role,created_at) VALUES ('m2','gh-502','owner','member',0);",
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

  const web = start("web", [
    "--config",
    "dist/server/wrangler.json",
    "--port",
    String(webPort),
    "--var",
    `APP_URL:${origin}`,
    "--var",
    `COORDINATOR_URL:ws://localhost:${coordinatorPort}/v1/tunnel`,
    "--local-upstream",
    `localhost:${webPort}`,
  ])
  const coordinator = start("coordinator", [
    "--port",
    String(coordinatorPort),
    "--var",
    "SWEEP_SECONDS:1",
    "--local-upstream",
    `localhost:${coordinatorPort}`,
  ])
  await healthy(`${origin}/api/health`, web)
  await healthy(`http://localhost:${coordinatorPort}/health`, coordinator)

  const env = {
    ...process.env,
    HIVE_SERVER: origin,
    HIVE_CONFIG: `${storage}/machine.json`,
  }
  const hived = (args) => {
    const child = spawn(`${storage}/hived`, args, {
      env,
      stdio: ["ignore", "pipe", "pipe"],
    })
    child.output = ""
    child.stdout.on("data", (chunk) => (child.output += chunk))
    child.stderr.on("data", (chunk) => (child.output += chunk))
    children.push(child)
    return child
  }

  // hived login: the daemon shows a code, the owner approves it in a browser.
  const login = hived(["login", "--no-browser", "--pool", "gh-501"])
  await eventually(
    () => /user_code=/.test(login.output),
    "the daemon never showed a code"
  )
  const userCode = login.output.match(/user_code=([A-Z0-9-]+)/i)[1]
  const cookies = []
  const browser = async (path, body) => {
    const response = await fetch(`${origin}${path}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Origin: origin,
        Cookie: cookies.join("; "),
      },
      body: JSON.stringify(body),
    })
    for (const cookie of response.headers.getSetCookie())
      cookies.push(cookie.split(";")[0])
    assert.ok(
      response.ok,
      `${path}: ${response.status} ${await response.clone().text()}`
    )
    return response
  }
  await browser("/api/auth/sign-in/email", {
    email: "owner@example.test",
    password,
  })
  // Opening the page is what claims the code for this browser's session.
  const devicePage = await fetch(`${origin}/device?user_code=${userCode}`, {
    headers: { Cookie: cookies.join("; ") },
  })
  assert.ok((await devicePage.text()).includes(userCode))
  await browser("/api/auth/device/approve", { userCode })
  const [loginCode] = await once(login, "exit")
  assert.equal(loginCode, 0, login.output)
  assert.match(login.output, /now serves: acme/)
  const [machine] = sql("select id, os, arch, speed_score from machine")
  assert.equal(machine.os, "darwin")
  assert.ok(machine.speed_score > 0)
  assert.equal(
    sql("select count(*) n from session where user_id='owner'")[0].n,
    1,
    "only the browser session remains"
  )
  console.log(
    `hived login enrolled ${machine.id} (speed score ${machine.speed_score}).`
  )

  // hived run: presence reaches the dashboard's database through the tunnel.
  const presence = (pool = "gh-501") =>
    sql(`select presence from machine_pool where organization_id='${pool}'`)[0]
      ?.presence
  let run = hived(["run"])
  await eventually(
    () => presence() !== "offline",
    `the machine never came online\n${run.output}`
  )
  const state = presence()
  assert.ok(["online", "paused"].includes(state), state)
  console.log(`hived run connected to acme (${state}).`)

  // Added to a second organization from the dashboard: the running daemon
  // picks it up by itself and serves both.
  assert.equal(presence("gh-502"), undefined)
  await browser("/api/machines/pools", {
    machineId: machine.id,
    organizationId: "gh-502",
  })
  await eventually(
    () => presence("gh-502") === state,
    `globex never saw the machine\n${run.output}`
  )
  assert.equal(presence(), state)
  // Dropped from it again: that tunnel goes, the first stays.
  await browser("/api/machines/revoke", {
    machineId: machine.id,
    organizationId: "gh-502",
  })
  await eventually(
    () => /no longer serving a pool/.test(run.output),
    "the daemon did not notice"
  )
  await delay(1500)
  assert.equal(presence("gh-502"), "offline")
  assert.equal(presence(), state)
  assert.equal(run.exitCode, null, "the daemon keeps serving acme")
  console.log("The same machine served a second organization, then left it.")

  // With a guest image installed the daemon keeps a VM warm. Stopping it
  // must take that VM, and its files, with it before the process exits.
  const jobs = process.env.HIVE_IMAGE ? `${process.env.HIVE_IMAGE}-jobs` : null
  const warm = jobs && existsSync(`${process.env.HIVE_IMAGE}/manifest.json`)
  if (warm) {
    await eventually(
      () => /a VM is booted and waiting/.test(run.output),
      `no warm VM\n${run.output}`
    )
    assert.ok(readdirSync(jobs).length > 0, "the warm VM has no directory")
  }
  run.kill("SIGTERM")
  const [stopCode] = await once(run, "exit")
  assert.equal(stopCode, 0, run.output)
  if (warm) {
    assert.deepEqual(readdirSync(jobs), [], "stopping left VM files on disk")
    console.log("Stopping the daemon destroyed its warm VM and left nothing.")
  }
  await eventually(
    () => presence() === "offline",
    "stopping the daemon did not mark it offline"
  )
  console.log("hived went offline cleanly.")

  // Retired while running: the daemon stops and says why, and stays out.
  run = hived(["run"])
  await eventually(
    () => presence() !== "offline",
    "the machine did not reconnect"
  )
  await browser("/api/machines/revoke", { machineId: machine.id })
  const [retiredCode] = await once(run, "exit")
  assert.equal(retiredCode, 1, run.output)
  assert.match(run.output, /no longer enrolled/)
  const again = hived(["run"])
  const [againCode] = await once(again, "exit")
  assert.equal(againCode, 1)
  assert.match(again.output, /no longer enrolled/)
  console.log("Retiring the machine stopped the daemon and it stays out.")
  passed = true
} finally {
  for (const child of children) {
    if (child.exitCode !== null) continue
    try {
      if (child.spawnargs[0] === "pnpm") process.kill(-child.pid, "SIGTERM")
      else child.kill("SIGKILL")
    } catch {
      /* Already exited. */
    }
    await once(child, "exit").catch(() => {})
  }
  closeSync(log)
  if (passed) rmSync(storage, { recursive: true, force: true })
  else console.error(`Local diagnostics retained at ${storage}`)
}
