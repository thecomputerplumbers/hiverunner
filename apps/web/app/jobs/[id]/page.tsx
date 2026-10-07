import Link from "next/link"
import { notFound } from "next/navigation"
import { getJob } from "@workspace/db/jobs"
import { getMachine } from "@workspace/db/machines"
import { microTokensToUsd } from "@workspace/pricing"

import { AccountPage } from "@/components/account-page"
import { formatDuration, formatUtc } from "@/components/console/format"
import { Panel, Snippet } from "@/components/console/panel"
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
  const db = getDb()
  // Scoped to the caller's pool: another pool's job id is simply not found.
  const job = await getJob(db, organizationId, decodeURIComponent(id))
  if (!job) notFound()
  const machine = await getMachine(db, job.machineId)

  const settled = job.amount !== null && job.computeUnits !== null
  const took = job.completedAt
    ? job.completedAt.getTime() - job.startedAt.getTime()
    : null
  const facts: Array<[string, string]> = [
    ["repository", job.repository],
    ["runs-on", `${job.label} (${job.vcpus} vCPUs)`],
    ["mac", machine?.name ?? job.machineId],
    ["started", `${formatUtc(job.startedAt.getTime())} UTC`],
    [
      "finished",
      job.completedAt
        ? `${formatUtc(job.completedAt.getTime())} UTC`
        : "still running",
    ],
    ["took", took === null ? "still running" : formatDuration(took)],
  ]
  const tone =
    job.result === "failed"
      ? "border-destructive text-destructive"
      : job.result
        ? "border-border text-muted-foreground"
        : "border-primary text-primary"
  const earning = job.earning ?? 0
  const amount = job.amount ?? 0
  const share = amount > 0 ? (earning / amount) * 100 : 0
  const backToPool = job.payeeType === "organization"

  return (
    <AccountPage title={`jobs/${job.jobName || job.repository}`}>
      <div className="flex flex-wrap items-center gap-3 font-mono text-xs">
        <Link
          href="/jobs"
          className="text-muted-foreground hover:text-foreground"
        >
          ← all jobs
        </Link>
        <span className={`border px-2 py-0.5 ${tone}`}>
          {job.result ?? "running"}
        </span>
        <span className="text-muted-foreground">{job.id}</span>
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title="run" bodyClassName="p-0">
          <dl className="divide-y divide-border font-mono text-xs">
            {facts.map(([label, value]) => (
              <div
                key={label}
                className="flex justify-between gap-4 px-4 py-2.5"
              >
                <dt className="text-muted-foreground">{label}</dt>
                <dd className="text-right tabular-nums">{value}</dd>
              </div>
            ))}
          </dl>
        </Panel>
        <Panel title="price" bodyClassName="flex flex-col gap-4">
          {settled ? (
            <>
              <div className="overflow-x-auto">
                <table className="w-full font-mono text-xs whitespace-nowrap [&_td]:py-1 [&_td:last-child]:text-right [&_td:last-child]:tabular-nums">
                  <tbody>
                    <tr>
                      <td className="text-muted-foreground">
                        {((took ?? 0) / 60_000).toFixed(2)} min × {job.vcpus}{" "}
                        vCPU × {job.speedScore.toFixed(2)} speed
                      </td>
                      <td>{job.computeUnits!.toFixed(2)} units</td>
                    </tr>
                    <tr>
                      <td className="text-muted-foreground">
                        × ${microTokensToUsd(job.rate).toFixed(4)} a unit
                      </td>
                      <td>{formatMoney(amount, "fine")} charged</td>
                    </tr>
                    <tr>
                      <td className="text-muted-foreground">
                        {backToPool
                          ? "− earned back by your machine"
                          : "paid to the machine's owner"}
                      </td>
                      <td>{formatMoney(earning, "fine")}</td>
                    </tr>
                    <tr className="border-t border-border text-sm [&>td]:pt-2">
                      <td>
                        {backToPool ? "left your balance" : "HiveWorker fee"}
                      </td>
                      <td className="text-primary">
                        {formatMoney(
                          backToPool ? amount - earning : (job.tax ?? 0),
                          "fine"
                        )}
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <div className="flex flex-col gap-1.5">
                <div
                  className="flex h-2 gap-0.5"
                  role="img"
                  aria-label={`${share.toFixed(0)}% of the charge went to the machine's owner and ${(100 - share).toFixed(0)}% was HiveWorker's fee`}
                >
                  <span
                    className="bg-foreground/40"
                    style={{ width: `${share}%` }}
                  />
                  <span className="flex-1 bg-primary" />
                </div>
                <p className="flex justify-between font-mono text-xs text-muted-foreground">
                  <span>machine&apos;s owner {share.toFixed(0)}%</span>
                  <span>HiveWorker fee {(100 - share).toFixed(0)}%</span>
                </p>
              </div>
            </>
          ) : (
            <p className="text-sm text-muted-foreground">
              Priced when GitHub reports the job finished: minutes × vCPUs × the
              machine&apos;s speed score of {job.speedScore.toFixed(2)}.
            </p>
          )}
        </Panel>
      </div>
      {job.receipt && (
        <Panel title="signed receipt" bodyClassName="flex flex-col gap-3">
          <p className="max-w-3xl text-sm text-muted-foreground">
            These are the exact bytes HiveWorker signed for this job. The
            signature is Ed25519, made with key <code>{job.receiptKeyId}</code>.
          </p>
          <Snippet>{job.receipt}</Snippet>
          <Snippet className="break-all whitespace-pre-wrap">
            {job.receiptSignature ?? ""}
          </Snippet>
        </Panel>
      )}
    </AccountPage>
  )
}
