import { and, eq, gte, isNull, lt, or, sql } from "drizzle-orm"

import type { AuthDb } from "@workspace/auth/db"
import {
  AUTO_TOP_UP_BELOW_USD_OPTIONS,
  AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS,
  MICRO,
  TOP_UP_AMOUNTS_USD,
} from "@workspace/pricing"

import { getBalance, recordPurchase } from "@workspace/db/ledger"
import { autoTopUp, ledgerEntry } from "@workspace/db/schema"

/**
 * How long a charge may be in flight before it is given up on and another
 * may start. Stripe confirms a card charge in seconds; an hour means the
 * confirmation was lost, not that it is still coming.
 */
const PENDING_FOR_MS = 60 * 60 * 1000

export type Card = {
  paymentMethodId: string
  brand: string | null
  last4: string | null
}

export function getAutoTopUp(db: AuthDb, organizationId: string) {
  return db
    .select()
    .from(autoTopUp)
    .where(eq(autoTopUp.organizationId, organizationId))
    .limit(1)
    .then(([row]) => row ?? null)
}

const among = (options: readonly number[], cents: number) =>
  options.some((dollars) => dollars * 100 === cents)
const offered = (cents: number) => among(TOP_UP_AMOUNTS_USD, cents)

/** Ledger amounts are millionths of a dollar; cards are charged in cents. */
const centsToLedger = (cents: number) => (cents * MICRO) / 100

/**
 * What automatic top-up has charged an organization so far this calendar
 * month, in cents. Automatic charges are the purchases keyed by a Stripe
 * PaymentIntent; ones made by hand through Checkout are keyed by its session
 * and do not count against the ceiling.
 */
export async function autoChargedThisMonth(
  db: AuthDb,
  organizationId: string,
  now = new Date()
) {
  const monthStart = new Date(
    Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1)
  )
  const [row] = await db
    .select({
      total: sql<number>`coalesce(sum(${ledgerEntry.amount}), 0)`.as("total"),
    })
    .from(ledgerEntry)
    .where(
      and(
        eq(ledgerEntry.accountType, "organization"),
        eq(ledgerEntry.accountId, organizationId),
        eq(ledgerEntry.kind, "purchase"),
        // An exact prefix, not LIKE: `_` is a wildcard there.
        sql`substr(${ledgerEntry.id}, 1, 12) = 'purchase:pi_'`,
        gte(ledgerEntry.createdAt, monthStart)
      )
    )
  return Math.round((Number(row?.total ?? 0) * 100) / MICRO)
}

/**
 * Remembers the card an organization just paid with. `enable` is whether the
 * person paying asked for automatic top-up at that moment; without it the
 * card is kept and nothing is ever charged to it unasked.
 */
export async function rememberCard(
  db: AuthDb,
  input: {
    organizationId: string
    card: Card
    amountCents: number
    enable: boolean
    now?: Date
  }
) {
  const now = input.now ?? new Date()
  const amountCents = offered(input.amountCents)
    ? input.amountCents
    : TOP_UP_AMOUNTS_USD[1] * 100
  const card = {
    paymentMethodId: input.card.paymentMethodId,
    cardBrand: input.card.brand,
    cardLast4: input.card.last4,
  }
  await db
    .insert(autoTopUp)
    .values({
      organizationId: input.organizationId,
      enabled: input.enable,
      amountCents,
      ...card,
      updatedAt: now,
    })
    .onConflictDoUpdate({
      target: autoTopUp.organizationId,
      set: {
        ...card,
        // A new card is a fresh start: whatever went wrong was the old one.
        lastError: null,
        updatedAt: now,
        // Paying without asking for it never turns it off, or on.
        ...(input.enable ? { enabled: true, amountCents } : {}),
      },
    })
}

/**
 * Turns automatic top-up on or off, or changes how it behaves: what it adds,
 * the balance it acts below, and the most it may charge in a month. It
 * cannot be turned on without a saved card.
 */
export async function setAutoTopUp(
  db: AuthDb,
  input: {
    organizationId: string
    enabled: boolean
    amountCents?: number
    belowCents?: number
    /** Null removes the ceiling. Left out, it stays as it was. */
    monthlyCapCents?: number | null
  }
) {
  const current = await getAutoTopUp(db, input.organizationId)
  if (input.amountCents !== undefined && !offered(input.amountCents))
    throw new Error("Choose one of the amounts offered")
  if (
    input.belowCents !== undefined &&
    !among(AUTO_TOP_UP_BELOW_USD_OPTIONS, input.belowCents)
  )
    throw new Error("Choose one of the balances offered")
  if (
    input.monthlyCapCents !== undefined &&
    input.monthlyCapCents !== null &&
    !among(AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS, input.monthlyCapCents)
  )
    throw new Error("Choose one of the monthly limits offered")
  const amount = input.amountCents ?? current?.amountCents ?? 0
  const cap =
    input.monthlyCapCents === undefined
      ? (current?.monthlyCapCents ?? null)
      : input.monthlyCapCents
  // A ceiling below one top-up would mean it never charges at all, while
  // saying it is on.
  if (cap !== null && cap < amount)
    throw new Error("The monthly limit must cover at least one top-up")
  if (!current?.paymentMethodId) {
    if (input.enabled)
      throw new Error("Add funds by card once before turning this on")
    return null
  }
  const [row] = await db
    .update(autoTopUp)
    .set({
      enabled: input.enabled,
      ...(input.amountCents === undefined
        ? {}
        : { amountCents: input.amountCents }),
      ...(input.belowCents === undefined
        ? {}
        : { belowCents: input.belowCents }),
      ...(input.monthlyCapCents === undefined
        ? {}
        : { monthlyCapCents: input.monthlyCapCents }),
      // Turning it back on is the owner saying the problem is dealt with.
      ...(input.enabled ? { lastError: null } : {}),
      updatedAt: new Date(),
    })
    .where(eq(autoTopUp.organizationId, input.organizationId))
    .returning()
  return row ?? null
}

/**
 * The organizations whose saved card should be charged now: automatic top-up
 * is on, the balance is under the line the organization chose, and the
 * charge would not take it past its monthly ceiling.
 *
 * Each comes back with the time its charge was claimed, which names that one
 * charge to Stripe. An organization with no charge in flight is claimed now,
 * by a conditional update, so two sweeps at once agree on one time. One whose
 * charge is already in flight comes back with the time it was first claimed:
 * asking Stripe again under the same name cannot charge twice, and it is how
 * a request that never arrived gets sent.
 */
export async function claimDueTopUps(db: AuthDb, now = new Date()) {
  const stale = new Date(now.getTime() - PENDING_FOR_MS)
  const free = or(
    isNull(autoTopUp.pendingSince),
    lt(autoTopUp.pendingSince, stale)
  )
  const candidates = await db
    .select()
    .from(autoTopUp)
    .where(eq(autoTopUp.enabled, true))
  const due = []
  for (const candidate of candidates) {
    if (!candidate.paymentMethodId) continue
    const balance = await getBalance(db, {
      accountType: "organization",
      accountId: candidate.organizationId,
    })
    if (balance >= centsToLedger(candidate.belowCents)) continue
    // The owner's ceiling. Reaching it is not a failure and turns nothing
    // off: charging resumes by itself when the month turns.
    if (
      candidate.monthlyCapCents !== null &&
      (await autoChargedThisMonth(db, candidate.organizationId, now)) +
        candidate.amountCents >
        candidate.monthlyCapCents
    )
      continue
    await db
      .update(autoTopUp)
      .set({ pendingSince: now, updatedAt: now })
      .where(
        and(
          eq(autoTopUp.organizationId, candidate.organizationId),
          eq(autoTopUp.enabled, true),
          free
        )
      )
    // Read back rather than trusted from the update: another sweep may have
    // made the claim, and its time is the one to use.
    const current = await getAutoTopUp(db, candidate.organizationId)
    if (current?.enabled && current.paymentMethodId && current.pendingSince)
      due.push({
        organizationId: current.organizationId,
        paymentMethodId: current.paymentMethodId,
        amountCents: current.amountCents,
        claimedAt: current.pendingSince,
      })
  }
  return due
}

/**
 * Credits an automatic charge that Stripe reports as paid, once, and frees
 * the organization for the next one.
 */
export async function creditAutoTopUp(
  db: AuthDb,
  input: { organizationId: string; paymentId: string; cents: number }
) {
  const credited = await recordPurchase(db, input)
  await db
    .update(autoTopUp)
    .set({ pendingSince: null, lastError: null, updatedAt: new Date() })
    .where(eq(autoTopUp.organizationId, input.organizationId))
  return credited
}

/**
 * Records that an automatic charge failed and turns automatic top-up off.
 * Retrying a card that was just declined annoys the cardholder and the bank,
 * and can get the account flagged; a person has to look at it.
 */
export async function failAutoTopUp(
  db: AuthDb,
  input: { organizationId: string; reason: string }
) {
  await db
    .update(autoTopUp)
    .set({
      enabled: false,
      pendingSince: null,
      lastError: input.reason.slice(0, 300),
      updatedAt: new Date(),
    })
    .where(eq(autoTopUp.organizationId, input.organizationId))
}
