import Link from "next/link"
import { Brand, BrandWordmark } from "@workspace/ui-shell/components/brand"
import {
  SiteFooter,
  SiteFooterColumn,
  SiteFooterColumns,
  SiteFooterHeading,
  SiteFooterLink,
  SiteFooterMeta,
} from "@workspace/ui-shell/components/site-footer"
import product from "@/config/product.json"
import { HiveMark } from "./hive-mark"
import { RouteLink } from "@/components/route-link"

const COLUMNS = [
  {
    heading: "Product",
    links: [
      { href: "/pricing", label: "Pricing" },
      { href: "/security", label: "Security" },
      { href: "/download", label: "Download the daemon" },
      { href: "/install", label: "Install the GitHub App" },
    ],
  },
  {
    heading: "Docs",
    links: [
      { href: "/docs/quickstart", label: "Quickstart" },
      { href: "/docs/agents", label: "Agents and MCP" },
      { href: "/docs/routing", label: "Routing and fallback" },
      { href: "/docs/labels-and-sizes", label: "Labels and sizes" },
      { href: "/docs/daemon", label: "The daemon" },
      { href: "/docs/faq", label: "FAQ" },
    ],
  },
  {
    heading: "Legal",
    links: [
      { href: "/terms", label: "Terms" },
      { href: "/privacy", label: "Privacy" },
    ],
  },
]

export function MarketingFooter() {
  return (
    <SiteFooter className="mt-0 px-5 sm:px-8">
      <SiteFooterColumns>
        <SiteFooterColumn className="gap-3">
          <Brand className="text-base">
            <HiveMark className="size-6" />
            <BrandWordmark className="font-medium">
              {product.name}
            </BrandWordmark>
          </Brand>
          <p className="max-w-xs text-sm leading-6 text-muted-foreground">
            GitHub Actions runners on the machines your team already has.
          </p>
        </SiteFooterColumn>
        {COLUMNS.map((column) => (
          <SiteFooterColumn key={column.heading}>
            <SiteFooterHeading className="font-mono tracking-normal text-foreground normal-case">
              {column.heading}
            </SiteFooterHeading>
            {column.links.map((link) => (
              <SiteFooterLink
                key={link.href}
                render={
                  // A route handler that redirects to GitHub, not a page.
                  link.href === "/install" ? (
                    <RouteLink to="/install" />
                  ) : (
                    <Link href={link.href} />
                  )
                }
              >
                {link.label}
              </SiteFooterLink>
            ))}
          </SiteFooterColumn>
        ))}
      </SiteFooterColumns>
      <SiteFooterMeta>
        <span>The Computer Plumbers LLC</span>
        <a className="hover:text-foreground" href={`sms:${product.supportSms}`}>
          Text {product.supportSmsDisplay}
        </a>
      </SiteFooterMeta>
    </SiteFooter>
  )
}
