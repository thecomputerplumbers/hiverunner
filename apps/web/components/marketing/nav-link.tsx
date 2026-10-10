"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { SiteHeaderLink } from "@workspace/ui-shell/components/site-header"
import { MARKETING_NAV } from "./site"

/** A header link that knows whether it points at the section being read. */
export function NavLink({
  href,
  children,
  className,
}: {
  href: string
  children: React.ReactNode
  className?: string
}) {
  const pathname = usePathname()
  const under = (path: string) =>
    pathname === path || pathname.startsWith(`${path}/`)
  // A page under two items, as /docs/agents is, belongs to the closer one.
  const active =
    under(href) &&
    !MARKETING_NAV.some(
      (item) => item.href.length > href.length && under(item.href)
    )
  return (
    <SiteHeaderLink
      active={active}
      aria-current={active ? "page" : undefined}
      className={className}
      render={<Link href={href} />}
    >
      {children}
    </SiteHeaderLink>
  )
}
