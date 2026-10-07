import {
  COMPETITOR_RATES,
  competitorUsdPerMinute,
  privatePoolUsdPerMinute,
} from "@workspace/pricing"
import { cn } from "@workspace/ui/lib/utils"
import { formatMultiple, formatRate } from "./format"

/**
 * One runner-minute on each option, drawn to scale. The bars share one axis,
 * so the gap between them is the real gap between the prices.
 */
export function PriceBars({
  vcpus,
  className,
}: {
  vcpus: number
  className?: string
}) {
  const ours = privatePoolUsdPerMinute(vcpus).net
  const rows = [
    {
      label: COMPETITOR_RATES.github.name,
      usd: competitorUsdPerMinute("github", vcpus),
      ours: false,
    },
    {
      label: COMPETITOR_RATES.blacksmith.name,
      usd: competitorUsdPerMinute("blacksmith", vcpus),
      ours: false,
    },
    { label: "HiveWorker", usd: ours, ours: true },
  ]
  const widest = Math.max(...rows.map((row) => row.usd))

  return (
    <ul className={cn("flex flex-col", className)}>
      {rows.map((row) => (
        <li
          key={row.label}
          className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-5 gap-y-2 border-t border-border py-4 first:border-t-0 sm:grid-cols-[13rem_minmax(0,1fr)_auto]"
        >
          <span
            className={cn(
              "font-mono text-sm",
              row.ours ? "text-foreground" : "text-muted-foreground"
            )}
          >
            {row.label}
          </span>
          <span className="order-last col-span-2 h-7 sm:order-0 sm:col-span-1">
            <span
              className={cn(
                "block h-full min-w-1",
                row.ours ? "bg-primary" : "bg-foreground/25"
              )}
              style={{ width: `${(row.usd / widest) * 100}%` }}
            />
          </span>
          <span className="flex items-baseline justify-end gap-3 font-mono tabular-nums">
            {row.usd > ours ? (
              <span className="text-xs text-muted-foreground">
                {formatMultiple(row.usd / ours)} the price
              </span>
            ) : null}
            <span className="w-[9ch] text-right text-base font-medium">
              {formatRate(row.usd)}
            </span>
          </span>
        </li>
      ))}
    </ul>
  )
}
