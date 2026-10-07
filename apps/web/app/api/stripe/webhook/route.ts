import product from "@/config/product.json"
import { logError } from "@/lib/diagnostics"
import { env } from "cloudflare:workers"
import {
  handleStripeWebhook,
  type StripeCheckoutSession,
  type StripeSubscription,
} from "@workspace/stripe"
import {
  recordStripeEvent,
  stripeEventSeen,
  reconcileSubscription,
} from "@workspace/stripe/store"

import {
  handleIntentFailed,
  handleIntentSucceeded,
  handlePaidCheckout,
  type PaymentIntent,
} from "@/operations/top-up"

import { getDb } from "@/db"
import { stripe } from "@/lib/stripe"

/**
 * The Stripe webhook.
 *
 * This is where access is actually granted. The redirect back from Checkout
 * is a courtesy for the person's browser and proves nothing — it can be
 * visited by hand, and it arrives before Stripe has necessarily settled the
 * payment. Every entitlement below is written in response to a signed event.
 *
 * Point Stripe at `https://<host>/api/stripe/webhook` and subscribe to:
 *   checkout.session.completed
 *   customer.subscription.created / .updated / .deleted
 *
 * Locally: `stripe listen --forward-to localhost:3000/api/stripe/webhook`,
 * then put the `whsec_…` it prints into STRIPE_WEBHOOK_SECRET.
 */
export async function POST(request: Request) {
  const db = getDb()

  async function syncSubscriptionEvent(event: { data: { object: unknown } }) {
    // A Stripe account can be shared with another product, and its webhook
    // delivers that product's subscriptions here too. They are only ours
    // when this app sells subscriptions at all.
    if (!product.features.billing) return
    // A deleted subscription arrives with `status: "canceled"`, so the same
    // write covers cancellation — there is nothing to delete locally.
    const subscription = event.data.object as StripeSubscription
    await reconcileSubscription(db, stripe(), subscription.id)
  }

  return handleStripeWebhook(request, env, {
    // Reconciliation is idempotent. A failure before recording completion can
    // safely run again; concurrent deliveries are fenced by the row revision.
    seen: (event) => stripeEventSeen(db, event.id),
    record: (event) => recordStripeEvent(db, event),

    handlers: {
      "checkout.session.completed": async (event) => {
        const checkout = event.data.object as unknown as StripeCheckoutSession

        if (
          product.features.billing &&
          checkout.mode === "subscription" &&
          checkout.subscription
        ) {
          await reconcileSubscription(db, stripe(), checkout.subscription, {
            organizationId:
              checkout.metadata?.organization_id ??
              checkout.client_reference_id ??
              undefined,
          })
        }

        // A card top-up is credited here and nowhere else, once however
        // many times Stripe delivers the event.
        await handlePaidCheckout(db, stripe(), checkout)
      },

      // Automatic top-ups charge a saved card with no Checkout involved.
      "payment_intent.succeeded": async (event) => {
        await handleIntentSucceeded(
          db,
          event.data.object as unknown as PaymentIntent
        )
      },
      "payment_intent.payment_failed": async (event) => {
        await handleIntentFailed(
          db,
          event.data.object as unknown as PaymentIntent
        )
      },

      "customer.subscription.created": syncSubscriptionEvent,
      "customer.subscription.updated": syncSubscriptionEvent,
      "customer.subscription.deleted": syncSubscriptionEvent,
    },

    onError: (error) => {
      logError(error, "stripe.webhook")
    },
  })
}
