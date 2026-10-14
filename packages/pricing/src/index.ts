/**
 * The one place prices come from.
 *
 * The coordinator charges from these numbers and the site renders from them,
 * so the marketing page cannot promise a price the meter does not charge.
 *
 * Everything marked PLACEHOLDER is a starting point from
 * docs/goals-and-decisions.md, not a decision.
 */

/** Ledger amounts are integers in millionths of a token. */
export const MICRO = 1_000_000

/**
 * PLACEHOLDER. USD shown beside token amounts. There is no market price until
 * the token exists; this is the reference used for estimates.
 */
export const REFERENCE_USD_PER_TOKEN = 1

/** PLACEHOLDER. Network-wide minimum, in USD per compute unit. */
export const FLOOR_USD_PER_COMPUTE_UNIT = 0.0005

/** PLACEHOLDER. Share of each job payment that goes to the treasury. */
export const JOB_TAX_RATE = 0.25

/** PLACEHOLDER. Tax on every other token transfer. */
export const TRANSFER_TAX_RATE = 0.01

/** PLACEHOLDER. Trial grant in USD, and the grant once a card is on file. */
export const TRIAL_GRANT_USD = 5
export const TRIAL_GRANT_WITH_CARD_USD = 10

/**
 * The amounts offered as one-click card payments, in USD. They are also the
 * only amounts automatic top-up will charge.
 */
export const TOP_UP_AMOUNTS_USD = [10, 25, 50, 100] as const

/**
 * The least and the most one card payment made by hand may add, in USD. Any
 * whole number of dollars between them can be paid; the browser names it and
 * the server checks it with `topUpAllowed`.
 */
export const TOP_UP_MIN_USD = 1
export const TOP_UP_MAX_USD = 1000

/** Whether an amount named by a browser is one a card payment may be for. */
export function topUpAllowed(dollars: unknown): dollars is number {
  return (
    typeof dollars === "number" &&
    Number.isInteger(dollars) &&
    dollars >= TOP_UP_MIN_USD &&
    dollars <= TOP_UP_MAX_USD
  )
}

/**
 * PLACEHOLDER. The balance, in USD, under which a hive's owners are told it
 * is running low. Under the trial grant, so a new hive is not warned the
 * moment it runs its first job.
 */
export const LOW_BALANCE_USD = 2

/**
 * How far below zero a hive's balance may go before new jobs wait. A hive
 * that runs out mid-afternoon keeps building until someone adds funds,
 * instead of stopping on the cent. Jobs already running always finish, so
 * the balance can end a little past this.
 */
export const OVERDRAFT_USD = 5

/** Whether a balance has used all of its overdraft, so new jobs wait. */
export function outOfCredit(balance: number) {
  return balance <= -usdToMicroTokens(OVERDRAFT_USD)
}

/** The share of a monthly budget at which a hive's owners are first told. */
export const BUDGET_WARNING_SHARE = 0.8

/** The largest monthly budget a hive can set, in whole USD. */
export const BUDGET_MAX_USD = 100_000

/**
 * The balances an organization can choose to top up below, in USD. The first
 * is the default: enough left that jobs keep running while the charge goes
 * through.
 */
export const AUTO_TOP_UP_BELOW_USD_OPTIONS = [5, 10, 25, 50] as const
export const AUTO_TOP_UP_BELOW_USD = AUTO_TOP_UP_BELOW_USD_OPTIONS[0]

/**
 * The most automatic top-up may charge in a calendar month, in USD, for an
 * organization that wants a ceiling. Payments made by hand do not count.
 */
export const AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS = [
  50, 100, 250, 500, 1000,
] as const

/**
 * PLACEHOLDER. The gift that comes with a card payment made by hand: a share
 * of what was paid, added again as credit, and which share is left to chance.
 * Every gift is worth something. `weight` is how many rolls in `GIFT_ROLLS`
 * land on a prize, so the shares average out to a little over a fifth of
 * what is paid in: generous on purpose, for now. Ordered from the commonest
 * to the rarest.
 */
export const GIFT_PRIZES = [
  { share: 0.05, weight: 400 },
  { share: 0.1, weight: 300 },
  { share: 0.25, weight: 180 },
  { share: 0.5, weight: 80 },
  { share: 1, weight: 30 },
  { share: 5, weight: 10 },
] as const

/** PLACEHOLDER. The most one gift is worth, in USD, whatever was paid. */
export const GIFT_CAP_USD = 250

/**
 * PLACEHOLDER. The most an organization's gifts add up to in a calendar
 * month, in USD. Without it, many small payments are many tries at the top
 * prize.
 */
export const GIFT_MONTHLY_CAP_USD = 500

export const GIFT_ROLLS = GIFT_PRIZES.reduce(
  (sum, prize) => sum + prize.weight,
  0
)

/** How long after a payment its gift can still be opened. */
export const GIFT_WINDOW_DAYS = 30

/**
 * Which prize a roll lands on, as an index into `GIFT_PRIZES`. A roll is a
 * whole number from 0 up to, and not including, `GIFT_ROLLS`.
 */
export function giftPrize(roll: number) {
  if (!Number.isInteger(roll) || roll < 0 || roll >= GIFT_ROLLS)
    throw new Error("A gift roll is a whole number below GIFT_ROLLS")
  let left = roll
  return GIFT_PRIZES.findIndex((prize) => (left -= prize.weight) < 0)
}

/**
 * What a prize is worth on a payment, in micro-tokens, to the whole cent and
 * no more than one gift may be.
 */
export function giftAmount(paid: number, prize: number) {
  const cent = MICRO / 100
  return Math.min(
    Math.round((paid * GIFT_PRIZES[prize]!.share) / cent) * cent,
    usdToMicroTokens(GIFT_CAP_USD)
  )
}

/**
 * What kind of machine a job runs on. Two are the architecture of a job's
 * Linux VM, and so of the machine that runs it. `macos` is not a VM at all:
 * the job runs on an Apple Silicon Mac itself, as a separate user, for the
 * things only macOS can do, such as building for iOS. Only a Mac whose owner
 * opted in serves it.
 */
export type RunnerArch = "arm64" | "x64" | "macos"

/**
 * Job sizes the site offers. Each label is its own scale set, because a
 * scale set is one queue and a runner cannot be chosen by size within it.
 *
 * A label names an architecture as well as a size: a job is only ever given
 * to a machine of its label's architecture. The plain labels are arm64, as
 * they have been since before there was a second one.
 */
export const RUNNER_SIZES = [
  { label: "hive", vcpus: 2, arch: "arm64" },
  { label: "hive-4cpu", vcpus: 4, arch: "arm64" },
  { label: "hive-8cpu", vcpus: 8, arch: "arm64" },
] as const

/**
 * The x64 sizes, which run on Linux x64 machines. They are listed apart from
 * `RUNNER_SIZES` because the site's prices, calculator and examples are all
 * written for arm64, the architecture every Mac in a hive has.
 */
export const X64_RUNNER_SIZES = [
  { label: "hive-x64", vcpus: 2, arch: "x64" },
  { label: "hive-x64-4cpu", vcpus: 4, arch: "x64" },
  { label: "hive-x64-8cpu", vcpus: 8, arch: "x64" },
] as const

/**
 * The macOS size, which runs on the Mac itself rather than in a VM, on Macs
 * whose owners opted in and in hives whose owners allow it. One size: a
 * native job is not confined to a core count, and four is what it is billed
 * and compared as, the same as GitHub's standard macOS runner.
 */
export const MACOS_RUNNER_SIZES = [
  { label: "hive-macos", vcpus: 4, arch: "macos" },
] as const

/** Every label a job can ask for, released or not. */
export const ALL_RUNNER_SIZES = [
  ...RUNNER_SIZES,
  ...X64_RUNNER_SIZES,
  ...MACOS_RUNNER_SIZES,
]

export type RunnerLabel = (typeof ALL_RUNNER_SIZES)[number]["label"]
export type RunnerSize = {
  label: RunnerLabel
  vcpus: number
  arch: RunnerArch
}

/** The size a `runs-on` label asks for, or undefined if it is not ours. */
export function runnerSize(label: string | null): RunnerSize | undefined {
  return ALL_RUNNER_SIZES.find((size) => size.label === label)
}

/**
 * How a machine's own word for its architecture reads here, or null for one
 * no job runs on. Go says `amd64` and Linux `x86_64` for what GitHub, and so
 * this product, calls `x64`.
 */
export function runnerArch(reported: unknown): RunnerArch | null {
  if (reported === "arm64" || reported === "aarch64") return "arm64"
  if (reported === "x64" || reported === "amd64" || reported === "x86_64")
    return "x64"
  return null
}

/** Cores every job leaves for the machine's owner. */
export const OWNER_CORES = 2

/**
 * Whether a machine is the kind a label names. An x64 job needs an x64
 * machine; a macOS job needs an arm64 Mac whose owner opted in to running
 * jobs on the Mac itself. A machine that does not say is arm64, which is all
 * there was before there were two. This is the one place the rule is
 * written: the coordinator's `serves` and the web's `get_hive` both call it,
 * so everything that asks what a machine can take gets the same answer.
 */
export function machineServes(
  machine: { arch?: RunnerArch | null; macos?: boolean },
  arch: RunnerArch
): boolean {
  const own = machine.arch ?? "arm64"
  if (arch === "macos") return own === "arm64" && machine.macos === true
  return own === arch
}

export function usdToMicroTokens(usd: number) {
  return Math.round((usd / REFERENCE_USD_PER_TOKEN) * MICRO)
}

export function microTokensToUsd(amount: number) {
  return (amount / MICRO) * REFERENCE_USD_PER_TOKEN
}

/** The floor as a ledger rate: micro-tokens per compute unit. */
export const FLOOR_RATE = usdToMicroTokens(FLOOR_USD_PER_COMPUTE_UNIT)

/**
 * One compute unit is one vCPU-minute at reference speed, the reference being
 * one core of a GitHub standard hosted runner. Minutes are billed whole.
 */
export function computeUnits(input: {
  seconds: number
  vcpus: number
  speedScore: number
}) {
  const minutes = Math.max(1, Math.ceil(input.seconds / 60))
  return minutes * input.vcpus * input.speedScore
}

/**
 * What one job moves, in micro-tokens. The three parts always sum to
 * `amount`, so the ledger balances to the unit.
 */
export function priceJob(input: {
  seconds: number
  vcpus: number
  speedScore: number
  /** Micro-tokens per compute unit. Refused below the floor. */
  rate?: number
}) {
  const rate = input.rate ?? FLOOR_RATE
  if (rate < FLOOR_RATE) throw new Error("Rate is below the network floor")
  const units = computeUnits(input)
  const amount = Math.round(units * rate)
  const tax = Math.round(amount * JOB_TAX_RATE)
  return { units, rate, amount, tax, earning: amount - tax }
}

/**
 * What a private pool costs the organization per runner-minute, in USD. The
 * org pays the floor and, when its own machines earn the payment back, is
 * left with only the tax.
 */
export function privatePoolUsdPerMinute(vcpus: number, speedScore = 1) {
  const gross = FLOOR_USD_PER_COMPUTE_UNIT * vcpus * speedScore
  return { gross, net: gross * JOB_TAX_RATE }
}

/**
 * Per-minute USD list prices for a Linux runner, by architecture and vCPU
 * count, for the comparison on the site. Check `asOf` before quoting these
 * anywhere new.
 *
 * The released labels are Linux arm64, so the site compares against `arm64`.
 * A job is compared at its own label's architecture: see
 * `competitorUsdPerMinuteForLabel`.
 */
export const COMPARISON_ARCH: RunnerArch = "arm64"

export const COMPETITOR_RATES = {
  asOf: "2026-10-03",
  github: {
    name: "GitHub-hosted",
    source:
      "https://docs.github.com/en/billing/reference/actions-runner-pricing",
    usdPerMinute: {
      arm64: { 2: 0.005, 4: 0.008, 8: 0.014 },
      x64: { 2: 0.006, 4: 0.012, 8: 0.022 },
      // The standard macOS runner, 3 or 4 cores, M1 or Intel.
      macos: { 4: 0.062 },
    } as Record<RunnerArch, Record<number, number>>,
  },
  blacksmith: {
    name: "Blacksmith",
    source: "https://www.blacksmith.sh/pricing",
    usdPerMinute: {
      arm64: { 2: 0.0025, 4: 0.005, 8: 0.01 },
      x64: { 2: 0.004, 4: 0.008, 8: 0.016 },
      // Blacksmith does not offer macOS runners.
      macos: {},
    } as Record<RunnerArch, Record<number, number>>,
  },
}

/** A competitor's list price for one runner-minute. Throws on an unknown size. */
export function competitorUsdPerMinute(
  provider: "github" | "blacksmith",
  vcpus: number,
  arch: RunnerArch = COMPARISON_ARCH
) {
  const rate = COMPETITOR_RATES[provider].usdPerMinute[arch][vcpus]
  if (rate === undefined)
    throw new Error(`No ${provider} ${arch} rate for ${vcpus} vCPUs`)
  return rate
}

/**
 * What a minute of a job with this label lists for at a competitor, at the
 * label's own architecture and size. Null for a label that is not ours, or a
 * size the competitor does not list: such a job is left out of a comparison
 * rather than guessed at.
 */
export function competitorUsdPerMinuteForLabel(
  provider: "github" | "blacksmith",
  label: string
) {
  const size = runnerSize(label)
  if (!size) return null
  return COMPETITOR_RATES[provider].usdPerMinute[size.arch][size.vcpus] ?? null
}

/**
 * What a month of runner-minutes costs on each option, in USD, at reference
 * speed. `net` is the private pool when the org's own machines earn the
 * payment back and only the tax is left.
 */
export function compareMonthlyUsd(input: {
  minutes: number
  vcpus: number
  arch?: RunnerArch
}) {
  const hive = privatePoolUsdPerMinute(input.vcpus)
  return {
    github:
      input.minutes * competitorUsdPerMinute("github", input.vcpus, input.arch),
    blacksmith:
      input.minutes *
      competitorUsdPerMinute("blacksmith", input.vcpus, input.arch),
    gross: input.minutes * hive.gross,
    net: input.minutes * hive.net,
  }
}
