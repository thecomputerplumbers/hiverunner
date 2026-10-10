import Link from "next/link"
import { Code } from "../code"

export function Pools() {
  return (
    <>
      <p>
        A hive is the set of machines that can run an organization's jobs.
        Installing the GitHub App creates one hive for the organization.
      </p>

      <h2 id="who-can-join">Who can add machines</h2>
      <p>
        Any member of the GitHub organization that installed the App can add a
        machine by installing the daemon and signing in with GitHub. There are
        no invite codes. Organization admins can narrow this to specific GitHub
        teams.
      </p>
      <p>
        GitHub is the source of truth for membership. When a person is removed
        from the organization, their machines are revoked and stop receiving
        jobs.
      </p>

      <h2 id="no-free-machine">When no machine is free</h2>
      <p>
        A job that asks for a <Code>hive</Code> label directly stays queued at
        GitHub until a machine of the right size is free. HiveWorker does not
        move a queued job to another runner.
      </p>
      <p>
        To avoid the wait, check for a free machine before the job is queued and
        use the runner you have now when there is none.{" "}
        <Link href="/docs/routing">Routing and fallback</Link> shows the
        workflow, and it is the setup we recommend.
      </p>

      <h2 id="empty-wallet">When the wallet is empty</h2>
      <p>
        HiveWorker stops taking new jobs for the organization. They wait in
        GitHub's queue and the dashboard says why. Jobs already running are not
        interrupted.
      </p>

      <h2 id="public-hive">The public hive</h2>
      <p>
        Your own hive is the product today. A public hive, where jobs can
        overflow to machines outside your organization, comes later. It will be
        opt-in per repository and limited to jobs that use no secrets. See{" "}
        <Link href="/security">Security</Link> for the trust model.
      </p>
    </>
  )
}
