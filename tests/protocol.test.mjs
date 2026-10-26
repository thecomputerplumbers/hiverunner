import assert from "node:assert/strict"
import { test } from "node:test"
import { readFileSync } from "node:fs"
import { parseDaemonMessage } from "../packages/protocol/src/index.ts"

const fixtures = JSON.parse(
  readFileSync(
    new URL("../packages/protocol/fixtures/daemon.json", import.meta.url),
    "utf8"
  )
)

test("every daemon fixture parses to itself", () => {
  for (const message of fixtures)
    assert.deepEqual(parseDaemonMessage(JSON.stringify(message)), message)
})

test("a reason for no macOS jobs never refuses the machine", () => {
  const hello = {
    type: "hello",
    protocol: 3,
    daemonVersion: "0.4.37",
    presence: "online",
  }
  assert.equal(
    parseDaemonMessage(
      JSON.stringify({ ...hello, macosProblem: "x".repeat(900) })
    )?.macosProblem?.length,
    500
  )
  // A Mac that takes them has none to say, and a number is not words.
  for (const macosProblem of [7, ""])
    assert.deepEqual(
      parseDaemonMessage(JSON.stringify({ ...hello, macosProblem })),
      hello
    )
  assert.deepEqual(
    parseDaemonMessage(
      JSON.stringify({ ...hello, macos: true, macosProblem: "stale" })
    ),
    { ...hello, macos: true }
  )
})

test("frames that are not daemon messages are refused", () => {
  for (const raw of [
    "",
    "null",
    "[]",
    "{",
    JSON.stringify({ type: "welcome", machineId: "m", protocol: 1 }),
    JSON.stringify({ type: "presence", presence: "asleep" }),
    JSON.stringify({
      type: "hello",
      protocol: "1",
      daemonVersion: "0",
      presence: "online",
    }),
    JSON.stringify({ type: "job.exited", assignmentId: "a", exitCode: "0" }),
    JSON.stringify({
      type: "hello",
      protocol: 3,
      daemonVersion: "0",
      presence: "busy",
      holding: [7],
    }),
    JSON.stringify({
      type: "hello",
      protocol: 3,
      daemonVersion: "0",
      presence: "online",
      maxJobs: 99,
    }),
    JSON.stringify({
      type: "hello",
      protocol: 3,
      daemonVersion: "0",
      presence: "online",
      arch: "amd64",
    }),
    JSON.stringify({
      type: "job.start",
      assignmentId: "a",
      jitConfig: "x",
      runnerName: "r",
    }),
    JSON.stringify({
      type: "job.rejected",
      assignmentId: "a",
      reason: "x".repeat(501),
    }),
    JSON.stringify({
      type: "presence",
      presence: "online",
      pad: "x".repeat(70_000),
    }),
  ])
    assert.equal(parseDaemonMessage(raw), null, raw.slice(0, 60))
})

test("unknown fields are dropped, not passed through", () => {
  assert.deepEqual(
    parseDaemonMessage(
      JSON.stringify({ type: "presence", presence: "online", admin: true })
    ),
    { type: "presence", presence: "online" }
  )
})

test("job.exited carries an optional kill reason", () => {
  assert.deepEqual(
    parseDaemonMessage(
      JSON.stringify({
        type: "job.exited",
        assignmentId: "a-1",
        exitCode: -1,
        reason: "guest_oom",
        detail: "runner killed: guest OOM (7.9/8 GB peak) on MacBook-Pro-5",
        extra: true,
      })
    ),
    {
      type: "job.exited",
      assignmentId: "a-1",
      exitCode: -1,
      reason: "guest_oom",
      detail: "runner killed: guest OOM (7.9/8 GB peak) on MacBook-Pro-5",
    }
  )
  assert.deepEqual(
    parseDaemonMessage(
      JSON.stringify({
        type: "job.exited",
        assignmentId: "a-1",
        exitCode: 0,
        reason: "made-up",
        detail: "",
      })
    ),
    { type: "job.exited", assignmentId: "a-1", exitCode: 0 }
  )
})
