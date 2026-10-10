import { consentReturnPath, installUrl } from "@/lib/github"
import { seeOther } from "@/lib/redirect"

/**
 * The one link the site uses for "install". With `?next=` naming an agent's
 * consent page, GitHub is asked to carry it through the install, so the
 * person lands back where they were.
 */
export function GET(request: Request) {
  const next = consentReturnPath(new URL(request.url).searchParams.get("next"))
  return seeOther(
    installUrl(next ?? undefined) ?? "/github/status?state=unconfigured"
  )
}
