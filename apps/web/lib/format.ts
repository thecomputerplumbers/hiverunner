import { microTokensToUsd } from "@workspace/pricing"

const whole = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
})
const fine = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 4,
})

/**
 * A ledger amount as the dollars a person thinks in. Balances and payments
 * show cents; a single job can cost a fraction of one, so `fine` keeps up to
 * four decimals rather than showing a real charge as $0.00.
 */
export function formatMoney(
  amount: number,
  precision: "cents" | "fine" = "cents"
) {
  const usd = microTokensToUsd(amount)
  return (precision === "fine" ? fine : whole).format(usd)
}
