import { Container } from "@workspace/ui-shell/components/page-header"
import { Code } from "@/components/marketing/code"
import { IsolationDiagram } from "@/components/marketing/isolation-diagram"
import { Band, PageIntro, textLink } from "@/components/marketing/page-parts"
import product from "@/config/product.json"
import { APP_EVENTS, APP_PERMISSIONS } from "@/components/marketing/site"

export const metadata = {
  title: "Security",
  description:
    "How HiveWorker isolates jobs, what its GitHub App can access, who can add machines to a hive, and what the coordinator does not see.",
}

export default function SecurityPage() {
  return (
    <>
      <PageIntro title="Your code runs on your machines and goes nowhere else.">
        A job runs in a throwaway VM on a Mac that belongs to someone in your
        organization, and talks to GitHub directly. HiveWorker decides which
        machine takes the job. It does not handle the job's code, logs or
        secrets.
      </PageIntro>
      <Container className="pb-14 sm:pb-20">
        <IsolationDiagram />
      </Container>

      <Band title="One VM per job, destroyed afterwards">
        <Facts>
          <Fact title="Hardware virtualization">
            Each job runs in its own hardware-virtualized Linux VM. A job cannot
            read the host's files, keychain or other processes, and it cannot
            see another job.
          </Fact>
          <Fact title="Nothing carries over">
            The VM is created for one job and destroyed when the job ends. The
            next job starts from a clean image.
          </Fact>
          <Fact title="GitHub's own runner">
            Inside the VM is GitHub's official runner, registered with a
            one-time configuration that is good for a single job.
          </Fact>
          <Fact title="The one exception: macOS jobs">
            A <code>hive-macos</code> job runs on a Mac itself, as a separate
            local user, because Xcode cannot run in a Linux VM. It cannot read
            the owner's files or keychain, but it can use what is installed on
            that Mac and its network. It runs only on Macs whose owners opted
            in, and only in hives whose owners turned it on.
          </Fact>
        </Facts>
      </Band>

      <Band title="What the GitHub App can access">
        <p className="leading-7">
          The App has no permission for code, repository contents or secrets. It
          asks for these five permissions and no others.
        </p>
        <div className="overflow-x-auto">
          <table className="w-full min-w-xl text-left text-sm">
            <thead className="font-mono text-xs text-muted-foreground">
              <tr>
                <th className="py-2 pr-4 font-medium">Permission</th>
                <th className="py-2 pr-4 font-medium">Access</th>
                <th className="py-2 font-medium">What it is for</th>
              </tr>
            </thead>
            <tbody>
              {APP_PERMISSIONS.map((row) => (
                <tr key={row.permission} className="border-t border-border">
                  <th className="py-3 pr-4 align-top font-medium">
                    {row.permission}
                    <span className="block font-normal text-muted-foreground">
                      {row.scope}
                    </span>
                  </th>
                  <td className="py-3 pr-4 align-top whitespace-nowrap">
                    {row.access}
                  </td>
                  <td className="py-3 align-top text-muted-foreground">
                    {row.why}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="text-sm leading-7 text-muted-foreground">
          It subscribes to these events:{" "}
          {APP_EVENTS.map((event, index) => (
            <span key={event}>
              {index > 0 ? ", " : null}
              <Code>{event}</Code>
            </span>
          ))}
          . Because the App cannot read contents, a job's checkout happens
          inside the VM with the token GitHub gives that job.
        </p>
      </Band>

      <Band title="Who can run your jobs">
        <Facts>
          <Fact title="Your organization's machines only">
            A hive contains machines added by members of the GitHub organization
            that installed the App. Nobody outside it can join, and admins can
            narrow the hive to specific GitHub teams.
          </Fact>
          <Fact title="GitHub decides membership">
            There are no invite codes and no separate user list. When someone is
            removed from the organization on GitHub, their machines are revoked.
          </Fact>
          <Fact title="Private repositories by default">
            The runner labels are created in your organization's default runner
            group, which GitHub does not offer to public repositories unless you
            change that.
          </Fact>
          <Fact title="The public hive comes later">
            Running jobs on machines outside your organization is not part of
            the first release. When it arrives it is opt-in per repository and
            limited to jobs that use no secrets.
          </Fact>
        </Facts>
      </Band>

      <Band title="The developer stays in control of their Mac">
        <Facts>
          <Fact title="It gives way">
            Jobs keep running on battery. Closing the lid pauses new jobs and
            discards the idle VM. Each job leaves two CPU cores for the owner.
          </Fact>
          <Fact title="Pause controls are planned">
            A menu bar item with a pause control is planned. The current daemon
            runs from the terminal.
          </Fact>
          <Fact title="Never root">
            The daemon runs as a per-user agent with the developer's own
            privileges. It does not run as root.
          </Fact>
          <Fact title="No inbound ports">
            The daemon opens one outbound connection to the coordinator and
            listens on nothing.
          </Fact>
        </Facts>
      </Band>

      <Band title="What the coordinator does not see">
        <p className="leading-7">
          The coordinator is the HiveWorker service that matches queued jobs to
          free machines. It is not in a job's data path. The runner inside the
          VM talks to GitHub directly for the job, its logs and its artifacts.
        </p>
        <Facts>
          <Fact title="It does not have">
            Your source code, build output, job logs, artifacts, caches or
            secrets. It stores no logs and proxies no job traffic.
          </Fact>
          <Fact title="It does have">
            Your organization and its members as GitHub reports them, each
            machine's hardware details and status, and for each job the
            repository name, run and job IDs, size, timings and price.
          </Fact>
        </Facts>
        <p className="text-sm leading-7 text-muted-foreground">
          HiveWorker has not had an external security audit. To report a
          problem, write to{" "}
          <a href={`mailto:${product.supportEmail}`} className={textLink}>
            {product.supportEmail}
          </a>
          .
        </p>
      </Band>
    </>
  )
}

function Facts({ children }: { children: React.ReactNode }) {
  return <dl className="grid gap-x-10 gap-y-6 sm:grid-cols-2">{children}</dl>
}

function Fact({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <div className="border-t border-border pt-4">
      <dt className="font-mono text-sm font-medium">{title}</dt>
      <dd className="mt-1.5 leading-7 text-muted-foreground">{children}</dd>
    </div>
  )
}
