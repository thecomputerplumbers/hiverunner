import { notFound } from "next/navigation"
import { getJob } from "@workspace/db/jobs"

import { AccountPage } from "@/components/account-page"
import { getDb } from "@/db"
import { formatMoney } from "@/lib/format"
import { requirePool } from "@/lib/session"

export const metadata = { title: "Job receipt" }

export default async function JobPage({
  params,
}: {
  params: Promise<{ id: string }>
}) {
  const { organizationId } = await requirePool()
  const { id } = await params
  // Scoped to the caller's pool: another pool's job id is simply not found.
  const job = await getJob(getDb(), organizationId, decodeURIComponent(id))
  if (!job) notFound()

  const rows: Array<[string, string]> = [
    ["Repository", job.repository],
    ["Label", `${job.label} (${job.vcpus} vCPUs)`],
    ["Started", job.startedAt.toLocaleString("en-US")],
    ["Finished", job.completedAt?.toLocaleString("en-US") ?? "Still running"],
    ["Result", job.result ?? "Running"],
    ["Speed score", job.speedScore.toFixed(2)],
  ]
  if (job.amount !== null && job.computeUnits !== null)
    rows.push(
      ["Compute units", job.computeUnits.toFixed(2)],
      ["Charged", formatMoney(job.amount, "fine")],
      ["Paid to the machine's owner", formatMoney(job.earning ?? 0, "fine")],
      ["HiveWorker fee", formatMoney(job.tax ?? 0, "fine")]
    )

  return (
    <AccountPage title={job.jobName || job.repository}>
      <dl className="divide-y rounded-lg border">
        {rows.map(([label, value]) => (
          <div key={label} className="flex justify-between gap-4 p-4">
            <dt className="text-muted-foreground">{label}</dt>
            <dd className="text-right tabular-nums">{value}</dd>
          </div>
        ))}
      </dl>
      {job.receipt && (
        <section className="space-y-3">
          <h2 className="text-lg font-semibold">Signed receipt</h2>
          <p className="text-sm text-muted-foreground">
            These are the exact bytes HiveWorker signed for this job. The
            signature is Ed25519, made with key <code>{job.receiptKeyId}</code>.
          </p>
          <pre className="overflow-x-auto rounded-lg border bg-muted p-4 text-xs">
            <code>{job.receipt}</code>
          </pre>
          <pre className="overflow-x-auto rounded-lg border bg-muted p-4 text-xs">
            <code>{job.receiptSignature}</code>
          </pre>
        </section>
      )}
    </AccountPage>
  )
}
