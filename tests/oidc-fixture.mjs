import { generateKeyPairSync, sign } from "node:crypto"

const { privateKey, publicKey } = generateKeyPairSync("rsa", {
  modulusLength: 2048,
})
export const oidcPublicJwk = publicKey.export({ format: "jwk" })
export const oidcPublicKey = await crypto.subtle.importKey(
  "jwk",
  oidcPublicJwk,
  { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
  false,
  ["verify"]
)
export function actionsToken(audience, overrides = {}) {
  const now = Math.floor(Date.now() / 1000)
  const payload = {
    iss: "https://token.actions.githubusercontent.com",
    aud: audience,
    iat: now,
    nbf: now - 1,
    exp: now + 300,
    sub: "repo:acme@501/shop@123:ref:refs/heads/main",
    repository_owner_id: "501",
    repository_owner: "acme",
    repository: "acme/shop",
    repository_id: "123",
    run_id: "456",
    ...overrides,
  }
  const encode = (value) =>
    Buffer.from(JSON.stringify(value)).toString("base64url")
  const unsigned = `${encode({ alg: "RS256", typ: "JWT", kid: "fixture" })}.${encode(payload)}`
  return `${unsigned}.${sign("RSA-SHA256", Buffer.from(unsigned), privateKey).toString("base64url")}`
}
