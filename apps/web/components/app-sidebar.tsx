"use client"
import product from "@/config/product.json"

import Link from "next/link"
import { usePathname } from "next/navigation"
import {
  Bot,
  Building2,
  CreditCard,
  LayoutDashboard,
  Settings2,
  Folder,
  Laptop,
  ListChecks,
} from "lucide-react"

import { useActiveOrganization } from "@workspace/auth/client"
import { SignOutButton } from "@workspace/auth/react/sign-out-button"
import {
  Brand,
  BrandMark,
  BrandWordmark,
} from "@workspace/ui-shell/components/brand"
import { NavMenu } from "@workspace/ui-shell/components/nav-menu"
import type { NavItem } from "@workspace/ui-shell/lib/nav"
import {
  AppSidebar as Sidebar,
  AppSidebarContent as SidebarContent,
  AppSidebarFooter as SidebarFooter,
  AppSidebarHeader as SidebarHeader,
} from "@workspace/ui-shell/components/app-shell"
import { ThemeToggle } from "@workspace/ui-shell/components/theme-toggle"

const icons: Record<string, typeof Building2> = {
  dashboard: LayoutDashboard,
  billing: CreditCard,
  account: Settings2,
  organization: Building2,
  projects: Folder,
  machines: Laptop,
  jobs: ListChecks,
  agents: Bot,
}
const nav: NavItem[] = (
  product.navigation as Array<{
    href: string
    label: string
    icon: string
    feature?: keyof typeof product.features
  }>
)
  .filter((item) => !item.feature || product.features[item.feature])
  .map((item) => ({ ...item, icon: icons[item.icon] }))

export function AppSidebar() {
  const pathname = usePathname()
  const { data: organization } = useActiveOrganization()

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader className="px-3 py-3">
        <Brand render={<Link href="/dashboard" />}>
          <BrandMark>
            <Building2 />
          </BrandMark>
          <BrandWordmark className="group-data-[collapsible=icon]:hidden">
            {organization?.name ?? product.name}
          </BrandWordmark>
        </Brand>
      </SidebarHeader>

      <SidebarContent>
        <NavMenu
          items={nav}
          pathname={pathname}
          renderLink={(item) => <Link href={item.href} />}
        />
      </SidebarContent>

      <SidebarFooter className="gap-1 p-2 group-data-[collapsible=icon]:items-center">
        <ThemeToggle />
        <SignOutButton className="justify-start group-data-[collapsible=icon]:justify-center" />
      </SidebarFooter>
    </Sidebar>
  )
}
