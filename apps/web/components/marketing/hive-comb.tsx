"use client"

import * as React from "react"
import { cn } from "@workspace/ui/lib/utils"

/** Pointy-top comb: 8 cells on even rows, 7 on the offset rows between. */
const RADIUS = 30
const CELL_WIDTH = Math.sqrt(3) * RADIUS
const ROW_HEIGHT = RADIUS * 1.5
const ROWS = [8, 7, 8, 7]
const PAD = 3

const CELLS = ROWS.flatMap((count, row) =>
  Array.from({ length: count }, (_, column) => ({
    x:
      PAD + CELL_WIDTH / 2 + column * CELL_WIDTH + (row % 2) * (CELL_WIDTH / 2),
    y: PAD + RADIUS + row * ROW_HEIGHT,
  }))
)

const VIEW_WIDTH = PAD * 2 + CELL_WIDTH * 8
const VIEW_HEIGHT = PAD * 2 + RADIUS * 2 + ROW_HEIGHT * (ROWS.length - 1)

/** Macs whose lid is closed. They take no jobs and keep no VM. */
const ASLEEP = new Set([7, 20])

/**
 * Which cells are running a job in each frame, as [cell, vCPUs]. Written out
 * rather than random so the server and the browser render the same comb.
 */
const FRAMES: ReadonlyArray<ReadonlyArray<readonly [number, number]>> = [
  [
    [3, 2],
    [9, 4],
    [17, 2],
    [22, 8],
  ],
  [
    [3, 2],
    [9, 4],
    [12, 2],
    [22, 8],
    [27, 2],
  ],
  [
    [9, 4],
    [12, 2],
    [22, 8],
    [27, 2],
    [5, 4],
  ],
  [
    [12, 2],
    [27, 2],
    [5, 4],
    [19, 2],
    [14, 8],
  ],
  [
    [5, 4],
    [19, 2],
    [14, 8],
    [1, 2],
  ],
  [
    [19, 2],
    [14, 8],
    [1, 2],
    [3, 2],
    [24, 4],
  ],
]

function hexPath(x: number, y: number, radius: number) {
  const points = Array.from({ length: 6 }, (_, corner) => {
    const angle = (Math.PI / 180) * (60 * corner - 90)
    return `${(x + radius * Math.cos(angle)).toFixed(2)} ${(y + radius * Math.sin(angle)).toFixed(2)}`
  })
  return `M${points.join("L")}Z`
}

/**
 * A pool drawn as comb: one cell for each Mac. An illustration of how a pool
 * behaves, not a readout of anyone's machines.
 */
export function HiveComb({ className }: { className?: string }) {
  const [frame, setFrame] = React.useState(0)

  React.useEffect(() => {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return
    const timer = window.setInterval(
      () => setFrame((current) => (current + 1) % FRAMES.length),
      1800
    )
    return () => window.clearInterval(timer)
  }, [])

  const running = new Map(FRAMES[frame])
  const waiting = CELLS.length - ASLEEP.size - running.size

  return (
    <figure className={cn("flex flex-col gap-5", className)}>
      <svg
        viewBox={`0 0 ${VIEW_WIDTH.toFixed(1)} ${VIEW_HEIGHT.toFixed(1)}`}
        role="img"
        aria-label="A hive of thirty Macs drawn as honeycomb. A few cells are running jobs, most hold a booted VM waiting for one, and two are asleep."
        className="w-full"
      >
        {CELLS.map((cell, index) => {
          const vcpus = running.get(index)
          const asleep = ASLEEP.has(index)
          return (
            <g key={index}>
              <path
                d={hexPath(cell.x, cell.y, RADIUS - 3)}
                strokeWidth={1.5}
                strokeDasharray={asleep ? "3 4" : undefined}
                className={cn(
                  "transition-[fill,stroke] duration-500",
                  vcpus
                    ? "fill-primary stroke-primary"
                    : asleep
                      ? "fill-transparent stroke-foreground/25"
                      : "fill-primary/8 stroke-primary/55"
                )}
              />
              {vcpus ? (
                <text
                  x={cell.x}
                  y={cell.y}
                  textAnchor="middle"
                  dominantBaseline="central"
                  className="fill-primary-foreground font-mono text-[15px] font-semibold"
                >
                  {vcpus}
                </text>
              ) : null}
            </g>
          )
        })}
      </svg>
      <figcaption className="flex flex-wrap gap-x-6 gap-y-2 font-mono text-xs text-muted-foreground">
        <Key swatch="bg-primary" count={running.size}>
          running a job, by vCPUs
        </Key>
        <Key swatch="border border-primary/55 bg-primary/8" count={waiting}>
          VM booted and waiting
        </Key>
        <Key
          swatch="border border-dashed border-foreground/40"
          count={ASLEEP.size}
        >
          lid closed
        </Key>
      </figcaption>
    </figure>
  )
}

function Key({
  swatch,
  count,
  children,
}: {
  swatch: string
  count: number
  children: React.ReactNode
}) {
  return (
    <span className="flex items-center gap-2">
      <span aria-hidden="true" className={cn("size-2.5", swatch)} />
      <span className="text-foreground tabular-nums">{count}</span>
      {children}
    </span>
  )
}
