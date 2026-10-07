import product from "@/config/product.json"
import { env } from "cloudflare:workers"
import { createStripeClient, getStripeConfig } from "@workspace/stripe"

/**
 * The plans this app sells, and the order they appear in.
 *
 * A plan key maps to a Stripe price through the environment — `"pro-monthly"`
 * reads `STRIPE_PRICE_PRO_MONTHLY`. The
 * browser only ever names a key; the price id is resolved here, on the
 * server, so a tampered request cannot buy Pro at the Starter price.
 */
export const PLANS = (product.plans as Array<{ id: string }>).map(
  (plan) => plan.id
)

export type Plan = (typeof PLANS)[number]

export function isPlan(value: unknown): value is Plan {
  return typeof value === "string" && PLANS.includes(value as Plan)
}

export function stripeConfig() {
  return getStripeConfig(env, { prices: PLANS })
}

export function stripe() {
  return createStripeClient(stripeConfig())
}

/** The price id for a plan in the active mode, or a clear error. */
export function priceFor(plan: Plan) {
  const config = stripeConfig()
  const price = config.prices[plan]
  if (!price) {
    throw new Error(`No Stripe price configured for ${plan}`)
  }
  return price
}

/**
 * Whether this deployment can take a card payment. Without both secrets the
 * wallet shows a balance and no way to add to it.
 */
export function canTakeCards() {
  return Boolean(
    product.features.funding &&
    env.STRIPE_SECRET_KEY &&
    env.STRIPE_WEBHOOK_SECRET
  )
}

/** Where Stripe should send people back to. */
export function appOrigin(request: Request) {
  return env.APP_URL || new URL(request.url).origin
}
