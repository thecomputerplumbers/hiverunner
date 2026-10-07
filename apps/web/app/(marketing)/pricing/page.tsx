import Link from "next/link"
import {
  competitorUsdPerMinute,
  COMPETITOR_RATES,
  JOB_TAX_RATE,
  RUNNER_SIZES,
  TRIAL_GRANT_USD,
  TRIAL_GRANT_WITH_CARD_USD,
  FLOOR_USD_PER_COMPUTE_UNIT,
  privatePoolUsdPerMinute,
} from "@workspace/pricing"
import { Code } from "@/components/marketing/code"
import { CostChart } from "@/components/marketing/cost-chart"
import {
  formatCount,
  formatIsoDate,
  formatRate,
  formatUsd,
} from "@/components/marketing/format"
import { Band, PageIntro, textLink } from "@/components/marketing/page-parts"
import { PricingCalculator } from "@/components/marketing/pricing-calculator"
import { Container } from "@workspace/ui-shell/components/page-header"

export const metadata = {
  title: "Pricing",
  description:
    "What a month of GitHub Actions minutes costs on GitHub-hosted runners, Blacksmith and a HiveWorker private pool.",
}

const percent = new Intl.NumberFormat("en-US", { style: "percent" })
const base = privatePoolUsdPerMinute(RUNNER_SIZES[0].vcpus)
const trialMinutes = Math.floor(TRIAL_GRANT_USD / base.net)

export default function PricingPage() {
  return (
    <>
      <PageIntro title="You pay per job, by the minute, and nothing else.">
        No subscription, no seats and no platform fee. Put in your minutes to
        see a month on a private pool beside the same month on hosted runners.
      </PageIntro>
      <Container className="pb-14 sm:pb-20">
        <PricingCalculator />
      </Container>

      <Band title="A month as usage grows">
        <CostChart vcpus={RUNNER_SIZES[0].vcpus} />
      </Band>

      <Band title="Price per minute">
        <div className="overflow-x-auto">
          <table className="w-full min-w-xl text-left text-sm">
            <caption className="sr-only">
              Per-minute prices for Linux arm64 runners by size
            </caption>
            <thead className="font-mono text-xs text-muted-foreground">
              <tr>
                <th className="py-2 pr-4 font-medium">Size</th>
                <th className="py-2 pr-4 font-medium">HiveWorker</th>
                <th className="py-2 pr-4 font-medium">
                  {COMPETITOR_RATES.github.name}
                </th>
                <th className="py-2 font-medium">
                  {COMPETITOR_RATES.blacksmith.name}
                </th>
              </tr>
            </thead>
            <tbody className="font-mono tabular-nums">
              {RUNNER_SIZES.map((size) => {
                const hive = privatePoolUsdPerMinute(size.vcpus)
                return (
                  <tr key={size.label} className="border-t border-border">
                    <th className="py-3 pr-4 font-medium">
                      {size.vcpus} vCPU <Code>{size.label}</Code>
                    </th>
                    <td className="py-3 pr-4 font-medium text-primary">
                      {formatRate(hive.net)}
                    </td>
                    <td className="py-3 pr-4">
                      {formatRate(competitorUsdPerMinute("github", size.vcpus))}
                    </td>
                    <td className="py-3">
                      {formatRate(
                        competitorUsdPerMinute("blacksmith", size.vcpus)
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
        <p className="text-sm leading-6 text-muted-foreground">
          HiveWorker runs Linux arm64 jobs, so the comparison uses each
          provider's Linux arm64 list price, checked on{" "}
          {formatIsoDate(COMPETITOR_RATES.asOf)} against{" "}
          <a href={COMPETITOR_RATES.github.source} className={textLink}>
            GitHub's runner pricing
          </a>{" "}
          and{" "}
          <a href={COMPETITOR_RATES.blacksmith.source} className={textLink}>
            Blacksmith's pricing page
          </a>
          . Free minutes and plan allowances are left out. HiveWorker prices are
          at reference speed in a private pool whose machines are your own.
        </p>
      </Band>

      <Band title="How the price works">
        <p className="leading-7">
          Your organization pays the full price of each job. HiveWorker keeps{" "}
          {percent.format(JOB_TAX_RATE)} of it, which is its only revenue, and
          the rest goes to the machine that ran the job.
        </p>
        <p className="leading-7 text-muted-foreground">
          In a private pool that machine belongs to your organization. When the
          pool is set to pay the organization's wallet, the{" "}
          {percent.format(1 - JOB_TAX_RATE)} comes straight back and can pay for
          the next job. The price is what is left: {formatRate(base.net)} for a{" "}
          {RUNNER_SIZES[0].vcpus} vCPU minute.
        </p>
      </Band>

      <Band title="The compute unit">
        <p className="leading-7">
          One compute unit is one vCPU for one minute at reference speed. The
          reference is one core of a standard GitHub-hosted runner. A compute
          unit is charged at {formatRate(FLOOR_USD_PER_COMPUTE_UNIT)} in a
          private pool, before the machine's share comes back.
        </p>
        <p className="border border-border px-4 py-3 font-mono text-sm">
          Job charge = minutes × vCPUs × speed score × rate per compute unit
        </p>
        <p className="leading-7 text-muted-foreground">
          Minutes are counted from GitHub's own job start and finish, rounded up
          to the whole minute. The speed score comes from a benchmark each
          machine runs when it joins: a machine that does a minute of reference
          work in half the time has a score of 2. It costs twice as much per
          minute and finishes the same work in half the minutes. Every job has a
          signed receipt showing these numbers.{" "}
          <Link href="/docs/pricing" className={textLink}>
            Pricing in the docs
          </Link>
        </p>
      </Band>

      <Band title="Trial grant">
        <p className="leading-7">
          Every new organization starts with a grant worth about{" "}
          {formatUsd(TRIAL_GRANT_USD)}. With a card on file it is{" "}
          {formatUsd(TRIAL_GRANT_WITH_CARD_USD)}.
        </p>
        <p className="leading-7 text-muted-foreground">
          {formatUsd(TRIAL_GRANT_USD)} pays for about{" "}
          {formatCount(trialMinutes)} minutes on the {RUNNER_SIZES[0].vcpus}{" "}
          vCPU runner on your own machines.
        </p>
      </Band>

      <Band title="How jobs are paid for">
        <p className="leading-7 text-muted-foreground">
          Jobs are paid for with the HiveWorker token. Each job has a price, a
          payer, a payee and a receipt. This site shows US dollar estimates so
          the figures can be compared with other runners.
        </p>
      </Band>
    </>
  )
}
