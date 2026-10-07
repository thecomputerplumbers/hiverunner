"use client"

import * as React from "react"
import Link from "next/link"
import type {
  BoardMachine,
  BoardPeriod,
  BoardPerson,
} from "@workspace/db/leaderboard"
import { cn } from "@workspace/ui/lib/utils"

import { formatMinutes, formatUsd } from "@/components/console/format"
import { Panel } from "@/components/console/panel"

const PERIODS: Array<[BoardPeriod, string]> = [
  ["week", "7 days"],
  ["month", "30 days"],
  ["all", "all time"],
]

const HEX = "M12 1.2 21.35 6.6v10.8L12 22.8 2.65 17.4V6.6z"

const initials = (name: string) =>
  name
    .split(/\s+/)
    .map((part) => part[0] ?? "")
    .join("")
    .slice(0, 2)
    .toUpperCase()

/**
 * Who is carrying the hive. People are ranked by the runner minutes their
 * Macs served, because that is the thing a teammate actually gives; jobs and
 * what those minutes list for elsewhere ride along.
 */
export function Board({
  people,
  machines,
  organization,
  userId,
}: {
  people: BoardPerson[]
  machines: BoardMachine[]
  organization: string
  userId: string
}) {
  const [period, setPeriod] = React.useState<BoardPeriod>("week")
  const ranked = [...people].sort(
    (a, b) =>
      b.scores[period].seconds - a.scores[period].seconds ||
      a.name.localeCompare(b.name)
  )
  const top = Math.max(1, ranked[0]?.scores[period].seconds ?? 0)
  const total = ranked.reduce(
    (sum, person) => sum + person.scores[period].seconds,
    0
  )
  const mine = ranked.findIndex((person) => person.userId === userId)
  const me = mine < 0 ? null : ranked[mine]!
  const ahead = mine > 0 ? ranked[mine - 1]! : null
  const podium = ranked
    .slice(0, 3)
    .filter((person) => person.scores[period].seconds > 0)
  const macs = [...machines]
    .sort((a, b) => b.scores[period].seconds - a.scores[period].seconds)
    .filter((mac) => mac.scores[period].seconds > 0)
    .slice(0, 5)
  const fastest = [...machines].sort((a, b) => b.speedScore - a.speedScore)[0]

  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-4">
        <p className="max-w-2xl text-sm text-muted-foreground">
          Who is carrying {organization}&apos;s CI. Ranked by the runner minutes
          each person&apos;s Macs served.
        </p>
        <div
          role="group"
          aria-label="Period"
          className="flex border border-border font-mono text-xs"
        >
          {PERIODS.map(([key, label]) => (
            <button
              key={key}
              type="button"
              aria-pressed={period === key}
              onClick={() => setPeriod(key)}
              className={cn(
                "px-3 py-1.5 outline-none focus-visible:ring-2 focus-visible:ring-ring",
                period === key
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </div>

      {podium.length ? (
        <ol className="grid grid-cols-3 items-end gap-3 py-4 sm:gap-8">
          {[podium[1], podium[0], podium[2]].map((person, slot) => {
            const place = slot === 1 ? 1 : slot === 0 ? 2 : 3
            if (!person) return <li key={slot} aria-hidden="true" />
            return (
              <li
                key={person.userId}
                className={cn(
                  "flex flex-col items-center gap-2 text-center",
                  place === 1 ? "order-2" : place === 2 ? "order-1" : "order-3"
                )}
              >
                <div
                  className={cn(
                    "relative",
                    place === 1 ? "size-28 sm:size-36" : "size-20 sm:size-24"
                  )}
                >
                  <svg viewBox="0 0 24 24" className="size-full">
                    <path
                      d={HEX}
                      strokeWidth={0.5}
                      className={
                        place === 1
                          ? "fill-primary stroke-primary"
                          : "fill-primary/10 stroke-primary/70"
                      }
                    />
                  </svg>
                  <span
                    className={cn(
                      "absolute inset-0 flex items-center justify-center font-mono font-medium",
                      place === 1
                        ? "text-3xl text-primary-foreground"
                        : "text-xl"
                    )}
                  >
                    {initials(person.name)}
                  </span>
                </div>
                <p className="font-mono text-xs text-muted-foreground">
                  {place === 1 ? "queen bee" : `#${place}`}
                </p>
                <p className="max-w-full truncate font-medium">
                  {person.name}
                  {person.userId === userId ? " (you)" : ""}
                </p>
                <p className="font-mono text-sm tabular-nums">
                  {formatMinutes(person.scores[period].seconds)} min
                </p>
              </li>
            )
          })}
        </ol>
      ) : (
        <p className="border-l-2 border-primary bg-card/50 px-4 py-3 text-sm">
          No jobs in this window yet. The board fills as Macs take work.
        </p>
      )}

      {me ? (
        <p className="border-l-2 border-primary bg-card/50 px-4 py-3 text-sm">
          {me.scores[period].seconds === 0 ? (
            <>
              Your Mac has not run a job in this window. Leave it awake and
              plugged in and it will.
            </>
          ) : ahead ? (
            <>
              You are <strong>#{mine + 1}</strong>,{" "}
              <strong className="font-mono tabular-nums">
                {formatMinutes(
                  ahead.scores[period].seconds - me.scores[period].seconds
                )}{" "}
                minutes
              </strong>{" "}
              behind {ahead.name}.
            </>
          ) : (
            <>
              You are the <strong>queen bee</strong>. Your Macs carried{" "}
              {total
                ? Math.round((me.scores[period].seconds / total) * 100)
                : 0}
              % of the hive&apos;s minutes.
            </>
          )}
        </p>
      ) : (
        <p className="border-l-2 border-primary bg-card/50 px-4 py-3 text-sm">
          You are not on the board: none of your Macs serve this hive.{" "}
          <Link className="underline" href="/machines">
            Add one
          </Link>
          .
        </p>
      )}

      <Panel title="people" aside={`${ranked.length} in the hive`}>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[30rem] font-mono text-xs">
            <thead>
              <tr className="text-left whitespace-nowrap text-muted-foreground [&>th]:pb-2 [&>th]:font-normal">
                <th className="w-8">#</th>
                <th>who</th>
                <th className="w-[38%]">runner minutes</th>
                <th className="text-right">jobs</th>
                <th className="hidden text-right sm:table-cell">
                  worth on GitHub
                </th>
              </tr>
            </thead>
            <tbody>
              {ranked.map((person, index) => {
                const score = person.scores[period]
                return (
                  <tr
                    key={person.userId}
                    className={cn(
                      "border-t border-border [&>td]:py-2.5 [&>td]:pr-3 last:[&>td]:pr-0",
                      person.userId === userId && "bg-primary/[0.06]"
                    )}
                  >
                    <td
                      className={
                        index === 0 && score.seconds
                          ? "text-primary"
                          : "text-muted-foreground"
                      }
                    >
                      {score.seconds ? index + 1 : "–"}
                    </td>
                    <td className="max-w-40 truncate font-sans text-sm">
                      {person.name}
                      <span className="ml-2 font-mono text-xs text-muted-foreground">
                        {person.machines}{" "}
                        {person.machines === 1 ? "Mac" : "Macs"}
                      </span>
                    </td>
                    <td>
                      <span className="flex items-center gap-2">
                        <span className="w-12 shrink-0 text-sm tabular-nums">
                          {formatMinutes(score.seconds)}
                        </span>
                        <span className="h-1.5 flex-1 bg-foreground/[0.07]">
                          <span
                            className={cn(
                              "block h-full",
                              index === 0 ? "bg-primary" : "bg-foreground/40"
                            )}
                            style={{
                              width: `${(score.seconds / top) * 100}%`,
                            }}
                          />
                        </span>
                      </span>
                    </td>
                    <td className="text-right tabular-nums">
                      {score.jobs.toLocaleString("en-US")}
                    </td>
                    <td className="hidden text-right tabular-nums sm:table-cell">
                      {formatUsd(score.githubUsd)}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </Panel>

      {macs.length ? (
        <Panel
          title="busiest Macs"
          aside={
            fastest
              ? `fastest: ${fastest.name}, ${fastest.speedScore.toFixed(2)}×`
              : undefined
          }
        >
          <ol className="flex flex-col divide-y divide-border font-mono text-xs">
            {macs.map((mac, index) => (
              <li
                key={mac.id}
                className="flex items-center gap-3 py-2.5 first:pt-0 last:pb-0"
              >
                <span className="w-5 text-muted-foreground">{index + 1}</span>
                <span className="min-w-0 flex-1 truncate text-sm">
                  {mac.name}
                  <span className="ml-2 text-xs text-muted-foreground">
                    {mac.owner} · {mac.cores} cores
                  </span>
                </span>
                <span className="tabular-nums">
                  {formatMinutes(mac.scores[period].seconds)} min
                </span>
              </li>
            ))}
          </ol>
        </Panel>
      ) : null}
    </>
  )
}
