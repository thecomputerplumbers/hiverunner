import { spawnSync } from "node:child_process"
import { readFileSync, writeFileSync } from "node:fs"
import { resolve } from "node:path"
import { setTimeout as delay } from "node:timers/promises"
import { root, targetConfig, wranglerArgs } from "./project-lib.mjs"
import { PROTOCOL_VERSION } from "../packages/protocol/src/index.ts"
const environment = process.argv[2]
if (!["staging", "production"].includes(environment))
  throw new Error("Usage: pnpm release production")
const config = targetConfig(environment)
function run(command, args, env = { ...process.env, CLOUDFLARE_ENV: "" }) {
  const result = spawnSync(command, args, { cwd: root, stdio: "inherit", env })
  if (result.status !== 0) throw new Error(`${command} failed`)
}
// All validation and the environment-specific build happen before any remote write.
run(process.execPath, ["scripts/doctor.mjs", environment])
run("pnpm", ["lint"])
run("pnpm", ["format:check"])
run("pnpm", ["typecheck"])
run("pnpm", ["test"])
const buildId = spawnSync("git", ["rev-parse", "HEAD"], {
  cwd: root,
  encoding: "utf8",
}).stdout.trim()
if (!/^[a-f0-9]{40}$/.test(buildId))
  throw new Error("Deploy from a committed Git checkout")
if (
  spawnSync("git", ["status", "--porcelain"], {
    cwd: root,
    encoding: "utf8",
  }).stdout.trim()
)
  throw new Error(
    "Commit changes before deployment so the health marker identifies the deployed code"
  )
run("pnpm", ["--filter", "web", "build"], {
  ...process.env,
  CLOUDFLARE_ENV: environment === "staging" ? "staging" : "",
})
const artifact = resolve(root, "apps/web/dist/server/wrangler.json"),
  built = JSON.parse(readFileSync(artifact, "utf8"))
if (
  built.name !== config.name ||
  built.d1_databases?.[0]?.database_id !== config.d1_databases?.[0]?.database_id
)
  throw new Error("Built Worker does not match the selected environment")
built.vars = { ...built.vars, BUILD_ID: buildId }
writeFileSync(artifact, JSON.stringify(built, null, 2))
run("pnpm", [
  "--filter",
  "web",
  "exec",
  "wrangler",
  "d1",
  "migrations",
  "apply",
  "DB",
  "--remote",
  "--config",
  "wrangler.jsonc",
  ...wranglerArgs(environment),
])
// Deploy the already-built artifact; never rebuild against a different environment.
run("pnpm", [
  "--filter",
  "web",
  "exec",
  "wrangler",
  "deploy",
  "--config",
  "dist/server/wrangler.json",
])
// The coordinator shares the database and is deployed after the migrations
// it depends on. Deploying it drops every machine's tunnel; daemons reconnect
// by themselves, and running jobs are unaffected.
run("pnpm", [
  "--filter",
  "coordinator",
  "exec",
  "wrangler",
  "deploy",
  ...wranglerArgs(environment),
])
// A custom domain's first deploy can take a few minutes to resolve.
let verified = false
for (let attempt = 0; attempt < 36 && !verified; attempt++) {
  try {
    const response = await fetch(`${config.vars.APP_URL}/api/health`, {
        signal: AbortSignal.timeout(10000),
        cache: "no-store",
      }),
      health = await response.json()
    const coordinator = await fetch(
      new URL("/health", config.vars.COORDINATOR_URL.replace(/^ws/, "http")),
      { signal: AbortSignal.timeout(10000), cache: "no-store" }
    )
    const coordinatorHealth = await coordinator.json()
    verified =
      response.ok &&
      health.ok &&
      health.buildId === buildId &&
      coordinator.ok &&
      coordinatorHealth.ok &&
      coordinatorHealth.protocol === PROTOCOL_VERSION &&
      coordinatorHealth.executionConfigured === true
  } catch {
    /* Wait for propagation. */
  }
  if (!verified) await delay(5000)
}
if (!verified)
  throw new Error(
    "Deployment uploaded, but independent health/build verification failed. Inspect the Workers before promoting or retrying."
  )
console.log(`Verified ${environment}: ${config.vars.APP_URL} (${buildId})`)
