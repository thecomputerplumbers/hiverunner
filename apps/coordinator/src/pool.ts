import { DurableObject } from "cloudflare:workers"
import { and, eq, inArray } from "drizzle-orm"
import { drizzle } from "drizzle-orm/d1"
import { githubInstallation, machine, machinePool } from "@workspace/db/schema"
import { RUNNER_SIZES } from "@workspace/pricing"
import {
  CLOSE,
  PING,
  PONG,
  PROTOCOL_VERSION,
  parseDaemonMessage,
  type CoordinatorMessage,
  type Presence,
} from "@workspace/protocol"

import { fits, pickMachine, type Candidate } from "./schedule"

/** Rides along with each socket, and survives the object hibernating. */
type Attachment = Candidate & {
  organizationId: string
  greeted: boolean
  label?: string
  accepted?: boolean
  started?: boolean
  offerExpires?: number
  exited?: boolean
}

export type Assignment = {
  assignmentId: string
  label: string
  vcpus: number
  timeoutMinutes: number
}

/**
 * One organization's pool: a socket per connected machine, and the only
 * place a machine is chosen for a job. A Durable Object runs one event at a
 * time, so two jobs cannot be handed the same machine.
 *
 * Sockets use the hibernation API. An idle pool with fifty laptops connected
 * costs nothing: the object is evicted and the sockets stay open.
 *
 * Nothing durable lives here. Who is connected is the sockets themselves;
 * what the dashboard shows is written through to D1.
 */
export class Pool extends DurableObject<CoordinatorEnv> {
  private readonly db

  constructor(ctx: DurableObjectState, env: CoordinatorEnv) {
    super(ctx, env)
    this.db = drizzle(env.DB)
    // Keepalives are answered at the edge without waking the object.
    ctx.setWebSocketAutoResponse(new WebSocketRequestResponsePair(PING, PONG))
  }

  /** The Worker has already authenticated the machine token. */
  async fetch(request: Request) {
    const machineId = request.headers.get("x-hive-machine")
    const organizationId = request.headers.get("x-hive-pool")
    const cores = Number(request.headers.get("x-hive-cores"))
    const speedScore = Number(request.headers.get("x-hive-speed"))
    if (
      !machineId ||
      !organizationId ||
      request.headers.get("upgrade") !== "websocket"
    )
      return new Response("Expected a tunnel", { status: 400 })

    // One connection per machine. A daemon that reconnects after a network
    // change wins over the half-dead socket it left behind.
    for (const old of this.ctx.getWebSockets(machineId))
      old.close(CLOSE.REPLACED, "Replaced by a newer connection")

    const { 0: client, 1: server } = new WebSocketPair()
    this.ctx.acceptWebSocket(server, [machineId])
    this.attach(server, {
      machineId,
      organizationId,
      cores,
      speedScore,
      presence: "offline",
      greeted: false,
    })
    this.send(server, {
      type: "welcome",
      machineId,
      protocol: PROTOCOL_VERSION,
    })
    await this.scheduleSweep()
    return new Response(null, { status: 101, webSocket: client })
  }

  async webSocketMessage(socket: WebSocket, raw: string | ArrayBuffer) {
    const message = typeof raw === "string" ? parseDaemonMessage(raw) : null
    if (!message)
      return this.send(socket, {
        type: "error",
        message: "Unrecognized message",
      })
    const state = this.attachment(socket)

    if (message.type === "hello") {
      if (message.protocol !== PROTOCOL_VERSION)
        return socket.close(CLOSE.UNSUPPORTED, "Update the HiveWorker daemon")
      state.greeted = true
      if (message.assignmentId) {
        const reservation = await this.ctx.storage.get<Attachment>(
          `assignment:${message.assignmentId}`
        )
        if (reservation?.machineId === state.machineId) {
          Object.assign(state, reservation, { greeted: true })
        } else {
          this.send(socket, {
            type: "job.release",
            assignmentId: message.assignmentId,
          })
        }
      }
      // Said again on every connection, so lifting a limit clears it here.
      state.maxVcpus = message.maxVcpus
      await this.db
        .update(machine)
        .set({ daemonVersion: message.daemonVersion })
        .where(eq(machine.id, state.machineId))
      return this.setPresence(
        socket,
        state,
        state.assignmentId ? "busy" : message.presence
      )
    }
    if (!state.greeted)
      return this.send(socket, { type: "error", message: "Say hello first" })

    if (message.type === "presence")
      return this.setPresence(
        socket,
        state,
        state.assignmentId ? "busy" : message.presence
      )
    if (message.type === "job.accepted") {
      if (message.assignmentId !== state.assignmentId) return
      state.accepted = true
      state.offerExpires = Date.now() + 20000
      await this.ctx.storage.put(`assignment:${state.assignmentId}`, state)
      return this.setPresence(socket, state, "busy")
    }
    // Rejected or exited: the machine is free again. What it is free *for*
    // is the daemon's to say, in its next presence message.
    if (message.assignmentId === state.assignmentId) {
      if (message.type === "job.exited")
        await this.ctx.storage.put(`assignment:${state.assignmentId}`, {
          ...state,
          exited: true,
        })
      else await this.ctx.storage.delete(`assignment:${state.assignmentId}`)
      state.assignmentId = undefined
      state.accepted = undefined
      state.started = undefined
      state.label = undefined
      state.offerExpires = undefined
      this.attach(socket, state)
    }
  }

  async webSocketClose(socket: WebSocket) {
    await this.gone(socket)
  }

  async webSocketError(socket: WebSocket) {
    await this.gone(socket)
  }

  /**
   * Offers a job to the best free machine. Returns the machine, or null when
   * nothing in the pool can take it, in which case the caller leaves the job
   * queued at GitHub.
   */
  async offer(assignment: Assignment) {
    const sockets = this.ctx.getWebSockets()
    const chosen = pickMachine(
      sockets.map((socket) => this.attachment(socket)),
      assignment.vcpus
    )
    if (!chosen) return null
    const socket = sockets.find(
      (candidate) => this.attachment(candidate).machineId === chosen.machineId
    )!
    const state = this.attachment(socket)
    state.assignmentId = assignment.assignmentId
    state.label = assignment.label
    state.accepted = false
    state.offerExpires = Date.now() + 10000
    this.attach(socket, state)
    await this.ctx.storage.put(`assignment:${assignment.assignmentId}`, state)
    this.send(socket, { type: "job.assign", ...assignment })
    return { machineId: chosen.machineId, speedScore: chosen.speedScore }
  }

  async offerStatus(assignmentId: string) {
    const state = await this.ctx.storage.get<Attachment>(
      `assignment:${assignmentId}`
    )
    if (!state) return "released" as const
    if (state.exited) return "exited" as const
    if (!state.started && (state.offerExpires ?? 0) < Date.now()) {
      await this.release(assignmentId)
      return "released" as const
    }
    if (
      !this.ctx
        .getWebSockets(state.machineId)
        .some((socket) => this.attachment(socket).assignmentId === assignmentId)
    )
      return state.started ? ("disconnected" as const) : ("pending" as const)
    return state.accepted ? ("accepted" as const) : ("pending" as const)
  }

  async start(input: {
    assignmentId: string
    jitConfig: string
    runnerName: string
  }) {
    const reservation = await this.ctx.storage.get<Attachment>(
      `assignment:${input.assignmentId}`
    )
    const socket = this.ctx
      .getWebSockets()
      .find(
        (socket) => this.attachment(socket).assignmentId === input.assignmentId
      )
    if (!reservation?.accepted || !socket) return false
    if (!reservation.started && (reservation.offerExpires ?? 0) < Date.now())
      return false
    reservation.started = true
    await this.ctx.storage.put(`assignment:${input.assignmentId}`, reservation)
    this.attach(socket, { ...this.attachment(socket), started: true })
    this.send(socket, { type: "job.start", ...input })
    return true
  }

  async release(assignmentId: string) {
    await this.ctx.storage.delete(`assignment:${assignmentId}`)
    for (const socket of this.ctx.getWebSockets()) {
      const state = this.attachment(socket)
      if (state.assignmentId !== assignmentId) continue
      const accepted = state.accepted
      this.send(socket, { type: "job.release", assignmentId })
      state.assignmentId = undefined
      state.accepted = undefined
      state.started = undefined
      state.offerExpires = undefined
      state.label = undefined
      // The daemon reports online after destroying its VM.
      if (accepted) state.presence = "busy"
      this.attach(socket, state)
    }
  }

  async capacity(label: string, vcpus: number) {
    return this.ctx.getWebSockets().filter((socket) => {
      const state = this.attachment(socket)
      return (
        state.greeted &&
        fits(state, vcpus) &&
        ((state.presence === "online" && !state.assignmentId) ||
          state.label === label)
      )
    }).length
  }

  /** An idle, currently authorized machine. Assigned listener slots don't count. */
  async availableCapacity(organizationId: string, vcpus: number) {
    const sockets = this.ctx.getWebSockets()
    const ids = sockets.map((socket) => this.attachment(socket).machineId)
    for (let i = 0; i < ids.length; i += 50) {
      const batch = ids.slice(i, i + 50)
      const rows = await this.db
        .select({ id: machine.id })
        .from(machinePool)
        .innerJoin(machine, eq(machine.id, machinePool.machineId))
        .innerJoin(
          githubInstallation,
          eq(githubInstallation.organizationId, machinePool.organizationId)
        )
        .where(
          and(
            eq(machinePool.organizationId, organizationId),
            inArray(machinePool.machineId, batch),
            eq(machinePool.status, "active"),
            eq(machine.status, "active"),
            eq(githubInstallation.status, "active")
          )
        )
      const otherPools = await this.db
        .select({ id: machinePool.machineId, presence: machinePool.presence })
        .from(machinePool)
        .where(
          and(
            inArray(machinePool.machineId, batch),
            eq(machinePool.status, "active")
          )
        )
      const occupied = new Set(
        otherPools
          .filter(
            (row) => row.presence === "busy" || row.presence === "draining"
          )
          .map((row) => row.id)
      )
      const active = new Set(rows.map((row) => row.id))
      // Re-read attachments after D1 awaits: a concurrent offer may have reserved it.
      if (
        sockets.some((socket) => {
          const state = this.attachment(socket)
          return (
            socket.readyState === WebSocket.OPEN &&
            state.organizationId === organizationId &&
            state.greeted &&
            state.presence === "online" &&
            !state.assignmentId &&
            fits(state, vcpus) &&
            active.has(state.machineId) &&
            !occupied.has(state.machineId)
          )
        })
      )
        return true
    }
    return false
  }

  private async wakeListeners(state: Attachment) {
    if (
      !this.env.GITHUB_APP_ID ||
      !this.env.GITHUB_APP_PRIVATE_KEY ||
      !this.env.RECEIPT_SIGNING_KEY
    )
      return
    for (const size of RUNNER_SIZES) {
      if (!fits(state, size.vcpus)) continue
      await this.env.LISTENER.getByName(
        `${state.organizationId}:${size.label}`
      ).activate(state.organizationId, size.label)
    }
  }

  /**
   * Revocation is a row changing in D1, written by the web app. Rather than
   * have every writer remember to tell the pool, the pool asks: while anyone
   * is connected, it re-checks them against D1 on a timer.
   */
  async alarm() {
    const sockets = this.ctx.getWebSockets()
    if (!sockets.length) return
    const organizationId = this.attachment(sockets[0]!).organizationId
    const ids = sockets.map((socket) => this.attachment(socket).machineId)
    const active = new Set<string>()
    for (let i = 0; i < ids.length; i += 50) {
      const rows = await this.db
        .select({
          id: machine.id,
          machine: machine.status,
          pool: machinePool.status,
          installation: githubInstallation.status,
        })
        .from(machinePool)
        .innerJoin(machine, eq(machine.id, machinePool.machineId))
        .innerJoin(
          githubInstallation,
          eq(githubInstallation.organizationId, machinePool.organizationId)
        )
        .where(
          and(
            eq(machinePool.organizationId, organizationId),
            inArray(machinePool.machineId, ids.slice(i, i + 50))
          )
        )
      for (const row of rows)
        if (
          row.machine === "active" &&
          row.pool === "active" &&
          row.installation === "active"
        )
          active.add(row.id)
    }
    const now = new Date()
    const seen: string[] = []
    for (const socket of sockets) {
      const state = this.attachment(socket)
      if (active.has(state.machineId)) {
        seen.push(state.machineId)
        if (state.presence === "online") await this.wakeListeners(state)
      } else
        socket.close(CLOSE.REVOKED, "This machine no longer serves this pool")
    }
    for (let i = 0; i < seen.length; i += 50)
      await this.db
        .update(machinePool)
        .set({ lastSeenAt: now })
        .where(
          and(
            eq(machinePool.organizationId, organizationId),
            inArray(machinePool.machineId, seen.slice(i, i + 50))
          )
        )
    await this.scheduleSweep()
  }

  private async setPresence(
    socket: WebSocket,
    state: Attachment,
    presence: Presence
  ) {
    state.presence = presence
    this.attach(socket, state)
    await this.db
      .update(machinePool)
      .set({ presence, lastSeenAt: new Date() })
      .where(this.row(state))
    if (presence === "online") await this.wakeListeners(state)
  }

  private async gone(socket: WebSocket) {
    const state = this.attachment(socket)
    // A replaced socket closing must not mark its successor offline.
    const successor = this.ctx
      .getWebSockets(state.machineId)
      .some((other) => other !== socket)
    if (!successor)
      await this.db
        .update(machinePool)
        .set({ presence: "offline", lastSeenAt: new Date() })
        .where(this.row(state))
  }

  /** This machine's row in this pool. */
  private row(state: Attachment) {
    return and(
      eq(machinePool.machineId, state.machineId),
      eq(machinePool.organizationId, state.organizationId)
    )
  }

  private async scheduleSweep() {
    if ((await this.ctx.storage.getAlarm()) !== null) return
    const seconds = Math.max(Number(this.env.SWEEP_SECONDS) || 60, 1)
    await this.ctx.storage.setAlarm(Date.now() + seconds * 1000)
  }

  private attachment(socket: WebSocket) {
    return socket.deserializeAttachment() as Attachment
  }

  private attach(socket: WebSocket, state: Attachment) {
    socket.serializeAttachment(state)
  }

  private send(socket: WebSocket, message: CoordinatorMessage) {
    socket.send(JSON.stringify(message))
  }
}
