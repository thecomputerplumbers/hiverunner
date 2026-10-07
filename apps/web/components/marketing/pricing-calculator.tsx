"use client"

import * as React from "react"
import { Input } from "@workspace/ui/components/input"
import {
  COMPETITOR_RATES,
  RUNNER_SIZES,
  compareMonthlyUsd,
} from "@workspace/pricing"
import { formatCount, formatUsd } from "./format"

/** The slider runs from a thousand to five million minutes on a log scale. */
const MIN_MINUTES = 1_000
const MAX_MINUTES = 5_000_000
const STEPS = 200
const SPAN = Math.log10(MAX_MINUTES / MIN_MINUTES)

function minutesAt(step: number) {
  const raw = MIN_MINUTES * 10 ** ((step / STEPS) * SPAN)
  // Two significant figures, so dragging lands on numbers a person would say.
  const magnitude = 10 ** (Math.floor(Math.log10(raw)) - 1)
  return Math.min(MAX_MINUTES, Math.round(raw / magnitude) * magnitude)
}

function stepFor(minutes: number) {
  const clamped = Math.min(MAX_MINUTES, Math.max(MIN_MINUTES, minutes))
  return Math.round((Math.log10(clamped / MIN_MINUTES) / SPAN) * STEPS)
}

export function PricingCalculator() {
  const [minutes, setMinutes] = React.useState(100_000)
  const [vcpus, setVcpus] = React.useState<number>(RUNNER_SIZES[0].vcpus)
  const id = React.useId()

  const month = compareMonthlyUsd({ minutes, vcpus })
  const rows = [
    { label: COMPETITOR_RATES.github.name, usd: month.github, ours: false },
    {
      label: COMPETITOR_RATES.blacksmith.name,
      usd: month.blacksmith,
      ours: false,
    },
    { label: "HiveWorker", usd: month.net, ours: true },
  ]
  const widest = Math.max(...rows.map((row) => row.usd))

  return (
    <div className="grid border border-border lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
      <div className="flex flex-col gap-7 border-b border-border p-5 sm:p-8 lg:border-r lg:border-b-0">
        <div className="flex flex-col gap-3">
          <label htmlFor={`${id}-minutes`} className="font-mono text-sm">
            Minutes per month
          </label>
          <Input
            id={`${id}-minutes`}
            type="number"
            inputMode="numeric"
            min={0}
            step={1000}
            value={minutes}
            onChange={(event) => {
              const next = Math.floor(event.target.valueAsNumber)
              setMinutes(Number.isFinite(next) && next > 0 ? next : 0)
            }}
            className="h-12 rounded-none font-mono text-xl tabular-nums md:text-xl"
          />
          {/* A native range: Base UI's slider needs a CJS shim that Vite cannot
              serve while @base-ui/react is excluded from pre-bundling. */}
          <input
            type="range"
            aria-label="Minutes per month"
            min={0}
            max={STEPS}
            value={stepFor(minutes)}
            onChange={(event) =>
              setMinutes(minutesAt(event.target.valueAsNumber))
            }
            className="h-6 w-full cursor-pointer accent-primary"
          />
          <div className="flex justify-between font-mono text-xs text-muted-foreground tabular-nums">
            <span>{formatCount(MIN_MINUTES)}</span>
            <span>{formatCount(MAX_MINUTES)}</span>
          </div>
        </div>
        <fieldset className="flex flex-col gap-3">
          <legend className="mb-3 font-mono text-sm">Runner size</legend>
          <div className="grid grid-cols-3 gap-2">
            {RUNNER_SIZES.map((size) => (
              <label
                key={size.label}
                className="flex cursor-pointer flex-col gap-0.5 border border-border px-3 py-2.5 text-sm has-checked:border-primary has-checked:bg-primary/10 has-focus-visible:ring-3 has-focus-visible:ring-ring/50"
              >
                <input
                  type="radio"
                  name={`${id}-size`}
                  className="sr-only"
                  checked={vcpus === size.vcpus}
                  onChange={() => setVcpus(size.vcpus)}
                />
                <span className="font-mono font-medium">{size.vcpus} vCPU</span>
                <code className="font-mono text-xs text-muted-foreground">
                  {size.label}
                </code>
              </label>
            ))}
          </div>
        </fieldset>
      </div>
      <div className="flex flex-col gap-6 p-5 sm:p-8" aria-live="polite">
        <div>
          <p className="font-mono text-sm text-muted-foreground">
            {formatCount(minutes)} minutes a month on {vcpus} vCPU Linux arm64
          </p>
          <p className="mt-3 font-heading text-4xl font-medium tracking-tight tabular-nums sm:text-5xl">
            {formatUsd(month.net)}
          </p>
          <p className="mt-2 leading-7 text-muted-foreground">
            On your own Macs, where {COMPETITOR_RATES.github.name} runners would
            cost {formatUsd(month.github)}.
          </p>
        </div>
        <ul className="flex flex-col gap-4 border-t border-border pt-6">
          {rows.map((row) => (
            <li key={row.label} className="flex flex-col gap-1.5">
              <div className="flex items-baseline justify-between gap-4">
                <span className="font-mono text-xs text-muted-foreground">
                  {row.label}
                </span>
                <span className="font-mono font-medium tabular-nums">
                  {formatUsd(row.usd)}
                </span>
              </div>
              <div className="h-5">
                <div
                  className={
                    row.ours
                      ? "h-full min-w-1 bg-primary transition-[width] duration-200"
                      : "h-full min-w-1 bg-foreground/25 transition-[width] duration-200"
                  }
                  style={{
                    width: widest > 0 ? `${(row.usd / widest) * 100}%` : 0,
                  }}
                />
              </div>
            </li>
          ))}
        </ul>
        <p className="text-sm leading-6 text-muted-foreground">
          USD estimates at reference speed, for jobs that run on your own
          organization's machines with the pool paying the organization's
          wallet.
        </p>
      </div>
    </div>
  )
}
