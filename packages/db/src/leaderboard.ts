import { and, eq, isNotNull, sql } from "drizzle-orm"

import type { AuthDb } from "@workspace/auth/db"
import { user } from "@workspace/auth/schema"
import { competitorUsdPerMinuteForLabel } from "@workspace/pricing"

import { job, machine, machinePool } from "@workspace/db/schema"

const DAY = 86_400_000

export const BOARD_PERIODS = ["week", "month", "all"] as const
export type BoardPeriod = (typeof BOARD_PERIODS)[number]

export type BoardScore = {
  jobs: number
  seconds: number
  /** What those minutes list for on GitHub-hosted runners, in USD. */
  githubUsd: number
}

export type BoardPerson = {
  userId: string
  name: string
  image: string | null
  /** Macs of theirs serving this hive now. */
  machines: number
  scores: Record<BoardPeriod, BoardScore>
}

export type BoardMachine = {
  id: string
  name: string
  owner: string
  cores: number
  speedScore: number
  scores: Record<BoardPeriod, BoardScore>
}

const empty = (): Record<BoardPeriod, BoardScore> => ({
  week: { jobs: 0, seconds: 0, githubUsd: 0 },
  month: { jobs: 0, seconds: 0, githubUsd: 0 },
  all: { jobs: 0, seconds: 0, githubUsd: 0 },
})

/**
 * Who has carried the hive's work, by person and by Mac, over the last week,
 * the last thirty days and all time. One grouped query; the three windows are
 * conditional sums over it.
 *
 * A job counts for whoever's Mac ran it, whether or not that Mac still serves
 * the hive, so removing a machine does not erase what it did.
 */
export async function getLeaderboard(
  db: AuthDb,
  organizationId: string,
  now = new Date()
) {
  const weekAgo = now.getTime() - 7 * DAY
  const monthAgo = now.getTime() - 30 * DAY
  const took = sql`(${job.completedAt} - ${job.startedAt}) / 1000.0`
  const since = (from: number) => sql`${job.startedAt} >= ${from}`
  const [rows, serving] = await Promise.all([
    db
      .select({
        machineId: machine.id,
        machine: machine.name,
        cores: machine.cores,
        speedScore: machine.speedScore,
        userId: user.id,
        name: user.name,
        image: user.image,
        label: job.label,
        jobs: sql<number>`count(*)`.as("jobs"),
        seconds: sql<number>`coalesce(sum(${took}), 0)`.as("seconds"),
        weekJobs: sql<number>`coalesce(sum(${since(weekAgo)}), 0)`.as(
          "week_jobs"
        ),
        weekSeconds:
          sql<number>`coalesce(sum(case when ${since(weekAgo)} then ${took} else 0 end), 0)`.as(
            "week_seconds"
          ),
        monthJobs: sql<number>`coalesce(sum(${since(monthAgo)}), 0)`.as(
          "month_jobs"
        ),
        monthSeconds:
          sql<number>`coalesce(sum(case when ${since(monthAgo)} then ${took} else 0 end), 0)`.as(
            "month_seconds"
          ),
      })
      .from(job)
      .innerJoin(machine, eq(machine.id, job.machineId))
      .innerJoin(user, eq(user.id, machine.userId))
      .where(
        and(eq(job.organizationId, organizationId), isNotNull(job.completedAt))
      )
      .groupBy(machine.id, job.label),
    db
      .select({
        userId: machine.userId,
        name: user.name,
        image: user.image,
        machines: sql<number>`count(*)`.as("machines"),
      })
      .from(machinePool)
      .innerJoin(machine, eq(machine.id, machinePool.machineId))
      .innerJoin(user, eq(user.id, machine.userId))
      .where(
        and(
          eq(machinePool.organizationId, organizationId),
          eq(machinePool.status, "active"),
          eq(machine.status, "active")
        )
      )
      .groupBy(machine.userId),
  ])

  const people = new Map<string, BoardPerson>()
  const machines = new Map<string, BoardMachine>()
  // Someone whose Mac has joined but not yet run a job is on the board too.
  for (const row of serving)
    people.set(row.userId, {
      userId: row.userId,
      name: row.name,
      image: row.image,
      machines: Number(row.machines),
      scores: empty(),
    })
  for (const row of rows) {
    const person = people.get(row.userId) ?? {
      userId: row.userId,
      name: row.name,
      image: row.image,
      machines: 0,
      scores: empty(),
    }
    people.set(row.userId, person)
    const mac = machines.get(row.machineId) ?? {
      id: row.machineId,
      name: row.machine,
      owner: row.name,
      cores: row.cores,
      speedScore: row.speedScore,
      scores: empty(),
    }
    machines.set(row.machineId, mac)
    // Compared at the label's own size and architecture. One GitHub does not
    // list is left out rather than guessed at.
    const rate = competitorUsdPerMinuteForLabel("github", row.label) ?? 0
    const windows: Array<[BoardPeriod, number, number]> = [
      ["week", Number(row.weekJobs), Number(row.weekSeconds)],
      ["month", Number(row.monthJobs), Number(row.monthSeconds)],
      ["all", Number(row.jobs), Number(row.seconds)],
    ]
    for (const [period, jobs, seconds] of windows)
      for (const scores of [person.scores, mac.scores]) {
        scores[period].jobs += jobs
        scores[period].seconds += seconds
        scores[period].githubUsd += (seconds / 60) * rate
      }
  }
  return { people: [...people.values()], machines: [...machines.values()] }
}
