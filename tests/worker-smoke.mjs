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
// An install begun on an agent's consent page asks GitHub to carry the way
// back; nothing else may ride along.
const installAt = "https://github.com/apps/hiveworker-smoke/installations/new"
await redirectsTo(
  await fetch(
    `${origin}/install?next=${encodeURIComponent("/connect?client_id=x")}`,
    { redirect: "manual" }
  ),
  `${installAt}?state=%2Fconnect%3Fclient_id%3Dx`
)
for (const next of ["https://evil.example", "/dashboard", "//evil.example"])
  await redirectsTo(
    await fetch(`${origin}/install?next=${encodeURIComponent(next)}`, {
      redirect: "manual",
    }),
    installAt
  )
for (const path of ["/billing", "/projects"])
  assert.equal((await fetch(`${origin}${path}`)).status, 404, path)
// MCP is on: its pages want a session and its endpoint wants a token, and
// says where a client finds out how to get one.
await redirectsTo(
  await fetch(`${origin}/connections`, { redirect: "manual" }),
  "/sign-in",
  307
)
{
  const response = await fetch(`${origin}/mcp`, { method: "POST" })
  assert.equal(response.status, 401)
  assert.match(response.headers.get("www-authenticate"), /resource_metadata/)
  // The body tells the agent behind the client what happens next.
  const { error } = await response.json()
  assert.equal(error.code, -32000)
  assert.match(error.message, /Sign in required/)
  assert.equal(error.data.docs, `${origin}/docs/agents`)
  assert.equal(error.data.llms, `${origin}/llms.txt`)
  assert.ok(error.data.steps.length > 0 && error.data.steps.length < 10)
  assert.ok(error.data.steps.some((step) => step.includes("/install")))
}
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
    macosJobs: false,
    routing: "fastest",
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
      { ...mac, os: "windows" },
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
// What the daemon polls says, per hive, whether macOS jobs are allowed.
assert.deepEqual((await json(await me(enrolled.token))).pools, [
  { ...enrolled.pools[0], macosJobs: false },
])
assert.equal((await me("hwm_nope")).status, 401)
assert.equal((await me(ownerToken)).status, 401)
// The sign-in token did its one job and is gone.
assert.equal(
  (await ownerDaemon.daemon("/api/machines", mac, ownerToken)).status,
  401
)
assert.ok((await page(owner, "/machines")).includes("Mac15,9"))

// Someone in no hive is told so in the browser instead of being offered an
// approval that can only fail, and a daemon that enrolls anyway learns why.
{
  const code = await json(
    await ownerDaemon.daemon("/api/auth/device/code", {
      client_id: "hiveworker-daemon",
    })
  )
  const noHive = await page(outsider, `/device?user_code=${code.user_code}`)
  assert.ok(noHive.includes("not in a hive yet"))
  assert.ok(noHive.includes('href="/install"'))
  assert.ok(!noHive.includes("Approve"))
  // The page never claimed the code, so approving it takes a deliberate
  // detour through the auth API; a daemon enrolling with the result is
  // refused with the same explanation.
  await json(await outsider(`/api/auth/device?user_code=${code.user_code}`))
  await json(
    await outsider("/api/auth/device/approve", { userCode: code.user_code })
  )
  const granted = await json(
    await ownerDaemon.daemon("/api/auth/device/token", {
      grant_type: "urn:ietf:params:oauth:grant-type:device_code",
      device_code: code.device_code,
      client_id: "hiveworker-daemon",
    })
  )
  const refused = await ownerDaemon.daemon(
    "/api/machines",
    mac,
    granted.access_token
  )
  assert.equal(refused.status, 403)
  const body = await json(refused, 403)
  assert.equal(body.code, "NO_HIVE")
  assert.match(body.error, /\/install/)
}

// hived unlink and hived link: a machine leaves a hive on its own token, and
// is added to one only by a person, signed in again at a browser.
{
  const link = (daemon, token, body) =>
    daemon("/api/machines/link", body, token)
  const unlink = (token, organizationId) =>
    ownerDaemon.daemon("/api/machines/unlink", { organizationId }, token)
  const acme = { id: organizationId, name: "acme" }
  const { daemon, granted } = await daemonSignIn(owner)
  const token = (await json(granted)).access_token
  const mine = { machineId: enrolled.machineId }
  // The machine token says which machine, not that its owner agreed.
  assert.equal((await link(daemon, enrolled.token, mine)).status, 401)
  assert.equal((await link(daemon, token, {})).status, 400)
  assert.equal((await link(daemon, token, { ...mine, hives: [] })).status, 400)

  assert.equal((await unlink("hwm_nope", organizationId)).status, 401)
  assert.equal((await unlink(enrolled.token, "gh-nowhere")).status, 404)
  assert.deepEqual(await json(await unlink(enrolled.token, organizationId)), {
    pools: [],
  })
  assert.equal((await unlink(enrolled.token, organizationId)).status, 404)
  assert.deepEqual((await json(await me(enrolled.token))).pools, [])
  // Out of the hive, and offered back to its owner as one click.
  assert.ok((await page(owner, "/machines")).includes("Add to acme"))

  // Named as GitHub names it, in any case; a hive she is not in is refused
  // by name without that stopping the one she is in.
  const linked = await json(
    await link(daemon, token, { ...mine, hives: ["ACME", "globex"] })
  )
  assert.deepEqual(linked.added, [acme])
  assert.deepEqual(linked.pools, [acme])
  assert.deepEqual(
    linked.refused.map((refusal) => refusal.name),
    ["globex"]
  )
  assert.ok(!(await page(owner, "/machines")).includes("Add to acme"))
  // That sign-in did its one job too.
  assert.equal((await link(daemon, token, mine)).status, 401)
}

// A member who is not on an allowed team cannot add a machine; one who is, can.
let memberDaemon = await daemonSignIn(member)
let memberToken = (await json(memberDaemon.granted)).access_token
// Nor can a member link a machine that is someone else's.
assert.equal(
  (
    await memberDaemon.daemon(
      "/api/machines/link",
      { machineId: enrolled.machineId },
      memberToken
    )
  ).status,
  404
)
assert.equal(
  (await memberDaemon.daemon("/api/machines", mac, memberToken)).status,
  403
)
await tellGitHub({ teams: { "acme/ci": ["member"] } })
let memberMachine = await json(
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

// An agent connects over MCP: OAuth discovery, dynamic registration, PKCE and
// consent bound to one hive, then real tool calls against the same state.
const metadata = await json(
  await fetch(`${origin}/.well-known/oauth-protected-resource`)
)
assert.equal(metadata.resource, `${origin}/mcp`)
const serverMetadata = await json(
  await fetch(`${origin}/.well-known/oauth-authorization-server/api/auth`)
)
const redirectUri = "https://client.example/callback"
const readScope = "openid profile email offline_access hive:read"
const fullScope = `${readScope} hive:write`
const client = await json(
  await fetch(serverMetadata.registration_endpoint, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      client_name: "Smoke client",
      redirect_uris: [redirectUri],
      token_endpoint_auth_method: "none",
      grant_types: ["authorization_code", "refresh_token"],
      response_types: ["code"],
      scope: fullScope,
    }),
  }),
  201
)
/** Returns the tokens, or null when consent is expected to be refused. */
async function connect(account, hive, scope = fullScope, consentStatus = 200) {
  const verifier = crypto.randomUUID() + crypto.randomUUID()
  const challenge = Buffer.from(
    await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier))
  ).toString("base64url")
  const query = new URLSearchParams({
    response_type: "code",
    client_id: client.client_id,
    redirect_uri: redirectUri,
    scope,
    code_challenge: challenge,
    code_challenge_method: "S256",
    state: crypto.randomUUID(),
    resource: `${origin}/mcp`,
  })
  const authorization = await account(
    `${serverMetadata.authorization_endpoint}?${query}`
  )
  assert.ok([200, 302].includes(authorization.status))
  const location =
    authorization.status === 200
      ? (await authorization.json()).url
      : authorization.headers.get("location")
  const consentUrl = new URL(location, origin)
  assert.equal(consentUrl.pathname, "/connect")
  // A hive the person is not in, and a scope the client did not sign.
  assert.equal(
    (
      await account("/api/connect", {
        accept: true,
        organizationId: crypto.randomUUID(),
        oauthQuery: consentUrl.search.slice(1),
      })
    ).status,
    400
  )
  const tampered = new URLSearchParams(consentUrl.search)
  tampered.set("scope", "hive:write")
  assert.equal(
    (
      await account("/api/connect", {
        accept: true,
        organizationId: hive,
        oauthQuery: tampered.toString(),
      })
    ).status,
    400
  )
  const consented = await account("/api/connect", {
    accept: true,
    organizationId: hive,
    oauthQuery: consentUrl.search.slice(1),
  })
  if (consentStatus !== 200) {
    assert.equal(consented.status, consentStatus)
    return null
  }
  const consent = await json(consented)
  const code = new URL(consent.url).searchParams.get("code")
  assert.ok(code)
  const tokens = await json(
    await fetch(serverMetadata.token_endpoint, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        grant_type: "authorization_code",
        client_id: client.client_id,
        redirect_uri: redirectUri,
        code,
        code_verifier: verifier,
        resource: `${origin}/mcp`,
      }),
    })
  )
  assert.ok(tokens.access_token)
  return tokens
}
const refresh = (tokens) =>
  fetch(serverMetadata.token_endpoint, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "refresh_token",
      client_id: client.client_id,
      refresh_token: tokens.refresh_token,
      resource: `${origin}/mcp`,
    }),
  })
function rpc(token, name, args = {}) {
  return fetch(`${origin}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
      "MCP-Protocol-Version": "2026-07-28",
      "Mcp-Method": name === "list" ? "tools/list" : "tools/call",
      ...(name === "list" ? {} : { "Mcp-Name": name }),
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: crypto.randomUUID(),
      method: name === "list" ? "tools/list" : "tools/call",
      params: {
        ...(name === "list" ? {} : { name, arguments: args }),
        _meta: {
          "io.modelcontextprotocol/protocolVersion": "2026-07-28",
          "io.modelcontextprotocol/clientCapabilities": {},
        },
      },
    }),
  })
}
/** A tool's structured result, which must not be an error. */
async function tool(tokens, name, args) {
  const { result } = await json(await rpc(tokens.access_token, name, args))
  assert.equal(result.isError, undefined, JSON.stringify(result))
  return result.structuredContent
}
/** A tool call that must come back as an error with this code. */
async function refusedTool(tokens, name, args, code) {
  const { result } = await json(await rpc(tokens.access_token, name, args))
  assert.equal(result.isError, true, `${name} should have been refused`)
  assert.equal(result.structuredContent.code, code, name)
  assert.ok(result.structuredContent.requestId)
}

// Someone signed in but in no hive is shown the way to one, and back.
{
  const html = await page(outsider, `/connect?client_id=${client.client_id}`)
  assert.ok(html.includes("/install?next="))
  assert.ok(html.includes("/github/sync?next="))
  assert.ok(
    html.includes(encodeURIComponent(`/connect?client_id=${client.client_id}`))
  )
  const allow = html.match(/<button[^>]*>(?:<!-- -->)?Allow access/)
  assert.ok(allow && /\sdisabled/.test(allow[0]), "Allow is disabled")
  // The owner, who has a hive, is not sent anywhere.
  assert.ok(
    !(await page(owner, `/connect?client_id=${client.client_id}`)).includes(
      "/install?next="
    )
  )
}
let tokens = await connect(owner, organizationId)
const listed = (await json(await rpc(tokens.access_token, "list"))).result.tools
for (const name of ["get_hive", "update_settings"])
  assert.ok(
    listed.some((each) => each.name === name),
    name
  )
assert.ok(listed.every((each) => each.title && each.description))
assert.ok(!listed.some((each) => each.name === "rename_organization"))
assert.equal((await tool(tokens, "get_current_user")).user.id, "owner")
assert.equal(
  (await rpc(`${tokens.access_token.slice(0, -10)}invalidjwt`, "get_hive"))
    .status,
  401
)

let hive = await tool(tokens, "get_hive")
assert.equal(hive.hive.login, "acme")
assert.equal(hive.hive.id, organizationId)
assert.equal(hive.hive.yourRole, "owner")
assert.equal(hive.balance.usd, 5)
assert.equal(hive.balance.microTokens, 5_000_000)
assert.deepEqual(hive.settings.allowedTeams, ["ci"])
assert.equal(hive.settings.version, 2)
assert.equal(hive.machines.total, 2)
assert.equal(hive.machines.offline, 2)
assert.equal(hive.jobs.canStartNow, false)
assert.equal(hive.jobs.blockedBy, "no-machine-online")
assert.equal(hive.budget, null)
// No card processor here, so automatic top-up is not offered at all.
assert.equal("autoTopUp" in hive, false)
assert.equal(hive.labels[0].label, "hive")
assert.equal(hive.labels[0].machinesAbleNow, 0)
assert.ok(!hive.labels.some((each) => each.label === "hive-macos"))
assert.ok(hive.nextSteps.some((step) => step.includes("hived run")))
assert.ok(hive.nextSteps.some((step) => step.includes("runs-on: hive")))
assert.equal(hive.urls.wallet, `${origin}/wallet`)

const machines = (await tool(tokens, "list_machines")).machines
assert.deepEqual(machines.map((machine) => machine.name).sort(), [
  "Ada's MacBook Pro",
  "Member's Mac mini",
])
assert.ok(
  machines.every(
    (machine) => machine.arch === "arm64" && machine.os === "darwin"
  )
)
assert.deepEqual((await tool(tokens, "list_jobs")).jobs, [])
assert.deepEqual(
  (await tool(tokens, "list_jobs", { status: "running", limit: 5 })).jobs,
  []
)
await refusedTool(tokens, "get_job", { id: "gh-999:1" }, "NOT_FOUND")
assert.equal((await tool(tokens, "get_usage")).totals.jobs, 0)
assert.equal(
  (await tool(tokens, "get_leaderboard", { period: "month" })).period,
  "month"
)

let pricing = await tool(tokens, "get_pricing")
assert.equal(pricing.labels[0].label, "hive")
assert.equal("month" in pricing, false)
pricing = await tool(tokens, "get_pricing", { minutes: 10000 })
assert.ok(pricing.month.hiveUsd < pricing.month.blacksmithUsd)
assert.ok(pricing.month.blacksmithUsd < pricing.month.githubUsd)
assert.ok(pricing.month.cheaperThanGithubBy > 0)
assert.ok(pricing.month.cheaperThanBlacksmithBy > 0)

const workflow = await tool(tokens, "get_workflow", { label: "hive-4cpu" })
assert.ok(workflow.yaml.includes("label: hive-4cpu"))
assert.ok(workflow.yaml.includes("thecomputerplumbers/hive-route@v1"))
assert.ok(
  (
    await tool(tokens, "get_workflow", { label: "hive", routed: false })
  ).yaml.includes("runs-on: hive")
)
assert.equal(
  (await tool(tokens, "set_budget", { monthlyUsd: 50, atLimit: "pause" }))
    .budget.monthlyUsd,
  50
)
hive = await tool(tokens, "get_hive")
assert.equal(hive.budget.monthlyUsd, 50)
assert.equal(hive.budget.atLimit, "pause")
assert.equal(hive.budget.held, false)
assert.equal(
  (await tool(tokens, "set_budget", { monthlyUsd: null })).budget,
  null
)
assert.equal((await tool(tokens, "get_hive")).budget, null)

const saved = await tool(tokens, "update_settings", { routing: "spread" })
assert.equal(saved.settings.version, 3)
assert.equal(saved.settings.routing, "spread")
// Only what was passed changed.
assert.deepEqual(saved.settings.allowedTeams, ["ci"])
assert.equal(saved.settings.earningsDestination, "developer")
await refusedTool(
  tokens,
  "update_settings",
  { routing: "spread", version: 2 },
  "CONFLICT"
)
await refusedTool(tokens, "set_auto_top_up", { enabled: true }, "CONFLICT")
await refusedTool(
  tokens,
  "remove_machine",
  { machineId: crypto.randomUUID() },
  "NOT_FOUND"
)
console.log("OAuth discovery, PKCE, hive-bound consent and owner tools passed.")

// Writing is for owners and admins, and it is refused when consent is given,
// not left to each tool.
assert.equal(await connect(member, organizationId, fullScope, 400), null)
const memberTokens = await connect(member, organizationId, readScope)
assert.equal((await tool(memberTokens, "get_hive")).hive.yourRole, "member")
await refusedTool(
  memberTokens,
  "update_settings",
  { routing: "fastest" },
  "FORBIDDEN"
)
await refusedTool(memberTokens, "set_budget", { monthlyUsd: 1 }, "FORBIDDEN")
// A read-only connection removes nothing, not even its own person's machine.
for (const machineId of [enrolled.machineId, memberMachine.machineId])
  await refusedTool(memberTokens, "remove_machine", { machineId }, "FORBIDDEN")
assert.equal((await tool(tokens, "get_hive")).machines.total, 2)
// An owner's does, from this hive only; the member enrolls again below so
// the rest of the story has their machine in it.
assert.equal(
  (
    await tool(tokens, "remove_machine", {
      machineId: memberMachine.machineId,
    })
  ).removed,
  true
)
assert.equal((await tool(tokens, "get_hive")).machines.total, 1)
await refusedTool(
  tokens,
  "remove_machine",
  { machineId: memberMachine.machineId },
  "NOT_FOUND"
)
memberDaemon = await daemonSignIn(member)
memberToken = (await json(memberDaemon.granted)).access_token
memberMachine = await json(
  await memberDaemon.daemon(
    "/api/machines",
    { ...mac, name: "Member's Mac mini", model: "Mac16,10" },
    memberToken
  ),
  201
)
assert.equal((await tool(tokens, "get_hive")).machines.total, 2)

const refreshed = await json(await refresh(tokens))
assert.ok(refreshed.access_token)
tokens = refreshed
assert.equal((await tool(tokens, "get_hive")).hive.login, "acme")
assert.ok((await page(owner, "/connections")).includes("Smoke client"))
const claims = JSON.parse(
  Buffer.from(tokens.access_token.split(".")[1], "base64url").toString()
)
assert.equal(claims.organization_id, organizationId)
await json(await owner("/api/connections", { id: claims.grant_id }))
assert.equal((await rpc(tokens.access_token, "get_hive")).status, 401)
assert.ok((await refresh(tokens)).status >= 400)
// Someone else's connection is untouched.
assert.equal((await tool(memberTokens, "get_hive")).hive.login, "acme")

const llms = await fetch(`${origin}/llms.txt`)
assert.equal(llms.status, 200)
assert.match(llms.headers.get("content-type"), /^text\/plain/)
const llmsText = await llms.text()
assert.ok(llmsText.startsWith("# HiveWorker\n"))
for (const part of [
  `${origin}/mcp`,
  "runs-on",
  "get_hive",
  "claude mcp add",
  "From nothing to a first job",
])
  assert.ok(llmsText.includes(part), part)
const agentsDoc = await fetch(`${origin}/docs/agents`)
assert.equal(agentsDoc.status, 200)
const agentsHtml = await agentsDoc.text()
assert.ok(agentsHtml.includes("get_hive"))
assert.ok(agentsHtml.includes("From nothing to a first job"))
console.log(
  "Member limits, refresh, revocation and the pages agents read passed."
)

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
// So does their agent's: the grant is rechecked against membership each call.
assert.equal((await rpc(memberTokens.access_token, "get_hive")).status, 401)
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
// Begun from an agent's consent page, the install returns there, and only
// there: `state` has been through GitHub and the address bar.
await redirectsTo(
  await owner(
    `/github/setup?installation_id=9002&state=${encodeURIComponent("/connect?client_id=x")}`
  ),
  "/connect?client_id=x"
)
for (const state of ["https://evil.example", "/wallet", "//evil.example"])
  await redirectsTo(
    await owner(
      `/github/setup?installation_id=9002&state=${encodeURIComponent(state)}`
    ),
    "/dashboard?installed=1"
  )
await redirectsTo(
  await owner(
    `/github/setup?setup_action=request&state=${encodeURIComponent("/connect?client_id=x")}`
  ),
  "/github/status?state=requested-connection"
)
assert.ok(
  (await page(owner, "/github/status?state=requested-connection")).includes(
    "restart it from your MCP client"
  )
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
