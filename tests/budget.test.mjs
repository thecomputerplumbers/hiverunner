import assert from "node:assert/strict"
import { test } from "node:test"
import { database } from "./database.mjs"
import {
  budgetHolds,
  getBudget,
  setBudget,
  spentThisMonth,
} from "../packages/db/src/budget.ts"
import { canAcquireJobs } from "../packages/db/src/jobs.ts"
import { getBalance, recordPurchase } from "../packages/db/src/ledger.ts"
import { failAutoTopUp, rememberCard } from "../packages/db/src/top-up.ts"
import { sendBillingNotices } from "../apps/web/operations/billing-notices.ts"

const now = new Date("2026-10-15T12:00:00Z")
const account = { accountType: "organization", accountId: "org_1" }

/** A hive with an owner, an admin, a plain member and some credit. */
async function hive(t, { dollars = 100 } = {}) {
  const { db, sqlite } = database(t)
  sqlite.exec(
    "INSERT INTO organization (id,name,slug,created_at) VALUES ('org_1','acme','org_1',0)"
  )
  for (const [id, role] of [
    ["owner", "owner"],
    ["admin", "admin"],
    ["dev", "member"],
  ]) {
    sqlite
      .prepare(
        "insert into user (id,name,email,email_verified,created_at,updated_at) values (?,?,?,1,0,0)"
      )
      .run(id, id, `${id}@example.test`)
    sqlite
      .prepare(
        "insert into member (id,organization_id,user_id,role,created_at) values (?,?,?,?,0)"
      )
      .run(`m_${id}`, "org_1", id, role)
  }
  if (dollars)
    await recordPurchase(db, {
      organizationId: "org_1",
      paymentId: "cs_seed",
      cents: dollars * 100,
      now: new Date("2026-09-01T00:00:00Z"),
    })
  let entries = 0
  /** A ledger entry on the hive's own account, in dollars. */
  const entry = (kind, usd, at = now) =>
    sqlite
      .prepare(
        "insert into ledger_entry (id,account_type,account_id,kind,amount,created_at) values (?,?,?,?,?,?)"
      )
      .run(
        `${kind}:${++entries}`,
        "organization",
        "org_1",
        kind,
        Math.round(usd * 1_000_000),
        at.getTime()
      )
  const mail = []
  const send = async (message) => void mail.push(message)
  const sweep = (at = now) =>
    sendBillingNotices(db, send, "https://hive.test", at)
  return { db, sqlite, entry, mail, sweep }
}

test("spend is what jobs cost this month, less what the hive earned back", async (t) => {
  const { db, entry } = await hive(t)
  assert.equal(await spentThisMonth(db, "org_1", now), 0)
  entry("charge", -4)
  entry("earning", 3)
  entry("charge", -1.5)
  // Last month's jobs, and money added, are not this month's spend.
  entry("charge", -50, new Date("2026-09-30T23:59:59Z"))
  entry("purchase", 25)
  entry("gift", 5)
  assert.equal(await spentThisMonth(db, "org_1", now), 2_500_000)
  // A hive whose machines earned more than its jobs cost has spent nothing.
  entry("earning", 10)
  assert.equal(await spentThisMonth(db, "org_1", now), 0)
})

test("a budget is whole dollars, changed in place and removed", async (t) => {
  const { db } = await hive(t)
  assert.equal(await getBudget(db, "org_1"), null)
  const set = await setBudget(db, { organizationId: "org_1", dollars: 20 })
  assert.equal(set.monthlyCents, 2000)
  assert.equal(set.atLimit, "warn")
  const changed = await setBudget(db, {
    organizationId: "org_1",
    dollars: 50,
    atLimit: "pause",
  })
  assert.equal(changed.monthlyCents, 5000)
  assert.equal(changed.atLimit, "pause")
  for (const dollars of [0, -1, 2.5, 100_001, Number.NaN])
    await assert.rejects(
      setBudget(db, { organizationId: "org_1", dollars }),
      /whole number/
    )
  await assert.rejects(
    setBudget(db, { organizationId: "org_1", dollars: 5, atLimit: "explode" }),
    /limit/
  )
  assert.equal(
    await setBudget(db, { organizationId: "org_1", dollars: null }),
    null
  )
  assert.equal(await getBudget(db, "org_1"), null)
})

test("a budget holds new jobs only when asked to, and only until the month turns", async (t) => {
  const { db, entry } = await hive(t)
  entry("charge", -10)
  assert.equal(await canAcquireJobs(db, "org_1"), true)

  // Only told: spent, and still running.
  await setBudget(db, { organizationId: "org_1", dollars: 10 })
  assert.equal(await budgetHolds(db, "org_1", now), false)
  assert.equal(await canAcquireJobs(db, "org_1"), true)

  await setBudget(db, {
    organizationId: "org_1",
    dollars: 10,
    atLimit: "pause",
  })
  assert.equal(await budgetHolds(db, "org_1", now), true)
  assert.ok((await getBalance(db, account)) > 0)

  // Raising it, or the 1st, starts jobs again.
  await setBudget(db, {
    organizationId: "org_1",
    dollars: 11,
    atLimit: "pause",
  })
  assert.equal(await budgetHolds(db, "org_1", now), false)
  await setBudget(db, {
    organizationId: "org_1",
    dollars: 10,
    atLimit: "pause",
  })
  assert.equal(
    await budgetHolds(db, "org_1", new Date("2026-11-01T00:00:00Z")),
    false
  )
})

test("job acquisition stops at a held budget", async (t) => {
  const { db, sqlite, entry } = await hive(t)
  // Entries dated today, so the real clock `canAcquireJobs` uses sees them.
  entry("charge", -10, new Date())
  await setBudget(db, {
    organizationId: "org_1",
    dollars: 10,
    atLimit: "pause",
  })
  assert.equal(await canAcquireJobs(db, "org_1"), false)
  sqlite.exec("update spend_budget set at_limit = 'warn'")
  assert.equal(await canAcquireJobs(db, "org_1"), true)
})

test("owners and admins are told once at 80% of a budget and once at the limit", async (t) => {
  const { db, entry, mail, sweep } = await hive(t)
  await setBudget(db, {
    organizationId: "org_1",
    dollars: 10,
    atLimit: "pause",
  })
  entry("charge", -7)
  assert.equal(await sweep(), 0)
  assert.equal(mail.length, 0)

  entry("charge", -1)
  assert.equal(await sweep(), 1)
  assert.deepEqual(mail.map((message) => message.to).sort(), [
    "admin@example.test",
    "owner@example.test",
  ])
  assert.match(mail[0].subject, /acme: 80% of this month's budget is spent/)
  assert.match(mail[0].message, /\$8\.00 .* budget of \$10\.00/)
  assert.match(mail[0].message, /new jobs will wait/)
  assert.equal(mail[0].url, "https://hive.test/wallet")
  assert.match(mail[0].footer, /owner or admin of acme/)

  // Said once, however many sweeps pass.
  assert.equal(await sweep(), 0)
  assert.equal(mail.length, 2)

  entry("charge", -2.5)
  assert.equal(await sweep(), 1)
  assert.match(mail[2].subject, /this month's budget of \$10\.00 is spent/)
  assert.match(mail[2].message, /New jobs are now waiting/)
  assert.equal(await sweep(), 0)
  assert.equal(mail.length, 4)

  // A new month is a new budget, and is told about afresh.
  const november = new Date("2026-11-20T00:00:00Z")
  assert.equal(await sweep(november), 0)
  entry("charge", -9, november)
  assert.equal(await sweep(november), 1)
  assert.match(mail[4].subject, /80%/)
})

test("a month that jumps past the limit is told of the limit, not the warning", async (t) => {
  const { db, entry, mail, sweep } = await hive(t)
  await setBudget(db, { organizationId: "org_1", dollars: 10 })
  entry("charge", -30)
  assert.equal(await sweep(), 1)
  assert.match(mail[0].subject, /budget of \$10\.00 is spent/)
  assert.match(mail[0].message, /Jobs are still running/)
  assert.equal(await sweep(), 0)
})

test("a low balance is told once, and again if it comes back", async (t) => {
  const { db, entry, mail, sweep } = await hive(t, { dollars: 5 })
  assert.equal(await sweep(), 0)
  entry("charge", -3.5)
  assert.equal(await sweep(), 1)
  assert.match(mail[0].subject, /acme: the balance is down to \$1\.50/)
  assert.equal(await sweep(), 0)

  // Below zero is still low: jobs keep starting on the overdraft.
  entry("charge", -3)
  assert.equal(await sweep(), 0)

  // Spending the overdraft too is its own, more urgent, notice.
  entry("charge", -3.5)
  assert.equal(await sweep(), 1)
  assert.match(mail[2].subject, /the balance has run out and jobs are waiting/)
  assert.equal(await sweep(), 0)

  // Funds arrive, then run low a second time.
  await recordPurchase(db, {
    organizationId: "org_1",
    paymentId: "cs_more",
    cents: 1500,
    now,
  })
  assert.equal(await sweep(), 0)
  entry("charge", -9)
  assert.equal(await sweep(), 1)
  assert.match(mail[4].subject, /down to \$1\.00/)
})

test("a hive that tops itself up is not warned its balance is low", async (t) => {
  const { db, entry, mail, sweep } = await hive(t, { dollars: 5 })
  await rememberCard(db, {
    organizationId: "org_1",
    card: { paymentMethodId: "pm_1", brand: "visa", last4: "4242" },
    amountCents: 2500,
    enable: true,
  })
  entry("charge", -4)
  assert.equal(await sweep(), 0)

  // The charge fails: that is told, and so now is the low balance.
  await failAutoTopUp(db, {
    organizationId: "org_1",
    reason: "Your card was declined.",
  })
  assert.equal(await sweep(), 2)
  const subjects = [...new Set(mail.map((message) => message.subject))].sort()
  assert.match(subjects[0], /automatic top-up was turned off/)
  assert.match(subjects[1], /down to \$1\.00/)
  assert.match(
    mail.find((message) => /turned off/.test(message.subject)).message,
    /Your card was declined\./
  )
  assert.equal(await sweep(), 0)
})

test("a notice that cannot be sent is tried again", async (t) => {
  const { db, entry } = await hive(t, { dollars: 5 })
  entry("charge", -4)
  const failing = async () => {
    throw new Error("mail is down")
  }
  await assert.rejects(
    sendBillingNotices(db, failing, "https://hive.test", now),
    /could not be sent/
  )
  const mail = []
  assert.equal(
    await sendBillingNotices(
      db,
      async (message) => void mail.push(message),
      "https://hive.test",
      now
    ),
    1
  )
  assert.equal(mail.length, 2)
})

test("a notice that reached someone is not sent to them again", async (t) => {
  const { db, entry } = await hive(t, { dollars: 5 })
  entry("charge", -4)
  const mail = []
  // The second address bounces, every time.
  const flaky = async (message) => {
    if (mail.length) throw new Error("mail is down")
    mail.push(message)
  }
  await assert.rejects(
    sendBillingNotices(db, flaky, "https://hive.test", now),
    /could not be sent/
  )
  assert.equal(await sendBillingNotices(db, flaky, "https://hive.test", now), 0)
  assert.equal(mail.length, 1)
})
