import Link from "next/link"
import product from "@/config/product.json"
import { Code } from "../code"
import { DAEMON_RELEASED } from "../site"

export function Faq() {
  return (
    <dl>
      <dt>Is HiveWorker available now?</dt>
      <dd>
        {DAEMON_RELEASED
          ? "Yes. An organization owner installs the GitHub App, and the daemon is released for Apple Silicon Macs and Linux machines with KVM."
          : "Not generally. It is in early access and the daemon has not been released. These docs describe how it is designed to work."}
      </dd>

      <dt>Can HiveWorker read my code?</dt>
      <dd>
        No. The GitHub App has no permission for code, contents or secrets. A
        job checks out code inside its VM, straight from GitHub. The full
        permission list is on the <Link href="/security">security page</Link>.
      </dd>

      <dt>Which jobs can run on it?</dt>
      <dd>
        Linux jobs, arm64 and x64. x64 jobs have{" "}
        <Link href="/docs/labels-and-sizes#x64">labels of their own</Link> and
        need a Linux x64 machine in your hive. macOS jobs run on a member&apos;s
        Mac itself, with its Xcode, once its owner and your hive both opt in;
        see the labels page. Windows and GPU jobs are not supported.
      </dd>

      <dt>Which machines can join a hive?</dt>
      <dd>
        Apple Silicon Macs and Linux machines with KVM, arm64 or x64, that
        belong to members of your organization. A cloud VM usually has no KVM; a
        desk machine, a home server or bare metal does.
      </dd>

      <dt>Do I have to move every workflow?</dt>
      <dd>
        No. Only jobs whose <Code>runs-on</Code> is a <Code>hive</Code> label
        run on your hive. Change the label back and the job returns to its
        previous runner.
      </dd>

      <dt>What happens if a laptop closes in the middle of a job?</dt>
      <dd>
        The job is stopped. It shows as failed in GitHub and can be re-run. If
        the developer only starts using the machine, the job keeps running:
        every job leaves two cores for the Mac's owner. <Code>hived pause</Code>{" "}
        stops new jobs, and a job already running finishes.
      </dd>

      <dt>What happens when no machine is free?</dt>
      <dd>
        A job sent straight to a <Code>hive</Code> label waits in GitHub's queue
        until one is. The{" "}
        <Link href="/docs/quickstart#route-a-workflow">
          recommended workflow
        </Link>{" "}
        checks first and falls back to a GitHub-hosted runner.
      </dd>

      <dt>Do I need a credit card to start?</dt>
      <dd>
        No. A new organization starts with a trial grant. Putting a card on file
        doubles it.
      </dd>

      <dt>What is the HiveWorker token?</dt>
      <dd>
        It is how jobs are paid for. Each job has a price, and prices are shown
        with a US dollar estimate. See <Link href="/docs/pricing">Pricing</Link>
        .
      </dd>

      <dt>Can I use it on a personal account?</dt>
      <dd>Not in the first release. HiveWorker installs on organizations.</dd>

      <dt>How do I get help or report a problem?</dt>
      <dd>
        Text{" "}
        <a href={`sms:${product.supportSms}`}>{product.supportSmsDisplay}</a>.
      </dd>
    </dl>
  )
}
