import Link from "next/link"
import { Button } from "@workspace/ui/components/button"
import { Container } from "@workspace/ui-shell/components/page-header"
import {
  JOB_TAX_RATE,
  RUNNER_SIZES,
  competitorUsdPerMinute,
  privatePoolUsdPerMinute,
} from "@workspace/pricing"
import { Code, CodeBlock } from "@/components/marketing/code"
import { CostChart } from "@/components/marketing/cost-chart"
import { EarlyAccessNote } from "@/components/marketing/early-access"
import { formatMultiple, formatRate } from "@/components/marketing/format"
import { HiveComb } from "@/components/marketing/hive-comb"
import { IsolationDiagram } from "@/components/marketing/isolation-diagram"
import { textLink } from "@/components/marketing/page-parts"
import { PriceBars } from "@/components/marketing/price-bars"
import {
  APP_PERMISSIONS,
  DAEMON_RELEASED,
  INSTALL_COMMAND,
} from "@/components/marketing/site"
import { StartTimeline } from "@/components/marketing/start-timeline"
import { WorkflowDiff } from "@/components/marketing/workflow-diff"
import { RouteLink } from "@/components/route-link"

export const metadata = {
  title: { absolute: "HiveWorker: GitHub Actions on your team's own Macs" },
  description:
    "Change runs-on to hive and your GitHub Actions jobs run in throwaway VMs on the Macs your developers already have, for a fraction of the price of hosted runners.",
}

const base = RUNNER_SIZES[0]
const ours = privatePoolUsdPerMinute(base.vcpus)
const github = competitorUsdPerMinute("github", base.vcpus)
const blacksmith = competitorUsdPerMinute("blacksmith", base.vcpus)
const percent = new Intl.NumberFormat("en-US", { style: "percent" })

export default function HomePage() {
  return (
    <>
      <section>
        <Container className="grid gap-12 py-14 sm:py-20 lg:grid-cols-[minmax(0,6fr)_minmax(0,5fr)] lg:items-center lg:gap-14">
          <div>
            <h1 className="font-heading text-[2rem] leading-[1.12] font-medium tracking-tight text-balance sm:text-5xl sm:leading-[1.1]">
              GitHub Actions minutes, {formatMultiple(blacksmith / ours.net)}{" "}
              cheaper.
            </h1>
            <p className="mt-6 max-w-xl text-lg leading-8 text-pretty text-muted-foreground">
              HiveWorker runs your jobs in throwaway VMs on the Macs your
              developers already have. When your own machines do the work, a{" "}
              {base.vcpus} vCPU minute costs {formatRate(ours.net)}. That is{" "}
              {formatMultiple(blacksmith / ours.net)} less than Blacksmith and{" "}
              {formatMultiple(github / ours.net)} less than a GitHub-hosted
              runner.
            </p>
            <div className="mt-8 flex flex-wrap gap-3">
              <Button
                nativeButton={false}
                size="lg"
                className="h-11 px-5 font-mono text-sm"
                render={<RouteLink to="/install" />}
              >
                Install the GitHub App
              </Button>
              <Button
                nativeButton={false}
                size="lg"
                variant="outline"
                className="h-11 px-5 font-mono text-sm"
                render={<Link href="/docs" />}
              >
                Read the docs
              </Button>
            </div>
            <EarlyAccessNote className="mt-8 max-w-xl" />
          </div>
          <div className="border border-border">
            <p className="border-b border-border px-4 py-2.5 font-mono text-xs text-muted-foreground">
              .github/workflows/ci.yml
            </p>
            <WorkflowDiff className="border-0 text-sm leading-9 sm:text-base sm:leading-10" />
            <p className="border-y border-border px-4 py-2.5 font-mono text-xs text-muted-foreground">
              Your organization's pool, one cell for each Mac
            </p>
            <HiveComb className="p-4 sm:p-5" />
          </div>
        </Container>
      </section>

      <section className="border-t border-border py-14 sm:py-20">
        <Container>
          <SectionHeading title="One runner-minute, drawn to scale.">
            A {base.vcpus} vCPU Linux arm64 minute at each provider's list
            price, beside the same minute in a private pool of your own Macs.{" "}
            <Link href="/pricing" className={textLink}>
              Work out your month
            </Link>
          </SectionHeading>
          <PriceBars
            vcpus={base.vcpus}
            className="mt-10 border border-border px-4 sm:px-6"
          />
        </Container>
      </section>

      <section className="border-t border-border py-14 sm:py-20">
        <Container>
          <SectionHeading title="The gap widens with every minute.">
            There is no plan to outgrow and no tier to step up to. Hover the
            chart to read a month at your volume.
          </SectionHeading>
          <CostChart
            vcpus={base.vcpus}
            className="mt-10 border border-border p-4 sm:p-6"
          />
        </Container>
      </section>

      <section className="border-t border-border py-14 sm:py-20">
        <Container>
          <SectionHeading title="No runner to wait for.">
            Jobs land on Apple Silicon your team already owns, on a VM that is
            already up.
          </SectionHeading>
          <StartTimeline className="mt-10" />
        </Container>
      </section>

      <section className="border-t border-border py-14 sm:py-20">
        <Container>
          <SectionHeading title="What you sign up for, in numbers." />
          <dl className="mt-10 grid border-t border-l border-border sm:grid-cols-2 lg:grid-cols-3">
            <Figure value="$0">
              Subscription, seats and platform fee. You pay per job, by the
              minute, and nothing else.
            </Figure>
            <Figure value={percent.format(JOB_TAX_RATE)}>
              Of each job's price is what HiveWorker keeps. The rest goes to the
              machine that ran it.
            </Figure>
            <Figure value="1 line">
              Changed in a workflow. Jobs without a <Code>hive</Code> label keep
              running where they run today.
            </Figure>
            <Figure value={`${APP_PERMISSIONS.length} permissions`}>
              For the GitHub App, and none of them reads code, contents or
              secrets.
            </Figure>
            <Figure value="1 VM">
              For each job, hardware-virtualized and destroyed when the job
              ends.
            </Figure>
            <Figure value="0 ports">
              Opened on a developer's Mac. The daemon makes one outbound
              connection and runs as the user, not root.
            </Figure>
          </dl>
        </Container>
      </section>

      <section className="border-t border-border py-14 sm:py-20">
        <Container>
          <SectionHeading title="Three steps to the first job." />
          <ol className="mt-10 grid border-t border-l border-border lg:grid-cols-3">
            <Step number={1} title="Install the GitHub App">
              <p>
                An organization owner installs HiveWorker on the organization.
                That creates the runner labels, a private pool, and a wallet
                holding a trial grant.
              </p>
              <ul className="flex flex-wrap gap-2">
                {RUNNER_SIZES.map((size) => (
                  <li key={size.label}>
                    <Code>{size.label}</Code>
                  </li>
                ))}
              </ul>
            </Step>
            <Step number={2} title="Developers add their Macs">
              <p>
                Each developer installs the daemon and signs in with GitHub.
                Belonging to your GitHub organization is what lets a machine
                join the pool.
              </p>
              <CodeBlock title="Terminal" wrap>
                {INSTALL_COMMAND}
              </CodeBlock>
              <p className="text-sm">
                {DAEMON_RELEASED
                  ? "One command, no password and nothing system-wide. "
                  : "The daemon is not released yet. "}
                <Link href="/download" className={textLink}>
                  {DAEMON_RELEASED
                    ? "What it installs"
                    : "Requirements and status"}
                </Link>
              </p>
            </Step>
            <Step number={3} title="Change one line">
              <p>
                Jobs with a <Code>hive</Code> label run on your pool. Switching
                back is the same one line.
              </p>
              <WorkflowDiff />
            </Step>
          </ol>
        </Container>
      </section>

      <section className="border-t border-border py-14 sm:py-20">
        <Container className="grid gap-10 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)] lg:gap-16">
          <div className="flex flex-col gap-5">
            <SectionHeading title="A job never touches the machine it runs on.">
              Every job gets its own hardware-virtualized VM, which is destroyed
              when the job ends. The VM talks to GitHub directly. HiveWorker
              assigns the job and is not in its data path.
            </SectionHeading>
            <p>
              <Link href="/security" className={textLink}>
                How isolation and trust work
              </Link>
            </p>
          </div>
          <IsolationDiagram />
        </Container>
      </section>

      <section className="border-t border-border bg-primary py-14 text-primary-foreground sm:py-16">
        <Container className="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
          <div className="max-w-2xl">
            <h2 className="font-heading text-2xl leading-tight font-medium tracking-tight text-balance sm:text-3xl">
              Try it on one workflow.
            </h2>
            <p className="mt-3 text-lg leading-8">
              Every new organization starts with a trial grant. HiveWorker is in
              early access, so installs are not open to everyone yet.
            </p>
          </div>
          <div className="flex flex-wrap gap-3">
            <Button
              nativeButton={false}
              size="lg"
              className="h-11 bg-primary-foreground px-5 font-mono text-sm text-primary hover:bg-primary-foreground/85"
              render={<RouteLink to="/install" />}
            >
              Install the GitHub App
            </Button>
            <Button
              nativeButton={false}
              size="lg"
              variant="ghost"
              className="h-11 border-primary-foreground/40 px-5 font-mono text-sm hover:bg-primary-foreground/10 hover:text-primary-foreground dark:hover:bg-primary-foreground/10"
              render={<Link href="/pricing" />}
            >
              See pricing
            </Button>
          </div>
        </Container>
      </section>
    </>
  )
}

/** A section's title behind a honey rule, with an optional sentence under it. */
function SectionHeading({
  title,
  children,
}: {
  title: string
  children?: React.ReactNode
}) {
  return (
    <div className="max-w-2xl">
      <h2 className="border-l-2 border-primary pl-4 font-heading text-2xl leading-snug font-medium tracking-tight text-balance sm:text-3xl">
        {title}
      </h2>
      {children ? (
        <p className="mt-5 text-lg leading-8 text-pretty text-muted-foreground">
          {children}
        </p>
      ) : null}
    </div>
  )
}

function Figure({
  value,
  children,
}: {
  value: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-3 border-r border-b border-border p-6 sm:p-8">
      <dt className="order-last leading-7 text-pretty text-muted-foreground">
        {children}
      </dt>
      <dd className="font-heading text-3xl font-medium tracking-tight">
        {value}
      </dd>
    </div>
  )
}

function Step({
  number,
  title,
  children,
}: {
  number: number
  title: string
  children: React.ReactNode
}) {
  return (
    <li className="flex min-w-0 flex-col gap-4 border-r border-b border-border p-6 sm:p-8">
      <div className="flex items-center gap-3">
        <span className="relative flex size-9 shrink-0 items-center justify-center font-mono text-sm font-medium">
          <svg
            viewBox="0 0 24 24"
            aria-hidden="true"
            className="absolute inset-0 size-full fill-none stroke-primary stroke-[1.5]"
          >
            <path d="M12 1.5 21.1 6.75v10.5L12 22.5 2.9 17.25V6.75z" />
          </svg>
          <span className="relative">{number}</span>
        </span>
        <h3 className="font-heading text-base font-medium tracking-tight">
          {title}
        </h3>
      </div>
      <div className="flex flex-col gap-4 leading-7 text-muted-foreground">
        {children}
      </div>
    </li>
  )
}
