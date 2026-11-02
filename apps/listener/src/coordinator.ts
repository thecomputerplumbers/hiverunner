import type { ScaleStatistics } from "@workspace/github/scale-sets"

export type Installation = {
  installationId: number
  accountLogin: string
  accountType: string
  status: string
}

export type PoolClient = ReturnType<CoordinatorClient["pool"]>
export type CoordinatorClient = ReturnType<typeof createCoordinatorClient>
export type { ScaleStatistics }

type Assignment = {
  assignmentId: string
  label: string
  vcpus: number
  arch: string
  timeoutMinutes: number
}

/**
 * Everything a listener needs that stays on Cloudflare, over one
 * authenticated route. The GitHub App's private key and the receipt signing
 * key never leave the coordinator: this process is handed short-lived
 * installation tokens, and asks it to record and sign what happened.
 */
export function createCoordinatorClient(config: {
  url: string
  token: string
}) {
  const tokens = new Map<number, { token: string; expiresAt: number }>()
  async function call<T>(operation: string, input: unknown): Promise<T> {
    const response = await fetch(
      new URL(`/v1/listener/${operation}`, config.url),
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${config.token}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify(input),
        signal: AbortSignal.timeout(30000),
      }
    )
    if (!response.ok)
      throw new Error(`Coordinator ${operation} responded ${response.status}`)
    return (await response.json()) as T
  }
  return {
    installation: (organizationId: string) =>
      call<Installation | null>("installation", { organizationId }),
    async installationToken(installationId: number) {
      const cached = tokens.get(installationId)
      // GitHub's last an hour; the margin is what a slow poll could outlive.
      if (cached && cached.expiresAt - 5 * 60000 > Date.now())
        return cached.token
      const issued = await call<{ token: string; expiresAt: number }>(
        "installation-token",
        { installationId }
      )
      tokens.set(installationId, issued)
      return issued.token
    },
    macosJobsAllowed: (organizationId: string) =>
      call<boolean>("macos-jobs-allowed", { organizationId }),
    canAcquireJobs: (organizationId: string) =>
      call<boolean>("can-acquire-jobs", { organizationId }),
    saveScaleSet: (input: {
      organizationId: string
      label: string
      installationId: number
      scaleSetId: number
      runnerGroupId: number
    }) => call<null>("save-scale-set", input),
    startJob: (input: {
      organizationId: string
      runnerRequestId: number | string
      machineId: string
      assignmentId: string
      speedScore?: number
      label: string
      vcpus: number
      repository: string
      jobName?: string
      workflowRunId?: number
      startedAt: Date
    }) => call<string | null>("start-job", input),
    settleJob: (input: {
      organizationId: string
      runnerRequestId: number | string
      completedAt: Date
      result: string
    }) => call<unknown>("settle-job", input),
    pool(organizationId: string) {
      const pool = <T>(method: string, ...args: unknown[]) =>
        call<T>("pool", { organizationId, method, args })
      return {
        capacity: (size: { vcpus: number; arch: string }) =>
          pool<number>("capacity", size),
        offer: (assignment: Assignment) =>
          pool<{ machineId: string; speedScore: number } | null>(
            "offer",
            assignment
          ),
        offerStatus: (assignmentId: string) =>
          pool<string>("offerStatus", assignmentId),
        start: (input: {
          assignmentId: string
          runnerName: string
          jitConfig: string
        }) => pool<boolean>("start", input),
        release: (assignmentId: string, reason?: string) =>
          pool<unknown>("release", assignmentId, reason),
      }
    },
  }
}
