import { env } from "cloudflare:workers"
import { safeReturnPath } from "@workspace/stripe"
import { redirect } from "next/navigation"

import { AuthForm } from "@workspace/auth/react/auth-form"
import { GitHubSignInButton } from "@workspace/auth/react/github-button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@workspace/ui/components/card"

import { getServerSession } from "@/lib/session"
import product from "@/config/product.json"

export const metadata = { title: "Sign in" }

export default async function SignInPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>
}) {
  const params = await searchParams
  const next = safeReturnPath(params.next, "/dashboard")
  if (!params.client_id && (await getServerSession())) redirect(next)

  // Memberships are synced on the way in, while the GitHub token is fresh.
  const afterSignIn = `/github/sync?next=${encodeURIComponent(next)}`
  const local = /^http:\/\/(localhost|127\.0\.0\.1)(:\d+)?$/.test(
    env.APP_URL ?? ""
  )
  // Someone following a teammate's invite is not here to set anything up.
  const invited = /^\/join\/([A-Za-z0-9-]{1,39})$/.exec(next)?.[1]
  const github = Boolean(env.GITHUB_CLIENT_ID && env.GITHUB_CLIENT_SECRET)

  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-6 p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>
            {invited
              ? `Add your Mac to ${invited}`
              : `Sign in to ${product.name}`}
          </CardTitle>
          <CardDescription>
            {invited
              ? `You were asked to lend ${invited}'s CI your spare cores. Sign in with GitHub so we know you are in ${invited}; there is no account to create and no card.`
              : "Your GitHub organizations decide which hives you can join. There is nothing else to set up."}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {github ? (
            <GitHubSignInButton redirectTo={afterSignIn} />
          ) : (
            <p className="text-sm text-muted-foreground">
              GitHub sign-in is not configured for this deployment.
            </p>
          )}
          {params.error && (
            <p role="alert" className="text-sm text-destructive">
              GitHub did not complete the sign-in. Try again.
            </p>
          )}
        </CardContent>
      </Card>
      {local && (
        <div className="flex w-full max-w-sm flex-col gap-2">
          <p className="text-center text-sm text-muted-foreground">
            Local development only: seeded accounts sign in with a password.
          </p>
          <AuthForm mode="sign-in" redirectTo={afterSignIn} />
        </div>
      )}
    </main>
  )
}
