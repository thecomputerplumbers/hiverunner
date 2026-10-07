"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter } from "next/navigation"

import { useActiveOrganization } from "@workspace/auth/client"
import { SignOutButton } from "@workspace/auth/react/sign-out-button"
import { ThemeToggle } from "@workspace/ui-shell/components/theme-toggle"
import { cn } from "@workspace/ui/lib/utils"

import product from "@/config/product.json"
import { HiveMark } from "@/components/marketing/hive-mark"
import { formatMoney, formatUsd, formatUtc } from "./format"
import { useLive, useNow } from "./live"

const nav = (
  product.navigation as Array<{
    href: string
    label: string
    feature?: keyof typeof product.features
  }>
).filter(
  (item) =>
    // Switching pools sits beside the pool's name, not among its pages.
    item.href !== "/pools" && (!item.feature || product.features[item.feature])
)

/** Cores a Mac keeps for the person using it, as the coordinator does. */
const OWNER_CORES = 2

const typing = (target: EventTarget | null) =>
  target instanceof HTMLElement &&
  (target.isContentEditable ||
    ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName))

/**
 * The signed-in frame: a bar of numbered tabs across the top, the page, and
 * a status line pinned to the bottom that says what the pool is doing right
 * now, whichever page is open.
 */
export function ConsoleShell({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  const pathname = usePathname()
  const router = useRouter()
  const { data: organization } = useActiveOrganization()

  React.useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.metaKey || event.ctrlKey || event.altKey) return
      if (typing(event.target)) return
      const item = nav[Number(event.key) - 1]
      if (item) router.push(item.href)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [router])

  return (
    <div className="flex min-h-svh flex-col">
      <header className="sticky top-0 z-30 border-b border-border bg-background/90 backdrop-blur-sm">
        <div className="mx-auto grid w-full max-w-7xl grid-cols-[1fr_auto] items-center gap-x-4 px-4 sm:px-6 md:grid-cols-[auto_1fr_auto]">
          <div className="flex h-12 min-w-0 items-center gap-2 font-mono text-sm">
            <Link
              href="/dashboard"
              className="flex min-w-0 items-center gap-2 font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <HiveMark className="size-5 shrink-0" />
              <span className="truncate">
                {organization?.name ?? product.name}
              </span>
            </Link>
            <Link
              href="/pools"
              className="shrink-0 border border-border px-1.5 py-0.5 text-[0.65rem] text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
            >
              switch
            </Link>
          </div>
          <nav
            aria-label="Console"
            className="order-last col-span-full -mx-4 flex h-10 [scrollbar-width:none] items-stretch overflow-x-auto border-t border-border px-1 sm:-mx-6 sm:px-4 md:order-none md:col-span-1 md:mx-0 md:h-12 md:border-t-0 md:px-0 [&::-webkit-scrollbar]:hidden"
          >
            {nav.map((item, index) => {
              const active =
                pathname === item.href || pathname.startsWith(`${item.href}/`)
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  aria-current={active ? "page" : undefined}
                  className={cn(
                    "-mb-px flex flex-1 shrink-0 items-center justify-center gap-1.5 border-b-2 px-1.5 font-mono text-xs whitespace-nowrap outline-none focus-visible:bg-muted md:flex-none md:px-3",
                    active
                      ? "border-primary text-foreground"
                      : "border-transparent text-muted-foreground hover:text-foreground"
                  )}
                >
                  <kbd
                    aria-hidden="true"
                    className={cn(
                      "hidden text-[0.65rem] lg:inline",
                      active ? "text-primary" : "text-muted-foreground/60"
                    )}
                  >
                    {index + 1}
                  </kbd>
                  {item.label}
                </Link>
              )
            })}
          </nav>
          <div className="flex shrink-0 items-center gap-1">
            <ThemeToggle />
            <SignOutButton
              size="icon-sm"
              aria-label="Sign out"
              className="[&>svg]:m-0"
            >
              <span className="sr-only">Sign out</span>
            </SignOutButton>
          </div>
        </div>
      </header>

      <main className="mx-auto w-full max-w-7xl flex-1 px-4 pt-8 pb-20 sm:px-6">
        <h1 className="mb-6 flex items-baseline gap-2 font-mono text-lg font-medium tracking-tight">
          <span aria-hidden="true" className="text-primary">
            ~/
          </span>
          {title}
        </h1>
        <div className="flex flex-col gap-8">{children}</div>
      </main>

      <StatusLine pool={organization?.name} />
    </div>
  )
}

function StatusLine({ pool }: { pool?: string }) {
  const { snapshot, connected } = useLive()
  const now = useNow()
  if (!snapshot) return null

  const awake = snapshot.machines.filter(
    (machine) => machine.presence === "online" || machine.presence === "busy"
  )
  const capacity = awake.reduce(
    (sum, machine) => sum + Math.max(0, machine.cores - OWNER_CORES),
    0
  )
  const inUse = snapshot.running.reduce((sum, job) => sum + job.vcpus, 0)
  const cells: Array<[string, string, string?]> = [
    ["macs", `${awake.length}/${snapshot.machines.length}`, "/machines"],
    ["running", String(snapshot.running.length), "/jobs"],
    ["vcpu", `${inUse}/${capacity}`],
    ["balance", formatMoney(snapshot.balance), "/wallet"],
    [
      "saved",
      formatUsd(
        Math.max(
          0,
          snapshot.lifetime.githubUsd - snapshot.lifetime.price / 1_000_000
        )
      ),
      "/dashboard",
    ],
  ]
  return (
    <footer className="fixed inset-x-0 bottom-0 z-30 flex h-7 items-stretch border-t border-border bg-background font-mono text-[0.7rem] leading-none">
      <p
        className={cn(
          "flex shrink-0 items-center gap-1.5 px-3 font-medium",
          connected
            ? "bg-primary text-primary-foreground"
            : "bg-destructive text-background"
        )}
      >
        <span
          aria-hidden="true"
          className={cn(
            "size-1.5 rounded-full bg-current",
            connected && "motion-safe:animate-pulse"
          )}
        />
        {connected ? "live" : "reconnecting"}
      </p>
      {pool ? (
        <p className="hidden items-center border-r border-border px-3 sm:flex">
          {pool}
        </p>
      ) : null}
      <dl className="flex min-w-0 flex-1 items-stretch overflow-hidden">
        {cells.map(([label, value, href]) => {
          const cell = (
            <>
              <dt className="text-muted-foreground">{label}</dt>
              <dd className="tabular-nums">{value}</dd>
            </>
          )
          return (
            <div
              key={label}
              className="flex shrink-0 items-stretch border-r border-border"
            >
              {href ? (
                <Link
                  href={href}
                  className="flex items-center gap-1.5 px-3 hover:bg-muted"
                >
                  {cell}
                </Link>
              ) : (
                <div className="flex items-center gap-1.5 px-3">{cell}</div>
              )}
            </div>
          )
        })}
      </dl>
      <p className="hidden shrink-0 items-center gap-1.5 border-l border-border px-3 text-muted-foreground md:flex">
        <span className="hidden lg:inline">press 1–{nav.length} to move</span>
        <span className="text-foreground tabular-nums">
          {formatUtc(now, "time")} UTC
        </span>
      </p>
    </footer>
  )
}
