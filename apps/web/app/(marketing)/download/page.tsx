import Link from "next/link"
import { Container } from "@workspace/ui-shell/components/page-header"
import { CodeBlock } from "@/components/marketing/code"
import { Band, PageIntro, textLink } from "@/components/marketing/page-parts"
import { DAEMON_RELEASED, INSTALL_COMMAND } from "@/components/marketing/site"

export const metadata = {
  title: "Download",
  description:
    "The HiveWorker daemon for Apple Silicon Macs: install command, system requirements and release status.",
}

export default function DownloadPage() {
  return (
    <>
      <PageIntro title="The daemon for Apple Silicon Macs">
        The daemon is the part of HiveWorker that runs on a developer's machine.
        It takes jobs from your organization's pool and runs each one in a
        throwaway VM.
      </PageIntro>
      <Container className="flex flex-col gap-5 pb-14 sm:pb-20">
        {DAEMON_RELEASED ? null : (
          <div
            role="note"
            className="border border-primary bg-primary/10 p-5 sm:p-6"
          >
            <h2 className="font-heading text-lg font-medium tracking-tight">
              Not released yet
            </h2>
            <p className="mt-2 max-w-2xl leading-7">
              There is no daemon to download today. The command below is the one
              that will install it, and it does not work until the first release
              is published.
            </p>
          </div>
        )}
        <CodeBlock title="Terminal" wrap className="max-w-3xl">
          {INSTALL_COMMAND}
        </CodeBlock>
        <p className="max-w-2xl leading-7 text-muted-foreground">
          After installing, the daemon opens a browser window to sign in with
          GitHub and add the machine to your organization's pool.{" "}
          <Link href="/docs/daemon" className={textLink}>
            What the daemon does
          </Link>
        </p>
      </Container>

      <Band title="System requirements">
        <ul className="flex flex-col gap-3 leading-7">
          <li>A Mac with Apple Silicon. Intel Macs are not supported.</li>
          <li>
            A GitHub account that belongs to an organization with the HiveWorker
            GitHub App installed.
          </li>
          <li>
            An outbound internet connection. The daemon needs no inbound ports
            and no changes to your router or firewall.
          </li>
        </ul>
        <p className="leading-7 text-muted-foreground">
          The minimum macOS version, and the memory and disk space a VM needs,
          will be listed here with the first release.
        </p>
      </Band>

      <Band title="Other platforms">
        <p className="leading-7 text-muted-foreground">
          Apple Silicon macOS is the only host platform in the first release.
          Linux hosts come later. Windows hosts are not part of the plan for
          now.
        </p>
      </Band>
    </>
  )
}
