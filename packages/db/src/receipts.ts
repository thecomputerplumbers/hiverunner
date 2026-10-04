/**
 * Signed receipts. A receipt is the priced facts of one job as a fixed JSON
 * string, with an Ed25519 signature over exactly those bytes. Anyone holding
 * the coordinator's public key can check a receipt without asking us.
 *
 * In step 2 the x402 voucher takes over as the payment instrument; the
 * receipt stays as the human-readable record of what was charged and why.
 */

export type ReceiptFacts = {
  jobId: string
  organizationId: string
  machineId: string
  repository: string
  label: string
  vcpus: number
  speedScore: number
  startedAt: string
  completedAt: string
  result: string
  computeUnits: number
  /** Micro-tokens. */
  rate: number
  amount: number
  tax: number
  earning: number
  payee: string
}

export type ReceiptSigner = {
  keyId: string
  sign(text: string): Promise<string>
}

const encoder = new TextEncoder()

const fromBase64 = (value: string) =>
  Uint8Array.from(atob(value.replace(/\s+/g, "")), (char) => char.charCodeAt(0))

function toBase64url(bytes: ArrayBuffer) {
  let binary = ""
  for (const byte of new Uint8Array(bytes)) binary += String.fromCharCode(byte)
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/, "")
}

const fromBase64url = (value: string) =>
  fromBase64(value.replaceAll("-", "+").replaceAll("_", "/"))

/**
 * The bytes that get signed. Keys are written in one fixed order so the same
 * facts always produce the same string; the stored string, not a re-encoding
 * of it, is what a verifier checks.
 */
export function receiptText(facts: ReceiptFacts) {
  return JSON.stringify({
    version: 1,
    jobId: facts.jobId,
    organizationId: facts.organizationId,
    machineId: facts.machineId,
    repository: facts.repository,
    label: facts.label,
    vcpus: facts.vcpus,
    speedScore: facts.speedScore,
    startedAt: facts.startedAt,
    completedAt: facts.completedAt,
    result: facts.result,
    computeUnits: facts.computeUnits,
    rate: facts.rate,
    amount: facts.amount,
    tax: facts.tax,
    earning: facts.earning,
    payee: facts.payee,
  })
}

/**
 * A signer from the `RECEIPT_SIGNING_KEY` secret: a base64 PKCS#8 Ed25519
 * private key (`openssl genpkey -algorithm ed25519 -outform DER | base64`).
 * The key id is derived from the public key, so rotating the key changes it.
 */
export async function createReceiptSigner(
  pkcs8Base64: string
): Promise<ReceiptSigner & { publicKey: string }> {
  // Extractable only so the public half can be derived for the key id.
  const privateKey = await crypto.subtle.importKey(
    "pkcs8",
    fromBase64(pkcs8Base64),
    { name: "Ed25519" },
    true,
    ["sign"]
  )
  const jwk = (await crypto.subtle.exportKey("jwk", privateKey)) as JsonWebKey
  const publicKey = jwk.x!
  const digest = await crypto.subtle.digest("SHA-256", fromBase64url(publicKey))
  return {
    keyId: toBase64url(digest).slice(0, 16),
    publicKey,
    sign: async (text) =>
      toBase64url(
        await crypto.subtle.sign("Ed25519", privateKey, encoder.encode(text))
      ),
  }
}

/** `publicKey` is the base64url raw key, as `createReceiptSigner` reports it. */
export async function verifyReceipt(
  publicKey: string,
  text: string,
  signature: string
) {
  try {
    const key = await crypto.subtle.importKey(
      "raw",
      fromBase64url(publicKey),
      { name: "Ed25519" },
      false,
      ["verify"]
    )
    return await crypto.subtle.verify(
      "Ed25519",
      key,
      fromBase64url(signature),
      encoder.encode(text)
    )
  } catch {
    return false
  }
}
