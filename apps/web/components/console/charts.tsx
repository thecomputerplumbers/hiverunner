"use client"

import * as React from "react"
import { cn } from "@workspace/ui/lib/utils"

import type { LiveDay } from "./days"
import { WEEKDAYS, formatMoney, formatUtc } from "./format"

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
