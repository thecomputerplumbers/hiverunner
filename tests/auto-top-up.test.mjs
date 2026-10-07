import assert from "node:assert/strict"
import { test } from "node:test"
import { database } from "./database.mjs"
import { getBalance, recordPurchase } from "../packages/db/src/ledger.ts"
import {
  claimDueTopUps,
  getAutoTopUp,
  rememberCard,
  setAutoTopUp,
} from "../packages/db/src/top-up.ts"
import { upsertCustomer } from "../packages/stripe/src/store.ts"
import { StripeError } from "../packages/stripe/src/client.ts"
import {
  chargeDueTopUps,
  handleIntentFailed,
  handleIntentSucceeded,
  handlePaidCheckout,
} from "../apps/web/operations/top-up.ts"

const account = { accountType: "organization", accountId: "org_1" }
const card = { paymentMethodId: "pm_1", brand: "visa", last4: "4242" }

/** An organization with a balance, a Stripe customer and, usually, a card. */
async function organization(
  t,
  { dollars = 2, saved = true, enable = true } = {}
) {
  const { db, sqlite } = database(t)
  sqlite.exec(
    "INSERT INTO organization (id,name,slug,created_at) VALUES ('org_1','acme','org_1',0)"
  )
  if (dollars)
    await recordPurchase(db, {
      organizationId: "org_1",
      paymentId: "seed",
      cents: dollars * 100,
    })
  await upsertCustomer(db, {
    id: "cus_1",
    organizationId: "org_1",
    email: "owner@example.test",
    livemode: false,
  })
  if (saved)
    await rememberCard(db, {
      organizationId: "org_1",
      card,
      amountCents: 2500,
      enable,
    })
  return db
}

/** Stripe, answering a charge the way the test says and keeping the calls. */
function stripe(answer = { id: "pi_1", status: "succeeded" }) {
  const calls = []
  return {
    calls,
    config: { livemode: false },
    get: async () => ({
      id: "pi_checkout",
      status: "succeeded",
      payment_method: {
        id: "pm_new",
        card: { brand: "mastercard", last4: "4444" },
      },
    }),
    post: async (path, params, options) => {
      calls.push({ path, params, options })
      if (answer instanceof Error) throw answer
      return answer
    },
  }
}

test("a low balance charges the saved card once, and the credit comes from Stripe's word", async (t) => {
  const db = await organization(t)
  const fake = stripe()
  const now = new Date("2026-10-07T12:00:00Z")
  assert.equal(await chargeDueTopUps(db, fake, now), 1)
  assert.equal(fake.calls.length, 1)
  const { path, params, options } = fake.calls[0]
  assert.equal(path, "payment_intents")
  assert.deepEqual(
    {
      amount: params.amount,
      currency: params.currency,
      customer: params.customer,
      payment_method: params.payment_method,
      off_session: params.off_session,
      confirm: params.confirm,
      metadata: params.metadata,
    },
    {
      amount: 2500,
      currency: "usd",
      customer: "cus_1",
      payment_method: "pm_1",
      off_session: true,
      confirm: true,
      metadata: { kind: "auto-top-up", organization_id: "org_1" },
    }
  )
  assert.equal(options.idempotencyKey, `auto-top-up:org_1:${now.getTime()}`)
  // Charging credits nothing. The balance is still $2.
  assert.equal(await getBalance(db, account), 2_000_000)

  // The sweep runs again before Stripe's event arrives. It asks again under
  // the same name, which Stripe answers with the same charge, not a new one.
  await chargeDueTopUps(db, fake, new Date(now.getTime() + 120_000))
  assert.equal(fake.calls.length, 2)
  assert.equal(fake.calls[1].options.idempotencyKey, options.idempotencyKey)

  const paid = {
    id: "pi_1",
    status: "succeeded",
    amount_received: 2500,
    currency: "usd",
    metadata: { kind: "auto-top-up", organization_id: "org_1" },
  }
  assert.equal(await handleIntentSucceeded(db, paid), "credited")
  assert.equal(await handleIntentSucceeded(db, paid), "already-credited")
  assert.equal(await getBalance(db, account), 27_000_000)
  // Topped up, so nothing is due, and the organization is free to be
  // charged again the next time it runs low.
  assert.equal(
    await chargeDueTopUps(db, fake, new Date(now.getTime() + 240_000)),
    0
  )
  assert.equal((await getAutoTopUp(db, "org_1")).pendingSince, null)
})

test("nothing is charged unless it was asked for and is needed", async (t) => {
  for (const [why, setup] of [
    ["the balance is not low", { dollars: 5 }],
    ["automatic top-up is off", { enable: false }],
    ["there is no saved card", { saved: false }],
  ]) {
    const db = await organization(t, setup)
    const fake = stripe()
    assert.equal(await chargeDueTopUps(db, fake), 0, why)
    assert.equal(fake.calls.length, 0, why)
  }
})

test("two sweeps at once name the same charge", async (t) => {
  const db = await organization(t)
  const now = new Date("2026-10-07T12:00:00Z")
  const later = new Date(now.getTime() + 1000)
  const [first] = await claimDueTopUps(db, now)
  const [second] = await claimDueTopUps(db, later)
  assert.equal(first.claimedAt.getTime(), now.getTime())
  assert.equal(second.claimedAt.getTime(), now.getTime())
  // A charge never heard of again is given up after an hour and named anew.
  const [almost] = await claimDueTopUps(
    db,
    new Date(now.getTime() + 59 * 60_000)
  )
  assert.equal(almost.claimedAt.getTime(), now.getTime())
  const again = new Date(now.getTime() + 61 * 60_000)
  const [renewed] = await claimDueTopUps(db, again)
  assert.equal(renewed.claimedAt.getTime(), again.getTime())
})

test("a declined card turns automatic top-up off and says why", async (t) => {
  const db = await organization(t)
  const declined = new StripeError("Your card was declined.", {
    status: 402,
    type: "card_error",
    code: "card_declined",
  })
  await chargeDueTopUps(db, stripe(declined))
  let saved = await getAutoTopUp(db, "org_1")
  assert.equal(saved.enabled, false)
  assert.equal(saved.lastError, "Your card was declined.")
  assert.equal(saved.pendingSince, null)
  // And it is not tried again on the next sweep.
  const fake = stripe()
  assert.equal(await chargeDueTopUps(db, fake), 0)
  assert.equal(fake.calls.length, 0)
  // Turning it back on clears the complaint.
  saved = await setAutoTopUp(db, { organizationId: "org_1", enabled: true })
  assert.equal(saved.enabled, true)
  assert.equal(saved.lastError, null)
})

test("a bank that wants approval, or a failure reported later, also stops it", async (t) => {
  let db = await organization(t)
  await chargeDueTopUps(db, stripe({ id: "pi_2", status: "requires_action" }))
  assert.equal((await getAutoTopUp(db, "org_1")).enabled, false)

  db = await organization(t)
  await chargeDueTopUps(db, stripe({ id: "pi_3", status: "processing" }))
  assert.equal((await getAutoTopUp(db, "org_1")).enabled, true)
  const failed = {
    id: "pi_3",
    status: "requires_payment_method",
    metadata: { kind: "auto-top-up", organization_id: "org_1" },
    last_payment_error: { message: "Your card has insufficient funds." },
  }
  assert.equal(await handleIntentFailed(db, failed), "stopped")
  const saved = await getAutoTopUp(db, "org_1")
  assert.equal(saved.enabled, false)
  assert.equal(saved.lastError, "Your card has insufficient funds.")
  assert.equal(await getBalance(db, account), 2_000_000)
})

test("Stripe being unreachable is not a declined card", async (t) => {
  const db = await organization(t)
  await assert.rejects(chargeDueTopUps(db, stripe(new Error("network down"))))
  await assert.rejects(
    chargeDueTopUps(
      await organization(t),
      stripe(new StripeError("Stripe is having trouble", { status: 503 }))
    )
  )
  // Still on, and still claimed: the next sweep sends the same charge again.
  const saved = await getAutoTopUp(db, "org_1")
  assert.equal(saved.enabled, true)
  assert.notEqual(saved.pendingSince, null)
  const fake = stripe()
  await chargeDueTopUps(db, fake)
  assert.equal(
    fake.calls[0].options.idempotencyKey,
    `auto-top-up:org_1:${saved.pendingSince.getTime()}`
  )
})

test("another payment on the account is none of our business", async (t) => {
  const db = await organization(t)
  for (const intent of [
    { id: "pi_x", status: "succeeded", amount_received: 900, currency: "usd" },
    {
      id: "pi_y",
      status: "succeeded",
      amount_received: 900,
      currency: "usd",
      metadata: { kind: "report_unlock" },
    },
  ]) {
    assert.equal(await handleIntentSucceeded(db, intent), "ignored")
    assert.equal(await handleIntentFailed(db, intent), "ignored")
  }
  assert.equal(await getBalance(db, account), 2_000_000)
  assert.equal((await getAutoTopUp(db, "org_1")).enabled, true)
  await assert.rejects(
    handleIntentSucceeded(db, {
      id: "pi_z",
      status: "succeeded",
      amount_received: 2500,
      currency: "eur",
      metadata: { kind: "auto-top-up", organization_id: "org_1" },
    }),
    /top-up/
  )
})

test("paying by hand saves the card only when asked, and never switches it off", async (t) => {
  const checkout = (extra = {}) => ({
    id: "cs_1",
    mode: "payment",
    payment_status: "paid",
    currency: "usd",
    amount_total: 5000,
    payment_intent: "pi_checkout",
    metadata: { kind: "top-up", organization_id: "org_1" },
    ...extra,
  })
  // Not asked: credited, and no card is remembered.
  let db = await organization(t, { dollars: 0, saved: false })
  assert.equal(await handlePaidCheckout(db, stripe(), checkout()), "credited")
  assert.equal(await getBalance(db, account), 50_000_000)
  assert.equal(await getAutoTopUp(db, "org_1"), null)

  // Asked: the card that paid is saved and automatic top-up is on, for the
  // amount just paid.
  db = await organization(t, { dollars: 0, saved: false })
  const asked = checkout({
    metadata: { kind: "top-up", organization_id: "org_1", auto: "1" },
  })
  await handlePaidCheckout(db, stripe(), asked)
  let saved = await getAutoTopUp(db, "org_1")
  assert.deepEqual(
    [
      saved.enabled,
      saved.amountCents,
      saved.paymentMethodId,
      saved.cardBrand,
      saved.cardLast4,
    ],
    [true, 5000, "pm_new", "mastercard", "4444"]
  )
  // Delivered again: one credit, same card.
  assert.equal(
    await handlePaidCheckout(db, stripe(), asked),
    "already-credited"
  )
  assert.equal(await getBalance(db, account), 50_000_000)

  // A later payment made without the box leaves automatic top-up as it was.
  await handlePaidCheckout(db, stripe(), checkout({ id: "cs_2" }))
  saved = await getAutoTopUp(db, "org_1")
  assert.equal(saved.enabled, true)
  assert.equal(saved.paymentMethodId, "pm_new")
})

test("automatic top-up cannot be turned on without a card, or for an amount not offered", async (t) => {
  let db = await organization(t, { saved: false })
  await assert.rejects(
    setAutoTopUp(db, { organizationId: "org_1", enabled: true }),
    /by card once/
  )
  assert.equal(
    await setAutoTopUp(db, { organizationId: "org_1", enabled: false }),
    null
  )
  db = await organization(t)
  await assert.rejects(
    setAutoTopUp(db, {
      organizationId: "org_1",
      enabled: true,
      amountCents: 1234,
    }),
    /amounts offered/
  )
  const saved = await setAutoTopUp(db, {
    organizationId: "org_1",
    enabled: true,
    amountCents: 10000,
  })
  assert.equal(saved.amountCents, 10000)
})
