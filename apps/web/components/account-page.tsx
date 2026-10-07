import type { ReactNode } from "react"
import type { LiveSnapshot } from "@workspace/db/live"

import { LiveProvider } from "@/components/console/live"
import { ConsoleShell } from "@/components/console/shell"

/**
 * Every signed-in page. Pass `live` when the page has already read the
 * snapshot, so the first paint is complete; without it the status line fills
 * in a moment after load.
 */
export function AccountPage({
  title,
  live,
  children,
}: {
  title: string
  live?: LiveSnapshot | null
  children: ReactNode
}) {
  return (
    <LiveProvider initial={live}>
      <ConsoleShell title={title}>{children}</ConsoleShell>
    </LiveProvider>
  )
}
