import { APIError } from "better-auth/api"
import { createResourceServerChallenge } from "@better-auth/oauth-provider"
import {
  createDpopReplayStore,
  enforceDpopBinding,
  parseAccessTokenAuthorization,
  verifyJwsAccessToken,
} from "better-auth/oauth2"
import type { Auth } from "@workspace/auth/auth"
import type { AuthDb } from "@workspace/auth/db"
import { requireGrant } from "@workspace/auth/grants"

/** Verify against this auth instance's keys, without a loopback HTTP request. */
export async function authenticateMcp(
  request: Request,
  auth: Auth,
  db: AuthDb,
  resource: string
) {
  const authorization = parseAccessTokenAuthorization(
    request.headers.get("authorization")
  )
  if (!authorization)
    throw new APIError("UNAUTHORIZED", { message: "Authorization required" })
  const context = await auth.$context
  const claims = await verifyJwsAccessToken(authorization.token, {
    jwksFetch: () => auth.api.getJwks(),
    jwksCacheKey: auth,
    verifyOptions: { issuer: context.baseURL, audience: resource },
  })
  await enforceDpopBinding({
    payload: claims,
    authorization,
    proofJwt: request.headers.get("dpop") ?? undefined,
    method: request.method,
    url: request.url,
    replayStore: createDpopReplayStore(context.internalAdapter),
  })
  if (
    typeof claims.sub !== "string" ||
    typeof claims.grant_id !== "string" ||
    typeof claims.client_id !== "string"
  )
    throw new APIError("UNAUTHORIZED")
  const grant = await requireGrant(
    db,
    claims.grant_id,
    claims.sub,
    claims.client_id
  )
  const scopes =
    typeof claims.scope === "string"
      ? claims.scope.split(" ").filter((scope) => grant.scopes.includes(scope))
      : []
  return {
    userId: claims.sub,
    organizationId: grant.organizationId,
    grantId: grant.id,
    scopes,
  }
}

/**
 * The answer to a request without a usable token. The status and the
 * `WWW-Authenticate` challenge are what clients act on and are the provider's
 * own. The body is for the agent behind the client: it says what happens
 * next and where to read more, so a cold request is not a dead end.
 */
export function mcpAuthError(
  error: unknown,
  resource: string,
  /** The way in, as a few short lines an agent can relay. */
  steps: string[] = []
) {
  const challenge =
    createResourceServerChallenge(error, resource) ??
    createResourceServerChallenge(
      new APIError("UNAUTHORIZED", {
        message: "Invalid or revoked connection",
      }),
      resource
    )!
  const origin = resource.replace(/\/mcp$/, "")
  return Response.json(
    {
      jsonrpc: "2.0",
      id: null,
      error: {
        code: -32000,
        message:
          challenge.statusCode === 403
            ? "This connection lacks the permission this needs. Connect again and approve it; changing a hive needs an owner or admin."
            : "Sign in required. Your MCP client opens a browser: sign in with GitHub and pick a hive. If you are not in a hive yet, that page says how to install HiveWorker on your organization.",
        data: {
          docs: `${origin}/docs/agents`,
          llms: `${origin}/llms.txt`,
          steps,
        },
      },
    },
    { status: challenge.statusCode, headers: challenge.headers }
  )
}
