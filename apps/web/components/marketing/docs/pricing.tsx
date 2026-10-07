import Link from "next/link"
import {
  FLOOR_USD_PER_COMPUTE_UNIT,
  JOB_TAX_RATE,
  TRIAL_GRANT_USD,
  TRIAL_GRANT_WITH_CARD_USD,
  microTokensToUsd,
  priceJob,
} from "@workspace/pricing"
import { formatRate, formatUsd } from "../format"

const percent = new Intl.NumberFormat("en-US", { style: "percent" })

/** The worked example: seven and a half minutes on four vCPUs. */
const example = { seconds: 450, vcpus: 4, speedScore: 1 }
const exampleJob = priceJob(example)

export function Pricing() {
  return (
    <>
      <p>
        Every job has a price. There is no subscription and no platform fee. The{" "}
        <Link href="/pricing">pricing page</Link> has a calculator; this page
        explains how a single job is charged and what comes back.
      </p>

      <h2 id="compute-unit">The compute unit</h2>
      <p>
        One compute unit is one vCPU for one minute at reference speed. The
        reference is one core of a standard GitHub-hosted runner. In a private
        pool a compute unit is charged at{" "}
        {formatRate(FLOOR_USD_PER_COMPUTE_UNIT)}, which is also the lowest rate
        the network accepts.
      </p>

      <h2 id="formula">The formula</h2>
      <p>
        <strong>
          Job charge = minutes × vCPUs × speed score × rate per compute unit
        </strong>
      </p>
      <ul>
        <li>
          <strong>Minutes</strong> come from GitHub's own record of when the job
          started and finished, not from the machine that ran it. They are
          rounded up to the whole minute, with a minimum of one.
        </li>
        <li>
          <strong>vCPUs</strong> come from the label the job asked for.
        </li>
        <li>
          <strong>Speed score</strong> comes from a benchmark each machine runs
          when it joins. A score of 1 is reference speed. A faster machine has a
          higher score, costs more per minute and needs fewer minutes.
        </li>
      </ul>
      <p>
        Example: a job that runs for {example.seconds / 60} minutes on{" "}
        {example.vcpus} vCPUs at speed score {example.speedScore} is billed for{" "}
        {exampleJob.units} compute units, a charge of{" "}
        {formatRate(microTokensToUsd(exampleJob.amount))}. On your own machine{" "}
        {formatRate(microTokensToUsd(exampleJob.earning))} of that comes back,
        so the job's price is {formatRate(microTokensToUsd(exampleJob.tax))}.
      </p>

      <h2 id="where-it-goes">Where the charge goes</h2>
      <p>
        HiveWorker keeps {percent.format(JOB_TAX_RATE)} of each job's charge.
        The rest goes to the machine that ran the job. In a private pool that
        machine belongs to your organization, so when the pool pays the
        organization's wallet, the price of a job is the{" "}
        {percent.format(JOB_TAX_RATE)}. That is the price quoted everywhere on
        this site.
      </p>

      <h2 id="receipts">Receipts</h2>
      <p>
        Each job produces a signed receipt with its minutes, vCPUs, speed score,
        rate and amount. Receipts can be downloaded from the dashboard.
      </p>

      <h2 id="payment">How jobs are paid for</h2>
      <p>
        Jobs are paid for with the HiveWorker token. Amounts are shown with a US
        dollar estimate beside them.
      </p>

      <h2 id="trial">Trial grant</h2>
      <p>
        A new organization's wallet starts with a grant worth about{" "}
        {formatUsd(TRIAL_GRANT_USD)}, or {formatUsd(TRIAL_GRANT_WITH_CARD_USD)}{" "}
        with a card on file.
      </p>
    </>
  )
}
