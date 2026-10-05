import assert from "node:assert/strict"
import { test } from "node:test"
import { verifyActionsIdentity } from "../apps/coordinator/src/oidc.ts"
import { actionsToken, oidcPublicKey } from "./oidc-fixture.mjs"

const audience = "https://connect.hiveworkr.thecomputerplumbers.com"
const verify = (token) =>
  verifyActionsIdentity(token, audience, async () => oidcPublicKey)
test("verified immutable GitHub owner binds the pool, with customizable subjects", async () => {
  assert.deepEqual(
    await verify(actionsToken(audience, { sub: "custom-subject" })),
    {
      organizationId: "gh-501",
      repository: "acme/shop",
      repositoryId: 123,
    }
  )
})
test("unsigned, tampered, expired, premature, wrong issuer/audience claims are rejected", async () => {
  const token = actionsToken(audience)
  assert.equal(await verify(`${token.slice(0, -12)}tampered1234`), null)
  assert.equal(await verify("not-a-token"), null)
  for (const overrides of [
    { iss: "https://attacker.test" },
    { aud: "other-service" },
    { exp: 1 },
    { nbf: Math.floor(Date.now() / 1000) + 300 },
    { iat: 1 },
    { repository_owner_id: "0" },
    { repository_owner_id: "501.0" },
    { repository_owner_id: "9007199254740992" },
    { repository: "globex/shop" },
    { repository_id: "" },
    { run_id: undefined },
    { sub: "" },
    { nbf: undefined },
  ])
    assert.equal(
      await verify(actionsToken(audience, overrides)),
      null,
      JSON.stringify(overrides)
    )
})
test("a signing key service failure remains an outage", async () => {
  await assert.rejects(
    verifyActionsIdentity(actionsToken(audience), audience, async () => {
      throw new Error("key service unavailable")
    })
  )
})
