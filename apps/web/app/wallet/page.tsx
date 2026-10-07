import { roleAtLeast } from "@workspace/auth"
import { getBalance, listEntries } from "@workspace/db/ledger"
import { getAutoTopUp } from "@workspace/db/top-up"
import {
  AUTO_TOP_UP_BELOW_USD,
  RUNNER_SIZES,
  TOP_UP_AMOUNTS_USD,
  TRIAL_GRANT_USD,
  privatePoolUsdPerMinute,
} from "@workspace/pricing"

import { AccountPage } from "@/components/account-page"
import { getDb } from "@/db"
import { formatMoney } from "@/lib/format"
import { requirePool } from "@/lib/session"
import { canTakeCards } from "@/lib/stripe"
import { AddFunds } from "./add-funds"
import { AutoTopUp } from "./auto-top-up"

export const metadata = { title: "Billing" }

/** What each kind of entry is, to the organization reading its own account. */
const kinds: Record<string, string> = {
  grant: "Trial credit",
  purchase: "Added by card",
  charge: "Job",
  earning: "Earned back by your machines",
  tax: "HiveWorker fee",
}

export default async function WalletPage({
  searchParams,
}: {
  searchParams: Promise<{ funded?: string }>
}) {
  const { organizationId, installation, membership } = await requirePool()
  const account = {
    accountType: "organization" as const,
    accountId: organizationId,
  }
  const db = getDb()
  const [balance, entries, saved, { funded }] = await Promise.all([
    getBalance(db, account),
    listEntries(db, account),
    getAutoTopUp(db, organizationId),
    searchParams,
  ])
  const card = saved?.paymentMethodId
    ? `the ${saved.cardBrand ? saved.cardBrand[0]!.toUpperCase() + saved.cardBrand.slice(1) : "card"} ending ${saved.cardLast4 ?? "····"}`
    : null
  const size = RUNNER_SIZES[0]
  const minutes = Math.max(
    0,
    Math.floor(balance / 1_000_000 / privatePoolUsdPerMinute(size.vcpus).net)
  )
  const mayPay = roleAtLeast(membership.role, "admin")
  // Only an amount this app offers is echoed back, whatever the URL says.
  const paid = TOP_UP_AMOUNTS_USD.find((amount) => String(amount) === funded)

  return (
    <AccountPage title="Billing">
      {paid && (
        <p role="status" className="rounded-lg border p-4">
          Thanks. Your ${paid} payment was received and is added to the balance
          as soon as the card network confirms it, usually within a few seconds.
        </p>
      )}
      {saved?.lastError && (
        <p role="alert" className="rounded-lg border border-destructive p-4">
          Automatic top-up was turned off because the last charge did not go
          through: {saved.lastError} Add funds below to pay with a card, which
          also saves it.
        </p>
      )}
      {balance <= 0 && (
        <p role="alert" className="rounded-lg border border-destructive p-4">
          The balance has run out, so new jobs are waiting in GitHub&apos;s
          queue. Jobs already running will finish. Add funds to start the queue
          moving again.
        </p>
      )}
      <section>
        <p className="text-sm text-muted-foreground">
          {installation.accountLogin} balance
        </p>
        <p className="text-4xl font-semibold tabular-nums">
          {formatMoney(balance)}
        </p>
        <p className="text-sm text-muted-foreground">
          About {minutes.toLocaleString("en-US")} minutes on a {size.vcpus}-vCPU
          runner. You pay per job, by the minute, and nothing else.
        </p>
      </section>

      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Add funds</h2>
        {!canTakeCards() ? (
          <p className="text-muted-foreground">
            Card payments are not open yet. Every organization starts with $
            {TRIAL_GRANT_USD} of credit, and there is nothing to pay until it is
            used.
          </p>
        ) : mayPay ? (
          <>
            <AddFunds
              amounts={TOP_UP_AMOUNTS_USD}
              below={AUTO_TOP_UP_BELOW_USD}
              offerAuto={!saved?.enabled}
            />
            <p className="text-sm text-muted-foreground">
              Paid by card through Stripe. A receipt is emailed to you, and the
              credit does not expire.
            </p>
          </>
        ) : (
          <p className="text-muted-foreground">
            An owner or admin of {installation.accountLogin} can add funds.
          </p>
        )}
      </section>

      {canTakeCards() && card && saved && (
        <section className="space-y-3">
          <h2 className="text-lg font-semibold">Automatic top-up</h2>
          {mayPay ? (
            <AutoTopUp
              enabled={saved.enabled}
              amount={saved.amountCents / 100}
              amounts={TOP_UP_AMOUNTS_USD}
              below={AUTO_TOP_UP_BELOW_USD}
              card={card}
            />
          ) : (
            <p className="text-muted-foreground">
              {saved.enabled
                ? `On: $${saved.amountCents / 100} is added when the balance falls below $${AUTO_TOP_UP_BELOW_USD}.`
                : "Off."}
            </p>
          )}
        </section>
      )}

      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Activity</h2>
        {!entries.length && <p>Nothing yet.</p>}
        <ul className="divide-y rounded-lg border">
          {entries.map((entry) => (
            <li
              key={entry.id}
              className="flex items-center justify-between gap-4 p-4"
            >
              <div>
                <p className="font-medium">{kinds[entry.kind] ?? entry.kind}</p>
                <p className="text-sm text-muted-foreground">
                  {entry.createdAt.toLocaleDateString("en-US", {
                    dateStyle: "medium",
                  })}
                </p>
              </div>
              <p className="tabular-nums">
                {entry.amount > 0 ? "+" : "−"}
                {formatMoney(
                  Math.abs(entry.amount),
                  entry.kind === "grant" || entry.kind === "purchase"
                    ? "cents"
                    : "fine"
                )}
              </p>
            </li>
          ))}
        </ul>
      </section>
    </AccountPage>
  )
}
