import assert from "node:assert/strict"
import { test } from "node:test"
import { generateKeyPairSync } from "node:crypto"
import { database } from "./database.mjs"
import {
  organizationIdFor,
  provisionInstallation,
} from "../packages/db/src/installations.ts"
import {
  canAcquireJobs,
  jobIdFor,
  settleJob,
  settleLedger,
  startJob,
} from "../packages/db/src/jobs.ts"
import { getBalance } from "../packages/db/src/ledger.ts"
import {
  createReceiptSigner,
  verifyReceipt,
} from "../packages/db/src/receipts.ts"
import { updatePoolSettings } from "../packages/db/src/settings.ts"

const orgId = organizationIdFor(501)
const org = { accountType: "organization", accountId: orgId }
const treasury = { accountType: "treasury", accountId: "treasury" }
const signer = await createReceiptSigner(
  generateKeyPairSync("ed25519")
    .privateKey.export({ type: "pkcs8", format: "der" })
    .toString("base64")
)

async function pool(t, speedScore = 1.5) {
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
      "insert into machine (id,user_id,name,model,os,arch,cores,memory_bytes,speed_score,daemon_version,token_hash,created_at) values ('mac','ada','Mac','Mac15,9','darwin','arm64',12,1,?,'0.1.0','h',0)"
    )
    .run(speedScore)
  sqlite
    .prepare(
      "insert into machine_pool (machine_id,organization_id,created_at) values ('mac',?,0)"
    )
    .run(orgId)
  return { db, sqlite }
}
const started = new Date("2026-10-03T12:00:00Z")
const start = (db, runnerRequestId = 1, extra = {}) =>
  startJob(db, {
    organizationId: orgId,
    runnerRequestId,
    machineId: "mac",
    label: "hive",
    vcpus: 2,
    repository: "acme/app",
    startedAt: started,
    ...extra,
  })
const after = (seconds) => new Date(started.getTime() + seconds * 1000)

test("a finished job is priced, charged, taxed and receipted", async (t) => {
  const { db, sqlite } = await pool(t)
  const jobId = await start(db)
  assert.equal(jobId, jobIdFor(orgId, 1))
  // 10 minutes x 2 vCPUs x 1.5 speed = 30 compute units, at 500 per unit.
  const result = await settleJob(db, signer, {
    jobId,
    completedAt: after(600),
    result: "succeeded",
  })
  assert.deepEqual(result, {
    settled: true,
    amount: 15_000,
    tax: 3_750,
    earning: 11_250,
  })
  // The organization's own machine earned it back: net cost is the tax.
  assert.equal(await getBalance(db, org), 5_000_000 - 3_750)
  assert.equal(await getBalance(db, treasury), 3_750)
  const jobEntries = sqlite
    .prepare(
      "select sum(amount) total, count(*) n from ledger_entry where job_id = ?"
    )
    .get(jobId)
  assert.deepEqual({ ...jobEntries }, { total: 0, n: 3 })

  const row = sqlite.prepare("select * from job").get()
  assert.equal(
    await verifyReceipt(signer.publicKey, row.receipt, row.receipt_signature),
    true
  )
  assert.equal(row.receipt_key_id, signer.keyId)
  assert.equal(JSON.parse(row.receipt).amount, 15_000)
  assert.equal(
    await verifyReceipt(
      signer.publicKey,
      row.receipt.replace("15000", "15"),
      row.receipt_signature
    ),
    false
  )
})

test("a completion delivered twice charges once", async (t) => {
  const { db } = await pool(t)
  const jobId = await start(db)
  const done = { jobId, completedAt: after(61), result: "failed" }
  assert.equal((await settleJob(db, signer, done)).settled, true)
  assert.deepEqual(
    await settleJob(db, signer, { ...done, completedAt: after(9999) }),
    {
      settled: false,
      reason: "already-settled",
    }
  )
  assert.equal(await settleLedger(db, jobId), true)
  // 2 minutes (rounded up) x 2 x 1.5 = 6 units = 3000, of which 750 is tax.
  assert.equal(await getBalance(db, org), 5_000_000 - 750)
  assert.deepEqual(
    await settleJob(db, signer, {
      jobId: "nope",
      completedAt: after(1),
      result: "x",
    }),
    { settled: false, reason: "unknown-job" }
  )
})

test("paying the developer moves the earning to them", async (t) => {
  const { db } = await pool(t)
  await updatePoolSettings(db, orgId, {
    allowedTeams: [],
    earningsDestination: "developer",
    version: 1,
  })
  const jobId = await start(db)
  await settleJob(db, signer, {
    jobId,
    completedAt: after(600),
    result: "succeeded",
  })
  assert.equal(await getBalance(db, org), 5_000_000 - 15_000)
  assert.equal(
    await getBalance(db, { accountType: "user", accountId: "ada" }),
    11_250
  )
})

test("the price is locked when the job starts", async (t) => {
  const { db, sqlite } = await pool(t)
  const jobId = await start(db)
  sqlite.prepare("update machine set speed_score = 8").run()
  const result = await settleJob(db, signer, {
    jobId,
    completedAt: after(600),
    result: "succeeded",
  })
  assert.equal(result.amount, 15_000)
})

test("a machine cannot be billed to a pool it does not serve, and a clock skew cannot go negative", async (t) => {
  const { db } = await pool(t)
  assert.equal(await start(db, 2, { organizationId: "gh-999" }), null)
  const jobId = await start(db, 3)
  const result = await settleJob(db, signer, {
    jobId,
    completedAt: after(-30),
    result: "succeeded",
  })
  // One minute minimum: 1 x 2 x 1.5 = 3 units.
  assert.equal(result.amount, 1_500)
})

test("a balance may go $5 below zero before new jobs stop", async (t) => {
  const { db, sqlite } = await pool(t)
  const charge = sqlite.prepare(
    "insert into ledger_entry (id,account_type,account_id,kind,amount,created_at) values (?,'organization',?,'charge',?,0)"
  )
  const balance = () =>
    getBalance(db, { accountType: "organization", accountId: orgId })
  assert.equal(await canAcquireJobs(db, orgId), true)
  charge.run("to-zero", orgId, -(await balance()))
  assert.equal(await balance(), 0)
  assert.equal(await canAcquireJobs(db, orgId), true)
  charge.run("overdrawn", orgId, -4_990_000)
  assert.equal(await canAcquireJobs(db, orgId), true)
  charge.run("overdraft-spent", orgId, -10_000)
  assert.equal(await balance(), -5_000_000)
  assert.equal(await canAcquireJobs(db, orgId), false)
})

test("a retry repairs ledger entries after the receipt was committed", async (t) => {
  const { db, sqlite } = await pool(t)
  const jobId = await start(db)
  await settleJob(db, signer, {
    jobId,
    completedAt: after(60),
    result: "succeeded",
  })
  sqlite.prepare("delete from ledger_entry where job_id = ?").run(jobId)
  assert.deepEqual(
    await settleJob(db, signer, {
      jobId,
      completedAt: after(60),
      result: "succeeded",
    }),
    { settled: false, reason: "already-settled" }
  )
  assert.equal(
    sqlite
      .prepare("select count(*) n from ledger_entry where job_id = ?")
      .get(jobId).n,
    3
  )
})

test("the accepted quote survives a pool revocation and score change", async (t) => {
  const { db, sqlite } = await pool(t)
  sqlite.exec(
    "update machine set speed_score=9; update machine_pool set status='revoked'"
  )
  assert.equal(await start(db), null)
  const id = await start(db, 2, { speedScore: 1.5 })
  assert.equal(id, jobIdFor(orgId, 2))
  assert.equal(
    sqlite.prepare("select speed_score from job where id = ?").get(id)
      .speed_score,
    1.5
  )
})
