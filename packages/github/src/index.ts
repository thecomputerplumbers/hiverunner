/**
 * The GitHub App, over `fetch` and WebCrypto.
 *
 * No Octokit: workerd will not host its Node built-ins, and the surface this
 * product needs is a dozen endpoints. Every call goes through `request`, so
 * the base URL can be pointed at a fake in tests.
 */

const API_VERSION = "2022-11-28"

export type GitHubConfig = {
  appId: string
  /** PEM. PKCS#8, or the PKCS#1 file GitHub issues. */
  privateKey: string
  webhookSecret: string
  /** Defaults to https://api.github.com. */
  apiUrl?: string
  fetch?: typeof fetch
}

export class GitHubError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = "GitHubError"
    this.status = status
  }
}

export type Installation = {
  id: number
  account: { id: number; login: string; type: "Organization" | "User" }
  suspended: boolean
  repositorySelection: "all" | "selected"
}

export type OrgRole = "admin" | "member"

const encoder = new TextEncoder()

function base64url(input: ArrayBuffer | Uint8Array | string) {
  const bytes =
    typeof input === "string"
      ? encoder.encode(input)
      : input instanceof Uint8Array
        ? input
        : new Uint8Array(input)
  let binary = ""
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/, "")
}

function derLength(length: number) {
  if (length < 0x80) return [length]
  const bytes = []
  for (let rest = length; rest > 0; rest >>= 8) bytes.unshift(rest & 0xff)
  return [0x80 | bytes.length, ...bytes]
}

/**
 * GitHub issues App keys as PKCS#1 ("BEGIN RSA PRIVATE KEY"); WebCrypto
 * imports only PKCS#8. The difference is a fixed wrapper, so add it here
 * rather than making every deployment remember to run openssl first.
 */
export function privateKeyToPkcs8(pem: string) {
  const pkcs1 = pem.includes("BEGIN RSA PRIVATE KEY")
  const body = pem.replace(/-----[^-]+-----/g, "").replace(/\s+/g, "")
  if (!body) throw new Error("GitHub App private key is empty")
  const key = Uint8Array.from(atob(body), (char) => char.charCodeAt(0))
  if (!pkcs1) return key
  // PrivateKeyInfo ::= SEQUENCE { version 0, rsaEncryption, OCTET STRING key }
  const algorithm = [
    0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01,
    0x01, 0x05, 0x00,
  ]
  const octets = [0x04, ...derLength(key.length)]
  const inner = 3 + algorithm.length + octets.length + key.length
  return Uint8Array.from([
    0x30,
    ...derLength(inner),
    0x02,
    0x01,
    0x00,
    ...algorithm,
    ...octets,
    ...key,
  ])
}

/**
 * GitHub signs the raw body. Verify before parsing, and compare with
 * `subtle.verify` so the comparison is constant-time.
 */
export async function verifyWebhookSignature(
  secret: string,
  body: string,
  header: string | null
) {
  const hex = header?.match(/^sha256=([0-9a-f]{64})$/)?.[1]
  if (!secret || !hex) return false
  const signature = Uint8Array.from(hex.match(/../g)!, (pair) =>
    parseInt(pair, 16)
  )
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["verify"]
  )
  return crypto.subtle.verify("HMAC", key, signature, encoder.encode(body))
}

export function createGitHubApp(config: GitHubConfig) {
  const api = (config.apiUrl ?? "https://api.github.com").replace(/\/$/, "")
  const send = config.fetch ?? fetch
  let signingKey: Promise<CryptoKey> | undefined
  // Per isolate. Installation tokens last an hour; minting one per request
  // would spend the App's rate limit on token exchange.
  const tokens = new Map<number, { token: string; expiresAt: number }>()

  async function request<T>(
    path: string,
    options: { token: string; method?: string; body?: unknown }
  ): Promise<T> {
    const response = await send(`${api}${path}`, {
      method: options.method ?? "GET",
      headers: {
        Accept: "application/vnd.github+json",
        Authorization: `Bearer ${options.token}`,
        "X-GitHub-Api-Version": API_VERSION,
        "User-Agent": "hiveworker",
        ...(options.body ? { "Content-Type": "application/json" } : {}),
      },
      body: options.body ? JSON.stringify(options.body) : undefined,
    })
    if (!response.ok) {
      // The body can echo request details; keep only the status.
      await response.body?.cancel()
      throw new GitHubError(
        response.status,
        `GitHub responded ${response.status} to ${options.method ?? "GET"} ${path.split("?")[0]}`
      )
    }
    return response.status === 204 ? (undefined as T) : response.json()
  }

  /** Short-lived token that identifies the App itself. */
  async function appToken(now = Date.now()) {
    signingKey ??= crypto.subtle.importKey(
      "pkcs8",
      privateKeyToPkcs8(config.privateKey),
      { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
      false,
      ["sign"]
    )
    const seconds = Math.floor(now / 1000)
    const unsigned = `${base64url(JSON.stringify({ alg: "RS256", typ: "JWT" }))}.${base64url(
      // Backdated a minute for clock drift; GitHub caps the lifetime at ten.
      JSON.stringify({
        iss: config.appId,
        iat: seconds - 60,
        exp: seconds + 540,
      })
    )}`
    const signature = await crypto.subtle.sign(
      "RSASSA-PKCS1-v1_5",
      await signingKey,
      encoder.encode(unsigned)
    )
    return `${unsigned}.${base64url(signature)}`
  }

  async function installationToken(installationId: number) {
    const cached = tokens.get(installationId)
    if (cached && cached.expiresAt - 60_000 > Date.now()) return cached.token
    const result = await request<{ token: string; expires_at: string }>(
      `/app/installations/${installationId}/access_tokens`,
      { token: await appToken(), method: "POST" }
    )
    tokens.set(installationId, {
      token: result.token,
      expiresAt: Date.parse(result.expires_at),
    })
    return result.token
  }

  async function absent<T>(call: Promise<T>): Promise<T | null> {
    try {
      return await call
    } catch (error) {
      if (error instanceof GitHubError && error.status === 404) return null
      throw error
    }
  }

  return {
    appToken,
    installationToken,
    forgetInstallation: (installationId: number) => {
      tokens.delete(installationId)
    },

    verifyWebhook: (body: string, signature: string | null) =>
      verifyWebhookSignature(config.webhookSecret, body, signature),

    /** Check selected-repository access with the installation's own token. */
    async canAccessRepository(
      installationId: number,
      repository: string,
      repositoryId: number
    ) {
      const path = repository.split("/").map(encodeURIComponent).join("/")
      // Public metadata can be read even when this App isn't installed on a
      // repository. Ask the App installation endpoint before trusting it.
      const installation = await absent(
        request<{ id: number }>(`/repos/${path}/installation`, {
          token: await appToken(),
        })
      )
      if (installation?.id !== installationId) return false
      const result = await absent(
        request<{ id: number }>(`/repos/${path}`, {
          token: await installationToken(installationId),
        })
      )
      return result?.id === repositoryId
    },

    async getInstallation(
      installationId: number
    ): Promise<Installation | null> {
      const result = await absent(
        request<RawInstallation>(`/app/installations/${installationId}`, {
          token: await appToken(),
        })
      )
      return result ? toInstallation(result) : null
    },

    /**
     * The person's standing in the organization, asked with the
     * installation's token rather than their own, so it does not depend on a
     * user token that expires in eight hours.
     */
    async getOrgRole(
      installationId: number,
      org: string,
      username: string
    ): Promise<OrgRole | null> {
      const result = await absent(
        request<{ state: string; role: string }>(
          `/orgs/${encodeURIComponent(org)}/memberships/${encodeURIComponent(username)}`,
          { token: await installationToken(installationId) }
        )
      )
      if (result?.state !== "active") return null
      return result.role === "admin" ? "admin" : "member"
    },

    async isTeamMember(
      installationId: number,
      org: string,
      teamSlug: string,
      username: string
    ) {
      const result = await absent(
        request<{ state: string }>(
          `/orgs/${encodeURIComponent(org)}/teams/${encodeURIComponent(teamSlug)}/memberships/${encodeURIComponent(username)}`,
          { token: await installationToken(installationId) }
        )
      )
      return result?.state === "active"
    },

    /** The login behind a numeric account id. Logins change; ids do not. */
    async getLogin(installationId: number, accountId: string) {
      const result = await absent(
        request<{ login: string }>(`/user/${encodeURIComponent(accountId)}`, {
          token: await installationToken(installationId),
        })
      )
      return result?.login ?? null
    },

    /** Who a user token belongs to. */
    async getViewer(userToken: string) {
      const result = await request<{ id: number; login: string }>("/user", {
        token: userToken,
      })
      return { id: result.id, login: result.login }
    },

    /**
     * Installations of this App the person can reach, by their own token.
     * This is the only trustworthy link between a browser and an
     * installation: an `installation_id` in a URL proves nothing.
     */
    async listUserInstallationIds(userToken: string, maxPages = 5) {
      const ids: number[] = []
      for (let page = 1; page <= maxPages; page++) {
        const result = await request<{ installations: Array<{ id: number }> }>(
          `/user/installations?per_page=100&page=${page}`,
          { token: userToken }
        )
        ids.push(...result.installations.map((installation) => installation.id))
        if (result.installations.length < 100) break
      }
      return ids
    },
  }
}

export type GitHubApp = ReturnType<typeof createGitHubApp>

type RawInstallation = {
  id: number
  account: { id: number; login: string; type: string }
  suspended_at?: string | null
  repository_selection?: string
}

/** The shape shared by the REST resource and the webhook payload. */
export function toInstallation(raw: RawInstallation): Installation {
  return {
    id: raw.id,
    account: {
      id: raw.account.id,
      login: raw.account.login,
      type: raw.account.type === "Organization" ? "Organization" : "User",
    },
    suspended: Boolean(raw.suspended_at),
    repositorySelection:
      raw.repository_selection === "selected" ? "selected" : "all",
  }
}
