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
