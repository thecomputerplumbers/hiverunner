import { Button } from "@workspace/ui/components/button"

import { Snippet } from "@/components/console/panel"
import { HexMark } from "@/components/hex-mark"
import { INSTALL_COMMAND } from "@/components/marketing/site"
import { RouteLink } from "@/components/route-link"

/**
 * The way out for someone signed in but in no hive. Shown wherever they
 * land: the dashboard, or the daemon's approval page. A hive is a GitHub
 * organization with the App installed, and the person has usually done
 * only part of that, so it is spelled out step by step.
 */
export function NoHiveSteps({
  syncTo = "/dashboard",
}: {
  /** Where "check again" returns to after re-reading GitHub. */
  syncTo?: string
}) {
  return (
    <div className="flex flex-col gap-5">
      <p className="text-muted-foreground">
        A hive belongs to a GitHub organization with HiveWorker installed on it.
        Your GitHub account is not in one yet. Three steps:
      </p>
      <ol className="flex flex-col gap-5 text-muted-foreground">
        <Step n={1}>
          <strong className="font-medium text-foreground">
            Have a GitHub organization.
          </strong>{" "}
          Any one you belong to works. If you have none,{" "}
          <a
            className="underline"
            href="https://github.com/organizations/plan"
            rel="noreferrer"
            target="_blank"
          >
            create a free one on GitHub
          </a>
          . A personal account cannot be a hive.
        </Step>
        <Step n={2}>
          <strong className="font-medium text-foreground">
            Install HiveWorker on that organization.
          </strong>{" "}
          On GitHub&apos;s install screen, choose the organization, not your
          personal account. If you are not an owner of it, GitHub sends the
          owners a request instead, and HiveWorker is ready once one of them
          approves it.
        </Step>
        <Step n={3}>
          <strong className="font-medium text-foreground">
            Add your machine.
          </strong>{" "}
          Run the install command again, and approve the new code it shows.
          <Snippet className="mt-3" prompt>
            {INSTALL_COMMAND}
          </Snippet>
        </Step>
      </ol>
      <div className="flex flex-wrap gap-3">
        <Button render={<RouteLink to="/install" />}>Install on GitHub</Button>
        <Button
          variant="outline"
          render={
            <a href={`/github/sync?next=${encodeURIComponent(syncTo)}`} />
          }
        >
          Check my organizations again
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">
        Joining a teammate&apos;s hive instead? Ask them for the invite link on
        their dashboard. That needs no installing on your side.
      </p>
    </div>
  )
}

/** The first step is the one to do now; nothing here knows how far they got. */
function Step({ n, children }: { n: number; children: React.ReactNode }) {
  return (
    <li className="flex gap-4">
      <HexMark n={n} state={n === 1 ? "next" : "todo"} />
      <div className="min-w-0 flex-1 pt-1">{children}</div>
    </li>
  )
}
