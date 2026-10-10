import type { LiveHour } from "@workspace/db/live"

/*
 * Not in charts.tsx: that file is a client module, and a server page that
 * calls a function exported from one fails to render.
 */

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
