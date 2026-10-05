import {
  createRemoteJWKSet,
  errors,
  jwtVerify,
  type JWTVerifyGetKey,
} from "jose"

const issuer = "https://token.actions.githubusercontent.com"
const githubKeys = createRemoteJWKSet(new URL(`${issuer}/.well-known/jwks`), {
  timeoutDuration: 2000,
})

export type ActionsIdentity = {
  organizationId: string
  repository: string
  repositoryId: number
}

/** Only GitHub's signing keys are trusted; no token-controlled key URLs. */
export async function verifyActionsIdentity(
  token: string,
  audience: string,
  getKey: JWTVerifyGetKey = githubKeys
): Promise<ActionsIdentity | null> {
  try {
    const { payload } = await jwtVerify(token, getKey, {
      issuer,
      audience,
      algorithms: ["RS256"],
      requiredClaims: [
        "exp",
        "nbf",
        "iat",
        "sub",
        "repository_owner_id",
        "repository_owner",
        "repository_id",
        "repository",
        "run_id",
      ],
      maxTokenAge: "10m",
      clockTolerance: 5,
    })
    const positiveId = (value: unknown) =>
      typeof value === "string" &&
      /^[1-9]\d*$/.test(value) &&
      Number.isSafeInteger(Number(value))
    if (
      !positiveId(payload.repository_owner_id) ||
      !positiveId(payload.repository_id) ||
      !positiveId(payload.run_id) ||
      typeof payload.sub !== "string" ||
      !payload.sub ||
      typeof payload.repository_owner !== "string" ||
      typeof payload.repository !== "string" ||
      !/^[\w.-]+\/[\w.-]+$/.test(payload.repository) ||
      payload.repository.split("/")[0] !== payload.repository_owner
    )
      return null
    // Subjects are customizable. Bind the pool to GitHub's immutable owner ID.
    return {
      organizationId: `gh-${payload.repository_owner_id}`,
      repository: payload.repository,
      repositoryId: Number(payload.repository_id),
    }
  } catch (error) {
    if (
      error instanceof errors.JOSEError &&
      !(error instanceof errors.JWKSTimeout)
    )
      return null
    throw error
  }
}
