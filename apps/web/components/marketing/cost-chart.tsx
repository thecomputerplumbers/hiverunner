"use client"

import * as React from "react"
import { COMPETITOR_RATES, compareMonthlyUsd } from "@workspace/pricing"
import { cn } from "@workspace/ui/lib/utils"
import { formatCount, formatUsd } from "./format"

const MAX_MINUTES = 1_000_000
const SNAP = 10_000
const X_TICKS = [0, 250_000, 500_000, 750_000, 1_000_000]
const Y_STEPS = 5

/**
 * Honey is ours and grey is theirs. The two greys differ by dash, so the
 * three stay apart without a third colour.
 */
const SERIES = [
  {
    key: "github",
    label: COMPETITOR_RATES.github.name,
    stroke: "stroke-foreground/45",
    dot: "fill-foreground/60",
    dashed: false,
  },
  {
    key: "blacksmith",
    label: COMPETITOR_RATES.blacksmith.name,
    stroke: "stroke-foreground/45",
    dot: "fill-foreground/60",
    dashed: true,
  },
  {
    key: "net",
    label: "HiveWorker",
    stroke: "stroke-primary",
    dot: "fill-primary",
    dashed: false,
  },
] as const

const compact = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 1,
})

/**
 * A month of runner-minutes on each option as usage grows. Every price is a
 * flat rate per minute, so the lines are straight and the gap between them is
 * what a private pool keeps.
 */
export function CostChart({
  vcpus,
  className,
}: {
  vcpus: number
  className?: string
}) {
  const frame = React.useRef<HTMLDivElement>(null)
  const [width, setWidth] = React.useState(720)
  const [minutes, setMinutes] = React.useState<number | null>(null)

  React.useEffect(() => {
    const element = frame.current
    if (!element) return
    const observer = new ResizeObserver(([entry]) => {
      if (entry) setWidth(Math.round(entry.contentRect.width))
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  const wide = width >= 640
  const height = wide ? 380 : 280
  const margin = { top: 16, right: wide ? 176 : 12, bottom: 34, left: 52 }
  const plotWidth = Math.max(40, width - margin.left - margin.right)
  const plotHeight = height - margin.top - margin.bottom

  const top = compareMonthlyUsd({ minutes: MAX_MINUTES, vcpus })
  const ceiling = top.github
  const x = (value: number) => margin.left + (value / MAX_MINUTES) * plotWidth
  const y = (usd: number) =>
    margin.top + plotHeight - (usd / ceiling) * plotHeight

  const at = minutes === null ? null : compareMonthlyUsd({ minutes, vcpus })

  function track(event: React.PointerEvent<SVGRectElement>) {
    const box = event.currentTarget.getBoundingClientRect()
    const ratio = Math.min(
      1,
      Math.max(0, (event.clientX - box.left) / box.width)
    )
    setMinutes(Math.max(SNAP, Math.round((ratio * MAX_MINUTES) / SNAP) * SNAP))
  }

  return (
    <figure className={cn("flex flex-col gap-4", className)}>
      <div ref={frame} className="relative">
        <svg
          width={width}
          height={height}
          role="img"
          aria-label={`Monthly cost by runner-minutes on ${vcpus} vCPU. At one million minutes: ${SERIES.map((series) => `${series.label} ${formatUsd(top[series.key])}`).join(", ")}.`}
          className="block max-w-full font-mono text-[11px]"
        >
          {Array.from({ length: Y_STEPS + 1 }, (_, step) => {
            const usd = (ceiling / Y_STEPS) * step
            return (
              <g key={step}>
                <line
                  x1={margin.left}
                  x2={margin.left + plotWidth}
                  y1={y(usd)}
                  y2={y(usd)}
                  className={
                    step === 0 ? "stroke-foreground/40" : "stroke-foreground/10"
                  }
                />
                <text
                  x={margin.left - 10}
                  y={y(usd)}
                  textAnchor="end"
                  dominantBaseline="central"
                  className="fill-muted-foreground"
                >
                  ${compact.format(usd)}
                </text>
              </g>
            )
          })}
          {X_TICKS.map((tick) => (
            <text
              key={tick}
              x={x(tick)}
              y={height - 10}
              textAnchor={
                tick === 0 ? "start" : tick === MAX_MINUTES ? "end" : "middle"
              }
              className="fill-muted-foreground"
            >
              {tick === 0 ? "0 min" : compact.format(tick)}
            </text>
          ))}
          {/* What stays with the organization: hosted price down to net. */}
          <path
            d={`M${x(0)} ${y(0)}L${x(MAX_MINUTES)} ${y(top.github)}L${x(MAX_MINUTES)} ${y(top.net)}Z`}
            className="fill-primary/8"
          />
          {SERIES.map((series) => (
            <g key={series.key}>
              <line
                x1={x(0)}
                y1={y(0)}
                x2={x(MAX_MINUTES)}
                y2={y(top[series.key])}
                strokeWidth={2}
                strokeLinecap="round"
                strokeDasharray={series.dashed ? "2 6" : undefined}
                className={series.stroke}
              />
              {wide ? (
                <text
                  x={x(MAX_MINUTES) + 12}
                  y={y(top[series.key])}
                  dominantBaseline="central"
                  className="fill-foreground"
                >
                  {series.label}
                </text>
              ) : null}
            </g>
          ))}
          {at && minutes !== null ? (
            <g pointerEvents="none">
              <line
                x1={x(minutes)}
                x2={x(minutes)}
                y1={margin.top}
                y2={margin.top + plotHeight}
                className="stroke-foreground/40"
              />
              {SERIES.map((series) => (
                <circle
                  key={series.key}
                  cx={x(minutes)}
                  cy={y(at[series.key])}
                  r={4.5}
                  strokeWidth={2}
                  className={cn(series.dot, "stroke-background")}
                />
              ))}
            </g>
          ) : null}
          <rect
            x={margin.left}
            y={margin.top}
            width={plotWidth}
            height={plotHeight}
            fill="transparent"
            className="cursor-crosshair touch-pan-y"
            onPointerMove={track}
            onPointerDown={track}
            onPointerLeave={() => setMinutes(null)}
          />
        </svg>
        {at && minutes !== null ? (
          <div
            className="pointer-events-none absolute top-4 z-10 w-60 border border-border bg-popover p-3 font-mono text-xs shadow-lg"
            style={
              x(minutes) > margin.left + plotWidth / 2
                ? { right: width - x(minutes) + 14 }
                : { left: x(minutes) + 14 }
            }
          >
            <p className="text-muted-foreground">
              {formatCount(minutes)} minutes a month
            </p>
            <ul className="mt-2 flex flex-col gap-1.5">
              {SERIES.map((series) => (
                <li
                  key={series.key}
                  className="flex items-center justify-between gap-3"
                >
                  <span className="flex items-center gap-2 text-muted-foreground">
                    <Swatch series={series} />
                    {series.label}
                  </span>
                  <span className="tabular-nums">
                    {formatUsd(at[series.key])}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        ) : null}
      </div>
      <figcaption className="flex flex-col gap-3">
        <ul className="flex flex-wrap gap-x-6 gap-y-2 font-mono text-xs text-muted-foreground">
          {SERIES.map((series) => (
            <li key={series.key} className="flex items-center gap-2">
              <Swatch series={series} />
              {series.label}
            </li>
          ))}
        </ul>
        <p className="text-sm leading-6 text-muted-foreground">
          Monthly cost in USD on {vcpus} vCPU Linux arm64, at list prices and
          reference speed. The shaded band is the difference between
          GitHub-hosted and HiveWorker on your own machines.
        </p>
      </figcaption>
    </figure>
  )
}

function Swatch({ series }: { series: (typeof SERIES)[number] }) {
  return (
    <svg width="18" height="6" aria-hidden="true" className="shrink-0">
      <line
        x1="1"
        x2="17"
        y1="3"
        y2="3"
        strokeWidth={2}
        strokeLinecap="round"
        strokeDasharray={series.dashed ? "2 5" : undefined}
        className={series.stroke}
      />
    </svg>
  )
}
