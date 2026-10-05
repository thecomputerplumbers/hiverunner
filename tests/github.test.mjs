import assert from "node:assert/strict"
import { test } from "node:test"
import { createHmac, generateKeyPairSync, createVerify } from "node:crypto"
import {
  createGitHubApp,
  verifyWebhookSignature,
} from "../packages/github/src/index.ts"

const { privateKey, publicKey } = generateKeyPairSync("rsa", {
  modulusLength: 2048,
})
const sign = (secret, body) =>
  `sha256=${createHmac("sha256", secret).update(body).digest("hex")}`

test("selected repository checks bind immutable IDs and do not treat outages as denial", async () => {
  const app = createGitHubApp({
    appId: "1",
    privateKey: privateKey.export({ type: "pkcs8", format: "pem" }),
    webhookSecret: "",
    fetch: async (url, init) => {
      const path = new URL(url).pathname
      if (path.endsWith("/access_tokens"))
        return Response.json({
          token: "installation-token",
          expires_at: new Date(Date.now() + 3600000).toISOString(),
        })
      if (path.endsWith("/installation")) {
        assert.notEqual(init.headers.Authorization, "Bearer installation-token")
        const claims = JSON.parse(
          Buffer.from(
            init.headers.Authorization.split(".")[1],
            "base64url"
          ).toString()
        )
        assert.equal(claims.iss, "1")
        if (path === "/repos/acme/shop/installation")
          return Response.json({ id: 7 })
        if (path === "/repos/acme/other-installation/installation")
          return Response.json({ id: 8 })
        return new Response(null, {
          status: path.includes("/outage/") ? 500 : 404,
        })
      }
      assert.equal(init.headers.Authorization, "Bearer installation-token")
      if (
        [
          "/repos/acme/shop",
          "/repos/acme/public-uninstalled",
          "/repos/acme/other-installation",
        ].includes(path)
      )
        return Response.json({ id: 123 })
      return new Response(null, {
        status: path.endsWith("/outage") ? 500 : 404,
      })
    },
  })
  assert.equal(await app.canAccessRepository(7, "acme/shop", 123), true)
  assert.equal(await app.canAccessRepository(7, "acme/shop", 124), false)
  assert.equal(await app.canAccessRepository(7, "acme/missing", 123), false)
  assert.equal(
    await app.canAccessRepository(7, "acme/public-uninstalled", 123),
    false
  )
  assert.equal(
    await app.canAccessRepository(7, "acme/other-installation", 123),
    false
  )
  await assert.rejects(app.canAccessRepository(7, "acme/outage", 123), {
    status: 500,
  })
})

test("webhook signatures are checked against the raw body", async () => {
  const body = JSON.stringify({ action: "created" })
  assert.equal(
    await verifyWebhookSignature("s3cret", body, sign("s3cret", body)),
    true
  )
  assert.equal(
    await verifyWebhookSignature("s3cret", `${body} `, sign("s3cret", body)),
    false
  )
  assert.equal(
    await verifyWebhookSignature("other", body, sign("s3cret", body)),
    false
  )
  assert.equal(await verifyWebhookSignature("s3cret", body, null), false)
  assert.equal(await verifyWebhookSignature("s3cret", body, "sha256=zz"), false)
  assert.equal(await verifyWebhookSignature("", body, sign("", body)), false)
})

for (const type of ["pkcs1", "pkcs8"])
  test(`app tokens are valid RS256 from a ${type} key`, async () => {
    const app = createGitHubApp({
      appId: "12345",
      privateKey: privateKey.export({ type, format: "pem" }),
      webhookSecret: "x",
    })
    const token = await app.appToken(1_700_000_000_000)
    const [header, payload, signature] = token.split(".")
    const claims = JSON.parse(Buffer.from(payload, "base64url").toString())
    assert.equal(claims.iss, "12345")
    assert.equal(claims.exp - claims.iat, 600)
    assert.ok(
      createVerify("RSA-SHA256")
        .update(`${header}.${payload}`)
        .verify(publicKey, Buffer.from(signature, "base64url"))
    )
  })

test("installation tokens are cached and memberships map to roles", async () => {
  const calls = []
  const app = createGitHubApp({
    appId: "1",
    privateKey: privateKey.export({ type: "pkcs8", format: "pem" }),
    webhookSecret: "x",
    apiUrl: "https://github.test/",
    fetch: async (url, init) => {
      const path = new URL(url).pathname
      calls.push(`${init.method} ${path}`)
      if (path.endsWith("/access_tokens"))
        return Response.json({
          token: "ghs_installation",
          expires_at: new Date(Date.now() + 3600_000).toISOString(),
        })
      assert.equal(init.headers.Authorization, "Bearer ghs_installation")
      if (path === "/orgs/acme/memberships/owner")
        return Response.json({ state: "active", role: "admin" })
      if (path === "/orgs/acme/memberships/pending")
        return Response.json({ state: "pending", role: "member" })
      if (path === "/orgs/acme/memberships/broken")
        return Response.json({ message: "boom" }, { status: 500 })
      return Response.json({ message: "Not Found" }, { status: 404 })
    },
  })
  assert.equal(await app.getOrgRole(7, "acme", "owner"), "admin")
  assert.equal(await app.getOrgRole(7, "acme", "pending"), null)
  assert.equal(await app.getOrgRole(7, "acme", "stranger"), null)
  assert.equal(await app.isTeamMember(7, "acme", "ci", "owner"), false)
  // An outage is not a "no": it must not be read as "remove this member".
  await assert.rejects(app.getOrgRole(7, "acme", "broken"), { status: 500 })
  assert.equal(
    calls.filter((call) => call.endsWith("/access_tokens")).length,
    1
  )
})
