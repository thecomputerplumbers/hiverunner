import assert from "node:assert/strict"
import { test } from "node:test"
import { generateKeyPairSync } from "node:crypto"
import { database } from "./database.mjs"
import {
  organizationIdFor,
  provisionInstallation,
} from "../packages/db/src/installations.ts"
import { settleJob, startJob } from "../packages/db/src/jobs.ts"
import { getLeaderboard } from "../packages/db/src/leaderboard.ts"
import {
  getBalanceHistory,
  getLiveSnapshot,
  listLiveJobs,
} from "../packages/db/src/live.ts"
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
const run = async (
  db,
  id,
  startedAt,
  minutes,
  result = "succeeded",
  size = { label: "hive", vcpus: 2 }
) => {
  const jobId = await startJob(db, {
    organizationId: orgId,
    runnerRequestId: id,
    machineId: "mac",
    ...size,
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
  // Savings count every finished job, however old, and none still running.
  assert.deepEqual(
    { ...live.lifetime, githubUsd: Math.round(live.lifetime.githubUsd * 1e6) },
    { jobs: 4, seconds: 2400, price: 4 * 3750, githubUsd: 200_000 }
  )
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

test("savings compare a job at its own label's architecture", async (t) => {
  const { db } = await pool(t)
  const x64 = { label: "hive-x64-4cpu", vcpus: 4 }
  // Ten arm64 minutes at $0.005, and in the same hour ten x64 minutes on
  // four cores at GitHub's x64 price of $0.012, not arm64's $0.008.
  await run(db, 1, at("2026-10-07T11:05:00Z"), 10)
  await run(db, 2, at("2026-10-07T11:20:00Z"), 10, "succeeded", x64)
  // The same size on arm64, to show the two are not priced alike.
  await run(db, 3, at("2026-10-07T10:20:00Z"), 10, "succeeded", {
    label: "hive-4cpu",
    vcpus: 4,
  })
  const micro = (usd) => Math.round(usd * 1e6)

  const live = await getLiveSnapshot(db, orgId, now)
  assert.equal(micro(live.hours.at(-2).githubUsd), 50_000 + 120_000)
  assert.equal(micro(live.hours.at(-3).githubUsd), 80_000)
  assert.equal(micro(live.lifetime.githubUsd), 250_000)
  assert.deepEqual(
    { jobs: live.lifetime.jobs, seconds: live.lifetime.seconds },
    { jobs: 3, seconds: 1800 }
  )

  const board = await getLeaderboard(db, orgId, now)
  const [mac] = board.machines
  assert.deepEqual(
    [
      mac.scores.all.jobs,
      mac.scores.all.seconds,
      micro(mac.scores.all.githubUsd),
    ],
    [3, 1800, 250_000]
  )
  assert.equal(micro(board.people[0].scores.week.githubUsd), 250_000)
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

test("the leaderboard credits whoever's Mac ran the job, by window", async (t) => {
  const { db, sqlite } = await pool(t)
  sqlite
    .prepare(
      "insert into user (id,name,email,email_verified,created_at,updated_at) values ('bo','Bo','bo@example.test',1,0,0)"
    )
    .run()
  sqlite
    .prepare(
      "insert into machine (id,user_id,name,model,os,arch,cores,memory_bytes,speed_score,daemon_version,token_hash,created_at) values ('idle','bo','Idle','Mac15,9','darwin','arm64',8,1,1,'0.1.0','i',0)"
    )
    .run()
  sqlite
    .prepare(
      "insert into machine_pool (machine_id,organization_id,created_at) values ('idle',?,0)"
    )
    .run(orgId)
  await run(db, 1, at("2026-10-07T11:05:00Z"), 10)
  await run(db, 2, at("2026-09-20T11:05:00Z"), 10)
  await run(db, 3, at("2026-08-01T11:05:00Z"), 10)
  await run(db, 4, at("2026-10-07T12:20:00Z"), null)

  const board = await getLeaderboard(db, orgId, now)
  const ada = board.people.find((person) => person.userId === "ada")
  assert.deepEqual(
    [ada.scores.week, ada.scores.month, ada.scores.all].map((score) => [
      score.jobs,
      score.seconds,
      Math.round(score.githubUsd * 1000),
    ]),
    [
      [1, 600, 50],
      [2, 1200, 100],
      [3, 1800, 150],
    ]
  )
  // A Mac that has joined and run nothing is still on the board, at zero.
  const bo = board.people.find((person) => person.userId === "bo")
  assert.deepEqual([bo.machines, bo.scores.all.jobs], [1, 0])
  assert.deepEqual(
    board.machines.map((mac) => [mac.name, mac.scores.all.jobs]),
    [["Mac", 3]]
  )
})

test("jobs are listed newest first, and narrowed by state and repository in SQL", async (t) => {
  const { db } = await pool(t)
  const first = await run(db, 1, at("2026-10-07T10:00:00Z"), 10)
  const second = await run(db, 2, at("2026-10-07T11:00:00Z"), 10, "failed")
  const running = await run(db, 3, at("2026-10-07T12:00:00Z"), null)
  const elsewhere = await startJob(db, {
    organizationId: orgId,
    runnerRequestId: 4,
    machineId: "mac",
    label: "hive",
    vcpus: 2,
    repository: "acme/site",
    startedAt: at("2026-10-07T12:10:00Z"),
  })
  const ids = async (limit, filter) =>
    (await listLiveJobs(db, orgId, limit, filter)).map((job) => job.id)

  // Without a filter, as the console has always called it.
  assert.deepEqual(await ids(), [elsewhere, running, second, first])
  assert.deepEqual(await ids(200, {}), [elsewhere, running, second, first])
  assert.deepEqual(await ids(200, { running: true }), [elsewhere, running])
  assert.deepEqual(await ids(200, { running: false }), [second, first])
  assert.deepEqual(await ids(200, { repository: "acme/site" }), [elsewhere])
  assert.deepEqual(
    await ids(200, { running: false, repository: "acme/site" }),
    []
  )
  // The limit is applied after the filter, not before it.
  assert.deepEqual(await ids(1, { running: false }), [second])
  // A machine in the snapshot says what kind it is.
  const [mac] = (await getLiveSnapshot(db, orgId, now)).machines
  assert.deepEqual([mac.os, mac.arch], ["darwin", "arm64"])
})
