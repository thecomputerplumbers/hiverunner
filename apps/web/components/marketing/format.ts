/** A per-minute rate: as many decimals as it needs, never rounded to $0.00. */
export function formatRate(usd: number) {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 3,
    maximumFractionDigits: 5,
  }).format(usd)
}

/** A dollar total: cents only below $100, and only when there are any. */
export function formatUsd(usd: number) {
  const digits = usd >= 100 || Number.isInteger(usd) ? 0 : 2
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(usd)
}

export function formatCount(value: number) {
  return new Intl.NumberFormat("en-US").format(value)
}

/** "2026-10-03" → "October 3, 2026", without a timezone shifting the day. */
export function formatIsoDate(date: string) {
  return new Intl.DateTimeFormat("en-US", {
    dateStyle: "long",
    timeZone: "UTC",
  }).format(new Date(`${date}T00:00:00Z`))
}

/** How many times one price is another: "20×", or "2.5×" when it is not whole. */
export function formatMultiple(ratio: number) {
  return `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 }).format(ratio)}×`
}
