"use client"

import * as React from "react"
import type { LiveHour } from "@workspace/db/live"
import { cn } from "@workspace/ui/lib/utils"

import { WEEKDAYS, formatMoney, formatUsd, formatUtc } from "./format"

const DAY = 86_400_000

export type LiveDay = {
  at: number
  jobs: number
  failed: number
  seconds: number
  price: number
  githubUsd: number
}

/** Folds hours into UTC days, oldest first. */
export function byDay(hours: LiveHour[]): LiveDay[] {
  const days = new Map<number, LiveDay>()
  for (const hour of hours) {
    const at = Math.floor(hour.at / DAY) * DAY
    const day = days.get(at) ?? {
      at,
      jobs: 0,
      failed: 0,
      seconds: 0,
      price: 0,
      githubUsd: 0,
    }
    day.jobs += hour.jobs
    day.failed += hour.failed
    day.seconds += hour.seconds
    day.price += hour.price
    day.githubUsd += hour.githubUsd
    days.set(at, day)
  }
  return [...days.values()].sort((a, b) => a.at - b.at)
}

function Tip({
  x,
  children,
}: {
  /** 0 to 1 across the plot. */
  x: number
  children: React.ReactNode
}) {
  return (
    <div
      role="tooltip"
      className="pointer-events-none absolute top-0 z-10 w-max border border-border bg-popover px-2.5 py-1.5 font-mono text-xs shadow-lg"
      style={{
        left: `${x * 100}%`,
        transform: `translateX(${x > 0.7 ? "-100%" : x < 0.3 ? "0" : "-50%"})`,
      }}
    >
      {children}
    </div>
  )
}

export function Legend({
  items,
}: {
  items: Array<{ label: string; swatch: string }>
}) {
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-xs text-muted-foreground">
      {items.map((item) => (
        <li key={item.label} className="flex items-center gap-1.5">
          <span aria-hidden="true" className={cn("size-2.5", item.swatch)} />
          {item.label}
        </li>
      ))}
    </ul>
  )
}

/** Jobs each day, with the failed ones stacked on top. */
export function JobsPerDay({ days }: { days: LiveDay[] }) {
  const [hover, setHover] = React.useState<number | null>(null)
  const top = Math.max(1, ...days.map((day) => day.jobs))
  const height = 132
  const day = hover === null ? null : days[hover]
  return (
    <div className="flex flex-col gap-3">
      <div className="relative">
        <div
          className="flex items-end gap-0.5"
          style={{ height }}
          onPointerLeave={() => setHover(null)}
        >
          {days.map((entry, index) => {
            const passed = entry.jobs - entry.failed
            return (
              <button
                key={entry.at}
                type="button"
                aria-label={`${formatUtc(entry.at, "day")}: ${entry.jobs} jobs, ${entry.failed} failed`}
                onPointerEnter={() => setHover(index)}
                onFocus={() => setHover(index)}
                onBlur={() => setHover(null)}
                className={cn(
                  "flex h-full flex-1 flex-col justify-end gap-0.5 outline-none",
                  hover !== null && hover !== index && "opacity-45"
                )}
              >
                {entry.failed ? (
                  <span
                    className="w-full bg-destructive"
                    style={{
                      height: Math.max(2, (entry.failed / top) * height),
                    }}
                  />
                ) : null}
                {passed ? (
                  <span
                    className="w-full bg-primary"
                    style={{ height: Math.max(2, (passed / top) * height) }}
                  />
                ) : (
                  <span className="h-px w-full bg-border" />
                )}
              </button>
            )
          })}
        </div>
        {day && hover !== null ? (
          <Tip x={(hover + 0.5) / days.length}>
            <p className="text-muted-foreground">
              {WEEKDAYS[new Date(day.at).getUTCDay()]}{" "}
              {formatUtc(day.at, "day")}
            </p>
            <p>
              {day.jobs} jobs
              {day.failed ? `, ${day.failed} failed` : ""}
            </p>
            <p>{formatMoney(day.price, "fine")}</p>
          </Tip>
        ) : null}
      </div>
      <div className="flex justify-between font-mono text-[0.7rem] text-muted-foreground">
        <span>{days[0] ? formatUtc(days[0].at, "day") : ""}</span>
        <span>peak {top}/day</span>
        <span>today</span>
      </div>
      <Legend
        items={[
          { label: "passed", swatch: "bg-primary" },
          { label: "failed", swatch: "bg-destructive" },
        ]}
      />
    </div>
  )
}

/**
 * What the same minutes cost so far on each side: GitHub-hosted in grey,
 * this pool in honey. The space between the lines is the saving.
 */
export function CostLines({ days }: { days: LiveDay[] }) {
  const [hover, setHover] = React.useState<number | null>(null)
  const points = days.reduce<
    Array<{ at: number; ours: number; theirs: number }>
  >((sofar, day) => {
    const last = sofar.at(-1)
    sofar.push({
      at: day.at,
      ours: (last?.ours ?? 0) + day.price / 1_000_000,
      theirs: (last?.theirs ?? 0) + day.githubUsd,
    })
    return sofar
  }, [])
  const ours = points.at(-1)?.ours ?? 0
  const theirs = points.at(-1)?.theirs ?? 0
  const top = Math.max(theirs, ours, 0.01)
  const width = 400
  const height = 132
  const x = (index: number) =>
    points.length > 1 ? (index / (points.length - 1)) * width : width
  const y = (value: number) => height - 2 - (value / top) * (height - 8)
  const line = (key: "ours" | "theirs") =>
    points
      .map(
        (point, index) =>
          `${index ? "L" : "M"}${x(index).toFixed(1)} ${y(point[key]).toFixed(1)}`
      )
      .join("")
  const point = hover === null ? null : points[hover]
  return (
    <div className="flex flex-col gap-3">
      <div className="relative">
        <svg
          viewBox={`0 0 ${width} ${height}`}
          preserveAspectRatio="none"
          className="block w-full overflow-visible"
          style={{ height }}
          role="img"
          aria-label={`Over ${days.length} days these jobs cost ${formatUsd(ours)} here and would have listed for ${formatUsd(theirs)} on GitHub-hosted runners.`}
          onPointerMove={(event) => {
            const box = event.currentTarget.getBoundingClientRect()
            const ratio = (event.clientX - box.left) / box.width
            setHover(
              Math.min(
                points.length - 1,
                Math.max(0, Math.round(ratio * (points.length - 1)))
              )
            )
          }}
          onPointerLeave={() => setHover(null)}
        >
          {[0.25, 0.5, 0.75].map((step) => (
            <line
              key={step}
              x1={0}
              x2={width}
              y1={height * step}
              y2={height * step}
              className="stroke-border"
              strokeDasharray="2 4"
              vectorEffect="non-scaling-stroke"
            />
          ))}
          <path
            d={`${line("theirs")}L${width} ${height}L0 ${height}Z`}
            className="fill-foreground/[0.04]"
          />
          <path
            d={line("theirs")}
            fill="none"
            strokeWidth={2}
            className="stroke-chart-3"
            vectorEffect="non-scaling-stroke"
          />
          <path
            d={line("ours")}
            fill="none"
            strokeWidth={2}
            className="stroke-primary"
            vectorEffect="non-scaling-stroke"
          />
          {hover !== null ? (
            <line
              x1={x(hover)}
              x2={x(hover)}
              y1={0}
              y2={height}
              className="stroke-foreground/40"
              vectorEffect="non-scaling-stroke"
            />
          ) : null}
        </svg>
        {point && hover !== null ? (
          <Tip x={hover / Math.max(1, points.length - 1)}>
            <p className="text-muted-foreground">
              through {formatUtc(point.at, "day")}
            </p>
            <p>GitHub-hosted {formatUsd(point.theirs)}</p>
            <p>this pool {formatUsd(point.ours)}</p>
          </Tip>
        ) : null}
      </div>
      <div className="flex justify-between font-mono text-[0.7rem] text-muted-foreground">
        <span>{days[0] ? formatUtc(days[0].at, "day") : ""}</span>
        <span>today</span>
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-xs text-muted-foreground">
        <li className="flex items-center gap-1.5">
          <span aria-hidden="true" className="h-0.5 w-3 bg-chart-3" />
          GitHub-hosted{" "}
          <span className="text-foreground tabular-nums">
            {formatUsd(theirs)}
          </span>
        </li>
        <li className="flex items-center gap-1.5">
          <span aria-hidden="true" className="h-0.5 w-3 bg-primary" />
          this pool{" "}
          <span className="text-foreground tabular-nums">
            {formatUsd(ours)}
          </span>
        </li>
      </ul>
    </div>
  )
}

/** Fourteen days by twenty-four hours: when the pool does its work. */
export function HourHeat({ hours }: { hours: LiveHour[] }) {
  const [hover, setHover] = React.useState<LiveHour | null>(null)
  const top = Math.max(1, ...hours.map((hour) => hour.jobs))
  const rows = new Map<number, Array<LiveHour | null>>()
  for (const hour of hours) {
    const day = Math.floor(hour.at / DAY) * DAY
    const row = rows.get(day) ?? Array.from({ length: 24 }, () => null)
    row[new Date(hour.at).getUTCHours()] = hour
    rows.set(day, row)
  }
  return (
    <div className="flex flex-col gap-2">
      <div
        className="grid gap-px"
        style={{ gridTemplateColumns: "2.75rem repeat(24, minmax(0, 1fr))" }}
        onPointerLeave={() => setHover(null)}
      >
        {[...rows.entries()].map(([day, row]) => (
          <React.Fragment key={day}>
            <span className="self-center font-mono text-[0.65rem] text-muted-foreground">
              {formatUtc(day, "day")}
            </span>
            {row.map((hour, index) => (
              <span
                key={index}
                onPointerEnter={() => setHover(hour)}
                className={cn(
                  "aspect-square min-h-2",
                  hour ? "bg-foreground/[0.06]" : "bg-transparent",
                  hover && hover === hour && "outline outline-foreground"
                )}
              >
                {hour?.jobs ? (
                  <span
                    className="block size-full bg-primary"
                    style={{ opacity: 0.25 + 0.75 * (hour.jobs / top) }}
                  />
                ) : null}
              </span>
            ))}
          </React.Fragment>
        ))}
        <span />
        {Array.from({ length: 24 }, (_, hour) => (
          <span
            key={hour}
            className="pt-1 text-center font-mono text-[0.6rem] text-muted-foreground"
          >
            {hour % 6 === 0 ? String(hour).padStart(2, "0") : ""}
          </span>
        ))}
      </div>
      <p className="min-h-4 font-mono text-xs text-muted-foreground">
        {hover
          ? `${formatUtc(hover.at)} UTC: ${hover.jobs} job${hover.jobs === 1 ? "" : "s"}${hover.failed ? `, ${hover.failed} failed` : ""}`
          : `hours are UTC; the busiest held ${top} job${top === 1 ? "" : "s"}`}
      </p>
    </div>
  )
}

/** A bare trend line for a stat; the stat beside it says what it measures. */
export function Spark({
  values,
  className,
}: {
  values: number[]
  className?: string
}) {
  const top = Math.max(...values, 0)
  if (!top)
    return <div className={cn("h-6 border-b border-border", className)} />
  return (
    <div
      aria-hidden="true"
      className={cn("flex h-6 items-end gap-px", className)}
    >
      {values.map((value, index) => (
        <span
          key={index}
          className={cn("flex-1", value ? "bg-primary/70" : "bg-border")}
          style={{ height: value ? `${Math.max(8, (value / top) * 100)}%` : 1 }}
        />
      ))}
    </div>
  )
}

/** The balance at each day's close, with a mark on the days funds arrived. */
export function BalanceChart({
  history,
}: {
  history: Array<{ at: number; balance: number; added: number }>
}) {
  const [hover, setHover] = React.useState<number | null>(null)
  const top = Math.max(1, ...history.map((day) => day.balance))
  const width = 600
  const height = 140
  const step = width / Math.max(1, history.length)
  const y = (value: number) =>
    height - 2 - (Math.max(0, value) / top) * (height - 10)
  const path = history
    .map(
      (day, index) =>
        `${index ? "L" : "M"}${(index * step).toFixed(1)} ${y(day.balance).toFixed(1)}H${((index + 1) * step).toFixed(1)}`
    )
    .join("")
  const day = hover === null ? null : history[hover]
  return (
    <div className="flex flex-col gap-2">
      <div className="relative">
        <svg
          viewBox={`0 0 ${width} ${height}`}
          preserveAspectRatio="none"
          className="block w-full"
          style={{ height }}
          role="img"
          aria-label={`Balance over the last ${history.length} days.`}
          onPointerMove={(event) => {
            const box = event.currentTarget.getBoundingClientRect()
            const ratio = (event.clientX - box.left) / box.width
            setHover(
              Math.min(
                history.length - 1,
                Math.max(0, Math.floor(ratio * history.length))
              )
            )
          }}
          onPointerLeave={() => setHover(null)}
        >
          <path d={`${path}V${height}H0Z`} className="fill-primary/10" />
          <path
            d={path}
            fill="none"
            strokeWidth={2}
            className="stroke-primary"
            vectorEffect="non-scaling-stroke"
          />
          {history.map((entry, index) =>
            entry.added > 0 ? (
              <line
                key={entry.at}
                x1={index * step}
                x2={index * step}
                y1={0}
                y2={height}
                className="stroke-foreground/50"
                strokeDasharray="2 3"
                vectorEffect="non-scaling-stroke"
              />
            ) : null
          )}
          {hover !== null ? (
            <rect
              x={hover * step}
              width={step}
              y={0}
              height={height}
              className="fill-foreground/10"
            />
          ) : null}
        </svg>
        {day && hover !== null ? (
          <Tip x={(hover + 0.5) / history.length}>
            <p className="text-muted-foreground">{formatUtc(day.at, "day")}</p>
            <p>closed at {formatMoney(day.balance)}</p>
            {day.added > 0 ? <p>+{formatMoney(day.added)} added</p> : null}
          </Tip>
        ) : null}
      </div>
      <div className="flex justify-between font-mono text-[0.7rem] text-muted-foreground">
        <span>{history[0] ? formatUtc(history[0].at, "day") : ""}</span>
        <span>dashed line: funds added</span>
        <span>today</span>
      </div>
    </div>
  )
}
