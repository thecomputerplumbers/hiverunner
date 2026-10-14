import { env } from "cloudflare:workers"
import { eq } from "drizzle-orm"
import { roleAtLeast } from "@workspace/auth"
import { organization } from "@workspace/auth/schema"
import {
  budgetAllowed,
  budgetHolds,
  getBudget,
  setBudget as saveBudget,
  spentThisMonth,
} from "@workspace/db/budget"
import { getInstallationByOrganization } from "@workspace/db/installations"
import { canAcquireJobs, getJob as readJob } from "@workspace/db/jobs"
import {
  BOARD_PERIODS,
  getLeaderboard as readLeaderboard,
  type BoardPeriod,
} from "@workspace/db/leaderboard"
import {
  getBalanceHistory,
  getLiveSnapshot,
  listLiveJobs,
  LIVE_DAYS,
  type LiveJob,
  type LiveMachine,
  type LiveSnapshot,
} from "@workspace/db/live"
import { getMachine, removeMachineFromPool } from "@workspace/db/machines"
import {
  getPoolSettings,
  updatePoolSettings,
  validatePoolSettings,
  type RoutingPolicy,
} from "@workspace/db/settings"
import {
  getAutoTopUp,
  setAutoTopUp as saveAutoTopUp,
} from "@workspace/db/top-up"
import {
  ALL_RUNNER_SIZES,
  AUTO_TOP_UP_BELOW_USD_OPTIONS,
  AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS,
  BUDGET_MAX_USD,
  compareMonthlyUsd,
  COMPETITOR_RATES,
  competitorUsdPerMinuteForLabel,
  FLOOR_USD_PER_COMPUTE_UNIT,
  JOB_TAX_RATE,
  machineServes,
  microTokensToUsd,
  outOfCredit,
  OVERDRAFT_USD,
  OWNER_CORES,
  privatePoolUsdPerMinute,
  runnerArch,
  runnerSize,
  RUNNER_SIZES,
  TOP_UP_AMOUNTS_USD,
  TRIAL_GRANT_USD,
} from "@workspace/pricing"

import { behind } from "@/components/console/format"
import {
  COORDINATOR_ORIGIN,
  INSTALL_COMMAND,
  releasedDaemonVersion,
} from "@/components/marketing/site"
import { AppError } from "@/lib/errors"
import { canTakeCards } from "@/lib/stripe"
import { plainWorkflow, routedWorkflow } from "@/lib/workflow"
import { authorize, type OperationContext } from "./context"

/**
 * What an agent can ask of a hive, and change about it. Every MCP tool is one
 * of these and nothing else: the scope, the caller's current membership and
 * the rules are all checked here, never in a tool.
 */

const DAY = 86_400_000

/** A sum of floating-point dollars, without the noise past a micro-dollar. */
const usd = (value: number) => Math.round(value * 1e6) / 1e6
const iso = (time: number | Date | null) =>
  time === null ? null : new Date(time).toISOString()

const urls = () => {
  const origin = env.APP_URL
  return {
    dashboard: `${origin}/dashboard`,
    machines: `${origin}/machines`,
    jobs: `${origin}/jobs`,
    wallet: `${origin}/wallet`,
    settings: `${origin}/settings`,
    install: `${origin}/install`,
    docs: `${origin}/docs`,
  }
}

type Budget = NonNullable<Awaited<ReturnType<typeof getBudget>>>

function budgetShape(budget: Budget | null, spent: number, held: boolean) {
  return budget
    ? {
        monthlyUsd: budget.monthlyCents / 100,
        atLimit: budget.atLimit,
        spentThisMonthUsd: microTokensToUsd(spent),
        held,
      }
    : null
}

type AutoTopUp = NonNullable<Awaited<ReturnType<typeof getAutoTopUp>>>

function autoTopUpShape(row: AutoTopUp | null) {
  return row
    ? {
        enabled: row.enabled,
        amountUsd: row.amountCents / 100,
        belowUsd: row.belowCents / 100,
        monthlyCapUsd:
          row.monthlyCapCents === null ? null : row.monthlyCapCents / 100,
        card: row.paymentMethodId
          ? `${row.cardBrand ?? "card"} ending ${row.cardLast4 ?? "????"}`
          : null,
      }
    : null
}

type Settings = NonNullable<Awaited<ReturnType<typeof getPoolSettings>>>

const settingsShape = (row: Settings) => ({
  allowedTeams: row.allowedTeams,
  earningsDestination: row.earningsDestination,
  macosJobs: row.macosJobs,
  routing: row.routing,
  version: row.version,
})

/** A machine with a job on it is busy, whatever it last said it was. */
function presenceOf(machine: LiveMachine, live: LiveSnapshot) {
  return live.running.some((job) => job.machineId === machine.id)
    ? "busy"
    : machine.presence
}

function jobShape(job: LiveJob) {
  return {
    id: job.id,
    repository: job.repository,
    jobName: job.jobName,
    label: job.label,
    vcpus: job.vcpus,
    machine: job.machine,
    startedAt: iso(job.startedAt),
    completedAt: iso(job.completedAt),
    result: job.result,
    durationSeconds:
      job.completedAt === null
        ? null
        : Math.max(0, Math.round((job.completedAt - job.startedAt) / 1000)),
    // What left the hive's balance; null while the job is running.
    priceUsd: job.price === null ? null : microTokensToUsd(job.price),
    url: `${env.APP_URL}/jobs/${encodeURIComponent(job.id)}`,
  }
}

/**
 * Who the caller is here. One person often wears several of these at once, so
 * each is its own answer: their role in the hive (which is their role in the
 * GitHub organization), what this connection was allowed, and the machines
 * that are theirs. `can` is the three put together, so an agent does not have
 * to work out for itself who "an owner" in a sentence is.
 */
function youShape(
  context: OperationContext,
  input: {
    role: string
    login: string
    presences: Array<{ yours: boolean; presence: string }>
    allowedTeams: string[]
  }
) {
  const { scopes } = context.actor
  // A browser session has no scopes and is held to the role alone.
  const writes = !scopes || scopes.includes("hive:write")
  const owner = roleAtLeast(input.role, "owner")
  const admin = roleAtLeast(input.role, "admin")
  const mine = input.presences.filter((each) => each.yours)
  const count = (...states: string[]) =>
    mine.filter((each) => states.includes(each.presence)).length
  const machines = {
    total: mine.length,
    online: count("online"),
    busy: count("busy"),
    paused: count("paused", "draining"),
    offline: count("offline"),
  }
  const hats = [
    owner
      ? `an owner of ${input.login} on GitHub, so an owner of this hive`
      : admin
        ? `an admin of this hive, but not an owner of ${input.login} on GitHub`
        : `a member of ${input.login}, not an owner or admin`,
    machines.total
      ? `the owner of ${machines.total} of the hive's machines`
      : "not running a machine in this hive yet",
  ]
  return {
    userId: context.actor.userId,
    role: input.role,
    // The hive's owners are exactly the organization's owners on GitHub.
    githubOrganizationOwner: owner,
    connection: {
      scopes: scopes ?? null,
      canWrite: writes,
    },
    machines,
    can: {
      // On GitHub, by an owner of the organization; anyone else can only ask.
      manageGitHubApp: owner,
      addMachine:
        owner || input.allowedTeams.length === 0
          ? ("yes" as const)
          : ("allowed-teams-only" as const),
      updateSettings: owner && writes,
      setBudget: admin && writes,
      removeOwnMachine: writes,
      removeAnyMachine: owner && writes,
    },
    summary: `You are ${hats.join(", and ")}. These are separate: a role in the organization says what you may change, and anyone in it may also run machines.`,
  }
}

/** Everything about the hive in one read. What an agent should call first. */
export async function getHive(context: OperationContext) {
  const membership = await authorize(context, "hive:read")
  const { db } = context
  const { organizationId } = context.actor
  const cards = canTakeCards()
  const [
    [named],
    installation,
    live,
    settings,
    budget,
    spent,
    held,
    acquirable,
    released,
    topUp,
  ] = await Promise.all([
    db
      .select({ name: organization.name })
      .from(organization)
      .where(eq(organization.id, organizationId))
      .limit(1),
    getInstallationByOrganization(db, organizationId),
    getLiveSnapshot(db, organizationId),
    getPoolSettings(db, organizationId),
    getBudget(db, organizationId),
    spentThisMonth(db, organizationId),
    budgetHolds(db, organizationId),
    canAcquireJobs(db, organizationId),
    releasedDaemonVersion(),
    cards ? getAutoTopUp(db, organizationId) : null,
  ])
  if (!installation) throw new AppError("NOT_FOUND", "No such hive")

  const login = installation.accountLogin
  const mine = live.machines.map((machine) => ({
    yours: machine.userId === context.actor.userId,
    presence: presenceOf(machine, live),
  }))
  const you = youShape(context, {
    role: membership.role,
    login,
    presences: mine,
    allowedTeams: settings?.allowedTeams ?? [],
  })
  const presences = mine.map((each) => each.presence)
  const count = (...states: string[]) =>
    presences.filter((presence) => states.includes(presence)).length
  const machines = {
    total: live.machines.length,
    online: count("online"),
    busy: count("busy"),
    // Draining is finishing a job and taking no more, as a paused one does.
    paused: count("paused", "draining"),
    offline: count("offline"),
    outdated: live.machines.filter((machine) =>
      behind(machine.daemonVersion, released)
    ).length,
  }
  const connected = machines.online + machines.busy

  // In the order a person would have to fix them.
  const blockedBy =
    installation.status === "removed"
      ? ("app-uninstalled" as const)
      : installation.status === "suspended"
        ? ("app-suspended" as const)
        : !acquirable
          ? outOfCredit(live.balance)
            ? ("empty-balance" as const)
            : ("budget-spent" as const)
          : connected === 0
            ? ("no-machine-online" as const)
            : null

  const macosJobs = settings?.macosJobs === true
  const labels = ALL_RUNNER_SIZES.filter(
    (size) => size.arch !== "macos" || macosJobs
  ).map((size) => ({
    label: size.label,
    vcpus: size.vcpus,
    arch: size.arch,
    usdPerMinute: privatePoolUsdPerMinute(size.vcpus).net,
    githubUsdPerMinute: competitorUsdPerMinuteForLabel("github", size.label),
    machinesAbleNow: live.machines.filter(
      (machine) =>
        machine.presence === "online" &&
        machineServes(
          { arch: runnerArch(machine.arch), macos: machine.macos },
          size.arch
        ) &&
        machine.cores - size.vcpus >= OWNER_CORES
    ).length,
  }))

  const links = urls()
  // Said to the person this is, not to "an owner" they then have to find.
  const asOwner = you.can.manageGitHubApp
    ? `You are an owner of ${login} on GitHub, so you can`
    : `Only an owner of ${login} on GitHub can, and you are not one: ask one to`
  const whoAdds =
    you.can.addMachine === "yes"
      ? "You can do this yourself, as can anyone in the organization the hive allows."
      : `Only members of these GitHub teams may: ${(settings?.allowedTeams ?? []).join(", ")}.`
  const nextSteps: string[] = []
  if (installation.status === "removed")
    nextSteps.push(
      `The GitHub App was uninstalled from ${login}. ${asOwner} reinstall it at ${links.install} to run jobs; the balance and history are kept.`
    )
  if (installation.status === "suspended")
    nextSteps.push(
      `The GitHub App is suspended on ${login}. ${asOwner} unsuspend it in the organization's GitHub settings before jobs are picked up.`
    )
  if (machines.total === 0)
    nextSteps.push(
      `Add a machine: on an Apple Silicon Mac or a Linux machine with KVM, run \`${INSTALL_COMMAND}\` and sign in with GitHub. ${whoAdds}`
    )
  else if (connected === 0)
    nextSteps.push(
      you.machines.total
        ? `None of the hive's machines is connected, and ${you.machines.total} of them ${you.machines.total === 1 ? "is" : "are"} yours. Start yours with \`hived run\`, or open its lid; jobs wait until one is.`
        : "A machine is in the hive but none is connected, and none of them is yours: its owner starts it with `hived run`, or you add your own. Jobs wait until one is connected. list_machines says whose each is."
    )
  if (machines.total > 0 && you.machines.total === 0)
    nextSteps.push(
      `You have no machine of your own in this hive; the more people who add one, the more jobs it can run. On yours, run \`${INSTALL_COMMAND}\`, or \`hived link\` if it already serves another hive. ${whoAdds}`
    )
  if (live.lifetime.jobs === 0 && live.running.length === 0)
    nextSteps.push(
      `Change one line: runs-on: ${RUNNER_SIZES[0].label} in a workflow, and push. Call get_workflow for the YAML.`
    )
  if (outOfCredit(live.balance))
    nextSteps.push(
      `The balance has run out, including the $${OVERDRAFT_USD} it may go below zero, so new jobs wait in GitHub's queue. Add funds at ${links.wallet}.`
    )
  else if (live.balance < 0)
    nextSteps.push(
      `The balance is below zero. Jobs keep starting until it is $${OVERDRAFT_USD} below, then wait in GitHub's queue. Add funds at ${links.wallet}.`
    )
  if (held)
    nextSteps.push(
      `This month's budget is spent, so new jobs wait in GitHub's queue. ${
        you.can.setBudget
          ? `Raise or remove it with set_budget, or at ${links.wallet}.`
          : roleAtLeast(membership.role, "admin")
            ? `You may raise or remove it at ${links.wallet}; this connection is read-only, so set_budget needs you to connect again and approve hive:write.`
            : `Only an owner or admin of the hive can raise or remove it, at ${links.wallet}, and you are neither: ask one.`
      }`
    )

  const spentUsd = microTokensToUsd(live.lifetime.price)
  return {
    hive: {
      id: organizationId,
      login,
      name: named?.name ?? login,
      installation: {
        status: installation.status,
        repositorySelection: installation.repositorySelection,
      },
      yourRole: membership.role,
    },
    you,
    balance: {
      usd: microTokensToUsd(live.balance),
      microTokens: live.balance,
      // As the wallet works it out: at the smallest size, on its own machines.
      minutesLeftOn2vcpu: Math.max(
        0,
        Math.floor(
          microTokensToUsd(live.balance) /
            privatePoolUsdPerMinute(RUNNER_SIZES[0].vcpus).net
        )
      ),
    },
    jobs: {
      canStartNow: blockedBy === null,
      blockedBy,
      running: live.running.length,
    },
    budget: budgetShape(budget, spent, held),
    settings: settings ? settingsShape(settings) : null,
    ...(cards ? { autoTopUp: autoTopUpShape(topUp) } : {}),
    machines,
    labels,
    lifetime: {
      jobs: live.lifetime.jobs,
      minutes: Math.round(live.lifetime.seconds / 60),
      spentUsd,
      githubUsd: usd(live.lifetime.githubUsd),
      savedUsd: usd(Math.max(0, live.lifetime.githubUsd - spentUsd)),
    },
    nextSteps,
    urls: links,
  }
}

export async function listMachines(context: OperationContext) {
  await authorize(context, "hive:read")
  const [live, released] = await Promise.all([
    getLiveSnapshot(context.db, context.actor.organizationId),
    releasedDaemonVersion(),
  ])
  return {
    machines: live.machines.map((machine) => ({
      id: machine.id,
      name: machine.name,
      model: machine.model,
      os: machine.os,
      arch: machine.arch,
      cores: machine.cores,
      memoryBytes: machine.memoryBytes,
      speedScore: machine.speedScore,
      macos: machine.macos,
      daemonVersion: machine.daemonVersion,
      outdated: behind(machine.daemonVersion, released),
      presence: presenceOf(machine, live),
      lastSeenAt: iso(machine.lastSeenAt),
      owner: machine.owner,
      yours: machine.userId === context.actor.userId,
      [`jobsLast${LIVE_DAYS}Days`]: machine.jobs,
      [`minutesLast${LIVE_DAYS}Days`]: Math.round(machine.seconds / 60),
    })),
  }
}

export async function listJobs(
  context: OperationContext,
  input: {
    limit?: number
    status?: "running" | "finished" | "all"
    repository?: string
  } = {}
) {
  await authorize(context, "hive:read")
  const limit = input.limit ?? 50
  const status = input.status ?? "all"
  if (!Number.isInteger(limit) || limit < 1 || limit > 200)
    throw new AppError("INVALID_INPUT", "limit is a whole number from 1 to 200")
  if (!["running", "finished", "all"].includes(status))
    throw new AppError("INVALID_INPUT", "status is running, finished or all")
  const jobs = await listLiveJobs(
    context.db,
    context.actor.organizationId,
    limit,
    {
      ...(status === "all" ? {} : { running: status === "running" }),
      ...(input.repository ? { repository: input.repository } : {}),
    }
  )
  return { jobs: jobs.map(jobShape) }
}

/** One job and, once it has finished, its signed receipt. */
export async function getJob(context: OperationContext, input: { id: string }) {
  await authorize(context, "hive:read")
  // Scoped to the grant's hive: another hive's job id is simply not found.
  const job = await readJob(context.db, context.actor.organizationId, input.id)
  if (!job) throw new AppError("NOT_FOUND", "No such job")
  const machine = await getMachine(context.db, job.machineId)
  const money = (amount: number | null) =>
    amount === null ? null : microTokensToUsd(amount)
  return {
    id: job.id,
    repository: job.repository,
    jobName: job.jobName,
    label: job.label,
    vcpus: job.vcpus,
    speedScore: job.speedScore,
    machine: machine?.name ?? job.machineId,
    startedAt: iso(job.startedAt),
    completedAt: iso(job.completedAt),
    result: job.result,
    durationSeconds: job.completedAt
      ? Math.max(
          0,
          Math.round(
            (job.completedAt.getTime() - job.startedAt.getTime()) / 1000
          )
        )
      : null,
    computeUnits: job.computeUnits,
    // The split always sums: amount = tax + earning.
    amountUsd: money(job.amount),
    amountMicroTokens: job.amount,
    taxUsd: money(job.tax),
    taxMicroTokens: job.tax,
    earningUsd: money(job.earning),
    earningMicroTokens: job.earning,
    payee: job.payeeType ? `${job.payeeType}:${job.payeeId}` : null,
    // The exact bytes that were signed; verify them against the key below.
    receipt: job.receipt,
    receiptSignature: job.receiptSignature,
    receiptKeyId: job.receiptKeyId,
    receiptKeyUrl: `${COORDINATOR_ORIGIN}/v1/receipt-key`,
    url: `${env.APP_URL}/jobs/${encodeURIComponent(job.id)}`,
  }
}

/** The chart window by UTC day, and the balance at the end of each day. */
export async function getUsage(context: OperationContext) {
  await authorize(context, "hive:read")
  const { organizationId } = context.actor
  const [live, history] = await Promise.all([
    getLiveSnapshot(context.db, organizationId),
    getBalanceHistory(context.db, organizationId),
  ])
  const empty = () => ({
    jobs: 0,
    failed: 0,
    seconds: 0,
    price: 0,
    githubUsd: 0,
  })
  const byDay = new Map<number, ReturnType<typeof empty>>()
  const totals = empty()
  for (const hour of live.hours) {
    const day = Math.floor(hour.at / DAY) * DAY
    const sum = byDay.get(day) ?? empty()
    byDay.set(day, sum)
    for (const into of [sum, totals]) {
      into.jobs += hour.jobs
      into.failed += hour.failed
      into.seconds += hour.seconds
      into.price += hour.price
      into.githubUsd += hour.githubUsd
    }
  }
  const shape = (sum: ReturnType<typeof empty>) => {
    const spentUsd = microTokensToUsd(sum.price)
    return {
      jobs: sum.jobs,
      failed: sum.failed,
      minutes: Math.round(sum.seconds / 60),
      spentUsd,
      githubUsd: usd(sum.githubUsd),
      savedUsd: usd(Math.max(0, sum.githubUsd - spentUsd)),
    }
  }
  return {
    windowDays: LIVE_DAYS,
    days: [...byDay].map(([day, sum]) => ({
      date: iso(day)!.slice(0, 10),
      ...shape(sum),
    })),
    totals: shape(totals),
    balanceHistory: history.map((day) => ({
      date: iso(day.at)!.slice(0, 10),
      balanceUsd: microTokensToUsd(day.balance),
      addedUsd: microTokensToUsd(day.added),
    })),
  }
}

/** Who and which machines carried the hive's work over one period. */
export async function getLeaderboard(
  context: OperationContext,
  input: { period?: BoardPeriod } = {}
) {
  await authorize(context, "hive:read")
  const period = input.period ?? "week"
  if (!BOARD_PERIODS.includes(period))
    throw new AppError("INVALID_INPUT", "period is week, month or all")
  const board = await readLeaderboard(context.db, context.actor.organizationId)
  const score = (scores: (typeof board.people)[number]["scores"]) => ({
    jobs: scores[period].jobs,
    minutes: Math.round(scores[period].seconds / 60),
    githubUsd: usd(scores[period].githubUsd),
  })
  const byMinutes = (a: { minutes: number }, b: { minutes: number }) =>
    b.minutes - a.minutes
  return {
    period,
    people: board.people
      .map((person) => ({
        userId: person.userId,
        name: person.name,
        machines: person.machines,
        ...score(person.scores),
      }))
      .sort(byMinutes),
    machines: board.machines
      .map((machine) => ({
        id: machine.id,
        name: machine.name,
        owner: machine.owner,
        cores: machine.cores,
        speedScore: machine.speedScore,
        ...score(machine.scores),
      }))
      .sort(byMinutes),
  }
}

/**
 * Every label's price beside what the same minute lists for elsewhere, and
 * with `minutes`, what a month of them costs on each. The same for every
 * hive: nothing here is read from the database.
 */
export async function getPricing(
  context: OperationContext,
  input: { minutes?: number; vcpus?: number; arch?: string } = {}
) {
  await authorize(context, "hive:read")
  const vcpus = input.vcpus ?? 2
  const arch = input.arch ?? "arm64"
  if (![2, 4, 8].includes(vcpus))
    throw new AppError("INVALID_INPUT", "vcpus is 2, 4 or 8")
  // Blacksmith lists no macOS runner, so a month cannot be compared there.
  if (arch !== "arm64" && arch !== "x64")
    throw new AppError("INVALID_INPUT", "arch is arm64 or x64")
  if (
    input.minutes !== undefined &&
    !(Number.isFinite(input.minutes) && input.minutes >= 0)
  )
    throw new AppError("INVALID_INPUT", "minutes is zero or more")
  const month =
    input.minutes === undefined
      ? null
      : compareMonthlyUsd({ minutes: input.minutes, vcpus, arch })
  return {
    labels: ALL_RUNNER_SIZES.map((size) => {
      const ours = privatePoolUsdPerMinute(size.vcpus)
      return {
        label: size.label,
        vcpus: size.vcpus,
        arch: size.arch,
        usdPerMinute: { net: ours.net, gross: ours.gross },
        githubUsdPerMinute: competitorUsdPerMinuteForLabel(
          "github",
          size.label
        ),
        blacksmithUsdPerMinute: competitorUsdPerMinuteForLabel(
          "blacksmith",
          size.label
        ),
      }
    }),
    taxRate: JOB_TAX_RATE,
    floorUsdPerComputeUnit: FLOOR_USD_PER_COMPUTE_UNIT,
    trialGrantUsd: TRIAL_GRANT_USD,
    competitorRatesAsOf: COMPETITOR_RATES.asOf,
    ...(month && input.minutes !== undefined
      ? {
          month: {
            minutes: input.minutes,
            vcpus,
            arch,
            hiveUsd: usd(month.net),
            hiveGrossUsd: usd(month.gross),
            githubUsd: usd(month.github),
            blacksmithUsd: usd(month.blacksmith),
            cheaperThanGithubBy: usd(month.github - month.net),
            cheaperThanBlacksmithBy: usd(month.blacksmith - month.net),
          },
        }
      : {}),
    notes: [
      'net is what a job costs a hive whose own machines earn the payment back (earningsDestination: "organization"): the hive pays gross, its machines earn all but the tax, and only the tax leaves.',
      'gross is what leaves the balance when developers are paid directly (earningsDestination: "developer").',
      "Minutes are billed whole, at the machine's speed score; these rates are at a speed score of 1.",
      "cheaperThan…By is USD saved over the month at the net rate.",
    ],
  }
}

/** A workflow file for one label, with or without the capacity probe. */
export async function getWorkflow(
  context: OperationContext,
  input: { label?: string; fallback?: string; routed?: boolean } = {}
) {
  await authorize(context, "hive:read")
  const label = input.label ?? RUNNER_SIZES[0].label
  const fallback = input.fallback ?? "ubuntu-latest"
  const size = runnerSize(label)
  if (!size)
    throw new AppError(
      "INVALID_INPUT",
      `label is one of: ${ALL_RUNNER_SIZES.map((each) => each.label).join(", ")}`
    )
  // It is written into YAML, so it is a runner label and nothing else.
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$/.test(fallback))
    throw new AppError("INVALID_INPUT", "fallback is a single runner label")
  const routed = input.routed ?? true
  return {
    path: ".github/workflows/ci.yml",
    yaml: routed ? routedWorkflow({ label, fallback }) : plainWorkflow(label),
    notes: [
      ...(routed
        ? [
            "The route job asks whether a machine is free, waits up to fifteen seconds for one, and falls back otherwise. It never fails the workflow.",
            "The probe does not reserve a machine. If the machine leaves before the job starts, the job waits in GitHub's queue.",
            "Add `needs: route` and the `runs-on` line to every job that should be routed.",
          ]
        : [
            "Without the route job, a job waits in GitHub's queue until a machine in the hive is free.",
          ]),
      size.arch === "arm64"
        ? `${label} is arm64 and ubuntu-latest is x64. If the job needs one architecture on both paths, fall back to ubuntu-24.04-arm.`
        : size.arch === "x64"
          ? `${label} is x64 and runs only on Linux x64 machines in the hive.`
          : `${label} runs on an Apple Silicon Mac itself, not in a VM, and only while the hive's macosJobs setting is on. Fall back to a macOS runner such as macos-latest.`,
      "Docker is in the VM, with buildx and compose, for `docker` in a step, `services:` and `container:`. A `container:` image must be Debian, Ubuntu or Alpine based, or otherwise have Perl. No image is on the disk beforehand; each job pulls what it uses.",
      "The steps shown are an example. Keep the workflow's own steps and change only where it runs.",
    ],
  }
}

/**
 * Changes hive settings. Only what is passed changes. `version` is the one
 * `get_hive` returned: passing it refuses to save over someone else's change;
 * left out, the save is made against whatever is current.
 */
export async function updateSettings(
  context: OperationContext,
  input: {
    allowedTeams?: string[]
    earningsDestination?: "organization" | "developer"
    macosJobs?: boolean
    routing?: RoutingPolicy
    version?: number
  }
) {
  await authorize(context, "hive:write", "owner")
  const { organizationId } = context.actor
  const current = await getPoolSettings(context.db, organizationId)
  if (!current) throw new AppError("NOT_FOUND", "This hive has no settings yet")
  const settings = {
    allowedTeams: (input.allowedTeams ?? current.allowedTeams).map((team) =>
      team.trim().toLowerCase()
    ),
    earningsDestination:
      input.earningsDestination ?? current.earningsDestination,
    macosJobs: input.macosJobs ?? current.macosJobs,
    routing: input.routing ?? current.routing,
    version: input.version ?? current.version,
  }
  const fields = validatePoolSettings(settings)
  if (fields) throw new AppError("INVALID_INPUT", "Check the settings", fields)
  if (!(await updatePoolSettings(context.db, organizationId, settings)))
    throw new AppError(
      "CONFLICT",
      "These settings changed since that version. Call get_hive and try again."
    )
  const saved = await getPoolSettings(context.db, organizationId)
  return { settings: settingsShape(saved!) }
}

/** Sets, changes or with `monthlyUsd: null` removes the monthly budget. */
export async function setBudget(
  context: OperationContext,
  input: { monthlyUsd: number | null; atLimit?: "warn" | "pause" }
) {
  await authorize(context, "hive:write", "admin")
  const { organizationId } = context.actor
  if (input.monthlyUsd !== null && !budgetAllowed(input.monthlyUsd))
    throw new AppError(
      "INVALID_INPUT",
      `A budget is a whole number of dollars from $1 to $${BUDGET_MAX_USD.toLocaleString("en-US")}.`
    )
  if (
    input.atLimit !== undefined &&
    input.atLimit !== "warn" &&
    input.atLimit !== "pause"
  )
    throw new AppError("INVALID_INPUT", "atLimit is warn or pause")
  // Left out, what happens at the limit stays as it was.
  const atLimit =
    input.atLimit ??
    (await getBudget(context.db, organizationId))?.atLimit ??
    "warn"
  const budget = await saveBudget(context.db, {
    organizationId,
    dollars: input.monthlyUsd,
    atLimit,
  })
  const [spent, held] = await Promise.all([
    spentThisMonth(context.db, organizationId),
    budgetHolds(context.db, organizationId),
  ])
  return { budget: budgetShape(budget, spent, held) }
}

/**
 * Turns automatic top-up on or off, or changes what it does. It charges the
 * card saved by a payment made at a browser; nothing here can add a card.
 */
export async function setAutoTopUp(
  context: OperationContext,
  input: {
    enabled: boolean
    amountUsd?: number
    belowUsd?: number
    monthlyCapUsd?: number | null
  }
) {
  await authorize(context, "hive:write", "admin")
  if (!canTakeCards())
    throw new AppError(
      "CONFLICT",
      "Card payments are not set up on this deployment"
    )
  if (typeof input.enabled !== "boolean")
    throw new AppError("INVALID_INPUT", "Say on or off.")
  // Each value is one this app offers, or it is refused.
  const offered = (options: readonly number[], value: number | undefined) =>
    value === undefined || options.includes(value)
  const fields: Record<string, string> = {}
  if (!offered(TOP_UP_AMOUNTS_USD, input.amountUsd))
    fields.amountUsd = `One of ${TOP_UP_AMOUNTS_USD.join(", ")}`
  if (!offered(AUTO_TOP_UP_BELOW_USD_OPTIONS, input.belowUsd))
    fields.belowUsd = `One of ${AUTO_TOP_UP_BELOW_USD_OPTIONS.join(", ")}`
  if (
    input.monthlyCapUsd !== null &&
    !offered(AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS, input.monthlyCapUsd)
  )
    fields.monthlyCapUsd = `One of ${AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS.join(", ")}, or null for no limit`
  if (Object.keys(fields).length)
    throw new AppError(
      "INVALID_INPUT",
      "Choose one of the options offered.",
      fields
    )
  const cents = (dollars: number | undefined) =>
    dollars === undefined ? undefined : dollars * 100
  let saved: AutoTopUp | null
  try {
    saved = await saveAutoTopUp(context.db, {
      organizationId: context.actor.organizationId,
      enabled: input.enabled,
      amountCents: cents(input.amountUsd),
      belowCents: cents(input.belowUsd),
      monthlyCapCents:
        input.monthlyCapUsd === null ? null : cents(input.monthlyCapUsd),
    })
  } catch (error) {
    // The store's refusals are sentences written for the person asking.
    throw new AppError(
      "INVALID_INPUT",
      error instanceof Error ? error.message : "Could not save."
    )
  }
  return { autoTopUp: autoTopUpShape(saved) }
}

/**
 * Stops a machine serving this hive, and only this one: it keeps serving its
 * others and its token stays good. Its owner or an owner of the hive may.
 */
export async function removeMachine(
  context: OperationContext,
  input: { machineId: string }
) {
  const membership = await authorize(context, "hive:write")
  const machine = await getMachine(context.db, input.machineId)
  if (!machine) throw new AppError("NOT_FOUND", "No such machine")
  if (
    machine.userId !== context.actor.userId &&
    !roleAtLeast(membership.role, "owner")
  )
    throw new AppError(
      "FORBIDDEN",
      "Only the machine's owner or a hive owner can remove it"
    )
  if (
    !(await removeMachineFromPool(context.db, {
      machineId: machine.id,
      organizationId: context.actor.organizationId,
    }))
  )
    throw new AppError("NOT_FOUND", "No such machine")
  return { removed: true, machineId: machine.id, name: machine.name }
}
