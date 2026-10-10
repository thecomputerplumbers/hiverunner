import Link from "next/link"
import { Container } from "@workspace/ui-shell/components/page-header"
import { HexMark } from "@/components/hex-mark"
import { Code, CodeBlock } from "@/components/marketing/code"
import { Band, PageIntro, textLink } from "@/components/marketing/page-parts"
import {
  DAEMON_MIN_MACOS,
  DAEMON_RELEASED,
  DAEMON_SIGNER,
  INSTALL_COMMAND,
} from "@/components/marketing/site"

export const metadata = {
  title: "Download",
  description:
    "Install the HiveWorker daemon on an Apple Silicon Mac or a Linux machine: one command, what it installs, and how to update or remove it.",
}

export default function DownloadPage() {
  return (
    <>
      <PageIntro title="Add your Mac to the hive">
        One command installs the daemon, signs this Mac in with GitHub and
        starts it. From then on the Mac runs your organization&apos;s jobs in
        throwaway VMs whenever it is awake.
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
          Paste it into Terminal. A browser window opens for you to sign in with
          GitHub and approve this Mac; being a member of your GitHub
          organization is what lets it join.{" "}
          <Link href="/docs/daemon" className={textLink}>
            What the daemon does
          </Link>
        </p>
      </Container>

      <Band title="What the command does">
        <ol className="flex flex-col gap-4 leading-7 [&>li]:flex [&>li]:gap-4">
          <li>
            <HexMark n={1} size="sm" className="mt-0.5" />
            <span>
              Downloads the daemon and the Linux image jobs run in, about 300
              MB, and checks both against their published checksums.
            </span>
          </li>
          <li>
            <HexMark n={2} size="sm" className="mt-0.5" />
            <span>
              Installs them under your home directory. Nothing is installed
              system-wide and it never asks for your password.
            </span>
          </li>
          <li>
            <HexMark n={3} size="sm" className="mt-0.5" />
            <span>Opens your browser to sign this Mac in with GitHub.</span>
          </li>
          <li>
            <HexMark n={4} size="sm" className="mt-0.5" />
            <span>
              Starts the daemon as a background service for your user, so it
              comes back after a restart.
            </span>
          </li>
        </ol>
        <p className="leading-7 text-muted-foreground">
          The daemon is signed and notarized by Apple under the developer ID{" "}
          {DAEMON_SIGNER}, and the installer refuses a download signed by anyone
          else.
        </p>
      </Band>

      <Band title="What you need">
        <ul className="flex flex-col gap-3 leading-7">
          <li>
            A Mac with Apple Silicon, or a Linux machine with KVM. Intel Macs
            are not supported.
          </li>
          <li>
            macOS {DAEMON_MIN_MACOS} or later, and about 3 GB of free disk.
          </li>
          <li>
            A GitHub account that belongs to an organization with the HiveWorker
            GitHub App installed.
          </li>
          <li>
            An outbound internet connection. The daemon needs no inbound ports
            and no changes to your router or firewall.
          </li>
        </ul>
      </Band>

      <Band title="Once it is running">
        <dl className="grid gap-x-10 gap-y-5 sm:grid-cols-2">
          {[
            ["hived status", "What this Mac lends, and whether it is lending."],
            ["hived pause", "Stop taking jobs. hived resume undoes it."],
            [
              "hived config",
              "How many jobs at once, how large, during which hours, and the size of the local Actions cache.",
            ],
            ["hived cache", "What the local Actions cache holds."],
          ].map(([command, meaning]) => (
            <div key={command} className="border-t border-border pt-4">
              <dt>
                <Code>{command}</Code>
              </dt>
              <dd className="mt-1.5 leading-7 text-muted-foreground">
                {meaning}
              </dd>
            </div>
          ))}
        </dl>
      </Band>

      <Band title="Updating and removing">
        <p className="leading-7">
          To update, run the install command again when the Mac is idle. To
          remove everything it installed, run <Code>hived uninstall</Code>.
        </p>
        <p className="leading-7 text-muted-foreground">
          Removing the daemon leaves the Mac listed in your hive as offline.
          Remove it from Machines in the dashboard.
        </p>
      </Band>

      <Band title="Other platforms">
        <p className="leading-7 text-muted-foreground">
          The same command works on Linux, arm64 or x64, on a machine with KVM.
          It needs QEMU, virtiofsd and passt, and tells you how to install
          whichever is missing; it never asks for your password. Windows hosts
          are not part of the plan for now.
        </p>
      </Band>
    </>
  )
}
