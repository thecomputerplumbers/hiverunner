import { and, desc, eq, sql } from "drizzle-orm"

import { usdToMicroTokens } from "@workspace/pricing"

import type { AuthDb } from "@workspace/auth/db"

import { ledgerEntry } from "@workspace/db/schema"

type Account = {
  accountType: "organization" | "user" | "treasury"
  accountId: string
}

const owned = (account: Account) =>
  and(
    eq(ledgerEntry.accountType, account.accountType),
    eq(ledgerEntry.accountId, account.accountId)
  )

/** Micro-tokens. Always computed; there is no stored balance to trust. */
export async function getBalance(db: AuthDb, account: Account) {
  const [row] = await db
    .select({
      balance: sql<number>`coalesce(sum(${ledgerEntry.amount}), 0)`.as(
        "balance"
      ),
    })
    .from(ledgerEntry)
    .where(owned(account))
  return Number(row?.balance ?? 0)
}

export async function listEntries(db: AuthDb, account: Account, limit = 50) {
  return db
    .select()
    .from(ledgerEntry)
    .where(owned(account))
    .orderBy(desc(ledgerEntry.createdAt), desc(ledgerEntry.id))
    .limit(Math.min(Math.max(limit, 1), 200))
}

/**
 * Credits an organization for a card payment. `paymentId` is the payment
 * processor's own id for it, so the same payment delivered twice, as webhooks
 * are, is credited once. Returns whether this call was the one that did.
 */
export async function recordPurchase(
  db: AuthDb,
  input: {
    organizationId: string
    paymentId: string
    /** What was paid, in cents. */
    cents: number
    now?: Date
  }
) {
  if (!Number.isInteger(input.cents) || input.cents <= 0)
    throw new Error("A purchase needs a positive whole number of cents")
  const written = await db
    .insert(ledgerEntry)
    .values({
      id: `purchase:${input.paymentId}`,
      accountType: "organization",
      accountId: input.organizationId,
      kind: "purchase",
      amount: usdToMicroTokens(input.cents / 100),
      createdAt: input.now ?? new Date(),
    })
    .onConflictDoNothing()
    .returning({ id: ledgerEntry.id })
  return written.length > 0
}

/** The parts of a completed Stripe Checkout that decide a top-up. */
export type PaidCheckout = {
  id: string
  mode: string
  payment_status: string
  currency: string | null
  amount_total: number | null
  metadata?: Record<string, string | undefined> | null
}

/**
 * Credits a completed card top-up, once. Anything that is not a paid top-up
 * is left alone: a subscription checkout, or a payment still unpaid.
 *
 * The amount credited is what Stripe says was paid. What the session's
 * metadata says the amount was is ignored: it was written before the
 * customer paid, and names an intention, not a payment.
 */
export async function creditTopUp(db: AuthDb, checkout: PaidCheckout) {
  if (
    checkout.mode !== "payment" ||
    checkout.metadata?.kind !== "top-up" ||
    checkout.payment_status !== "paid"
  )
    return "ignored" as const
  const organizationId = checkout.metadata.organization_id
  if (!organizationId || checkout.currency !== "usd" || !checkout.amount_total)
    throw new Error(
      "A paid top-up arrived without an organization or a USD amount"
    )
  const credited = await recordPurchase(db, {
    organizationId,
    paymentId: checkout.id,
    cents: checkout.amount_total,
  })
  return credited ? ("credited" as const) : ("already-credited" as const)
}
