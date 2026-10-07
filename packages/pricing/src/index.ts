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
 * What an organization can add to its balance in one card payment, in USD.
 * The browser names one of these; it never names an amount of its own.
 */
export const TOP_UP_AMOUNTS_USD = [10, 25, 50, 100] as const

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
 * Job sizes. Each label is its own scale set, because a scale set is one
 * queue and a runner cannot be chosen by size within it.
 */
export const RUNNER_SIZES = [
  { label: "hive", vcpus: 2 },
  { label: "hive-4cpu", vcpus: 4 },
  { label: "hive-8cpu", vcpus: 8 },
] as const

export type RunnerLabel = (typeof RUNNER_SIZES)[number]["label"]

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
 * v1 runs Linux ARM jobs, so the site compares against `arm64`. The `x64`
 * rates are here so nobody quotes them against an ARM job by accident.
 */
export type RunnerArch = "arm64" | "x64"

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
    } as Record<RunnerArch, Record<number, number>>,
  },
  blacksmith: {
    name: "Blacksmith",
    source: "https://www.blacksmith.sh/pricing",
    usdPerMinute: {
      arm64: { 2: 0.0025, 4: 0.005, 8: 0.01 },
      x64: { 2: 0.004, 4: 0.008, 8: 0.016 },
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
