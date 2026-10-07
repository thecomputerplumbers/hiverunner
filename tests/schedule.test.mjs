import assert from "node:assert/strict"
import { test } from "node:test"
import { pickMachine, room } from "../apps/coordinator/src/schedule.ts"

const machine = (machineId, extra = {}) => ({
  machineId,
  presence: "online",
  cores: 12,
  speedScore: 1,
  ...extra,
})

test("only free, online machines are offered jobs", () => {
  assert.equal(pickMachine([], 2), null)
  for (const presence of ["offline", "paused", "busy", "draining"])
    assert.equal(pickMachine([machine("a", { presence })], 2), null, presence)
  assert.equal(
    pickMachine([machine("a", { jobs: { x: { vcpus: 2 } } })], 2),
    null
  )
  assert.equal(pickMachine([machine("a")], 2).machineId, "a")
})

test("the fastest machine that fits wins", () => {
  const pool = [
    machine("slow", { speedScore: 0.9 }),
    machine("fast", { speedScore: 2.1 }),
    machine("fastest-but-busy", { speedScore: 3, presence: "busy" }),
  ]
  assert.equal(pickMachine(pool, 2).machineId, "fast")
})

test("a job never takes a machine's last cores from its owner", () => {
  assert.equal(pickMachine([machine("air", { cores: 8 })], 8), null)
  assert.equal(pickMachine([machine("air", { cores: 8 })], 4).machineId, "air")
  assert.equal(
    pickMachine([machine("studio", { cores: 24 })], 8).machineId,
    "studio"
  )
})

test("a job never exceeds the limit a machine's owner set", () => {
  const studio = machine("studio", { cores: 24, maxVcpus: 4 })
  assert.equal(pickMachine([studio], 8), null)
  assert.equal(pickMachine([studio], 4).machineId, "studio")
  // A faster machine with a limit loses the job to one that may take it.
  const pool = [
    machine("fast", { cores: 24, speedScore: 2, maxVcpus: 2 }),
    machine("slow", { cores: 24, speedScore: 1 }),
  ]
  assert.equal(pickMachine(pool, 8).machineId, "slow")
})

test("a machine takes several jobs only as far as its owner and cores allow", () => {
  const studio = machine("studio", { cores: 18, maxJobs: 3 })
  assert.equal(room(studio, 8), 2)
  assert.equal(room(studio, 2), 3)
  const working = { ...studio, jobs: { a: { vcpus: 8 } } }
  assert.equal(room(working, 8), 1)
  assert.equal(pickMachine([working], 8).machineId, "studio")
  const full = { ...studio, jobs: { a: { vcpus: 8 }, b: { vcpus: 8 } } }
  // A slot is left, but not the cores: sixteen are lent and two are the owner's.
  assert.equal(room(full, 2), 0)
  assert.equal(pickMachine([full], 2), null)
  // Without a word from its owner a machine runs one job, as it always has.
  assert.equal(room(machine("air", { jobs: { a: { vcpus: 2 } } }), 2), 0)
})

test("work spreads across equal machines before it stacks", () => {
  const pool = [
    machine("one", { cores: 18, maxJobs: 2, jobs: { a: { vcpus: 4 } } }),
    machine("two", { cores: 18, maxJobs: 2 }),
  ]
  assert.equal(pickMachine(pool, 4).machineId, "two")
})
