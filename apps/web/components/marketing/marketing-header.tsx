import Link from "next/link"
import { Button } from "@workspace/ui/components/button"
import { Brand, BrandWordmark } from "@workspace/ui-shell/components/brand"
import {
  SiteHeader,
  SiteHeaderActions,
  SiteHeaderNav,
} from "@workspace/ui-shell/components/site-header"
import { ThemeToggle } from "@workspace/ui-shell/components/theme-toggle"
import product from "@/config/product.json"
import { HiveMark } from "./hive-mark"
import { NavLink } from "./nav-link"
import { MARKETING_NAV } from "./site"
import { RouteLink } from "@/components/route-link"

/** Square, mono, and underlined in honey on the section being read. */
const navLink =
  "rounded-none font-mono text-xs decoration-primary decoration-2 underline-offset-8 hover:bg-transparent data-active:bg-transparent data-active:underline"

export function MarketingHeader() {
  return (
    <div className="sticky top-0 z-30 border-b border-border bg-background">
      <SiteHeader variant="plain" className="px-1 pt-0 sm:px-2 sm:pt-0">
        <Brand render={<Link href="/" />} className="text-base">
          <HiveMark className="size-6" />
          <BrandWordmark className="font-medium">{product.name}</BrandWordmark>
        </Brand>
        <SiteHeaderNav aria-label="Main" className="hidden md:flex">
          {MARKETING_NAV.map((item) => (
            <NavLink key={item.href} href={item.href} className={navLink}>
              {item.label}
            </NavLink>
          ))}
        </SiteHeaderNav>
        <SiteHeaderActions>
          <ThemeToggle />
          <Button
            nativeButton={false}
            variant="ghost"
            className="hidden font-mono text-xs sm:inline-flex"
            render={<Link href="/sign-in" />}
          >
            Sign in
          </Button>
          <Button
            nativeButton={false}
            className="font-mono text-xs"
            render={<RouteLink to="/install" />}
          >
            Install
          </Button>
        </SiteHeaderActions>
      </SiteHeader>
      {/* Below md the sections move to their own row instead of a menu. */}
      <nav
        aria-label="Main"
        className="flex gap-1 overflow-x-auto border-t border-border px-3 py-1.5 md:hidden"
      >
        {MARKETING_NAV.map((item) => (
          <NavLink key={item.href} href={item.href}>
            {item.label}
          </NavLink>
        ))}
        <NavLink href="/sign-in" className="sm:hidden">
          Sign in
        </NavLink>
      </nav>
    </div>
  )
}
