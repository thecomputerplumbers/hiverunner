import Link from "next/link"
import { notFound } from "next/navigation"
import { roleAtLeast } from "@workspace/auth"
import { AccountPage } from "@/components/account-page"
import { requireActiveOrganization } from "@/lib/session"
import { getDb } from "@/db"
import { listProjects } from "@/operations/projects"
import { Button } from "@workspace/ui/components/button"
import product from "@/config/product.json"
export default async function Page({
  searchParams,
}: {
  searchParams: Promise<{ after?: string }>
}) {
  if (!product.features.projects) notFound()
  const { session, organizationId, membership } =
      await requireActiveOrganization(),
    { after } = await searchParams
  const { items, nextCursor } = await listProjects(
    { db: getDb(), actor: { userId: session.user.id, organizationId } },
    { after }
  )
  return (
    <AccountPage title="Projects">
      <p className="text-muted-foreground">
        Plan and track work with your organization.
      </p>
      {roleAtLeast(membership.role, "admin") && (
        <div>
          <Button render={<Link href="/projects/new" />}>New project</Button>
        </div>
      )}
      <section className="space-y-3">
        {!items.length && <p>No projects here yet.</p>}
        {items.map((item) => (
          <Link
            className="block border border-border bg-card/50 p-4 outline-none hover:bg-muted focus-visible:ring-3 focus-visible:ring-ring/50 sm:p-6"
            key={item.id}
            href={`/projects/${item.id}`}
          >
            <span>{item.name}</span>
            <span className="ml-3 text-sm text-muted-foreground">
              {item.status}
            </span>
            <p className="mt-2 line-clamp-2 text-sm text-muted-foreground">
              {item.description}
            </p>
          </Link>
        ))}
      </section>
      <nav aria-label="Project pages" className="flex gap-4">
        {after && (
          <Link className="underline" href="/projects">
            First page
          </Link>
        )}
        {nextCursor && (
          <Link className="underline" href={`/projects?after=${nextCursor}`}>
            Next page
          </Link>
        )}
      </nav>
    </AccountPage>
  )
}
