import Link from "next/link"
import { listJobs } from "@workspace/db/jobs"

import { AccountPage } from "@/components/account-page"
import { getDb } from "@/db"
import { formatMoney } from "@/lib/format"
import { requirePool } from "@/lib/session"

export const metadata = { title: "Jobs" }

export default async function JobsPage() {
  const { organizationId, installation } = await requirePool()
  const jobs = await listJobs(getDb(), organizationId)
  return (
    <AccountPage title="Jobs">
      <p className="text-muted-foreground">
        Jobs that ran in the {installation.accountLogin} pool. Logs stay on
        GitHub; each job here has a signed receipt.
      </p>
      {!jobs.length && (
        <p>
          No jobs yet. Set <code>runs-on: hive</code> in a workflow to send one.
        </p>
      )}
      <ul className="divide-y rounded-lg border empty:hidden">
        {jobs.map((job) => (
          <li key={job.id}>
            <Link
              className="flex flex-wrap items-center justify-between gap-3 p-4 hover:bg-muted"
              href={`/jobs/${encodeURIComponent(job.id)}`}
            >
              <div>
                <p className="font-medium">
                  {job.repository}
                  {job.jobName ? ` · ${job.jobName}` : ""}
                </p>
                <p className="text-sm text-muted-foreground">
                  {job.label} on {job.machine} ·{" "}
                  {job.startedAt.toLocaleString("en-US")} ·{" "}
                  {job.result ?? "running"}
                </p>
              </div>
              {job.amount !== null && (
                <p className="text-right tabular-nums">
                  {formatMoney(job.amount, "fine")}
                </p>
              )}
            </Link>
          </li>
        ))}
      </ul>
    </AccountPage>
  )
}
