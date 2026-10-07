import assert from "node:assert/strict"
import { test } from "node:test"
import { database } from "./database.mjs"
import {
  creditTopUp,
  getBalance,
  listEntries,
  recordPurchase,
} from "../packages/db/src/ledger.ts"
import { canAcquireJobs } from "../packages/db/src/jobs.ts"

const account = { accountType: "organization", accountId: "org_1" }

/** A completed Checkout for a $25 top-up, as Stripe reports it. */
const paid = (extra = {}) => ({
  id: "cs_test_1",
  mode: "payment",
  payment_status: "paid",
  currency: "usd",
  amount_total: 2500,
  metadata: { kind: "top-up", organization_id: "org_1", amount_usd: "25" },
  ...extra,
})

test("a paid top-up is credited once, however often it is delivered", async (t) => {
  const { db } = database(t)
  assert.equal(await creditTopUp(db, paid()), "credited")
  assert.equal(await creditTopUp(db, paid()), "already-credited")
  assert.equal(await creditTopUp(db, paid()), "already-credited")
  assert.equal(await getBalance(db, account), 25_000_000)
  const [entry] = await listEntries(db, account)
  assert.equal(entry.kind, "purchase")
  assert.equal(entry.id, "purchase:cs_test_1")
  // A second payment is a second credit.
  assert.equal(await creditTopUp(db, paid({ id: "cs_test_2" })), "credited")
  assert.equal(await getBalance(db, account), 50_000_000)
})

test("the credit is what was paid, not what the session says it was for", async (t) => {
  const { db } = database(t)
  await creditTopUp(
    db,
    paid({
      amount_total: 1000,
      metadata: {
        kind: "top-up",
        organization_id: "org_1",
        amount_usd: "100",
      },
    })
  )
  assert.equal(await getBalance(db, account), 10_000_000)
})

test("only a paid top-up is credited", async (t) => {
  const { db } = database(t)
  for (const [why, checkout] of [
    ["not paid yet", paid({ payment_status: "unpaid" })],
    ["a subscription", paid({ mode: "subscription" })],
    ["some other payment", paid({ metadata: { organization_id: "org_1" } })],
    ["no metadata at all", paid({ metadata: null })],
  ])
    assert.equal(await creditTopUp(db, checkout), "ignored", why)
  assert.equal(await getBalance(db, account), 0)
})

test("a paid top-up that cannot be credited fails loudly, so Stripe retries", async (t) => {
  const { db } = database(t)
  for (const [why, checkout] of [
    ["no organization", paid({ metadata: { kind: "top-up" } })],
    ["not dollars", paid({ currency: "eur" })],
    ["no amount", paid({ amount_total: null })],
    ["nothing paid", paid({ amount_total: 0 })],
  ])
    await assert.rejects(creditTopUp(db, checkout), /top-up/, why)
  assert.equal(await getBalance(db, account), 0)
  await assert.rejects(
    recordPurchase(db, {
      organizationId: "org_1",
      paymentId: "x",
      cents: 12.5,
    }),
    /whole number of cents/
  )
})

test("adding funds starts the queue moving again", async (t) => {
  const { db } = database(t)
  assert.equal(await canAcquireJobs(db, "org_1"), false)
  await creditTopUp(db, paid())
  assert.equal(await canAcquireJobs(db, "org_1"), true)
})
