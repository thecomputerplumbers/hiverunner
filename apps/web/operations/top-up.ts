import type { AuthDb } from "@workspace/auth/db"
import { creditTopUp, type PaidCheckout } from "@workspace/db/ledger"
import {
  claimDueTopUps,
  creditAutoTopUp,
  failAutoTopUp,
  rememberCard,
} from "@workspace/db/top-up"
import { StripeError, type StripeClient } from "@workspace/stripe"
import { getCustomerByOrganization } from "@workspace/stripe/store"

/**
 * Card payments into an organization's balance: the one made by hand through
 * Checkout, and the automatic one against a saved card.
 *
 * Nothing here reads the environment. The database and the Stripe client are
 * passed in, so the same code runs in the Worker and in a test.
 */

/** The parts of a Stripe PaymentIntent this app reads. */
export type PaymentIntent = {
  id: string
  status: string
  amount_received?: number | null
  currency?: string | null
  metadata?: Record<string, string | undefined> | null
  payment_method?:
    | string
    | { id: string; card?: { brand?: string; last4?: string } | null }
    | null
  last_payment_error?: { message?: string | null } | null
}

type Stripe = Pick<StripeClient, "get" | "post" | "config">

const AUTO = "auto-top-up"

/**
 * A completed Checkout: credit it, and if the person paying asked for
 * automatic top-up, remember the card they used.
 */
export async function handlePaidCheckout(
  db: AuthDb,
  stripe: Stripe,
  checkout: PaidCheckout & { payment_intent?: string | null }
) {
  const outcome = await creditTopUp(db, checkout)
  if (outcome === "ignored") return outcome
  // Only when asked. A card is saved at Stripe only when the payment said
  // so, and that is decided by the box the person ticked before paying.
  if (checkout.metadata?.auto !== "1" || !checkout.payment_intent)
    return outcome
  const intent = await stripe.get<PaymentIntent>(
    `payment_intents/${encodeURIComponent(checkout.payment_intent)}`,
    { expand: ["payment_method"] }
  )
  const method = intent.payment_method
  if (!method || typeof method === "string" || !checkout.amount_total)
    return outcome
  await rememberCard(db, {
    organizationId: checkout.metadata.organization_id!,
    card: {
      paymentMethodId: method.id,
      brand: method.card?.brand ?? null,
      last4: method.card?.last4 ?? null,
    },
    amountCents: checkout.amount_total,
    enable: true,
  })
  return outcome
}

/**
 * Charges the saved card of every organization whose balance has fallen
 * under the line. Run on a timer.
 *
 * The balance is not credited here. It is credited when Stripe's signed
 * event says the money moved, the same as a payment made by hand.
 */
export async function chargeDueTopUps(
  db: AuthDb,
  stripe: Stripe,
  now = new Date()
) {
  const due = await claimDueTopUps(db, now)
  // One organization's trouble must not hold up the next one's charge.
  const troubles: unknown[] = []
  for (const claim of due) {
    const { organizationId } = claim
    try {
      const customer = await getCustomerByOrganization(
        db,
        organizationId,
        stripe.config.livemode
      )
      if (!customer) {
        await failAutoTopUp(db, {
          organizationId,
          reason: "There is no card on file. Add funds by card to save one.",
        })
        continue
      }
      const intent = await stripe.post<PaymentIntent>(
        "payment_intents",
        {
          amount: claim.amountCents,
          currency: "usd",
          customer: customer.id,
          payment_method: claim.paymentMethodId,
          off_session: true,
          confirm: true,
          description: "HiveWorker credit (automatic top-up)",
          metadata: { kind: AUTO, organization_id: organizationId },
        },
        // The claim's time names this one charge. Sending it again, from a
        // retry or from the next sweep, cannot become a second charge.
        {
          idempotencyKey: `${AUTO}:${organizationId}:${claim.claimedAt.getTime()}`,
        }
      )
      if (intent.status !== "succeeded" && intent.status !== "processing")
        await failAutoTopUp(db, {
          organizationId,
          reason:
            "Your bank wants this payment approved by you. Add funds by card to approve it.",
        })
    } catch (error) {
      // A refusal from Stripe is an answer: the card was declined, expired
      // or removed. Anything else is a failure to ask. The claim is left in
      // place, and the next sweep asks again under the same name.
      if (
        error instanceof StripeError &&
        error.status >= 400 &&
        error.status < 500 &&
        error.status !== 409 &&
        error.status !== 429
      )
        await failAutoTopUp(db, { organizationId, reason: error.message })
      else troubles.push(error)
    }
  }
  if (troubles.length)
    throw new AggregateError(
      troubles,
      "Some automatic top-ups could not be sent"
    )
  return due.length
}

/** Stripe reports an automatic charge paid: credit it. */
export async function handleIntentSucceeded(db: AuthDb, intent: PaymentIntent) {
  if (intent.metadata?.kind !== AUTO) return "ignored" as const
  const organizationId = intent.metadata.organization_id
  if (!organizationId || intent.currency !== "usd" || !intent.amount_received)
    throw new Error(
      "A paid automatic top-up arrived without an organization or a USD amount"
    )
  const credited = await creditAutoTopUp(db, {
    organizationId,
    paymentId: intent.id,
    cents: intent.amount_received,
  })
  return credited ? ("credited" as const) : ("already-credited" as const)
}

/** Stripe reports an automatic charge failed: stop, and say why. */
export async function handleIntentFailed(db: AuthDb, intent: PaymentIntent) {
  if (intent.metadata?.kind !== AUTO) return "ignored" as const
  const organizationId = intent.metadata.organization_id
  if (!organizationId) return "ignored" as const
  await failAutoTopUp(db, {
    organizationId,
    reason: intent.last_payment_error?.message ?? "Your card was declined.",
  })
  return "stopped" as const
}
