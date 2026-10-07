import Link from "next/link"
import { Button } from "@workspace/ui/components/button"

import { AuthPage } from "@/components/auth-page"
import { RouteLink } from "@/components/route-link"

export const metadata = { title: "GitHub installation" }

const states: Record<string, { title: string; body: string; retry?: boolean }> =
  {
    requested: {
      title: "Waiting for an owner",
      body: "Your request was sent to your organization's owners on GitHub. HiveWorker is ready as soon as one of them approves it.",
    },
    personal: {
      title: "Organizations only, for now",
      body: "HiveWorker runs on GitHub organizations. Personal accounts are not supported yet. Install it on an organization instead.",
      retry: true,
    },
    denied: {
      title: "We could not confirm that installation",
      body: "GitHub does not list that installation for your account. Sign in as someone who can see it, or install the App again.",
      retry: true,
    },
    "not-member": {
      title: "You are not a member of that organization",
      body: "The App is installed, but GitHub says you are not an active member of the organization, so you cannot join its hive.",
    },
    "github-required": {
      title: "Sign in with GitHub",
      body: "Finishing an installation needs a GitHub sign-in, so that GitHub can confirm the installation is yours.",
    },
    unavailable: {
      title: "GitHub did not answer",
      body: "We could not reach GitHub to confirm the installation. Nothing was lost. Try again in a minute.",
      retry: true,
    },
    unconfigured: {
      title: "Not open yet",
      body: "HiveWorker is in early access and installation is not open on this site yet.",
    },
    invalid: {
      title: "That link is incomplete",
      body: "Start the installation again from GitHub.",
      retry: true,
    },
  }

export default async function Page({
  searchParams,
}: {
  searchParams: Promise<{ state?: string }>
}) {
  const { state } = await searchParams
  const message = states[state ?? ""] ?? states.invalid!
  return (
    <AuthPage title={message.title}>
      <div className="flex flex-col gap-6">
        <p className="text-muted-foreground">{message.body}</p>
        <div className="flex flex-wrap gap-3">
          {message.retry && (
            <Button render={<RouteLink to="/install" />}>
              Install on GitHub
            </Button>
          )}
          <Button variant="outline" render={<Link href="/dashboard" />}>
            Go to the dashboard
          </Button>
        </div>
      </div>
    </AuthPage>
  )
}
