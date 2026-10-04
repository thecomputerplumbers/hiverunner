/** Secrets are provisioned separately from public Wrangler configuration. */
interface CoordinatorEnv {
  GITHUB_APP_ID?: string
  GITHUB_APP_PRIVATE_KEY?: string
  RECEIPT_SIGNING_KEY?: string
  /** Local fake only. Production never overrides the GitHub API. */
  GITHUB_API_URL?: string
  /** Explicitly opt into a loopback-only fake API; never set in production. */
  LOCAL_DEVELOPMENT?: string
}
