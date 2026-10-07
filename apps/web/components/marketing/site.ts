/**
 * Facts the marketing pages share. Hosts live here as constants so a move to
 * the product domain is one edit, never a search for string-built URLs.
 */

/** Where the daemon's installer is served from (the releases bucket). */
export const INSTALL_SCRIPT_URL =
  "https://get.hiveworkr.thecomputerplumbers.com/install.sh"

export const INSTALL_COMMAND = `curl -fsSL ${INSTALL_SCRIPT_URL} | sh`

/** The coordinator's public origin, and the OIDC audience a capacity probe asks for. */
export const COORDINATOR_ORIGIN =
  "https://connect.hiveworkr.thecomputerplumbers.com"

/**
 * Whether `INSTALL_COMMAND` works: a signed daemon and its image are
 * published where it downloads from. Every page that shows the command
 * reads this, so flipping it is the whole of announcing a release.
 */
export const DAEMON_RELEASED = false

/** The oldest macOS the daemon runs on. `install.sh` checks the same. */
export const DAEMON_MIN_MACOS = 13

/**
 * Whose name macOS shows for the daemon: the Apple developer ID it is
 * signed and notarized under.
 */
export const DAEMON_SIGNER = "Layups AI LLC"

export const MARKETING_NAV = [
  { href: "/pricing", label: "Pricing" },
  { href: "/security", label: "Security" },
  { href: "/docs", label: "Docs" },
  { href: "/download", label: "Download" },
] as const

/**
 * What the GitHub App asks for, from docs/spec.md §5. Keep in step with the
 * App manifest: the security page lists exactly these and nothing else.
 */
export const APP_PERMISSIONS = [
  {
    scope: "Organization",
    permission: "Self-hosted runners",
    access: "Read and write",
    why: "Create the hive runner scale sets and register a one-time runner for each job.",
  },
  {
    scope: "Organization",
    permission: "Members",
    access: "Read",
    why: "Check that a person adding a machine belongs to your organization, and to the teams you allow.",
  },
  {
    scope: "Repository",
    permission: "Actions",
    access: "Read",
    why: "Receive workflow job events, so a queued job can be matched to a free machine.",
  },
  {
    scope: "Repository",
    permission: "Metadata",
    access: "Read",
    why: "Required by GitHub for every App.",
  },
  {
    scope: "Account",
    permission: "Email addresses",
    access: "Read",
    why: "Sign in with GitHub.",
  },
] as const

export const APP_EVENTS = [
  "workflow_job",
  "organization",
  "membership",
  "team",
  "installation_repositories",
] as const
