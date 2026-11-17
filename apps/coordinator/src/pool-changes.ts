/**
 * A single cheap D1 read replaces a recurring alarm in every connected pool.
 * Triggers commit the notification with the access change, including deletes.
 * A revision-checked acknowledgement cannot erase a newer change.
 */
export async function dispatchPoolChanges(env: CoordinatorEnv) {
  const { results } = await env.DB.prepare(
    "SELECT organization_id, revision FROM pool_change LIMIT 100"
  ).all<{ organization_id: string; revision: string }>()
  const failures: unknown[] = []
  for (let offset = 0; offset < results.length; offset += 10) {
    const outcomes = await Promise.allSettled(
      results.slice(offset, offset + 10).map(async (change) => {
        await env.POOL.getByName(change.organization_id).reconcile()
        await env.DB.prepare(
          "DELETE FROM pool_change WHERE organization_id = ? AND revision = ?"
        )
          .bind(change.organization_id, change.revision)
          .run()
      })
    )
    for (const outcome of outcomes)
      if (outcome.status === "rejected") failures.push(outcome.reason)
  }
  if (failures.length)
    throw new AggregateError(failures, "Pool access reconciliation failed")
}
