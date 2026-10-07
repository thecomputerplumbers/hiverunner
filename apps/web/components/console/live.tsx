"use client"

import * as React from "react"
import type { LiveSnapshot } from "@workspace/db/live"

type Live = {
  snapshot: LiveSnapshot | null
  /** False once a poll has failed, until the next one succeeds. */
  connected: boolean
}

const LiveContext = React.createContext<Live>({
  snapshot: null,
  connected: true,
})
const NowContext = React.createContext(0)

const BUSY_EVERY = 4_000
const IDLE_EVERY = 9_000

/**
 * Keeps the console current. One poller for the whole page: every panel
 * reads the same snapshot, and a clock that ticks each second from the
 * server's time, so timers and totals move between polls without the
 * browser's clock being trusted for anything.
 */
export function LiveProvider({
  initial = null,
  organizationId,
  children,
}: {
  initial?: LiveSnapshot | null
  /** Read this pool rather than the session's active one. */
  organizationId?: string
  children: React.ReactNode
}) {
  const [live, setLive] = React.useState<Live>({
    snapshot: initial,
    connected: true,
  })
  const [elapsed, setElapsed] = React.useState(0)
  const received = React.useRef(0)

  React.useEffect(() => {
    received.current = performance.now()
    let stopped = false
    let timer = 0
    let busy = Boolean(initial?.running.length)

    async function poll() {
      if (document.visibilityState === "visible") {
        try {
          const response = await fetch(
            organizationId
              ? `/api/pool/live?organization=${encodeURIComponent(organizationId)}`
              : "/api/pool/live",
            { cache: "no-store" }
          )
          // Signed out, or no pool: nothing here will change by asking again.
          if (response.status >= 400 && response.status < 500) return
          if (!response.ok) throw new Error(String(response.status))
          const snapshot = (await response.json()) as LiveSnapshot
          if (stopped) return
          received.current = performance.now()
          busy = snapshot.running.length > 0
          setElapsed(0)
          setLive({ snapshot, connected: true })
        } catch {
          if (stopped) return
          setLive((current) => ({ ...current, connected: false }))
        }
      }
      if (!stopped)
        timer = window.setTimeout(poll, busy ? BUSY_EVERY : IDLE_EVERY)
    }
    timer = window.setTimeout(poll, initial ? BUSY_EVERY : 0)
    // A tab left in the background is not polled; catch up the moment
    // someone looks at it again.
    function onVisible() {
      if (document.visibilityState !== "visible") return
      window.clearTimeout(timer)
      void poll()
    }
    document.addEventListener("visibilitychange", onVisible)
    const tick = window.setInterval(
      () => setElapsed(performance.now() - received.current),
      1000
    )
    return () => {
      stopped = true
      document.removeEventListener("visibilitychange", onVisible)
      window.clearTimeout(timer)
      window.clearInterval(tick)
    }
    // The first snapshot seeds the state; later ones arrive by polling.
    // oxlint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <LiveContext.Provider value={live}>
      <NowContext.Provider value={(live.snapshot?.now ?? 0) + elapsed}>
        {children}
      </NowContext.Provider>
    </LiveContext.Provider>
  )
}

export const useLive = () => React.useContext(LiveContext)

/** The server's time, advancing each second. Zero before the first snapshot. */
export const useNow = () => React.useContext(NowContext)
