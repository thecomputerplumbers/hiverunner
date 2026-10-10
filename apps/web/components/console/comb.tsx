"use client"

import * as React from "react"
import Link from "next/link"
import type { LiveJob, LiveMachine } from "@workspace/db/live"
import { cn } from "@workspace/ui/lib/utils"

import { behind, gib } from "./format"

const RADIUS = 46
const CELL_WIDTH = Math.sqrt(3) * RADIUS
const ROW_HEIGHT = RADIUS * 1.5
const PAD = 4

function hexPath(x: number, y: number, radius: number) {
  const points = Array.from({ length: 6 }, (_, corner) => {
    const angle = (Math.PI / 180) * (60 * corner - 90)
    return `${(x + radius * Math.cos(angle)).toFixed(2)} ${(y + radius * Math.sin(angle)).toFixed(2)}`
  })
  return `M${points.join("L")}Z`
}

/** Rows of `columns` and `columns - 1` cells, enough to seat every machine. */
function layout(count: number) {
  const columns = Math.min(9, Math.max(6, Math.ceil(Math.sqrt(count * 2.2))))
  const cells: Array<{ x: number; y: number }> = []
  let row = 0
  while (cells.length < count || row < 2) {
    const inRow = row % 2 ? columns - 1 : columns
    for (let column = 0; column < inRow; column++)
      cells.push({
        x:
          PAD +
          CELL_WIDTH / 2 +
          column * CELL_WIDTH +
          (row % 2) * (CELL_WIDTH / 2),
        y: PAD + RADIUS + row * ROW_HEIGHT,
      })
    row++
  }
  return {
    cells,
    width: PAD * 2 + CELL_WIDTH * columns,
    height: PAD * 2 + RADIUS * 2 + ROW_HEIGHT * (row - 1),
  }
}

export const PRESENCE: Record<string, string> = {
  busy: "running a job",
  online: "ready",
  draining: "finishing up",
  paused: "paused by its owner",
  offline: "offline",
}

/**
 * The pool as comb: one cell for each Mac that serves it, filled while that
 * Mac runs a job. Unlike the one on the home page this is a readout, and the
 * empty cells are only room.
 */
export function PoolComb({
  machines,
  running,
  seat,
  released,
  className,
}: {
  machines: LiveMachine[]
  running: LiveJob[]
  /** The daemon version currently released, to mark machines behind it. */
  released?: string | null
  /** Marks the first empty cell as an open place, and where it leads. */
  seat?: { label: string; href?: string }
  className?: string
}) {
  const [focus, setFocus] = React.useState<string | null>(null)
  const { cells, width, height } = layout(machines.length + (seat ? 1 : 0))
  const load = new Map<string, number>()
  for (const job of running)
    load.set(job.machineId, (load.get(job.machineId) ?? 0) + job.vcpus)
  // Stable seats: a Mac keeps its cell as others come and go.
  const seated = [...machines].sort((a, b) => a.id.localeCompare(b.id))
  const focused = seated.find((machine) => machine.id === focus)
  const focusedCell = focused ? cells[seated.indexOf(focused)] : null

  return (
    <div className={cn("relative", className)}>
      <svg
        viewBox={`0 0 ${width.toFixed(1)} ${height.toFixed(1)}`}
        role="img"
        aria-label={`${machines.length} Macs in this hive, ${load.size} running jobs.`}
        className="mx-auto w-full"
        style={{ maxWidth: width * 1.25 }}
      >
        {cells.map((cell, index) => {
          const machine = seated[index]
          if (!machine && seat && index === seated.length) {
            const open = (
              <>
                <path
                  d={hexPath(cell.x, cell.y, RADIUS - 4)}
                  strokeWidth={1.5}
                  strokeDasharray="5 4"
                  className="fill-primary/5 stroke-primary motion-safe:animate-pulse"
                />
                <text
                  x={cell.x}
                  y={cell.y}
                  textAnchor="middle"
                  dominantBaseline="central"
                  className="fill-primary font-mono text-[11px] font-medium"
                >
                  {seat.label}
                </text>
              </>
            )
            return seat.href ? (
              <a key="seat" href={seat.href} className="outline-none">
                {open}
              </a>
            ) : (
              <g key="seat">{open}</g>
            )
          }
          if (!machine)
            return (
              <path
                key={index}
                d={hexPath(cell.x, cell.y, RADIUS - 4)}
                strokeWidth={1}
                strokeDasharray="2 5"
                className="fill-transparent stroke-foreground/15"
              />
            )
          const vcpus = load.get(machine.id)
          const busy = Boolean(vcpus) || machine.presence === "busy"
          const awake = busy || machine.presence === "online"
          return (
            <Link
              key={machine.id}
              href={`/machines#${machine.id}`}
              aria-label={`${machine.name}, ${PRESENCE[machine.presence] ?? machine.presence}`}
              onPointerEnter={() => setFocus(machine.id)}
              onPointerLeave={() => setFocus(null)}
              onFocus={() => setFocus(machine.id)}
              onBlur={() => setFocus(null)}
              className="group outline-none"
            >
              <path
                d={hexPath(cell.x, cell.y, RADIUS - 4)}
                strokeWidth={1.5}
                strokeDasharray={
                  machine.presence === "paused" ? "4 4" : undefined
                }
                className={cn(
                  "transition-[fill,stroke] duration-500 group-focus-visible:stroke-foreground",
                  busy
                    ? "fill-primary stroke-primary"
                    : machine.presence === "online"
                      ? "fill-primary/10 stroke-primary/70 group-hover:fill-primary/20"
                      : machine.presence === "paused"
                        ? "fill-transparent stroke-foreground/45 group-hover:fill-foreground/5"
                        : "fill-foreground/[0.03] stroke-foreground/20 group-hover:fill-foreground/5"
                )}
              />
              {busy ? (
                <path
                  d={hexPath(cell.x, cell.y, RADIUS - 0.5)}
                  strokeWidth={1.5}
                  className="fill-transparent stroke-primary motion-safe:animate-pulse"
                />
              ) : null}
              <text
                x={cell.x}
                y={cell.y - 13}
                textAnchor="middle"
                dominantBaseline="central"
                className={cn(
                  "font-mono text-[11px] font-medium",
                  busy
                    ? "fill-primary-foreground"
                    : awake
                      ? "fill-foreground"
                      : "fill-muted-foreground"
                )}
              >
                {machine.name.length > 9
                  ? `${machine.name.slice(0, 8)}…`
                  : machine.name}
              </text>
              <text
                x={cell.x}
                y={cell.y + 3}
                textAnchor="middle"
                dominantBaseline="central"
                className={cn(
                  "font-mono text-[10px]",
                  busy ? "fill-primary-foreground/80" : "fill-muted-foreground"
                )}
              >
                {busy && vcpus
                  ? `${vcpus}/${machine.cores} vcpu`
                  : `${machine.cores} cores`}
              </text>
              <text
                x={cell.x}
                y={cell.y + 18}
                textAnchor="middle"
                dominantBaseline="central"
                className={cn(
                  "font-mono text-[9px]",
                  busy
                    ? "fill-primary-foreground/80"
                    : behind(machine.daemonVersion, released)
                      ? "fill-foreground"
                      : "fill-muted-foreground"
                )}
              >
                {short(machine.daemonVersion)}
                {behind(machine.daemonVersion, released) ? " ↑" : ""}
              </text>
            </Link>
          )
        })}
      </svg>
      {focused && focusedCell ? (
        <div
          role="tooltip"
          className="pointer-events-none absolute z-10 w-max max-w-56 -translate-x-1/2 border border-border bg-popover px-3 py-2 font-mono text-xs shadow-lg"
          style={{
            left: `${(focusedCell.x / width) * 100}%`,
            top: `calc(${((focusedCell.y + RADIUS) / height) * 100}% + 4px)`,
          }}
        >
          <p className="font-medium">{focused.name}</p>
          <p className="text-muted-foreground">
            {focused.model} · {focused.cores} cores · {gib(focused.memoryBytes)}
          </p>
          <p className="text-muted-foreground">
            {focused.owner} · speed {focused.speedScore.toFixed(2)}×
          </p>
          <p className="text-muted-foreground">
            hived {focused.daemonVersion}
            {behind(focused.daemonVersion, released) ? (
              <span className="text-foreground"> · {released} is released</span>
            ) : null}
          </p>
          <p className="mt-1">
            {PRESENCE[focused.presence] ?? focused.presence}
          </p>
        </div>
      ) : null}
    </div>
  )
}

/** A version as it fits in a cell: `v1.4.2`, cut short if it is a long one. */
const short = (version: string) => {
  const text = version.trim().replace(/^v?/, "v")
  return text.length > 10 ? `${text.slice(0, 9)}…` : text
}

/**
 * Which daemon versions the hive is on and how many Macs are on each, newest
 * first as text sorts them, so a hive on one version says so in one line.
 */
export function CombVersions({
  machines,
  released,
}: {
  machines: LiveMachine[]
  /** The daemon version currently released, to mark versions behind it. */
  released?: string | null
}) {
  const counts = new Map<string, number>()
  for (const machine of machines)
    counts.set(
      machine.daemonVersion,
      (counts.get(machine.daemonVersion) ?? 0) + 1
    )
  if (!counts.size) return null
  const versions = [...counts].sort(([a], [b]) =>
    b.localeCompare(a, "en", { numeric: true })
  )
  const stale = versions.some(([version]) => behind(version, released))
  return (
    <p className="font-mono text-xs text-muted-foreground">
      hived{" "}
      {versions.map(([version, count], index) => (
        <React.Fragment key={version}>
          {index ? " · " : ""}
          <span
            className={
              behind(version, released) ? "text-foreground" : undefined
            }
          >
            {version}
          </span>
          {versions.length > 1 ? (
            <span className="tabular-nums"> ×{count}</span>
          ) : null}
        </React.Fragment>
      ))}
      {versions.length === 1 && machines.length > 1 ? " on every Mac" : ""}
      {stale ? (
        <>
          {" · "}
          <span
            className="text-foreground"
            title="The daemon updates itself when idle unless its owner turned that off; or they run: hived update"
          >
            {released} is released
          </span>
        </>
      ) : null}
    </p>
  )
}

/** The states the pool's Macs are in now, with how many are in each. */
export function CombLegend({
  machines,
  running = [],
}: {
  machines: LiveMachine[]
  /** A Mac with a job here is counted as running one, as its cell is drawn. */
  running?: LiveJob[]
}) {
  const working = new Set(running.map((job) => job.machineId))
  const count = (presence: string) =>
    machines.filter(
      (machine) =>
        (working.has(machine.id) ? "busy" : machine.presence) === presence
    ).length
  const rows: Array<[string, string, string]> = [
    ["busy", "running a job", "border-primary bg-primary"],
    ["online", "ready", "border-primary/70 bg-primary/10"],
    ["draining", "finishing up", "border-foreground/20 bg-foreground/[0.03]"],
    ["paused", "paused", "border-dashed border-foreground/45"],
    ["offline", "offline", "border-foreground/20 bg-foreground/[0.03]"],
  ]
  const present = rows.filter(([presence]) => count(presence) > 0)
  if (!present.length) return null
  return (
    <ul className="flex flex-wrap gap-x-5 gap-y-1 font-mono text-xs text-muted-foreground">
      {present.map(([presence, label, swatch]) => (
        <li key={presence} className="flex items-center gap-1.5">
          <span aria-hidden="true" className={cn("size-2.5 border", swatch)} />
          <span className="text-foreground tabular-nums">
            {count(presence)}
          </span>{" "}
          {label}
        </li>
      ))}
    </ul>
  )
}
