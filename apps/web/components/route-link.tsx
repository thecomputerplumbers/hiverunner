import type { ComponentProps } from "react"

/**
 * A link to a route handler rather than a page: `/install` and
 * `/github/sync` answer with a redirect, often to GitHub, which a client-side
 * navigation cannot follow. This is a plain anchor on purpose. Either may
 * carry a `?next=` saying where to come back to.
 */
export function RouteLink({
  to,
  ...props
}: Omit<ComponentProps<"a">, "href"> & {
  to:
    | "/install"
    | "/github/sync"
    | `/install?next=${string}`
    | `/github/sync?next=${string}`
}) {
  return <a href={to} {...props} />
}
