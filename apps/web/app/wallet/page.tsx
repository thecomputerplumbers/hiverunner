import { roleAtLeast } from "@workspace/auth"
import { getBalance, listEntries } from "@workspace/db/ledger"
import { getBalanceHistory, getLiveSnapshot } from "@workspace/db/live"
import { autoChargedThisMonth, getAutoTopUp } from "@workspace/db/top-up"
import {
  AUTO_TOP_UP_BELOW_USD,
  AUTO_TOP_UP_BELOW_USD_OPTIONS,
  AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS,
  RUNNER_SIZES,
  TOP_UP_AMOUNTS_USD,
  TRIAL_GRANT_USD,
  privatePoolUsdPerMinute,
} from "@workspace/pricing"

import Link from "next/link"
import { AccountPage } from "@/components/account-page"
import { BalanceChart } from "@/components/console/charts"
import { formatUtc } from "@/components/console/format"
import { Notice, Panel } from "@/components/console/panel"
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
  const [balance, entries, saved, { funded }, live, history] =
    await Promise.all([
      getBalance(db, account),
      listEntries(db, account),
      getAutoTopUp(db, organizationId),
      searchParams,
      getLiveSnapshot(db, organizationId),
      getBalanceHistory(db, organizationId),
    ])
  const weekSpend =
    live.hours.slice(-24 * 7).reduce((sum, hour) => sum + hour.price, 0) / 7
  const runway = weekSpend > 0 ? Math.round(balance / weekSpend) : null
  const chargedThisMonth = saved
    ? await autoChargedThisMonth(db, organizationId)
    : 0
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
    <AccountPage title="billing" live={live}>
      {paid && (
        <Notice>
          Thanks. Your ${paid} payment was received and is added to the balance
          as soon as the card network confirms it, usually within a few seconds.
        </Notice>
      )}
      {saved?.lastError && (
        <Notice tone="alert">
          Automatic top-up was turned off because the last charge did not go
          through: {saved.lastError} Add funds below to pay with a card, which
          also saves it.
        </Notice>
      )}
      {balance <= 0 && (
        <Notice tone="alert">
          The balance has run out, so new jobs are waiting in GitHub&apos;s
          queue. Jobs already running will finish. Add funds to start the queue
          moving again.
        </Notice>
      )}
      <Panel
        title={`${installation.accountLogin} balance`}
        aside="30 days, UTC"
        bodyClassName="grid gap-6 lg:grid-cols-[minmax(0,18rem)_1fr]"
      >
        <div className="flex flex-col gap-2">
          <p className="font-mono text-4xl font-medium tracking-tight tabular-nums">
            {formatMoney(balance)}
          </p>
          <p className="text-sm text-muted-foreground">
            About {minutes.toLocaleString("en-US")} minutes on a {size.vcpus}
            -vCPU runner. You pay per job, by the minute, and nothing else.
          </p>
          <dl className="mt-2 grid grid-cols-2 gap-3 border-t border-border pt-3 font-mono text-xs [&_dd]:text-sm [&_dd]:tabular-nums [&_dt]:text-muted-foreground">
            <div>
              <dt>a day, this week</dt>
              <dd>{formatMoney(weekSpend, "fine")}</dd>
            </div>
            <div>
              <dt>lasts about</dt>
              <dd>
                {runway === null
                  ? "no spend yet"
                  : runway >= 365
                    ? "a year or more"
                    : `${Math.max(1, runway)} days`}
              </dd>
            </div>
          </dl>
        </div>
        <BalanceChart history={history} />
      </Panel>

      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title="add funds" bodyClassName="flex flex-col gap-3">
          {!canTakeCards() ? (
            <p className="text-muted-foreground">
              Card payments are not open yet. Every organization starts with $
              {TRIAL_GRANT_USD} of credit, and there is nothing to pay until it
              is used.
            </p>
          ) : mayPay ? (
            <>
              <AddFunds
                amounts={TOP_UP_AMOUNTS_USD}
                below={(saved?.belowCents ?? AUTO_TOP_UP_BELOW_USD * 100) / 100}
                offerAuto={!saved?.enabled}
              />
              <p className="text-sm text-muted-foreground">
                Paid by card through Stripe. A receipt is emailed to you, and
                the credit does not expire.
              </p>
            </>
          ) : (
            <p className="text-muted-foreground">
              An owner or admin of {installation.accountLogin} can add funds.
            </p>
          )}
        </Panel>

        <Panel title="automatic top-up" bodyClassName="flex flex-col gap-3">
          {canTakeCards() && card && saved ? (
            mayPay ? (
              <AutoTopUp
                enabled={saved.enabled}
                amount={saved.amountCents / 100}
                below={saved.belowCents / 100}
                cap={
                  saved.monthlyCapCents === null
                    ? null
                    : saved.monthlyCapCents / 100
                }
                amounts={TOP_UP_AMOUNTS_USD}
                belows={AUTO_TOP_UP_BELOW_USD_OPTIONS}
                caps={AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS}
                card={card}
                chargedThisMonth={chargedThisMonth / 100}
              />
            ) : (
              <p className="text-muted-foreground">
                {saved.enabled
                  ? `On: $${saved.amountCents / 100} is added when the balance falls below $${saved.belowCents / 100}.`
                  : "Off."}
              </p>
            )
          ) : (
            <p className="text-sm text-muted-foreground">
              Off. Once a card has paid here, the balance can refill itself when
              it runs low, up to a monthly cap you choose.
            </p>
          )}
        </Panel>
      </div>

      <Panel title="activity" aside={`last ${entries.length}`}>
        {!entries.length && <p>Nothing yet.</p>}
        <div className="overflow-x-auto">
          <table className="w-full min-w-[26rem] font-mono text-xs">
            <tbody>
              {entries.map((entry) => (
                <tr
                  key={entry.id}
                  className="border-t border-border first:border-t-0 [&>td]:py-2 [&>td]:pr-3 last:[&>td]:pr-0"
                >
                  <td className="w-28 whitespace-nowrap text-muted-foreground">
                    {formatUtc(entry.createdAt.getTime())}
                  </td>
                  <td>
                    {entry.jobId ? (
                      <Link
                        className="hover:text-primary"
                        href={`/jobs/${encodeURIComponent(entry.jobId)}`}
                      >
                        {kinds[entry.kind] ?? entry.kind}
                      </Link>
                    ) : (
                      (kinds[entry.kind] ?? entry.kind)
                    )}
                  </td>
                  <td
                    className={
                      entry.amount > 0
                        ? "text-right whitespace-nowrap tabular-nums"
                        : "text-right whitespace-nowrap text-muted-foreground tabular-nums"
                    }
                  >
                    {entry.amount > 0 ? "+" : "−"}
                    {formatMoney(
                      Math.abs(entry.amount),
                      entry.kind === "grant" || entry.kind === "purchase"
                        ? "cents"
                        : "fine"
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>
    </AccountPage>
  )
}
