import assert from "node:assert/strict"
import { test } from "node:test"
import {
  declined,
  demand,
  fits,
  full,
  offerBackoff,
  pickMachine,
  refusing,
  room,
  routeBackoff,
  serves,
} from "../apps/coordinator/src/schedule.ts"

const arm = (vcpus) => ({ vcpus, arch: "arm64" })
const x64 = (vcpus) => ({ vcpus, arch: "x64" })
const macos = { vcpus: 4, arch: "macos" }

const machine = (machineId, extra = {}) => ({
  machineId,
  presence: "online",
  cores: 12,
  speedScore: 1,
  ...extra,
})

test("only free, online machines are offered jobs", () => {
  assert.equal(pickMachine([], arm(2)), null)
  for (const presence of ["offline", "paused", "busy", "draining"])
    assert.equal(
      pickMachine([machine("a", { presence })], arm(2)),
      null,
      presence
    )
  assert.equal(
    pickMachine([machine("a", { jobs: { x: { vcpus: 2 } } })], arm(2)),
    null
  )
  assert.equal(pickMachine([machine("a")], arm(2)).machineId, "a")
})

test("a hive's routing policy decides among the machines with room", () => {
  const pool = [
    machine("slow", { speedScore: 0.9, maxJobs: 2 }),
    machine("fast", { speedScore: 2.1, maxJobs: 2 }),
    machine("middling", { speedScore: 1.4, maxJobs: 2 }),
  ]
  const working = pool.map((candidate) =>
    candidate.machineId === "fast"
      ? { ...candidate, jobs: { x: { vcpus: 2 } } }
      : candidate
  )
  const pick = (...rest) => pickMachine(...rest).machineId

  // Fastest stacks a second job on the fast machine; spread goes elsewhere.
  assert.equal(pick(working, arm(2)), "fast")
  assert.equal(pick(working, arm(2), "fastest"), "fast")
  assert.equal(pick(working, arm(2), "spread"), "middling")
  assert.equal(pick(pool, arm(2), "spread"), "fast")

  // In turn: never given a job first, then whoever has waited longest.
  assert.equal(pick(pool, arm(2), "round-robin"), "fast")
  assert.equal(pick(pool, arm(2), "round-robin", { fast: 1 }), "middling")
  const turns = { fast: 1, middling: 2, slow: 3 }
  assert.equal(pick(pool, arm(2), "round-robin", turns), "fast")
  assert.equal(
    pick(pool, arm(2), "round-robin", { ...turns, fast: 4 }),
    "middling"
  )
  // No policy sends a job to a machine that cannot take it.
  const paused = pool.map((candidate) =>
    candidate.machineId === "middling"
      ? { ...candidate, presence: "paused" }
      : candidate
  )
  assert.equal(pick(paused, arm(2), "round-robin", { fast: 1 }), "slow")
  assert.equal(pick(pool, arm(8), "round-robin", {}), "fast")
  for (const policy of ["fastest", "spread", "round-robin"])
    assert.equal(pickMachine(pool, arm(12), policy), null, policy)
})

test("the fastest machine that fits wins", () => {
  const pool = [
    machine("slow", { speedScore: 0.9 }),
    machine("fast", { speedScore: 2.1 }),
    machine("fastest-but-busy", { speedScore: 3, presence: "busy" }),
  ]
  assert.equal(pickMachine(pool, arm(2)).machineId, "fast")
})

test("a job never takes a machine's last cores from its owner", () => {
  assert.equal(pickMachine([machine("air", { cores: 8 })], arm(8)), null)
  assert.equal(
    pickMachine([machine("air", { cores: 8 })], arm(4)).machineId,
    "air"
  )
  assert.equal(
    pickMachine([machine("studio", { cores: 24 })], arm(8)).machineId,
    "studio"
  )
})

test("a job never exceeds the limit a machine's owner set", () => {
  const studio = machine("studio", { cores: 24, maxVcpus: 4 })
  assert.equal(pickMachine([studio], arm(8)), null)
  assert.equal(pickMachine([studio], arm(4)).machineId, "studio")
  // A faster machine with a limit loses the job to one that may take it.
  const pool = [
    machine("fast", { cores: 24, speedScore: 2, maxVcpus: 2 }),
    machine("slow", { cores: 24, speedScore: 1 }),
  ]
  assert.equal(pickMachine(pool, arm(8)).machineId, "slow")
})

test("a machine takes several jobs only as far as its owner and cores allow", () => {
  const studio = machine("studio", { cores: 18, maxJobs: 3 })
  assert.equal(room(studio, arm(8)), 2)
  assert.equal(room(studio, arm(2)), 3)
  const working = { ...studio, jobs: { a: { vcpus: 8 } } }
  assert.equal(room(working, arm(8)), 1)
  assert.equal(pickMachine([working], arm(8)).machineId, "studio")
  const full = { ...studio, jobs: { a: { vcpus: 8 }, b: { vcpus: 8 } } }
  // A slot is left, but not the cores: sixteen are lent and two are the owner's.
  assert.equal(room(full, arm(2)), 0)
  assert.equal(pickMachine([full], arm(2)), null)
  // Without a word from its owner a machine runs one job, as it always has.
  assert.equal(room(machine("air", { jobs: { a: { vcpus: 2 } } }), arm(2)), 0)
})

test("work spreads across equal machines before it stacks", () => {
  const pool = [
    machine("one", { cores: 18, maxJobs: 2, jobs: { a: { vcpus: 4 } } }),
    machine("two", { cores: 18, maxJobs: 2 }),
  ]
  assert.equal(pickMachine(pool, arm(4)).machineId, "two")
})

test("a job only ever goes to a machine of its label's architecture", () => {
  const mac = machine("mac", { arch: "arm64", speedScore: 3 })
  const box = machine("box", { arch: "x64", cores: 32, maxJobs: 4 })
  // A machine that has never said what it is predates x64: it is arm64.
  const old = machine("old")
  assert.equal(pickMachine([mac, old], x64(2)), null)
  assert.equal(pickMachine([box], arm(2)), null)
  // However fast the Mac and however idle, the x64 job goes to the x64 box.
  assert.equal(pickMachine([mac, old, box], x64(2)).machineId, "box")
  assert.equal(pickMachine([box, old], arm(2)).machineId, "old")
  assert.equal(pickMachine([box, old, mac], arm(2)).machineId, "mac")
  // Nor does it count as room: capacity GitHub is told about is by label.
  assert.equal(room(mac, x64(2)), 0)
  assert.equal(room(old, x64(2)), 0)
  assert.equal(room(box, arm(2)), 0)
  assert.equal(room(box, x64(8)), 3)
  assert.equal(fits(box, arm(2)), false)
  assert.equal(fits(box, x64(2)), true)
})

test("a macOS job goes only to a Mac whose owner opted in", () => {
  const plain = machine("plain", { arch: "arm64", cores: 10 })
  const opted = machine("opted", { arch: "arm64", cores: 10, macos: true })
  const box = machine("box", { arch: "x64", cores: 32, macos: true })
  const old = machine("old", { cores: 10, macos: true })
  assert.equal(fits(plain, macos), false)
  assert.equal(fits(box, macos), false)
  // A machine that never said its architecture is an arm64 Mac.
  assert.equal(fits(old, macos), true)
  assert.equal(pickMachine([plain, box], macos), null)
  assert.equal(pickMachine([plain, box, opted], macos).machineId, "opted")
  // Opting in takes nothing away: the Mac still takes Linux arm64 jobs.
  assert.equal(fits(opted, arm(2)), true)
  assert.equal(serves({ arch: "arm64", macos: true }, "macos"), true)
  assert.equal(serves({ arch: "arm64" }, "macos"), false)
})

test("a batch of jobs is wanted whole, whatever counts came with it", () => {
  // Three jobs queued together, in a message whose counts name only one.
  const counts = {
    totalAssignedJobs: 0,
    totalRunningJobs: 0,
    totalAvailableJobs: 1,
  }
  const known = { 101: {}, 102: {}, 103: {} }
  assert.deepEqual(demand(counts, known, {}), {
    assigned: 0,
    running: 0,
    available: 3,
  })
  // The first is acquired and the counts have not caught up.
  assert.deepEqual(demand(counts, known, { 101: "owned" }), {
    assigned: 1,
    running: 0,
    available: 2,
  })
  // Counts ahead of what this listener has heard of are kept.
  assert.deepEqual(
    demand(
      { totalAssignedJobs: 4, totalRunningJobs: 2, totalAvailableJobs: 5 },
      known,
      {}
    ),
    { assigned: 4, running: 2, available: 5 }
  )
  // Before any message: only what is known.
  assert.deepEqual(demand(undefined, { 7: {} }, {}), {
    assigned: 0,
    running: 0,
    available: 1,
  })
})

test("a machine that refused a job is passed over for a while", () => {
  const now = 1_000_000
  const refused = declined(undefined, 4, true, now)
  const both = [
    machine("short", { arch: "x64", speedScore: 3, declined: refused }),
    machine("other", { arch: "x64", speedScore: 1 }),
  ]
  // The faster machine refused 4 vCPUs a moment ago: the job goes elsewhere,
  // and so does a larger one, but a smaller one may still fit it.
  assert.equal(pickMachine(both, x64(4), "fastest", {}, now).machineId, "other")
  assert.equal(pickMachine(both, x64(8), "fastest", {}, now).machineId, "other")
  assert.equal(pickMachine(both, x64(2), "fastest", {}, now).machineId, "short")
  // Once the backoff has passed, it is offered again.
  assert.equal(
    pickMachine(both, x64(4), "fastest", {}, now + offerBackoff(1)).machineId,
    "short"
  )
  // A second refusal in a row waits longer, up to a minute.
  assert.equal(offerBackoff(1), 5000)
  assert.equal(offerBackoff(2), 10000)
  assert.equal(offerBackoff(9), 60000)
  // Taking a job of that size, or a larger one, clears it.
  assert.deepEqual(declined(refused, 4, false, now), {})
  assert.deepEqual(declined(refused, 2, false, now), refused)
})

test("a machine that keeps refusing a size is not counted free for routing", () => {
  const now = 1_000_000
  const once = { declined: declined(undefined, 4, true, now) }
  const twice = { declined: declined(once.declined, 4, true, now) }
  assert.equal(refusing(once, 4, now + 1000, routeBackoff), false)
  assert.equal(refusing(twice, 4, now + 1000, routeBackoff), true)
  assert.equal(refusing(twice, 8, now + 9 * 60000, routeBackoff), true)
  assert.equal(refusing(twice, 2, now + 1000, routeBackoff), false)
  assert.equal(refusing(twice, 4, now + 10 * 60000, routeBackoff), false)
})

test("only a full machine's refusal leaves it counted", () => {
  // The daemon's ReasonBusy and ReasonMacOSVMs.
  assert.ok(full("this machine is running as many jobs as its owner allows"))
  assert.ok(
    full("this Mac is running as many macOS VMs as Apple's licence allows")
  )
  assert.ok(!full("this machine does not have the memory free for this job"))
  assert.ok(!full("this job is larger than this machine's owner allows"))
})
