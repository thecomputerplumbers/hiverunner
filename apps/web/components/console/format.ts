import { formatMoney } from "@/lib/format"

export { formatMoney }

const two = (value: number) => String(value).padStart(2, "0")

/** `4:07`, `1:02:09`. For a clock that ticks, so never rounded to words. */
export function formatElapsed(milliseconds: number) {
  const total = Math.max(0, Math.floor(milliseconds / 1000))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  return hours
    ? `${hours}:${two(minutes)}:${two(total % 60)}`
    : `${minutes}:${two(total % 60)}`
}

/** `42s`, `3m 05s`, `1h 12m`. For a job that has finished. */
export function formatDuration(milliseconds: number) {
  const total = Math.max(0, Math.round(milliseconds / 1000))
  if (total < 60) return `${total}s`
  if (total < 3600) return `${Math.floor(total / 60)}m ${two(total % 60)}s`
  return `${Math.floor(total / 3600)}h ${two(Math.floor((total % 3600) / 60))}m`
}

export function formatAgo(from: number, now: number) {
  const seconds = Math.max(0, Math.floor((now - from) / 1000))
  if (seconds < 45) return "just now"
  if (seconds < 3600) return `${Math.max(1, Math.round(seconds / 60))}m ago`
  if (seconds < 86_400) return `${Math.round(seconds / 3600)}h ago`
  return `${Math.round(seconds / 86_400)}d ago`
}

/**
 * Times are drawn in UTC and say so. The server and the browser then render
 * the same text, and it matches the timestamps in GitHub's logs.
 */
export function formatUtc(at: number, parts: "time" | "day" | "full" = "full") {
  const date = new Date(at)
  const day = `${MONTHS[date.getUTCMonth()]} ${date.getUTCDate()}`
  const time = `${two(date.getUTCHours())}:${two(date.getUTCMinutes())}`
  if (parts === "day") return day
  if (parts === "time") return `${time}:${two(date.getUTCSeconds())}`
  return `${day} ${time}`
}

const MONTHS = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
]

export const WEEKDAYS = ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"]

export function formatMinutes(seconds: number) {
  const minutes = seconds / 60
  if (minutes >= 10_000) return `${(minutes / 1000).toFixed(1)}k`
  return Math.round(minutes).toLocaleString("en-US")
}

export const formatUsd = (usd: number) =>
  usd.toLocaleString("en-US", {
    style: "currency",
    currency: "USD",
    maximumFractionDigits: usd >= 100 ? 0 : 2,
  })

export const gib = (bytes: number) => `${Math.round(bytes / 2 ** 30)} GB`
