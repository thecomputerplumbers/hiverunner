import { betterAuth } from "better-auth"
import { drizzleAdapter } from "better-auth/adapters/drizzle"
import { mcp } from "@better-auth/mcp"
import {
  consentGrant,
  requireGrant,
  MCP_SCOPES,
  hasGrantSelection,
} from "@workspace/auth/grants"
import { organization, jwt, bearer } from "better-auth/plugins"
import { deviceAuthorization } from "better-auth/plugins/device-authorization"

import type { AuthDb } from "@workspace/auth/db"
import * as schema from "@workspace/auth/schema"

export type CreateAuthOptions = {
  db: AuthDb
  /**
   * Signs session cookies and tokens. Rotating it signs everyone out.
   * Required outside local development — see `resolveSecret`.
   */
  secret: string
  /** The app's own origin, e.g. `https://example.com`. */
  baseURL?: string
  trustedOrigins?: string[]
  /**
   * The GitHub App's own OAuth credentials. One App covers installation and
   * sign-in. It needs the account permission "Email addresses: read", or
   * GitHub returns no email and the sign-in is rejected.
   */
  github?: { clientId: string; clientSecret: string }
  /**
   * Email and password, for seeded local users and the smoke test only.
   * GitHub is the only way in anywhere a real person signs in, so the app
   * turns this on for localhost and nowhere else.
   */
  localPasswordSignIn?: boolean
  /** Appended to the built-in plugins. */
  plugins?: Parameters<typeof betterAuth>[0]["plugins"]
}

/**
 * Builds the Better Auth instance.
 *
 * Do not call this at module scope in a Worker: Better Auth starts plugin
 * setup — including database queries — as soon as it is constructed, and
 * workerd forbids I/O outside a request handler. Use `lazyAuth` below, or
 * construct it inside the handler.
 */
export function createAuth(options: CreateAuthOptions) {
  return betterAuth({
    database: drizzleAdapter(options.db, {
      provider: "sqlite",
      schema,
    }),
    secret: options.secret,
    baseURL: options.baseURL,
    trustedOrigins: options.trustedOrigins,

    socialProviders: options.github
      ? {
          github: {
            clientId: options.github.clientId,
            clientSecret: options.github.clientSecret,
          },
        }
      : undefined,

    emailAndPassword: { enabled: options.localPasswordSignIn === true },

    session: {
      expiresIn: 60 * 60 * 24 * 30,
      updateAge: 60 * 60 * 24,
      cookieCache: { enabled: false },
    },

    advanced: {
      // Cloudflare puts the real client address here; without this every
      // session records the edge's address and rate limits become useless.
      ipAddress: { ipAddressHeaders: ["cf-connecting-ip"] },
      database: { generateId: "uuid" },
    },

    rateLimit: {
      enabled: true,
      storage: "database",
      customRules: {
        "/sign-in/email": { window: 60, max: 10 },
        "/sign-in/social": { window: 60, max: 20 },
        "/oauth2/register": { window: 3600, max: 20 },
        // A daemon waiting for its owner to approve asks every 5 seconds for
        // 10 minutes. The limiter starts a new count only after a pause as
        // long as its window, which that never has, so the default of 100
        // was reached after 8 minutes. Room for ten machines at one address.
        "/device/token": { window: 10, max: 1200 },
      },
    },

    plugins: [
      // Organizations come from GitHub App installations and members from
      // GitHub. Nobody creates, invites or removes through Better Auth; the
      // Worker entry also refuses those endpoints outright.
      organization({ allowUserToCreateOrganization: false }),
      // How the daemon signs in: it shows a code, the person approves it in
      // a browser where they are signed in with GitHub, and the daemon
      // receives a session token it presents as a bearer token.
      deviceAuthorization({
        expiresIn: "10m",
        interval: "5s",
        verificationUri: "/device",
        validateClient: (clientId) => clientId === DAEMON_CLIENT_ID,
      }),
      bearer(),
      jwt(),
      mcp({
        resource: `${options.baseURL}/mcp`,
        loginPage: "/sign-in",
        consentPage: "/connect",
        scopes: [...MCP_SCOPES],
        grantTypes: ["authorization_code", "refresh_token"],
        allowDynamicClientRegistration: true,
        allowUnauthenticatedClientRegistration: true,
        clientRegistrationDefaultScopes: [
          "openid",
          "profile",
          "email",
          "offline_access",
          "hive:read",
        ],
        clientRegistrationAllowedScopes: ["hive:write"],
        accessTokenExpiresIn: 300,
        postLogin: {
          page: "/connect",
          shouldRedirect: async () => !hasGrantSelection(),
          consentReferenceId: async ({ user, scopes }) => {
            if (!user.emailVerified) throw new Error("Verify your email first")
            return (await consentGrant(options.db, user.id, scopes)).id
          },
        },
        customAccessTokenClaims: async ({ user, referenceId }) => {
          if (!user || !referenceId)
            throw new Error("Organization grant required")
          const grant = await requireGrant(options.db, referenceId, user.id)
          return { grant_id: grant.id, organization_id: grant.organizationId }
        },
      }),
      ...(options.plugins ?? []),
    ],
  })
}

/** The only client allowed to start a device sign-in. */
export const DAEMON_CLIENT_ID = "hiveworker-daemon"

export type Auth = ReturnType<typeof createAuth>

/**
 * Defers construction to the first property access, which is always inside a
 * request. Export the returned proxy as `auth` and use it like the instance.
 */
export function lazyAuth(factory: () => Auth): Auth {
  let instance: Auth | undefined
  const resolve = () => (instance ??= factory())

  return new Proxy({} as Auth, {
    get: (_target, property) => Reflect.get(resolve(), property),
    has: (_target, property) => Reflect.has(resolve(), property),
  })
}

const DEVELOPMENT_SECRET = "starter-development-secret-change-me"

/**
 * Refuses to fall back to the development secret anywhere but localhost.
 *
 * A shared default secret in production means anyone who has read this
 * repository can mint a valid session cookie for any account.
 */
export function resolveSecret(
  secret: string | undefined,
  appUrl: string | undefined
) {
  const local =
    !!appUrl && /^https?:\/\/(localhost|127\.0\.0\.1)(?::\d+)?$/.test(appUrl)

  if (!secret && !local) {
    throw new Error(
      "BETTER_AUTH_SECRET must be set outside local development " +
        "(generate one with `openssl rand -base64 32`)"
    )
  }

  return secret || DEVELOPMENT_SECRET
}
