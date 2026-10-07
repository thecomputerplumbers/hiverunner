import assert from "node:assert/strict"
import { test } from "node:test"
import { generateKeyPairSync } from "node:crypto"
import { database } from "./database.mjs"
import {
  organizationIdFor,
  provisionInstallation,
} from "../packages/db/src/installations.ts"
import { settleJob, startJob } from "../packages/db/src/jobs.ts"
import { getBalanceHistory, getLiveSnapshot } from "../packages/db/src/live.ts"
import { createReceiptSigner } from "../packages/db/src/receipts.ts"
import { updatePoolSettings } from "../packages/db/src/settings.ts"

const orgId = organizationIdFor(501)
const signer = await createReceiptSigner(
  generateKeyPairSync("ed25519")
    .privateKey.export({ type: "pkcs8", format: "der" })
    .toString("base64")
)
const now = new Date("2026-10-07T12:30:00Z")
const at = (iso) => new Date(iso)

async function pool(t) {
  const { db, sqlite } = database(t)
  await provisionInstallation(db, {
    id: 9001,
    account: { id: 501, login: "acme", type: "Organization" },
    suspended: false,
    repositorySelection: "all",
  })
  sqlite
    .prepare(
      "insert into user (id,name,email,email_verified,created_at,updated_at) values ('ada','Ada','ada@example.test',1,0,0)"
    )
    .run()
  sqlite
    .prepare(
      "insert into machine (id,user_id,name,model,os,arch,cores,memory_bytes,speed_score,daemon_version,token_hash,created_at) values ('mac','ada','Mac','Mac15,9','darwin','arm64',12,1,1.5,'0.1.0','h',0)"
    )
    .run()
  sqlite
    .prepare(
      "insert into machine_pool (machine_id,organization_id,created_at) values ('mac',?,0)"
    )
    .run(orgId)
  return { db, sqlite }
}
const run = async (db, id, startedAt, minutes, result = "succeeded") => {
  const jobId = await startJob(db, {
    organizationId: orgId,
    runnerRequestId: id,
    machineId: "mac",
    label: "hive",
    vcpus: 2,
    repository: "acme/app",
    startedAt,
  })
  if (minutes !== null)
    await settleJob(db, signer, {
      jobId,
      completedAt: new Date(startedAt.getTime() + minutes * 60_000),
      result,
    })
  return jobId
}

test("the snapshot counts the window by the hour and prices it as the balance saw it", async (t) => {
  const { db } = await pool(t)
  // 10 min x 2 vCPU x 1.5 = 30 units = 15,000 charged, 11,250 earned back.
  await run(db, 1, at("2026-10-07T11:05:00Z"), 10)
  await run(db, 2, at("2026-10-07T11:40:00Z"), 10, "failed")
  await run(db, 3, at("2026-10-01T03:00:00Z"), 10)
  // Older than the window: in the balance, not in the charts.
  await run(db, 4, at("2026-09-01T03:00:00Z"), 10)
  const running = await run(db, 5, at("2026-10-07T12:20:00Z"), null)

  const live = await getLiveSnapshot(db, orgId, now)
  assert.equal(live.hours.length, 14 * 24)
  assert.equal(live.hours.at(-1).at, at("2026-10-07T12:00:00Z").getTime())
  const eleven = live.hours.at(-2)
  assert.deepEqual(
    { jobs: eleven.jobs, failed: eleven.failed, seconds: eleven.seconds },
    { jobs: 2, failed: 1, seconds: 1200 }
  )
  assert.equal(eleven.price, 2 * 3750)
  // Twenty minutes on GitHub's 2-vCPU arm64 runner at $0.005.
  assert.ok(Math.abs(eleven.githubUsd - 0.1) < 1e-9)
  assert.equal(
    live.hours.reduce((sum, hour) => sum + hour.jobs, 0),
    4
  )
  assert.equal(live.balance, 5_000_000 - 4 * 3750)
  assert.equal(live.earnsBack, true)
  assert.deepEqual(
    live.running.map((job) => [job.id, job.price, job.completedAt]),
    [[running, null, null]]
  )
  assert.equal(live.recent[0].id, running)
  assert.equal(live.recent[1].price, 3750)
  assert.deepEqual(
    live.machines.map((machine) => [machine.name, machine.jobs]),
    [["Mac", 4]]
  )
})

test("a pool that pays its developers shows the whole charge as the price", async (t) => {
  const { db } = await pool(t)
  await updatePoolSettings(db, orgId, {
    allowedTeams: [],
    earningsDestination: "developer",
    version: 1,
  })
  await run(db, 1, at("2026-10-07T11:05:00Z"), 10)
  const live = await getLiveSnapshot(db, orgId, now)
  assert.equal(live.earnsBack, false)
  assert.equal(live.recent[0].price, 15_000)
  assert.equal(live.balance, 5_000_000 - 15_000)
})

test("balance history walks back from today through the ledger", async (t) => {
  const { db, sqlite } = await pool(t)
  // The trial credit arrived long before the days being drawn.
  sqlite
    .prepare("update ledger_entry set created_at = 0 where kind = 'grant'")
    .run()
  await run(db, 1, at("2026-10-06T11:05:00Z"), 10)
  await run(db, 2, at("2026-10-07T11:05:00Z"), 10)
  const history = await getBalanceHistory(db, orgId, 3, now)
  assert.deepEqual(
    history.map((day) => day.balance),
    [5_000_000, 5_000_000 - 3750, 5_000_000 - 7500]
  )
})
