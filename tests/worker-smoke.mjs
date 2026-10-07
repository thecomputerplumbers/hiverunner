// Run by tests/run-worker-smoke.mjs against the built Worker, a fresh local
// D1 and a fake GitHub. Nothing here reaches the network.
import assert from "node:assert/strict"
import { createHmac } from "node:crypto"
import product from "../apps/web/config/product.json" with { type: "json" }
const [origin, storage] = process.argv.slice(2)
const {
  SMOKE_GITHUB_URL: githubUrl,
  SMOKE_WEBHOOK_SECRET: webhookSecret,
  SMOKE_PASSWORD: password,
} = process.env
assert.equal(new URL(origin).hostname, "localhost")
assert.ok(storage?.startsWith("/tmp/hiveworker-smoke."))

function browser() {
  const cookies = new Map()
  return async (path, body, extra = {}) => {
    const response = await fetch(new URL(path, origin), {
      method: body ? "POST" : "GET",
      redirect: "manual",
      headers: {
        Origin: origin,
        "Content-Type": "application/json",
        Cookie: [...cookies].map(([k, v]) => `${k}=${v}`).join("; "),
        ...extra,
      },
      body: body ? JSON.stringify(body) : undefined,
    })
    for (const cookie of response.headers.getSetCookie()) {
      const first = cookie.split(";")[0],
        i = first.indexOf("=")
      cookies.set(first.slice(0, i), first.slice(i + 1))
    }
    return response
  }
}
async function json(response, status = 200) {
  assert.equal(response.status, status, await response.clone().text())
  return response.json()
}
async function redirectsTo(response, location, status = 302) {
  assert.equal(response.status, status, await response.clone().text())
  assert.equal(response.headers.get("location"), location)
}
async function page(request, path) {
  const response = await request(path)
  assert.equal(response.status, 200, path)
  return response.text()
}
async function signIn(id) {
  const request = browser()
  await json(
    await request("/api/auth/sign-in/email", {
      email: `${id}@example.test`,
      password,
    })
  )
  return request
}

// What GitHub knows. Replaced wholesale whenever the story changes.
const github = {
  users: {
    "token-owner": { id: 101, login: "owner" },
    "token-member": { id: 102, login: "member" },
    "token-outsider": { id: 103, login: "outsider" },
  },
  visible: {
    "token-owner": [9001, 9100],
    "token-member": [9001],
    "token-outsider": [],
  },
  installations: {
    9001: { account: { id: 501, login: "acme", type: "Organization" } },
    9100: { account: { id: 101, login: "owner", type: "User" } },
  },
  roles: { "acme/owner": "admin", "acme/member": "member" },
}
async function tellGitHub(change = {}) {
  Object.assign(github, change)
  const response = await fetch(`${githubUrl}/__state`, {
    method: "PUT",
    body: JSON.stringify(github),
  })
  assert.equal(response.status, 200)
}
let delivery = 0
function webhook(event, payload, { secret = webhookSecret, id } = {}) {
  const body = JSON.stringify(payload)
  return fetch(`${origin}/api/github/webhook`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-GitHub-Event": event,
      "X-GitHub-Delivery": id ?? `delivery-${++delivery}`,
      "X-Hub-Signature-256": `sha256=${createHmac("sha256", secret).update(body).digest("hex")}`,
    },
    body,
  })
}
const installed = (action, extra = {}) => ({
  action,
  installation: {
    id: 9001,
    account: { id: 501, login: "acme", type: "Organization" },
    repository_selection: "all",
    ...extra,
  },
})
await tellGitHub()

// The public surface answers without a session.
assert.equal((await json(await fetch(`${origin}/api/health`))).ok, true)
assert.equal((await fetch(origin)).status, 200)
await redirectsTo(
  await fetch(`${origin}/install`, { redirect: "manual" }),
  "https://github.com/apps/hiveworker-smoke/installations/new"
)
for (const path of ["/billing", "/projects", "/mcp", "/connections"])
  assert.equal((await fetch(`${origin}${path}`)).status, 404, path)
// Returning before consuming a POST body made the local Workers proxy drop
// about 5% of these requests. Exercise the actual HTTP boundary repeatedly;
// a single request rarely catches it. None of these routes may mutate D1.
for (const path of [
  "/api/auth/organization/create",
  ...(!product.features.mcp ? ["/mcp"] : []),
  ...(!product.features.billing ? ["/api/stripe/checkout"] : []),
]) {
  for (let attempt = 0; attempt < 100; attempt++) {
    const response = await fetch(`${origin}${path}`, {
      method: "POST",
      headers: { Origin: origin, "Content-Type": "application/json" },
      body: JSON.stringify({ name: "Rogue", slug: "rogue" }),
    })
    assert.equal(response.status, 404, `${path}, request ${attempt + 1}`)
  }
  // Also drain a chunked upload larger than a single network buffer.
  let remaining = 16
  const chunk = new TextEncoder().encode("x".repeat(64 * 1024))
  const response = await fetch(`${origin}${path}`, {
    method: "POST",
    headers: { Origin: origin, "Content-Type": "application/octet-stream" },
    duplex: "half",
    body: new ReadableStream({
      pull(controller) {
        controller.enqueue(chunk)
        if (--remaining === 0) controller.close()
      },
    }),
  })
  assert.equal(response.status, 404, `${path}, chunked upload`)
  assert.equal(await response.text(), "Not found")
}
console.log("Repeated early POST rejections preserved their responses.")
console.log("Public pages, install link and disabled features passed.")

// Webhooks: only GitHub's signature gets in, and a redelivery changes nothing.
assert.equal(
  (await webhook("installation", installed("created"), { secret: "wrong" }))
    .status,
  401
)
assert.equal(
  (await json(await webhook("installation", installed("created"), { id: "a" })))
    .outcome,
  "provisioned"
)
assert.equal(
  (await json(await webhook("installation", installed("created"), { id: "a" })))
    .outcome,
  "duplicate"
)
console.log("Signed webhook provisioning and delivery dedupe passed.")

// Returning from GitHub: the installation id in the URL proves nothing.
const anonymous = browser()
await redirectsTo(
  await anonymous("/github/setup?installation_id=9001&setup_action=install"),
  `/sign-in?next=${encodeURIComponent("/github/setup?installation_id=9001&setup_action=install")}`
)
const owner = await signIn("owner")
assert.ok((await page(owner, "/dashboard")).includes("No hive yet"))
await redirectsTo(
  await owner("/github/setup?installation_id=7777"),
  "/github/status?state=denied"
)
await redirectsTo(
  await owner("/github/setup?installation_id=abc"),
  "/github/status?state=invalid"
)
await redirectsTo(
  await owner("/github/setup?installation_id=9001&setup_action=request"),
  "/github/status?state=requested"
)
await redirectsTo(
  await owner("/github/setup?installation_id=9100"),
  "/github/status?state=personal"
)
const outsider = await signIn("outsider")
await redirectsTo(
  await outsider("/github/setup?installation_id=9001"),
  "/github/status?state=denied"
)
// Able to see the installation, but not a member of the organization.
await tellGitHub({ visible: { ...github.visible, "token-outsider": [9001] } })
await redirectsTo(
  await outsider("/github/setup?installation_id=9001"),
  "/github/status?state=not-member"
)
const local = await signIn("local")
await redirectsTo(
  await local("/github/setup?installation_id=9001"),
  "/github/status?state=github-required"
)
await redirectsTo(
  await owner("/github/setup?installation_id=9001&setup_action=install"),
  "/dashboard?installed=1"
)
let dashboard = await page(owner, "/dashboard?installed=1")
assert.ok(dashboard.includes("acme"))
assert.ok(dashboard.includes("runs-on: hive"))
// A new organization is walked to its first job, and told what it has.
assert.ok(dashboard.includes("Get your first job running"))
assert.ok(dashboard.includes("Add a Mac"))
assert.ok(dashboard.includes("$5.00"))
// The page text also appears in its RSC payload, so count relative to now.
const grants = (html) => html.split("Trial credit").length - 1
const grantsAtInstall = grants(await page(owner, "/wallet"))
assert.ok(grantsAtInstall > 0)
assert.ok((await page(outsider, "/dashboard")).includes("No hive yet"))
// Money is dollars, and with no card processor configured nobody is offered
// a payment that could not be taken.
const billing = await page(owner, "/wallet")
assert.ok(billing.includes("$5.00") && !billing.includes("HiveWorker tokens"))
assert.ok(billing.includes("Card payments are not open yet"))
assert.equal((await owner("/api/wallet/top-up", { amount: 25 })).status, 503)
await redirectsTo(await outsider("/wallet"), "/dashboard", 307)
console.log(
  "Installation claim check, personal accounts and non-members passed."
)

// Members arrive by signing in; nobody is invited.
const member = await signIn("member")
await redirectsTo(await member("/github/sync"), "/dashboard")
assert.ok((await page(member, "/dashboard")).includes("acme"))
assert.ok((await page(member, "/hives")).includes("acme"))
await redirectsTo(
  await member("/github/sync?next=https://evil.example"),
  "/dashboard"
)
const organizationId = "gh-501"
// The console's snapshot is for members, and a teammate's invite link asks
// only for their Mac: no pool is revealed to someone outside the organization.
const snapshot = await json(await member("/api/pool/live"))
assert.equal(snapshot.balance, 5_000_000)
assert.deepEqual(snapshot.machines, [])
assert.equal(snapshot.hours.length, 14 * 24)
assert.equal(
  (await outsider(`/api/pool/live?organization=${organizationId}`)).status,
  403
)
assert.equal((await anonymous("/api/pool/live")).status, 401)
assert.ok(
  (await page(member, "/leaderboard")).includes("No jobs in this window")
)
const joining = await page(member, "/join/acme")
assert.ok(joining.includes("has a cell waiting for your Mac"))
assert.ok(joining.includes("install.sh"))
const refused = await page(outsider, "/join/acme")
assert.ok(refused.includes("We can") && !refused.includes("install.sh"))
assert.equal(
  await page(outsider, "/join/no-such-org").then((html) =>
    html.includes("install.sh")
  ),
  false
)
await redirectsTo(
  await anonymous("/join/acme"),
  "/sign-in?next=%2Fjoin%2Facme",
  307
)
assert.ok(
  (await page(anonymous, "/sign-in?next=%2Fjoin%2Facme")).includes(
    "Add your Mac to acme"
  )
)
const settings = (request, change = {}) =>
  request("/api/pool/settings", {
    organizationId,
    allowedTeams: ["ci"],
    earningsDestination: "developer",
    version: 1,
    ...change,
  })
assert.equal((await settings(member)).status, 403)
assert.equal((await settings(outsider)).status, 403)
assert.equal(
  (await settings(owner, { allowedTeams: ["Not a slug"] })).status,
  400
)
await json(await settings(owner))
assert.equal((await settings(owner)).status, 409, "a stale version is refused")
assert.ok((await page(owner, "/settings")).includes('value="ci"'))
// Vinext transfers the body into a routed Request, so the entry's original
// Request can no longer drain it when the origin guard rejects it.
for (let attempt = 0; attempt < 100; attempt++) {
  assert.equal(
    (
      await fetch(`${origin}/api/pool/settings`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Origin: "https://evil.example",
        },
        body: "{}",
      })
    ).status,
    403,
    `wrong-origin request ${attempt + 1}`
  )
}
// Better Auth's own organization endpoints would bypass GitHub.
for (const [path, body] of [
  ["create", { name: "Rogue", slug: "rogue" }],
  ["invite-member", { organizationId, email: "x@example.test", role: "owner" }],
  ["update-member-role", { organizationId, memberId: "x", role: "owner" }],
  ["remove-member", { organizationId, memberIdOrEmail: "owner@example.test" }],
  ["delete", { organizationId }],
])
  assert.equal(
    (await member(`/api/auth/organization/${path}`, body)).status,
    404,
    path
  )
console.log("Member sign-in, owner-only settings and locked endpoints passed.")

// The daemon signs in with a device code, enrolls, and keeps only a machine
// token. The pool is limited to the `ci` team by the settings saved above.
const mac = {
  name: "Ada's MacBook Pro",
  model: "Mac15,9",
  os: "darwin",
  arch: "arm64",
  cores: 12,
  memoryBytes: 36 * 2 ** 30,
  daemonVersion: "0.1.0",
  speedScore: 1.8,
}
async function daemonSignIn(approver, decision = "approve") {
  const daemon = (path, body, token) =>
    fetch(`${origin}${path}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify(body),
    })
  const code = await json(
    await daemon("/api/auth/device/code", { client_id: "hiveworker-daemon" })
  )
  assert.ok(code.verification_uri_complete.includes(code.user_code))
  assert.ok(
    (await page(approver, `/device?user_code=${code.user_code}`)).includes(
      code.user_code
    )
  )
  await json(
    await approver(`/api/auth/device/${decision}`, { userCode: code.user_code })
  )
  const granted = await daemon("/api/auth/device/token", {
    grant_type: "urn:ietf:params:oauth:grant-type:device_code",
    device_code: code.device_code,
    client_id: "hiveworker-daemon",
  })
  return { daemon, granted }
}
assert.ok(
  (
    await fetch(`${origin}/api/auth/device/code`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ client_id: "someone-else" }),
    })
  ).status >= 400
)
await redirectsTo(
  await anonymous("/device?user_code=ABCD"),
  `/sign-in?next=${encodeURIComponent("/device?user_code=ABCD")}`,
  307
)
assert.ok((await daemonSignIn(owner, "deny")).granted.status >= 400)

const ownerDaemon = await daemonSignIn(owner)
const ownerToken = (await json(ownerDaemon.granted)).access_token
assert.equal(
  (
    await ownerDaemon.daemon(
      "/api/machines",
      { ...mac, os: "linux" },
      ownerToken
    )
  ).status,
  400
)
const enrolled = await json(
  await ownerDaemon.daemon("/api/machines", mac, ownerToken),
  201
)
assert.ok(enrolled.token.startsWith("hwm_"))
assert.deepEqual(enrolled.pools, [{ id: organizationId, name: "acme" }])
const me = (token) =>
  fetch(`${origin}/api/machines/me`, {
    headers: { Authorization: `Bearer ${token}` },
  })
assert.deepEqual((await json(await me(enrolled.token))).pools, enrolled.pools)
assert.equal((await me("hwm_nope")).status, 401)
assert.equal((await me(ownerToken)).status, 401)
// The sign-in token did its one job and is gone.
assert.equal(
  (await ownerDaemon.daemon("/api/machines", mac, ownerToken)).status,
  401
)
assert.ok((await page(owner, "/machines")).includes("Mac15,9"))

// A member who is not on an allowed team cannot add a machine; one who is, can.
let memberDaemon = await daemonSignIn(member)
let memberToken = (await json(memberDaemon.granted)).access_token
assert.equal(
  (await memberDaemon.daemon("/api/machines", mac, memberToken)).status,
  403
)
await tellGitHub({ teams: { "acme/ci": ["member"] } })
const memberMachine = await json(
  await memberDaemon.daemon(
    "/api/machines",
    { ...mac, name: "Member's Mac mini", model: "Mac16,10" },
    memberToken
  ),
  201
)
assert.ok((await page(owner, "/machines")).includes("Mac16,10"))
// Members can remove their own machine, not anyone else's.
const remove = (request, machineId, pool = organizationId) =>
  request("/api/machines/revoke", { machineId, organizationId: pool })
assert.equal((await remove(member, enrolled.machineId)).status, 403)
assert.equal((await remove(outsider, enrolled.machineId)).status, 404)
// Retiring a machine outright is its owner's call alone, and nobody can
// volunteer someone else's machine for a pool.
assert.equal(
  (await member("/api/machines/revoke", { machineId: enrolled.machineId }))
    .status,
  404
)
assert.equal(
  (
    await member("/api/machines/pools", {
      machineId: enrolled.machineId,
      organizationId,
    })
  ).status,
  404
)
console.log("Device sign-in, enrollment, team limits and removal rules passed.")

// GitHub being down is not the same as GitHub saying no.
await tellGitHub({ down: true })
await redirectsTo(await member("/github/sync"), "/dashboard")
assert.ok((await page(member, "/dashboard")).includes("acme"))
await tellGitHub({ down: false })

// Leaving the organization on GitHub ends access here.
assert.equal(
  (
    await json(
      await webhook("organization", {
        action: "member_removed",
        organization: { id: 501, login: "acme" },
        membership: { user: { id: 102 } },
      })
    )
  ).outcome,
  "member-removed"
)
assert.ok((await page(member, "/dashboard")).includes("No hive yet"))
assert.equal((await settings(member, { version: 2 })).status, 403)
// Their machine left with them; the owner's is untouched.
const remaining = await page(owner, "/machines")
assert.ok(!remaining.includes("Mac16,10") && remaining.includes("Mac15,9"))
assert.ok(memberMachine.token)
await json(
  await owner("/api/machines/revoke", { machineId: enrolled.machineId })
)
assert.ok(!(await page(owner, "/machines")).includes("Mac15,9"))
// And a sync agrees with the webhook once GitHub's own answer changes.
await tellGitHub({ roles: { "acme/owner": "admin" } })
await redirectsTo(await member("/github/sync"), "/dashboard")
assert.ok((await page(member, "/dashboard")).includes("No hive yet"))
console.log("GitHub outage tolerance and member removal passed.")

// The installation's own lifecycle.
await json(await webhook("installation", installed("suspend")))
assert.ok((await page(owner, "/dashboard")).includes("suspended"))
await json(await webhook("installation", installed("unsuspend")))
assert.ok(!(await page(owner, "/dashboard")).includes("suspended"))
await json(await webhook("installation", installed("deleted")))
dashboard = await page(owner, "/dashboard")
assert.ok(dashboard.includes("uninstalled"))
// Reinstalling gets a new installation id and the same pool and balance.
await tellGitHub({
  installations: {
    ...github.installations,
    9002: { account: { id: 501, login: "acme", type: "Organization" } },
  },
  visible: { ...github.visible, "token-owner": [9002] },
})
await json(await webhook("installation", installed("created", { id: 9002 })))
await redirectsTo(
  await owner("/github/setup?installation_id=9002"),
  "/dashboard?installed=1"
)
assert.equal(
  grants(await page(owner, "/wallet")),
  grantsAtInstall,
  "one grant, ever"
)
assert.equal(
  (
    await json(
      await webhook(
        "installation",
        installed("created", {
          id: 9100,
          account: { id: 101, login: "owner", type: "User" },
        })
      )
    )
  ).outcome,
  "unsupported"
)
console.log("Suspend, uninstall, reinstall and the single grant passed.")

// Sessions.
const another = await signIn("owner")
const current = await json(await another("/api/auth/get-session"))
await json(await owner("/api/account/sessions", { id: current.session.id }))
assert.equal(await json(await another("/api/auth/get-session")), null)
for (const path of [
  "/account",
  "/settings",
  "/wallet",
  "/jobs",
  "/machines",
  "/leaderboard",
])
  assert.equal((await owner(path)).status, 200, path)
assert.equal((await owner("/jobs/gh-999%3A1")).status, 404)
await redirectsTo(await anonymous("/dashboard"), "/sign-in", 307)
console.log("Session revocation and account screens passed.")
