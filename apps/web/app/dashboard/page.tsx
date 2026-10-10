import { env } from "cloudflare:workers"
import Link from "next/link"
import { TRIAL_GRANT_USD } from "@workspace/pricing"
import { budgetHolds } from "@workspace/db/budget"
import { getLiveSnapshot } from "@workspace/db/live"
import { listUserMachines } from "@workspace/db/machines"
import { getPoolSettings } from "@workspace/db/settings"
import { roleAtLeast } from "@workspace/auth"
import { cn } from "@workspace/ui/lib/utils"

import { AccountPage } from "@/components/account-page"
import { AgentPrompt } from "@/components/agent-prompt"
import { CopyButton } from "@/components/console/invite"
import { ActionButton } from "@/components/action-button"
import { Notice, Panel, Snippet } from "@/components/console/panel"
import { HexMark } from "@/components/hex-mark"
import { RunnerSizes } from "@/components/console/runner-sizes"
import { SetupTabs } from "@/components/console/setup-tabs"
import { WorkflowDiff } from "@/components/marketing/workflow-diff"
import { getDb } from "@/db"
import { NoHiveSteps } from "@/components/no-hive-steps"
import {
  DAEMON_RELEASED,
  INSTALL_COMMAND,
  releasedDaemonVersion,
} from "@/components/marketing/site"
import { getPool } from "@/lib/session"
import { ROUTED_WORKFLOW } from "@/lib/workflow"
import { setupPrompt } from "@/mcp/journey"
import { LiveDashboard } from "./live-dashboard"

export const metadata = { title: "Dashboard" }

export default async function DashboardPage({
  searchParams,
}: {
  searchParams: Promise<{ installed?: string }>
}) {
  const { pool, session } = await getPool()
  if (!pool) return <NoPool />

  const { installed } = await searchParams
  const { organizationId, installation, membership } = pool
  const db = getDb()
  const [live, settings, released, held] = await Promise.all([
    getLiveSnapshot(db, organizationId),
    getPoolSettings(db, organizationId),
    releasedDaemonVersion(),
    budgetHolds(db, organizationId),
  ])
  const online = live.machines.filter(
    (machine) => machine.presence === "online" || machine.presence === "busy"
  ).length
  // Set up means a job has run here. Until then the page leads with a checklist.
  const ready = live.recent.length > 0
  // The caller's machines that serve other hives. Only asked for while this
  // hive has none: they are the quickest way to give it one.
  const elsewhere =
    ready || live.machines.length > 0
      ? []
      : await listUserMachines(db, session.user.id)
  const owner = roleAtLeast(membership.role, "owner")

  return (
    <AccountPage title={installation.accountLogin} live={live}>
      {installed && (
        <Notice>
          HiveWorker is installed on{" "}
          <strong>{installation.accountLogin}</strong>, with ${TRIAL_GRANT_USD}{" "}
          of credit to start. The steps to your first job are below.
        </Notice>
      )}
      {installation.status !== "active" && (
        <Notice tone="alert">
          {installation.status === "suspended"
            ? "The GitHub App is suspended on this organization. Jobs will not be picked up until an owner unsuspends it on GitHub."
            : "The GitHub App was uninstalled from this organization. Reinstall it to run jobs; your balance and history are kept."}
        </Notice>
      )}
      {held && (
        <Notice tone="alert">
          This month&apos;s budget is spent, so new jobs are waiting in
          GitHub&apos;s queue.{" "}
          <Link className="underline" href="/wallet">
            Raise or remove the budget
          </Link>{" "}
          to start it moving again.
        </Notice>
      )}
      {live.balance <= 0 && (
        <Notice tone="alert">
          The balance has run out, so new jobs are waiting in GitHub&apos;s
          queue.{" "}
          <Link className="underline" href="/wallet">
            Add funds
          </Link>{" "}
          to start it moving again.
        </Notice>
      )}

      {!ready && (
        <FirstJob
          machines={live.machines.length}
          online={online}
          jobs={live.recent.length}
          organization={installation.accountLogin}
          organizationId={organizationId}
          elsewhere={elsewhere}
          owner={owner}
          agentPrompt={setupPrompt(env.APP_URL)}
        />
      )}

      <LiveDashboard
        released={released}
        invite={{
          organization: installation.accountLogin,
          url: `${env.APP_URL}/join/${installation.accountLogin}`,
          teams: settings?.allowedTeams ?? [],
        }}
      />

      <div className="flex flex-col gap-1 text-sm text-muted-foreground">
        {installation.repositorySelection === "selected" && (
          <p>
            The App is installed on selected repositories only. Workflows in
            other repositories will not reach this hive.
          </p>
        )}
        {owner && (
          <p>
            Any member of {installation.accountLogin} on GitHub can add a
            machine.{" "}
            <Link className="text-foreground underline" href="/settings">
              Narrow it to teams
            </Link>
            .
          </p>
        )}
      </div>
    </AccountPage>
  )
}

/**
 * The path to a first job, with each step ticked as it actually happens.
 * It is the whole page's purpose until a job has run, and gone after.
 */
function FirstJob({
  machines,
  online,
  jobs,
  organization,
  organizationId,
  elsewhere,
  owner,
  agentPrompt,
}: {
  machines: number
  online: number
  jobs: number
  organization: string
  organizationId: string
  /** The caller's own machines, serving other hives and not this one. */
  elsewhere: Array<{ id: string; name: string; model: string }>
  /** Credit and cards are the owners' business; a member is not told of them. */
  owner: boolean
  /** The prompt that has a coding agent do these steps instead. */
  agentPrompt: string
}) {
  const steps = [
    {
      done: true,
      title: owner
        ? `HiveWorker is installed on ${organization}, with $${TRIAL_GRANT_USD} of credit and no card needed`
        : `HiveWorker is installed on ${organization}`,
      body: null,
    },
    {
      // A Mac that is not connected takes no job, so it is not done yet.
      done: online > 0,
      title:
        machines > 0
          ? online > 0
            ? `${machines === 1 ? "A Mac is" : `${machines} Macs are`} in the hive`
            : "A Mac is in the hive, but not connected right now"
          : "Add a Mac",
      body:
        machines > 0 ? (
          online > 0 ? null : (
            <p>
              Start it with <code>hived run</code>, or open its lid. Jobs wait
              until a Mac is connected.
            </p>
          )
        ) : (
          <>
            {elsewhere.length > 0 && (
              <>
                <p>
                  {elsewhere.length === 1
                    ? "A machine of yours already runs HiveWorker for another hive."
                    : "Machines of yours already run HiveWorker for another hive."}{" "}
                  Add one here with a click, or from its terminal with{" "}
                  <code>hived link {organization}</code>. Nothing to install; it
                  picks this hive up within a minute.
                </p>
                <div className="flex flex-wrap gap-2">
                  {elsewhere.map((machine) => (
                    <ActionButton
                      key={machine.id}
                      label={`Add ${machine.name} to ${organization}`}
                      path="/api/machines/pools"
                      body={{ machineId: machine.id, organizationId }}
                    />
                  ))}
                </div>
                <p>Or add a different machine:</p>
              </>
            )}
            {DAEMON_RELEASED ? (
              <p>
                On any Apple Silicon Mac or Linux machine with KVM, paste this
                into a terminal and sign in with GitHub when the browser opens.
                Anyone in {organization} can. It asks for no password.
              </p>
            ) : (
              <p>
                The installer is not released yet. This is the command that will
                add a Mac, and it does not work today.
              </p>
            )}
            <Snippet prompt>{INSTALL_COMMAND}</Snippet>
            <p>
              <Link className="underline" href="/download">
                {DAEMON_RELEASED
                  ? "What it installs, and what the Mac needs"
                  : "Status and requirements"}
              </Link>
            </p>
          </>
        ),
    },
    {
      done: jobs > 0,
      title: "Send a workflow to your hive",
      body: (
        <>
          <p>
            Add one small job in front of the ones you have. It asks whether a
            Mac in your hive is free, and falls back to the runner you use now
            when none is, so a closed lid never leaves a job waiting.
          </p>
          <Snippet>{ROUTED_WORKFLOW}</Snippet>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
            <CopyButton text={ROUTED_WORKFLOW} variant="outline">
              Copy the workflow
            </CopyButton>
            <Link className="underline" href="/docs/routing">
              How routing and fallback work
            </Link>
          </div>
          <p>For a job that can wait for a Mac, change one line instead:</p>
          <div className="grid items-start gap-6 md:grid-cols-2">
            <WorkflowDiff />
            <RunnerSizes />
          </div>
          <p>
            Push it, and the job runs on a Mac in your hive. This page updates
            when it does. If it is still queued after a minute, check the label
            and that a Mac shows ready below.
          </p>
        </>
      ),
    },
  ]
  const done = steps.filter((step) => step.done).length
  // One thing to do at a time: only the next step is open. The rest can be
  // opened, so the workflow line is there for someone preparing it early.
  const next = steps.findIndex((step) => !step.done)
  return (
    <Panel
      title="Get your first job running"
      aside={
        <>
          <span className="tabular-nums">
            {done} of {steps.length} done
          </span>
          <span
            aria-hidden="true"
            className="hidden h-2.5 w-24 border border-primary/50 sm:block"
          >
            <span
              className="block h-full bee-stripes"
              style={{ width: `${(done / steps.length) * 100}%` }}
            />
          </span>
        </>
      }
      flush
    >
      <SetupTabs
        byHand={
          <ol className="divide-y divide-border">
            {steps.map((step, index) => (
              <li key={step.title} className="flex gap-4 px-4 py-5 sm:px-6">
                <HexMark
                  n={index + 1}
                  state={step.done ? "done" : index === next ? "next" : "todo"}
                />
                {step.body ? (
                  <details
                    open={index === next}
                    className="group min-w-0 flex-1 pt-1"
                  >
                    <summary
                      className={cn(
                        "flex cursor-pointer list-none items-baseline justify-between gap-3 outline-none focus-visible:underline [&::-webkit-details-marker]:hidden",
                        index === next ? "font-medium" : "text-muted-foreground"
                      )}
                    >
                      <span>
                        <span className="sr-only">
                          {step.done ? "Done: " : "To do: "}
                        </span>
                        {step.title}
                      </span>
                      <span
                        aria-hidden="true"
                        className="font-mono text-xs font-normal text-muted-foreground group-open:hidden"
                      >
                        show
                      </span>
                    </summary>
                    <div className="mt-3 flex max-w-3xl flex-col gap-3 text-sm text-muted-foreground">
                      {step.body}
                    </div>
                  </details>
                ) : (
                  <p className="min-w-0 flex-1 pt-1 text-muted-foreground">
                    <span className="sr-only">
                      {step.done ? "Done: " : "To do: "}
                    </span>
                    {step.title}
                  </p>
                )}
              </li>
            ))}
          </ol>
        }
        withAgent={<AgentPrompt prompt={agentPrompt} bare />}
      />
    </Panel>
  )
}

function NoPool() {
  return (
    <AccountPage title="No hive yet">
      <div className="flex max-w-2xl flex-col gap-8 sm:gap-10">
        <NoHiveSteps />
        <AgentPrompt prompt={setupPrompt(env.APP_URL)} />
      </div>
    </AccountPage>
  )
}
