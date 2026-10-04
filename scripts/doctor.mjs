import { existsSync, readFileSync } from "node:fs"
import { resolve } from "node:path"
import { spawnSync } from "node:child_process"
import { root, readJson, targetConfig, wranglerArgs } from "./project-lib.mjs"
const environment = process.argv[2] ?? "local",
  config = targetConfig(environment),
  product = readJson(resolve(root, "apps/web/config/product.json"))
let failures = 0
function check(ok, message) {
  console.log(`${ok ? "OK" : "MISSING"} ${message}`)
  if (!ok) failures++
}
check(process.versions.node.split(".")[0] === "26", "Node 26")
check(
  existsSync(resolve(root, "node_modules")),
  "Installed dependencies (mise run setup)"
)
check(
  config.d1_databases?.some((db) => db.binding === "DB"),
  "DB binding"
)
check(
  config.durable_objects?.bindings?.some(
    (binding) => binding.name === "APP_DATABASE"
  ),
  "APP_DATABASE binding"
)
check(
  config.send_email?.some((binding) => binding.name === "EMAIL"),
  "EMAIL binding"
)
if (environment === "local") {
  const file = resolve(root, "apps/web/.dev.vars")
  check(
    existsSync(file),
    "Local configuration (copy apps/web/.dev.vars.example)"
  )
  if (existsSync(file))
    check(
      /^APP_URL=http:\/\/(localhost|127\.0\.0\.1)(:\d+)?\s*$/m.test(
        readFileSync(file, "utf8")
      ),
      "Local APP_URL"
    )
  const result = spawnSync(
    "pnpm",
    [
      "--filter",
      "web",
      "exec",
      "wrangler",
      "d1",
      "execute",
      "DB",
      "--local",
      "--config",
      "wrangler.jsonc",
      "--command",
      "SELECT id FROM user LIMIT 0; SELECT account_id FROM github_installation LIMIT 0; SELECT id FROM agent_grant LIMIT 0;",
    ],
    { cwd: root, encoding: "utf8", env: { ...process.env, CLOUDFLARE_ENV: "" } }
  )
  check(result.status === 0, "Local schema migrated (pnpm db:migrate:local)")
} else {
  check(/^[a-f0-9]{32}$/.test(config.account_id ?? ""), "Cloudflare account ID")
  check(
    config.d1_databases?.every((db) => /^[a-f0-9-]{36}$/.test(db.database_id)),
    `${environment} D1 database IDs`
  )
  check(
    !process.env.CLOUDFLARE_ACCOUNT_ID ||
      process.env.CLOUDFLARE_ACCOUNT_ID === config.account_id,
    "Environment account ID matches Wrangler configuration"
  )
  const all = readJson(resolve(root, "apps/web/wrangler.jsonc"))
  check(
    all.d1_databases?.[0]?.database_id !==
      all.env?.staging?.d1_databases?.[0]?.database_id,
    "Separate staging and production databases"
  )
  check(
    (config.vars?.APP_URL ?? "").startsWith("https://"),
    "Canonical HTTPS APP_URL"
  )
  check(
    Boolean(config.vars?.EMAIL_FROM),
    "EMAIL_FROM (domain must also be verified in Cloudflare)"
  )
  const result = spawnSync(
    "pnpm",
    [
      "--filter",
      "web",
      "exec",
      "wrangler",
      "secret",
      "list",
      "--config",
      "wrangler.jsonc",
      ...wranglerArgs(environment),
    ],
    { cwd: root, encoding: "utf8", env: { ...process.env, CLOUDFLARE_ENV: "" } }
  )
  let secrets = []
  try {
    secrets = JSON.parse(result.stdout)
  } catch {
    /* Wrangler authentication failure. */
  }
  check(
    result.status === 0,
    "Cloudflare authentication and Worker secret inventory"
  )
  const required = [
    "BETTER_AUTH_SECRET",
    ...(product.features.billing
      ? [
          "STRIPE_SECRET_KEY",
          "STRIPE_WEBHOOK_SECRET",
          ...product.plans.map(
            (plan) =>
              `STRIPE_PRICE_${plan.id.toUpperCase().replaceAll("-", "_")}`
          ),
        ]
      : []),
  ]
  for (const name of required)
    check(
      secrets.some((secret) => secret.name === name),
      name
    )
  // Without the GitHub App the site still serves; sign-in and install say
  // "not open yet". Reported, but not a reason to refuse a deploy.
  for (const name of [
    "GITHUB_APP_ID",
    "GITHUB_APP_SLUG",
    "GITHUB_APP_PRIVATE_KEY",
    "GITHUB_WEBHOOK_SECRET",
    "GITHUB_CLIENT_ID",
    "GITHUB_CLIENT_SECRET",
  ])
    if (!secrets.some((secret) => secret.name === name))
      console.log(`NOT SET ${name} (GitHub sign-in and install stay closed)`)
  const coordinator = spawnSync(
    "pnpm",
    [
      "--filter",
      "coordinator",
      "exec",
      "wrangler",
      "secret",
      "list",
      ...wranglerArgs(environment),
    ],
    { cwd: root, encoding: "utf8", env: { ...process.env, CLOUDFLARE_ENV: "" } }
  )
  let coordinatorSecrets = []
  try {
    coordinatorSecrets = JSON.parse(coordinator.stdout)
  } catch {
    /* Inventory unavailable. */
  }
  check(coordinator.status === 0, "Coordinator secret inventory")
  for (const name of [
    "GITHUB_APP_ID",
    "GITHUB_APP_PRIVATE_KEY",
    "RECEIPT_SIGNING_KEY",
  ])
    check(
      coordinatorSecrets.some((secret) => secret.name === name),
      `Coordinator ${name}`
    )
}
process.exitCode = failures ? 1 : 0
