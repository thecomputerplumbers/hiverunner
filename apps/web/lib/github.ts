import { env } from "cloudflare:workers"
import { and, eq } from "drizzle-orm"
import { account } from "@workspace/auth/schema"
import { createGitHubApp, type GitHubApp } from "@workspace/github"
import { safeReturnPath } from "@workspace/stripe"

import { getDb } from "@/db"
import { auth } from "@/lib/auth"
import { logError } from "@/lib/diagnostics"

const local = () =>
  /^http:\/\/(localhost|127\.0\.0\.1)(:\d+)?$/.test(env.APP_URL ?? "")

let app: GitHubApp | undefined

/**
 * Null until the App's credentials are set. Local development without a
 * GitHub App still works: seeded pools and members simply are not re-checked.
 */
export function getGitHubApp() {
  if (
    !env.GITHUB_APP_ID ||
    !env.GITHUB_APP_PRIVATE_KEY ||
    !env.GITHUB_WEBHOOK_SECRET
  )
    return null
  return (app ??= createGitHubApp({
    appId: env.GITHUB_APP_ID,
    privateKey: env.GITHUB_APP_PRIVATE_KEY,
    webhookSecret: env.GITHUB_WEBHOOK_SECRET,
    // Never redirect a deployed Worker's GitHub traffic by configuration.
    apiUrl: local() ? env.GITHUB_API_URL : undefined,
  }))
}

/**
 * Where the App is installed. `state` comes back, untouched and so
 * untrusted, on the setup URL GitHub returns the browser to.
 */
export function installUrl(state?: string) {
  if (!env.GITHUB_APP_SLUG) return null
  const url = `https://github.com/apps/${encodeURIComponent(env.GITHUB_APP_SLUG)}/installations/new`
  return state ? `${url}?state=${encodeURIComponent(state)}` : url
}

/**
 * The one place an install may return to other than the dashboard: the
 * consent page of an agent's connection, which is where the person was when
 * they found they had no hive. Anything else is ignored. The page checks the
 * signed query itself, so a made-up one gets nowhere.
 */
export function consentReturnPath(value: string | null) {
  const path = safeReturnPath(value, "")
  return path.startsWith("/connect?") ? path : null
}

/**
 * The person's own GitHub token, or null when they did not sign in with
 * GitHub or the token can no longer be renewed.
 *
 * At sign-in it is minutes old. Later callers, such as the return from
 * installing the App on a second organization, find it expired, since GitHub
 * issues it for eight hours; it is renewed here with the refresh token, which
 * lasts six months. Everything else is asked with installation tokens.
 */
export async function githubUserToken(userId: string) {
  const [linked] = await getDb()
    .select({
      id: account.id,
      accessToken: account.accessToken,
      expiresAt: account.accessTokenExpiresAt,
      refreshToken: account.refreshToken,
    })
    .from(account)
    .where(and(eq(account.userId, userId), eq(account.providerId, "github")))
    .limit(1)
  if (!linked?.accessToken) return null
  if (!linked.expiresAt || linked.expiresAt.getTime() >= Date.now() + 30_000)
    return linked.accessToken
  if (!linked.refreshToken) return null
  try {
    const renewed = await auth.api.getAccessToken({
      body: { accountId: linked.id, userId },
    })
    return renewed.accessToken || null
  } catch (error) {
    // A revoked or spent refresh token: the person has to sign in again.
    logError(error, "github.user-token")
    return null
  }
}
