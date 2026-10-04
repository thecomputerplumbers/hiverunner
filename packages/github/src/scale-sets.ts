/**
 * GitHub's Actions scale-set service over fetch. Protocol reference:
 * https://github.com/actions/scaleset (MIT). No upstream implementation is
 * vendored here. Service credentials never go to a machine or into logs.
 */
export type ScaleSet = { id: number; name: string; runnerGroupId: number }
export type ScaleSession = {
  sessionId: string
  messageQueueUrl: string
  messageQueueAccessToken: string
  statistics?: ScaleStatistics
}
export type ScaleStatistics = {
  totalAssignedJobs: number
  totalRunningJobs: number
  totalAvailableJobs?: number
}
export type ScaleJob = {
  messageType: "JobAvailable" | "JobAssigned" | "JobStarted" | "JobCompleted"
  runnerRequestId: number
  /** Direct-assignment messages can use request id 0; jobId stays unique. */
  jobId?: string
  repositoryName: string
  ownerName: string
  jobDisplayName?: string
  workflowRunId?: number
  runnerName?: string
  runnerAssignTime?: string
  finishTime?: string
  result?: string
}
export type ScaleMessage = {
  messageId: number
  statistics: ScaleStatistics
  jobs: ScaleJob[]
}

export class ScaleSetError extends Error {
  readonly status: number
  constructor(status: number) {
    super(`GitHub Actions service responded ${status}`)
    this.status = status
    this.name = "ScaleSetError"
  }
}

type Config = {
  organization: string
  installationToken: () => Promise<string>
  fetch?: typeof fetch
  /** Test servers only; never populated from user input or a webhook. */
  apiUrl?: string
  requestTimeoutMs?: number
}

export function createScaleSetClient(config: Config) {
  const send = config.fetch ?? fetch
  const api = new URL(config.apiUrl ?? "https://api.github.com")
  let connection: { url: string; token: string; expires: number } | undefined

  // Credential-bearing redirects are never followed. Only GitHub's Actions
  // service may receive an admin/session token. A test API can trust its own
  // origin, but production cannot opt into arbitrary destinations.
  function serviceURL(raw: string) {
    const url = new URL(raw)
    const testOrigin = config.apiUrl && url.origin === api.origin
    if (
      url.username ||
      url.password ||
      url.hash ||
      (!testOrigin &&
        (url.protocol !== "https:" ||
          !url.hostname.endsWith(".actions.githubusercontent.com")))
    )
      throw new Error("Untrusted GitHub Actions service URL")
    return url
  }

  async function request<T>(
    url: URL,
    token: string,
    method = "GET",
    body?: unknown,
    extraHeaders: Record<string, string> = {},
    signal?: AbortSignal
  ): Promise<T | null> {
    const response = await send(url.toString(), {
      method,
      // workerd supports manual/follow, not the browser's error mode.
      // A redirect is a non-OK response below, so no credential follows it.
      redirect: "manual",
      signal: signal ?? AbortSignal.timeout(config.requestTimeoutMs ?? 60000),
      headers: {
        "User-Agent": "hiveworker",
        Accept: "application/json; api-version=6.0-preview",
        Authorization: token,
        "Content-Type": "application/json",
        ...extraHeaders,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    if (!response.ok) {
      await response.body?.cancel()
      throw new ScaleSetError(response.status)
    }
    if (response.status === 202 || response.status === 204) return null
    return response.json() as Promise<T>
  }

  async function admin() {
    if (connection && connection.expires > Date.now() + 60000) return connection
    const registration = await request<{ token: string }>(
      new URL(
        `/orgs/${encodeURIComponent(config.organization)}/actions/runners/registration-token`,
        api
      ),
      `Bearer ${await config.installationToken()}`,
      "POST",
      {},
      {
        Accept: "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
      }
    )
    if (!registration?.token)
      throw new Error("Missing runner registration token")
    const response = await request<{ url: string; token: string }>(
      new URL("/actions/runner-registration", api),
      `RemoteAuth ${registration.token}`,
      "POST",
      {
        url: `https://github.com/${config.organization}`,
        runner_event: "register",
      }
    )
    if (!response?.token || !response.url)
      throw new Error("Missing Actions service connection")
    serviceURL(response.url)
    // exp is a cache hint only; the token came from GitHub over verified TLS.
    let expires = Date.now() + 5 * 60000
    try {
      const payload = response.token.split(".")[1]!
      const claims = JSON.parse(
        atob(payload.replaceAll("-", "+").replaceAll("_", "/"))
      )
      if (Number.isFinite(claims.exp)) expires = claims.exp * 1000
    } catch {
      /* Conservative refresh for a non-JWT service token. */
    }
    connection = { url: response.url, token: response.token, expires }
    return connection
  }

  async function service<T>(
    path: string,
    method = "GET",
    body?: unknown,
    token?: string
  ) {
    const auth = await admin()
    const url = serviceURL(
      `${auth.url.replace(/\/$/, "")}/${path.replace(/^\//, "")}`
    )
    url.searchParams.set("api-version", "6.0-preview")
    try {
      return await request<T>(
        url,
        `Bearer ${token ?? auth.token}`,
        method,
        body
      )
    } catch (error) {
      if (error instanceof ScaleSetError && error.status === 401)
        connection = undefined
      throw error
    }
  }

  return {
    async ensureScaleSet(label: string): Promise<ScaleSet> {
      const groups = await service<{
        value: Array<{ id: number; isDefaultGroup?: boolean }>
      }>("_apis/runtime/runnergroups/?groupName=default")
      const group = groups?.value?.[0]
      if (!group) throw new Error("Default GitHub runner group is unavailable")
      const query = new URLSearchParams({
        runnerGroupId: String(group.id),
        name: label,
      })
      const sets = await service<{ value: ScaleSet[] }>(
        `_apis/runtime/runnerscalesets?${query}`
      )
      if ((sets?.value.length ?? 0) > 1)
        throw new Error("Ambiguous GitHub runner scale set")
      if (sets?.value[0]) return sets.value[0]
      const created = await service<ScaleSet>(
        "_apis/runtime/runnerscalesets",
        "POST",
        {
          name: label,
          runnerGroupId: group.id,
          labels: [{ name: label, type: "System" }],
          RunnerSetting: { disableUpdate: false },
        }
      )
      if (!created?.id)
        throw new Error("GitHub did not create a runner scale set")
      return created
    },
    async openSession(
      scaleSetId: number,
      owner: string
    ): Promise<ScaleSession> {
      const session = await service<ScaleSession>(
        `_apis/runtime/runnerscalesets/${scaleSetId}/sessions`,
        "POST",
        { ownerName: owner }
      )
      if (
        !session?.sessionId ||
        !session.messageQueueAccessToken ||
        !session.messageQueueUrl
      )
        throw new Error("Missing Actions message session")
      serviceURL(session.messageQueueUrl)
      return session
    },
    async closeSession(scaleSetId: number, session: ScaleSession) {
      await service(
        `_apis/runtime/runnerscalesets/${scaleSetId}/sessions/${encodeURIComponent(session.sessionId)}`,
        "DELETE"
      )
    },
    async refreshSession(
      scaleSetId: number,
      session: ScaleSession
    ): Promise<ScaleSession> {
      const refreshed = await service<ScaleSession>(
        `_apis/runtime/runnerscalesets/${scaleSetId}/sessions/${encodeURIComponent(session.sessionId)}`,
        "PATCH"
      )
      if (
        !refreshed?.sessionId ||
        !refreshed.messageQueueAccessToken ||
        !refreshed.messageQueueUrl
      )
        throw new Error("Missing refreshed Actions session")
      serviceURL(refreshed.messageQueueUrl)
      return refreshed
    },
    async poll(
      session: ScaleSession,
      lastMessageId: number,
      capacity: number,
      signal?: AbortSignal
    ): Promise<ScaleMessage | null> {
      const url = serviceURL(session.messageQueueUrl)
      if (lastMessageId > 0)
        url.searchParams.set("lastMessageId", String(lastMessageId))
      const raw = await request<{
        messageId: number
        body: string
        statistics: ScaleStatistics
      }>(
        url,
        `Bearer ${session.messageQueueAccessToken}`,
        "GET",
        undefined,
        { "X-ScaleSetMaxCapacity": String(Math.max(0, Math.floor(capacity))) },
        signal
      )
      if (!raw) return null
      const jobs: unknown = JSON.parse(raw.body)
      if (
        !Number.isSafeInteger(raw.messageId) ||
        !Array.isArray(jobs) ||
        !raw.statistics ||
        !Number.isSafeInteger(raw.statistics.totalAssignedJobs) ||
        raw.statistics.totalAssignedJobs < 0 ||
        !Number.isSafeInteger(raw.statistics.totalRunningJobs) ||
        raw.statistics.totalRunningJobs < 0
      )
        throw new Error("Invalid Actions message")
      for (const job of jobs) {
        if (
          !job ||
          !Number.isSafeInteger(job.runnerRequestId) ||
          job.runnerRequestId < 0 ||
          ![
            "JobAvailable",
            "JobAssigned",
            "JobStarted",
            "JobCompleted",
          ].includes(job.messageType)
        )
          throw new Error("Invalid Actions job message")
      }
      return { messageId: raw.messageId, statistics: raw.statistics, jobs }
    },
    async acknowledge(session: ScaleSession, messageId: number) {
      const url = serviceURL(session.messageQueueUrl)
      url.pathname = `${url.pathname.replace(/\/$/, "")}/${messageId}`
      await request(url, `Bearer ${session.messageQueueAccessToken}`, "DELETE")
    },
    async acquire(
      scaleSetId: number,
      session: ScaleSession,
      requestIds: number[]
    ): Promise<number[]> {
      const result = await service<{ value: number[] }>(
        `_apis/runtime/runnerscalesets/${scaleSetId}/acquirejobs`,
        "POST",
        requestIds,
        session.messageQueueAccessToken
      )
      return result?.value ?? []
    },
    async jit(
      scaleSetId: number,
      runnerName: string
    ): Promise<{
      runner: { id: number; name: string }
      encodedJITConfig: string
    }> {
      const result = await service<{
        runner: { id: number; name: string }
        encodedJITConfig: string
      }>(
        `_apis/runtime/runnerscalesets/${scaleSetId}/generatejitconfig`,
        "POST",
        { name: runnerName, workFolder: "_work" }
      )
      if (!result?.encodedJITConfig || !result.runner?.id)
        throw new Error("Missing JIT runner configuration")
      return result
    },
    async runnerByName(name: string): Promise<{ id: number } | null> {
      const result = await service<{ value: Array<{ id: number }> }>(
        `_apis/distributedtask/pools/0/agents?${new URLSearchParams({ agentName: name })}`
      )
      if ((result?.value.length ?? 0) > 1)
        throw new Error("Ambiguous runner name")
      return result?.value[0] ?? null
    },
    async removeRunner(id: number) {
      try {
        await service(`_apis/distributedtask/pools/0/agents/${id}`, "DELETE")
      } catch (error) {
        if (!(error instanceof ScaleSetError) || error.status !== 404)
          throw error
      }
    },
  }
}

export type ScaleSetClient = ReturnType<typeof createScaleSetClient>
