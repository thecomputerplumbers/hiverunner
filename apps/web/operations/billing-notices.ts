import type { AuthDb } from "@workspace/auth/db"
import {
  claimBillingNotices,
  releaseBillingNotice,
  type BillingNotice,
} from "@workspace/db/notices"
import {
  BUDGET_WARNING_SHARE,
  microTokensToUsd,
  OVERDRAFT_USD,
} from "@workspace/pricing"

/**
 * Tells a hive's owners and admins, by email, what has changed about its
 * money: the balance running low or out, an automatic charge failing, and a
 * monthly budget nearly or fully spent. Run on a timer.
 *
 * Nothing here reads the environment. The database, the mailer and the
 * site's address are passed in, so the same code runs in the Worker and in
 * a test.
 */

type Send = (mail: {
  to: string
  subject: string
  message: string
  url: string
  label: string
  footer?: string
}) => Promise<unknown>

const money = (micro: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(
    microTokensToUsd(micro)
  )

/** What a notice says. Plain sentences; the mailer escapes them. */
export function billingNoticeMail(notice: BillingNotice) {
  const hive = notice.organization
  const budget = notice.budget === null ? "" : money(notice.budget)
  switch (notice.kind) {
    case "empty":
      return {
        subject: `${hive}: the balance has run out and jobs are waiting`,
        message: `The Hive Runner balance for ${hive} has run out, including the $${OVERDRAFT_USD} it may go below zero, so new jobs are waiting in GitHub's queue. Jobs already running will finish. Add funds to start the queue moving again.`,
        label: "Add funds",
      }
    case "low-balance":
      return {
        subject: `${hive}: the balance is down to ${money(notice.balance)}`,
        message: `The Hive Runner balance for ${hive} is down to ${money(notice.balance)}. Jobs keep starting until it is $${OVERDRAFT_USD} below zero; then new jobs wait in GitHub's queue until funds are added. Automatic top-up is off; you can add funds, or turn it on so the balance refills itself.`,
        label: "Add funds",
      }
    case "auto-top-up-failed":
      return {
        subject: `${hive}: automatic top-up was turned off`,
        message: `Automatic top-up for ${hive} was turned off because the last charge did not go through: ${notice.reason ?? "the card was declined."} The balance is ${money(notice.balance)}. Add funds by card to pay and save a working card.`,
        label: "Open billing",
      }
    case "budget-warning":
      return {
        subject: `${hive}: ${Math.round(BUDGET_WARNING_SHARE * 100)}% of this month's budget is spent`,
        message: `${hive} has spent ${money(notice.spent)} on jobs this month, of a budget of ${budget}. ${
          notice.holds
            ? "When the budget is spent, new jobs will wait until next month or until the budget is raised."
            : "Jobs will keep running past the budget; this is only to let you know."
        }`,
        label: "Open billing",
      }
    case "budget-reached":
      return {
        subject: `${hive}: this month's budget of ${budget} is spent`,
        message: `${hive} has spent ${money(notice.spent)} on jobs this month, which reaches its budget of ${budget}. ${
          notice.holds
            ? "New jobs are now waiting in GitHub's queue. Jobs already running will finish. Raise or remove the budget to start the queue moving again, or it starts again on the 1st."
            : "Jobs are still running; the budget is set to tell you, not to stop them."
        }`,
        label: "Open billing",
      }
  }
}

/** Sends what is newly true. Returns how many notices went out. */
export async function sendBillingNotices(
  db: AuthDb,
  send: Send,
  origin: string,
  now = new Date()
) {
  const notices = await claimBillingNotices(db, now)
  // One hive's trouble must not hold up the next one's notice.
  const troubles: unknown[] = []
  let sent = 0
  for (const notice of notices) {
    const mail = billingNoticeMail(notice)
    let reached = 0
    try {
      for (const to of notice.to) {
        await send({
          to,
          ...mail,
          url: `${origin}/wallet`,
          footer: `You are getting this because you are an owner or admin of ${notice.organization} on Hive Runner.`,
        })
        reached++
      }
      sent++
    } catch (error) {
      // Handed back for the next sweep only if it reached nobody. Once one
      // person has it, trying again would send it to them every sweep.
      if (!reached) await releaseBillingNotice(db, notice.id)
      troubles.push(error)
    }
  }
  if (troubles.length)
    throw new AggregateError(troubles, "Some billing notices could not be sent")
  return sent
}
