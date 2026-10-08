import { DurableObject } from "cloudflare:workers"
import { eq } from "drizzle-orm"
import { drizzle } from "drizzle-orm/d1"
import {
  canAcquireJobs,
  jobIdFor,
  startJob,
  settleJob,
} from "@workspace/db/jobs"
import { createReceiptSigner } from "@workspace/db/receipts"
import { githubInstallation, runnerScaleSet } from "@workspace/db/schema"
import { macosJobsAllowed } from "@workspace/db/settings"
import { createGitHubApp } from "@workspace/github"
import {
  createScaleSetClient,
  ScaleSetError,
  type ScaleJob,
  type ScaleSession,
  type ScaleSetClient,
  type ScaleStatistics,
} from "@workspace/github/scale-sets"
import { runnerSize } from "@workspace/pricing"
import { githubTestAPI } from "./config"

type Settings = { organizationId: string; label: string; vcpus: number }
type Slot = {
  assignmentId: string
  machineId: string
  speedScore: number
  requestId: number | string
  phase: "offered" | "acquiring" | "acquired" | "starting" | "running"
  createdAt: number
  runnerName: string
  runnerId?: number
  jitConfig?: string
  githubStarted?: boolean
}
type State = {
  installationId: number
  scaleSetId: number
  session?: ScaleSession
  cursor: number
  available: Record<string, ScaleJob>
  slots: Record<string, Slot>
  /** Acquisition belongs to a request, not to a disposable runner. */
  claims: Record<string, "owned" | "uncertain">
  statistics?: ScaleStatistics
}

/**
 * One listener per organization and size. Everything needed to retry an
 * alarm lives in DO storage, including the message cursor and explicit
 * runner-to-machine mapping. Alarms keep listening while machines are
 * online; webhooks are not required to wake a queued job.
 */
export class ScaleListener extends DurableObject<CoordinatorEnv> {
  private stage = "idle"
  // Disposable credential caches only. Durable sessions/assignments below
  // remain authoritative if this isolate is evicted.
  private clientCache?: { identity: string; client: ScaleSetClient }
  async activate(organizationId: string, label: string) {
    const size = runnerSize(label)
    if (!size) throw new Error("Unknown runner size")
    const settings = await this.ctx.storage.get<Settings>("settings")
    if (
      settings &&
      (settings.organizationId !== organizationId || settings.label !== label)
    )
      throw new Error("Listener identity mismatch")
    if (!settings)
      await this.ctx.storage.put("settings", {
        organizationId,
        label,
        vcpus: size.vcpus,
      })
    // Do not continually move a scheduled poll forward on heartbeats.
    if ((await this.ctx.storage.getAlarm()) === null)
      await this.ctx.storage.setAlarm(Date.now() + 1000)
  }

  async alarm() {
    try {
      await this.tick()
    } catch (error) {
      // Status only: requests, JIT configs and tokens never reach logs.
      console.error("Scale listener retry", {
        status: error instanceof ScaleSetError ? error.status : 0,
        stage: this.stage,
        kind: error instanceof Error ? error.name : "unknown",
      })
      await this.ctx.storage.setAlarm(Date.now() + 5000)
    }
  }

  private async tick() {
    this.stage = "installation"
    const settings = await this.ctx.storage.get<Settings>("settings")
    if (
      !settings ||
      !this.env.GITHUB_APP_ID ||
      !this.env.GITHUB_APP_PRIVATE_KEY ||
      !this.env.RECEIPT_SIGNING_KEY
    )
      return
    const db = drizzle(this.env.DB)
    const [installation] = await db
      .select()
      .from(githubInstallation)
      .where(eq(githubInstallation.organizationId, settings.organizationId))
      .limit(1)
    let state = await this.ctx.storage.get<State>("state")
    const pool = this.env.POOL.getByName(settings.organizationId)
    const apiUrl = githubTestAPI(this.env)
    const app = createGitHubApp({
      appId: this.env.GITHUB_APP_ID,
      privateKey: this.env.GITHUB_APP_PRIVATE_KEY,
      webhookSecret: "",
      apiUrl,
    })
    if (!installation) return
    const identity = `${installation.installationId}:${installation.accountLogin}`
    if (this.clientCache?.identity !== identity)
      this.clientCache = {
        identity,
        client: createScaleSetClient({
          organization: installation.accountLogin,
          installationToken: () =>
            app.installationToken(installation.installationId),
          apiUrl,
          requestTimeoutMs: 10000,
        }),
      }
    const client = this.clientCache.client
    if (
      installation.status !== "active" ||
      installation.accountType !== "Organization"
    ) {
      if (state) {
        for (const slot of Object.values(state.slots))
          await pool.release(slot.assignmentId)
        if (state.session) await this.close(client, state)
        await this.ctx.storage.delete("state")
      }
      return
    }
    if (state && state.installationId !== installation.installationId) {
      for (const slot of Object.values(state.slots))
        await pool.release(slot.assignmentId)
      if (state.session) await this.close(client, state)
      state = undefined
    }
    this.stage = "capacity"
    // The label names the architecture as well as the size. It is read from
    // the label, not from storage: listeners that predate a second
    // architecture stored only a size.
    const size = {
      vcpus: settings.vcpus,
      arch: runnerSize(settings.label)?.arch ?? "arm64",
    }
    // A macOS job runs on a member's Mac itself, so the hive's owner must
    // have allowed it; until then this label has no capacity, whatever the
    // Macs say, and no scale set for it is created at GitHub.
    const allowed =
      size.arch !== "macos" ||
      (await macosJobsAllowed(db, settings.organizationId))
    const capacity = allowed ? await pool.capacity(size) : 0
    if (!capacity && !Object.keys(state?.slots ?? {}).length) {
      if (state?.session) {
        await this.close(client, state)
        await this.save(state)
      }
      return
    }
    if (!state) {
      this.stage = "scale-set"
      const set = await client.ensureScaleSet(settings.label)
      state = {
        installationId: installation.installationId,
        scaleSetId: set.id,
        cursor: 0,
        available: {},
        slots: {},
        claims: {},
      }
      await db
        .insert(runnerScaleSet)
        .values({
          organizationId: settings.organizationId,
          label: settings.label,
          installationId: installation.installationId,
          scaleSetId: set.id,
          runnerGroupId: set.runnerGroupId,
          updatedAt: new Date(),
        })
        .onConflictDoUpdate({
          target: [runnerScaleSet.organizationId, runnerScaleSet.label],
          set: {
            installationId: installation.installationId,
            scaleSetId: set.id,
            runnerGroupId: set.runnerGroupId,
            updatedAt: new Date(),
          },
        })
      await this.save(state)
    }
    if (!state.session) {
      this.stage = "session"
      state.session = await client.openSession(
        state.scaleSetId,
        `hiveworker-${this.ctx.id.toString()}`
      )
      state.cursor = 0
      state.statistics = state.session.statistics
      await this.save(state)
    }
    this.stage = "balance"
    const mayAcquire = await canAcquireJobs(db, settings.organizationId)

    // Finish handshakes before a long poll can consume the offer's deadline.
    for (const slot of Object.values(state.slots)) {
      const status = await pool.offerStatus(slot.assignmentId)
      // A VM can be working while lifecycle polling is unavailable. Only
      // consider no-start cleanup after a healthy, caught-up poll below.
      if (slot.phase === "running") continue
      if (status === "pending") continue
      if (status !== "accepted") {
        await pool.release(slot.assignmentId)
        slot.jitConfig = undefined
        // A lost JIT response can have created the runner, too. Look up the
        // unique persisted name so a retry cannot leave an offline runner.
        if (slot.phase !== "offered") {
          const runnerId =
            slot.runnerId ?? (await client.runnerByName(slot.runnerName))?.id
          if (runnerId) await client.removeRunner(runnerId)
        }
        delete state.slots[slot.assignmentId]
        if (String(slot.requestId).startsWith("demand:"))
          delete state.claims[String(slot.requestId)]
        await this.save(state)
        continue
      }
      if (slot.phase === "offered" || slot.phase === "acquiring") {
        this.stage = "acquire"
        if (
          !mayAcquire &&
          slot.phase === "offered" &&
          !state.claims[String(slot.requestId)]
        ) {
          await pool.release(slot.assignmentId)
          delete state.slots[slot.assignmentId]
          await this.save(state)
          continue
        }
        if (state.claims[String(slot.requestId)] !== "owned") {
          const retry = state.claims[String(slot.requestId)] === "uncertain"
          slot.phase = "acquiring"
          state.claims[String(slot.requestId)] = "uncertain"
          await this.save(state)
          const acquired = await client.acquire(
            state.scaleSetId,
            state.session,
            [Number(slot.requestId)]
          )
          if (!acquired.includes(Number(slot.requestId)) && !retry) {
            await pool.release(slot.assignmentId)
            delete state.slots[slot.assignmentId]
            delete state.available[String(slot.requestId)]
            delete state.claims[String(slot.requestId)]
            await this.save(state)
            continue
          }
          // A successful acquisition with a lost response may be absent on
          // retry. An extra idle JIT runner is bounded by the no-start timeout.
          state.claims[String(slot.requestId)] = "owned"
        }
        slot.phase = "acquired"
        await this.save(state)
      }
      if (slot.phase === "acquired") {
        this.stage = "jit"
        // A previous request may have registered it before losing its reply.
        const existing = await client.runnerByName(slot.runnerName)
        if (existing) await client.removeRunner(existing.id)
        const jit = await client.jit(state.scaleSetId, slot.runnerName)
        slot.runnerId = jit.runner.id
        slot.jitConfig = jit.encodedJITConfig
        slot.phase = "starting"
        await this.save(state)
      }
      if (slot.phase === "starting" && slot.jitConfig) {
        if (
          await pool.start({
            assignmentId: slot.assignmentId,
            runnerName: slot.runnerName,
            jitConfig: slot.jitConfig,
          })
        ) {
          slot.phase = "running"
          slot.createdAt = Date.now()
          slot.jitConfig = undefined
          await this.save(state)
        }
      }
    }
    if (mayAcquire || Object.keys(state.claims).length) {
      this.stage = "offer"
      for (const available of Object.values(state.available)) {
        if (!mayAcquire && !state.claims[String(jobKey(available))]) continue
        // GitHub supplies aggregate demand even when a message only carries
        // fifty jobs. Message count is never the desired runner count.
        const desired = state.statistics
          ? state.statistics.totalAssignedJobs +
            (state.statistics.totalAvailableJobs ?? 0)
          : Object.keys(state.available).length
        if (
          !state.claims[String(jobKey(available))] &&
          Object.keys(state.slots).length >= desired
        )
          break
        if (
          Object.values(state.slots).some(
            (slot) => String(slot.requestId) === String(jobKey(available))
          )
        )
          continue
        const assignmentId = crypto.randomUUID()
        const chosen = await pool.offer({
          assignmentId,
          label: settings.label,
          ...size,
          timeoutMinutes: 360,
        })
        if (!chosen) break
        state.slots[assignmentId] = {
          assignmentId,
          ...chosen,
          requestId: jobKey(available)!,
          phase: "offered",
          createdAt: Date.now(),
          runnerName: `hw-${assignmentId}`,
        }
        await this.save(state)
      }
    }
    // Initial session statistics and direct JobAssigned messages can arrive
    // without JobAvailable. Scale from aggregate already-assigned demand;
    // these requests need a runner, not a second acquisition.
    const desired = Math.min(capacity, state.statistics?.totalAssignedJobs ?? 0)
    while (mayAcquire && Object.keys(state.slots).length < desired) {
      const assignmentId = crypto.randomUUID()
      const chosen = await pool.offer({
        assignmentId,
        label: settings.label,
        ...size,
        timeoutMinutes: 360,
      })
      if (!chosen) break
      const requestId = `demand:${assignmentId}`
      state.claims[requestId] = "owned"
      state.slots[assignmentId] = {
        assignmentId,
        ...chosen,
        requestId,
        phase: "offered",
        createdAt: Date.now(),
        runnerName: `hw-${assignmentId}`,
      }
      await this.save(state)
    }
    const handshaking = Object.values(state.slots).some(
      (slot) => slot.phase !== "running"
    )
    // Poll briefly during an offer, so repeated refusals cannot hide job
    // cancellations or fresh demand behind an endlessly renewed offer.
    const pollSignal = AbortSignal.timeout(handshaking ? 1000 : 55000)
    // Capacity includes runners already assigned to this set. Exhausted
    // balances prevent new assignments without cancelling active jobs.
    try {
      this.stage = "poll"
      const message = await client.poll(
        state.session,
        state.cursor,
        mayAcquire ? capacity : Object.keys(state.slots).length,
        pollSignal
      )
      if (message) {
        state.statistics = message.statistics
        console.info("Scale demand", {
          label: settings.label,
          capacity,
          assigned: message.statistics.totalAssignedJobs,
          available: message.statistics.totalAvailableJobs ?? 0,
          messages: message.jobs.length,
        })
        for (const event of message.jobs) {
          const key = jobKey(event)
          if (key === null) continue
          if (
            event.messageType === "JobAvailable" ||
            event.messageType === "JobAssigned"
          ) {
            if (
              event.messageType === "JobAvailable" &&
              event.runnerRequestId === 0
            )
              continue
            if (event.messageType === "JobAssigned")
              state.claims[String(key)] = "owned"
            state.available[String(key)] = event
          } else if (
            event.messageType === "JobStarted" ||
            event.messageType === "JobCompleted"
          ) {
            if (event.messageType === "JobCompleted") {
              delete state.available[String(key)]
              delete state.claims[String(key)]
            }
            const slot = Object.values(state.slots).find(
              (slot) => slot.runnerName === event.runnerName
            )
            if (!slot) {
              await this.save(state)
              continue
            }
            const startedAt = githubDate(event.runnerAssignTime)
            const completedAt = githubDate(event.finishTime)
            if (
              !startedAt ||
              (event.messageType === "JobCompleted" &&
                (!completedAt || !event.result))
            ) {
              // Preserve attribution for repair without wedging the queue or
              // substituting the payee's own timestamps for GitHub's.
              await this.ctx.storage.put(`unsettled:${key}`, {
                assignmentId: slot.assignmentId,
                machineId: slot.machineId,
                runnerName: slot.runnerName,
                requestId: key,
                reason: "invalid-github-lifecycle",
                receivedAt: Date.now(),
              })
              if (event.messageType === "JobCompleted") {
                await pool.release(slot.assignmentId)
                delete state.slots[slot.assignmentId]
              }
              await this.save(state)
              continue
            }
            slot.githubStarted = true
            delete state.available[String(key)]
            const id = await startJob(db, {
              organizationId: settings.organizationId,
              runnerRequestId: key,
              machineId: slot.machineId,
              speedScore: slot.speedScore,
              label: settings.label,
              vcpus: settings.vcpus,
              repository: `${event.ownerName}/${event.repositoryName}`,
              jobName: event.jobDisplayName,
              workflowRunId: event.workflowRunId,
              startedAt,
            })
            if (!id) {
              await this.ctx.storage.put(`unsettled:${key}`, {
                assignmentId: slot.assignmentId,
                machineId: slot.machineId,
                requestId: key,
                reason: "missing-machine",
                receivedAt: Date.now(),
              })
              if (event.messageType === "JobCompleted") {
                await pool.release(slot.assignmentId)
                delete state.slots[slot.assignmentId]
              }
              await this.save(state)
              continue
            }
            if (event.messageType === "JobCompleted") {
              const signer = await createReceiptSigner(
                this.env.RECEIPT_SIGNING_KEY
              )
              await settleJob(db, signer, {
                jobId: jobIdFor(settings.organizationId, key),
                completedAt: completedAt!,
                result: event.result!,
              })
              await this.ctx.storage.delete(`unsettled:${key}`)
              await pool.release(slot.assignmentId)
              delete state.slots[slot.assignmentId]
              delete state.claims[String(slot.requestId)]
              delete state.available[String(key)]
            }
          }
          await this.save(state)
        }
        if (
          message.statistics.totalAssignedJobs === 0 &&
          (message.statistics.totalAvailableJobs ?? 0) === 0
        ) {
          for (const slot of Object.values(state.slots)) {
            if (slot.phase === "running" || slot.githubStarted) continue
            await pool.release(slot.assignmentId)
            if (slot.runnerId) await client.removeRunner(slot.runnerId)
            slot.jitConfig = undefined
            delete state.slots[slot.assignmentId]
            delete state.claims[String(slot.requestId)]
          }
          await this.save(state)
        }
        await client.acknowledge(state.session, message.messageId)
        state.cursor = message.messageId
        await this.save(state)
      }
      // Process queued lifecycle facts first, then use a fresh runner-specific
      // status. A failed poll never reaches this path, even during an outage.
      for (const slot of Object.values(state.slots)) {
        if (
          slot.phase !== "running" ||
          slot.githubStarted ||
          Date.now() - slot.createdAt < 180000 ||
          (await pool.offerStatus(slot.assignmentId)) === "pending"
        )
          continue
        // Confirm that the runner is idle (or already gone). Unknown or
        // busy runners are preserved; an API error retries without a kill.
        this.stage = "runner-status"
        const runner = await client.runnerByName(slot.runnerName)
        const status = runner ? await client.runnerStatus(runner.id) : null
        if (status?.busy) continue
        await pool.release(slot.assignmentId)
        if (runner) await client.removeRunner(runner.id)
        delete state.slots[slot.assignmentId]
        if (String(slot.requestId).startsWith("demand:"))
          delete state.claims[String(slot.requestId)]
        await this.save(state)
      }
    } catch (error) {
      if (pollSignal.aborted) {
        await this.ctx.storage.setAlarm(Date.now() + 1000)
        return
      }
      if (
        this.stage === "poll" &&
        error instanceof ScaleSetError &&
        error.status === 401
      ) {
        state.session = await client.refreshSession(
          state.scaleSetId,
          state.session
        )
        await this.save(state)
      }
      throw error
    }
    await this.ctx.storage.setAlarm(Date.now() + 1000)
  }

  private save(state: State) {
    return this.ctx.storage.put("state", state)
  }
  private async close(client: ScaleSetClient, state: State) {
    if (!state.session) return
    try {
      await client.closeSession(state.scaleSetId, state.session)
    } catch (error) {
      if (
        !(error instanceof ScaleSetError) ||
        ![401, 404].includes(error.status)
      )
        throw error
    }
    state.session = undefined
    state.cursor = 0
  }
}

function githubDate(raw: string | undefined) {
  const date = new Date(raw ?? "")
  // The API's year-0001 zero timestamp is not a verified Actions job time.
  return Number.isFinite(date.getTime()) && date.getTime() > 0 ? date : null
}

function jobKey(event: ScaleJob): number | string | null {
  if (event.runnerRequestId > 0) return event.runnerRequestId
  if (event.jobId && /^[a-zA-Z0-9-]{1,128}$/.test(event.jobId))
    return event.jobId
  // A request id of zero alone must never collapse unrelated receipts.
  return null
}
