import { and, eq, gte, inArray, sql } from "drizzle-orm"

import { roleAtLeast } from "@workspace/auth/organization"
import type { AuthDb } from "@workspace/auth/db"
import { member, organization, user } from "@workspace/auth/schema"
import {
  BUDGET_WARNING_SHARE,
  LOW_BALANCE_USD,
  MICRO,
  outOfCredit,
  usdToMicroTokens,
} from "@workspace/pricing"

import { monthName, monthStart } from "@workspace/db/budget"
import {
  autoTopUp,
  billingNotice,
  ledgerEntry,
  spendBudget,
} from "@workspace/db/schema"

/**
 * The things about money a hive's owners and admins are told by email, each
 * once for as long as it stays true.
 *
 * Nothing here sends mail. `claimBillingNotices` works out what is newly
 * true, records it so a later sweep does not say it again, and returns what
 * to send. A send that fails is handed back with `releaseBillingNotice`.
 */

export type BillingNoticeKind =
  | "low-balance"
  | "empty"
  | "auto-top-up-failed"
  | "budget-warning"
  | "budget-reached"

export type BillingNotice = {
  id: string
  kind: BillingNoticeKind
  organizationId: string
  /** What the organization is called on GitHub. */
  organization: string
  /** The owners and admins to tell. */
  to: string[]
  /** Micro-tokens. */
  balance: number
  /** Micro-tokens spent on jobs this calendar month. */
  spent: number
  /** The monthly budget in micro-tokens, when there is one. */
  budget: number | null
  /** Whether new jobs are held at the budget. */
  holds: boolean
  /** Why the last automatic charge failed, when one did. */
  reason: string | null
}

/** Conditions that pass and can come back; a month's budget cannot. */
const PASSING: BillingNoticeKind[] = [
  "low-balance",
  "empty",
  "auto-top-up-failed",
]

export async function claimBillingNotices(db: AuthDb, now = new Date()) {
  const since = monthStart(now)
  const [balances, spends, budgets, cards, sent] = await Promise.all([
    db
      .select({
        organizationId: ledgerEntry.accountId,
        balance: sql<number>`coalesce(sum(${ledgerEntry.amount}), 0)`.as(
          "balance"
        ),
      })
      .from(ledgerEntry)
      .where(eq(ledgerEntry.accountType, "organization"))
      .groupBy(ledgerEntry.accountId),
    db
      .select({
        organizationId: ledgerEntry.accountId,
        net: sql<number>`coalesce(sum(${ledgerEntry.amount}), 0)`.as("net"),
      })
      .from(ledgerEntry)
      .where(
        and(
          eq(ledgerEntry.accountType, "organization"),
          inArray(ledgerEntry.kind, ["charge", "earning"]),
          gte(ledgerEntry.createdAt, since)
        )
      )
      .groupBy(ledgerEntry.accountId),
    db.select().from(spendBudget),
    db
      .select({
        organizationId: autoTopUp.organizationId,
        enabled: autoTopUp.enabled,
        lastError: autoTopUp.lastError,
      })
      .from(autoTopUp),
    db.select().from(billingNotice),
  ])
  const spent = new Map(
    spends.map((row) => [row.organizationId, Math.max(0, -Number(row.net))])
  )
  const budget = new Map(budgets.map((row) => [row.organizationId, row]))
  const card = new Map(cards.map((row) => [row.organizationId, row]))
  const month = monthName(now)

  // What is true of each organization now, by the id its notice is kept
  // under.
  const facts = new Map<string, Omit<BillingNotice, "organization" | "to">>()
  for (const row of balances) {
    const organizationId = row.organizationId
    const balance = Number(row.balance)
    const limit = budget.get(organizationId)
    const state = {
      organizationId,
      balance,
      spent: spent.get(organizationId) ?? 0,
      budget: limit ? (limit.monthlyCents * MICRO) / 100 : null,
      holds: limit?.atLimit === "pause",
      reason: card.get(organizationId)?.lastError ?? null,
    }
    const note = (kind: BillingNoticeKind, id: string) =>
      facts.set(id, { ...state, id, kind })
    if (outOfCredit(balance)) note("empty", `empty:${organizationId}`)
    // A hive that tops itself up is about to be refilled, not about to stop.
    else if (
      balance < usdToMicroTokens(LOW_BALANCE_USD) &&
      !card.get(organizationId)?.enabled
    )
      note("low-balance", `low-balance:${organizationId}`)
    if (state.reason)
      note("auto-top-up-failed", `auto-top-up-failed:${organizationId}`)
    if (state.budget !== null && state.spent >= state.budget)
      note("budget-reached", `budget-reached:${organizationId}:${month}`)
    else if (
      state.budget !== null &&
      state.spent >= state.budget * BUDGET_WARNING_SHARE
    )
      note("budget-warning", `budget-warning:${organizationId}:${month}`)
  }

  // A condition that has passed is forgotten, so it is told again if it
  // comes back.
  const passed = sent
    .filter(
      (row) =>
        PASSING.includes(row.kind as BillingNoticeKind) && !facts.has(row.id)
    )
    .map((row) => row.id)
  if (passed.length)
    await db.delete(billingNotice).where(inArray(billingNotice.id, passed))

  const already = new Set(sent.map((row) => row.id))
  const fresh = [...facts.values()].filter((fact) => !already.has(fact.id))
  if (!fresh.length) return []

  const claimed: typeof fresh = []
  for (const fact of fresh) {
    // Told once, even if two sweeps run at the same time.
    const written = await db
      .insert(billingNotice)
      .values({
        id: fact.id,
        organizationId: fact.organizationId,
        kind: fact.kind,
        createdAt: now,
      })
      .onConflictDoNothing()
      .returning({ id: billingNotice.id })
    if (written.length) claimed.push(fact)
  }
  if (!claimed.length) return []

  const organizations = [...new Set(claimed.map((fact) => fact.organizationId))]
  const [names, people] = await Promise.all([
    db
      .select({ id: organization.id, name: organization.name })
      .from(organization)
      .where(inArray(organization.id, organizations)),
    db
      .select({
        organizationId: member.organizationId,
        role: member.role,
        email: user.email,
      })
      .from(member)
      .innerJoin(user, eq(user.id, member.userId))
      .where(inArray(member.organizationId, organizations)),
  ])
  const name = new Map(names.map((row) => [row.id, row.name]))
  return claimed.map((fact): BillingNotice => ({
    ...fact,
    organization: name.get(fact.organizationId) ?? "your hive",
    to: people
      .filter(
        (person) =>
          person.organizationId === fact.organizationId &&
          roleAtLeast(person.role, "admin")
      )
      .map((person) => person.email),
  }))
}

/** Hands back a notice that could not be sent, so the next sweep tries. */
export async function releaseBillingNotice(db: AuthDb, id: string) {
  await db.delete(billingNotice).where(eq(billingNotice.id, id))
}
