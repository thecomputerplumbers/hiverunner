import { DurableObject } from "cloudflare:workers"
import { and, eq, inArray, ne } from "drizzle-orm"
import { drizzle } from "drizzle-orm/d1"
import { recordJobExit } from "@workspace/db/jobs"
import { githubInstallation, machine, machinePool } from "@workspace/db/schema"
import { routingPolicy } from "@workspace/db/settings"
import {
  ALL_RUNNER_SIZES,
  runnerArch,
  type RunnerArch,
} from "@workspace/pricing"
import {
  CLOSE,
  PING,
  PONG,
  PROTOCOL_VERSION,
  parseDaemonMessage,
  type CoordinatorMessage,
  type Presence,
} from "@workspace/protocol"

import {
  declined,
  fits,
  full,
  pickMachine,
  refusing,
  room,
  routeBackoff,
  serves,
  type Candidate,
  type JobSize,
} from "./schedule"

/**
 * A job this pool has placed on a machine. Kept small: every job a machine
 * holds rides in its socket's attachment, which has two kilobytes in all.
 */
type Held = {
  vcpus: number
  accepted?: boolean
  started?: boolean
  offerExpires?: number
  /**
   * The pool backed out of a job whose VM was already up. The cores stay
   * counted until the daemon says the VM is gone.
   */
  releasing?: boolean
}

/** Rides along with each socket, and survives the object hibernating. */
type Attachment = Candidate & {
  arch: RunnerArch
  organizationId: string
  greeted: boolean
  jobs: Record<string, Held>
}

/** What outlives a socket: which machine holds a job, and how far it got. */
type Reservation = Held & { machineId: string; exited?: boolean }

export type Assignment = {
  assignmentId: string
  label: string
  /** The architecture the label names. Only such a machine is offered it. */
  arch: RunnerArch
  vcpus: number
  timeoutMinutes: number
}

/**
 * One organization's pool: a socket per connected machine, and the only
 * place a machine is chosen for a job. A Durable Object runs one event at a
 * time, so two jobs cannot be handed the same cores.
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
      // What the machine said it was when it enrolled. A machine enrolled
      // before there were two architectures is arm64.
      arch: runnerArch(request.headers.get("x-hive-arch")) ?? "arm64",
      cores,
      speedScore,
      presence: "offline",
      greeted: false,
      jobs: {},
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
        return socket.close(CLOSE.UNSUPPORTED, "Update the Hive Runner daemon")
      state.greeted = true
      state.jobs = {}
      // A daemon that has started again, or whose owner changed what it
      // lends, is asked afresh.
      state.declined = {}
      for (const assignmentId of message.holding ?? []) {
        const reservation = await this.ctx.storage.get<Reservation>(
          `assignment:${assignmentId}`
        )
        if (reservation?.machineId === state.machineId && !reservation.exited) {
          const { machineId: _, exited: __, ...held } = reservation
          state.jobs[assignmentId] = held
        } else
          this.send(socket, {
            type: "job.release",
            assignmentId,
            reason: "the coordinator no longer holds it for this machine",
          })
      }
      // Said again on every connection, so lifting a limit clears it here.
      state.maxVcpus = message.maxVcpus
      state.maxJobs = message.maxJobs
      // The daemon knows what it is running on better than its enrollment
      // does: a machine file can be carried to another machine. One that
      // does not say is whatever it enrolled as.
      if (message.arch) state.arch = message.arch
      // Said again on every connection, like the limits: turning it off
      // clears it here.
      state.macos = message.macos === true
      await this.db
        .update(machine)
        .set({
          daemonVersion: message.daemonVersion,
          macos: state.macos,
          ...(message.arch ? { arch: message.arch } : {}),
        })
        .where(eq(machine.id, state.machineId))
      return this.setPresence(socket, state, message.presence)
    }
    if (!state.greeted)
      return this.send(socket, { type: "error", message: "Say hello first" })

    if (message.type === "presence")
      return this.setPresence(socket, state, message.presence)
    const held = state.jobs[message.assignmentId]
    if (!held) return
    if (message.type === "job.accepted") {
      state.declined = declined(state.declined, held.vcpus, false, Date.now())
      held.accepted = true
      held.offerExpires = Date.now() + 20000
      await this.reserve(message.assignmentId, state, held)
      return this.setPresence(socket, state, state.presence)
    }
    // Rejected or exited: those cores are free again. A job the pool backed
    // out of has no reservation left to mark.
    if (message.type === "job.exited") {
      if (message.reason)
        await recordJobExit(this.db, {
          assignmentId: message.assignmentId,
          organizationId: state.organizationId,
          machineId: state.machineId,
          exitCode: message.exitCode,
          reason: message.reason,
          detail: message.detail ?? "",
        })
      if (!held.releasing)
        await this.ctx.storage.put<Reservation>(
          `assignment:${message.assignmentId}`,
          { ...held, machineId: state.machineId, exited: true }
        )
      else await this.ctx.storage.delete(`assignment:${message.assignmentId}`)
    } else {
      await this.ctx.storage.delete(`assignment:${message.assignmentId}`)
      // Full is not a no to the job. Anything else is, for this size: it
      // goes to another machine for a while (see `offerBackoff`).
      if (!full(message.reason)) {
        state.declined = declined(state.declined, held.vcpus, true, Date.now())
        const refusal = state.declined[held.vcpus]!
        if (refusal.count === 1 || refusal.count % 20 === 0)
          console.info("Machine refused a job", {
            vcpus: held.vcpus,
            refusals: refusal.count,
          })
      }
    }
    delete state.jobs[message.assignmentId]
    return this.setPresence(socket, state, state.presence)
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
    if (!sockets.length) return null
    const policy = await routingPolicy(
      this.db,
      this.attachment(sockets[0]!).organizationId
    )
    // Who was given a job when, as a count of offers. Kept in storage, not
    // on the socket, so a laptop that reconnects does not come back to the
    // front of the line.
    const turns =
      policy === "round-robin"
        ? ((await this.ctx.storage.get<Record<string, number>>("turns")) ?? {})
        : {}
    const chosen = pickMachine(
      sockets.map((socket) => this.attachment(socket)),
      assignment,
      policy,
      turns
    )
    if (!chosen) return null
    if (policy === "round-robin")
      await this.ctx.storage.put("turns", {
        ...turns,
        [chosen.machineId]: Math.max(0, ...Object.values(turns)) + 1,
      })
    const socket = sockets.find(
      (candidate) => this.attachment(candidate).machineId === chosen.machineId
    )!
    const state = this.attachment(socket)
    const held: Held = {
      vcpus: assignment.vcpus,
      accepted: false,
      offerExpires: Date.now() + 10000,
    }
    state.jobs[assignment.assignmentId] = held
    this.attach(socket, state)
    await this.reserve(assignment.assignmentId, state, held)
    this.send(socket, { type: "job.assign", ...assignment })
    return { machineId: chosen.machineId, speedScore: chosen.speedScore }
  }

  async offerStatus(assignmentId: string) {
    const reservation = await this.ctx.storage.get<Reservation>(
      `assignment:${assignmentId}`
    )
    if (!reservation) return "released" as const
    if (reservation.exited) return "exited" as const
    if (!reservation.started && (reservation.offerExpires ?? 0) < Date.now()) {
      await this.release(
        assignmentId,
        "the machine did not accept the job in time"
      )
      return "released" as const
    }
    if (!this.holder(assignmentId, reservation.machineId))
      return reservation.started
        ? ("disconnected" as const)
        : ("pending" as const)
    return reservation.accepted ? ("accepted" as const) : ("pending" as const)
  }

  async start(input: {
    assignmentId: string
    jitConfig: string
    runnerName: string
  }) {
    const reservation = await this.ctx.storage.get<Reservation>(
      `assignment:${input.assignmentId}`
    )
    const socket = this.holder(input.assignmentId)
    if (!reservation?.accepted || !socket) return false
    if (!reservation.started && (reservation.offerExpires ?? 0) < Date.now())
      return false
    reservation.started = true
    await this.ctx.storage.put(`assignment:${input.assignmentId}`, reservation)
    const state = this.attachment(socket)
    state.jobs[input.assignmentId]!.started = true
    this.attach(socket, state)
    this.send(socket, { type: "job.start", ...input })
    return true
  }

  /** `reason` is for the machine's log: its owner reads why. */
  async release(assignmentId: string, reason?: string) {
    await this.ctx.storage.delete(`assignment:${assignmentId}`)
    const socket = this.holder(assignmentId)
    if (!socket) return
    const state = this.attachment(socket)
    const held = state.jobs[assignmentId]!
    this.send(socket, {
      type: "job.release",
      assignmentId,
      ...(reason ? { reason } : {}),
    })
    // A started job has a VM to destroy. Its cores are not free until the
    // daemon reports the job exited, which it does for a released job too.
    if (held.started) held.releasing = true
    else delete state.jobs[assignmentId]
    await this.setPresence(socket, state, state.presence)
  }

  /**
   * How many runners of this size the pool can have at once: the ones
   * already placed, and the room willing machines have for more. Only
   * machines of the size's architecture count, for either.
   */
  async capacity(size: JobSize) {
    let total = 0
    for (const socket of this.ctx.getWebSockets()) {
      const state = this.attachment(socket)
      if (!state.greeted || !serves(state, size.arch)) continue
      total += Object.values(state.jobs).filter(
        (held) => held.vcpus === size.vcpus && !held.releasing
      ).length
      if (state.presence === "online") total += room(state, size)
    }
    return total
  }

  /**
   * How many more jobs of this size idle, currently authorized machines
   * could start now. Assigned listener slots don't count.
   */
  async availableCapacity(organizationId: string, size: JobSize) {
    const sockets = this.ctx.getWebSockets()
    const ids = sockets.map((socket) => this.attachment(socket).machineId)
    let free = 0
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
            // This pool's own jobs are counted from its sockets, exactly.
            ne(machinePool.organizationId, organizationId),
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
      const now = Date.now()
      for (const socket of sockets) {
        const state = this.attachment(socket)
        if (
          batch.includes(state.machineId) &&
          socket.readyState === WebSocket.OPEN &&
          state.organizationId === organizationId &&
          state.greeted &&
          state.presence === "online" &&
          // One that keeps refusing this size would leave the job queued.
          !refusing(state, size.vcpus, now, routeBackoff) &&
          active.has(state.machineId) &&
          !occupied.has(state.machineId)
        )
          free += room(state, size)
      }
    }
    return free
  }

  private async wakeListeners(state: Attachment) {
    if (
      !this.env.GITHUB_APP_ID ||
      !this.env.GITHUB_APP_PRIVATE_KEY ||
      !this.env.RECEIPT_SIGNING_KEY
    )
      return
    for (const size of ALL_RUNNER_SIZES) {
      if (!fits(state, size)) continue
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

  /**
   * Records what the daemon says it is. The dashboard, and other pools
   * deciding whether this machine is spoken for, see `busy` for as long as
   * it holds any of this pool's jobs.
   */
  private async setPresence(
    socket: WebSocket,
    state: Attachment,
    presence: Presence
  ) {
    state.presence = presence
    this.attach(socket, state)
    const working = Object.keys(state.jobs).length > 0
    await this.db
      .update(machinePool)
      .set({
        presence:
          working && presence !== "offline" && presence !== "draining"
            ? "busy"
            : presence,
        lastSeenAt: new Date(),
      })
      .where(this.row(state))
    if (presence === "online") await this.wakeListeners(state)
  }

  private reserve(assignmentId: string, state: Attachment, held: Held) {
    return this.ctx.storage.put<Reservation>(`assignment:${assignmentId}`, {
      ...held,
      machineId: state.machineId,
    })
  }

  /** The socket of the machine holding a job, if it is connected. */
  private holder(assignmentId: string, machineId?: string) {
    const sockets = machineId
      ? this.ctx.getWebSockets(machineId)
      : this.ctx.getWebSockets()
    return sockets.find(
      (socket) => assignmentId in this.attachment(socket).jobs
    )
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
