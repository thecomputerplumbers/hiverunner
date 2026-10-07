import { getLiveSnapshot, listLiveJobs } from "@workspace/db/live"

import { AccountPage } from "@/components/account-page"
import { getDb } from "@/db"
import { requirePool } from "@/lib/session"
import { JobList } from "./job-list"

export const metadata = { title: "Jobs" }

export default async function JobsPage() {
  const { organizationId, installation } = await requirePool()
  const db = getDb()
  const [live, jobs] = await Promise.all([
    getLiveSnapshot(db, organizationId),
    listLiveJobs(db, organizationId),
  ])
  return (
    <AccountPage title="jobs" live={live}>
      <p className="max-w-3xl text-sm text-muted-foreground">
        Jobs that ran in the {installation.accountLogin} pool. Logs stay on
        GitHub; each job here has a signed receipt.
      </p>
      <JobList jobs={jobs} />
    </AccountPage>
  )
}
