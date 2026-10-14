# Hive Runner: build spec

As of October 3, 2026. Product goals and agreed decisions are in
[goals-and-decisions.md](goals-and-decisions.md); this document does not repeat
them. It covers what gets built in this repository, on what, and in what order.

Scope: build-order step 1 (private pool, metering only) in full, with the seams
that steps 2–5 plug into. Items marked **Verify** are assumptions to confirm in
a spike before building on them. Items marked **Open** need a decision.

## 1. System map

| Piece          | What it does                                                              | Runs on                              | Lives in                       |
| -------------- | ------------------------------------------------------------------------- | ------------------------------------ | ------------------------------ |
| Web            | Marketing site, docs, dashboard, sign-in, GitHub App callbacks + webhooks | Cloudflare Worker (vinext)           | `apps/web`                     |
| Coordinator    | Holds scale set sessions, machine tunnels, scheduling, metering           | Cloudflare Worker + Durable Objects  | `apps/coordinator`             |
| Daemon         | Takes jobs, runs them in a throwaway VM, reports presence                 | Developer's Mac (Go, per-user agent) | `daemon/`                      |
| Runner         | GitHub's official runner binary, one job, JIT config                      | Inside the VM                        | VM image, built from `daemon/` |
| Token + escrow | ERC-20 with tax, escrow channel                                           | Base                                 | Separate repo                  |
| Releases       | Daemon binaries, VM images, update manifest                               | R2                                   | Published by CI                |

The coordinator is never in a job's data path. The runner inside the VM talks
to GitHub directly for the job, its logs, and its artifacts. Hive Runner stores
no logs and proxies no job traffic, which is most of what keeps it lean.

Everything Hive Runner operates runs on Cloudflare. The things that cannot:
the daemon (developer machines), GitHub, Base and Coinbase's facilitator, and
Stripe.

## 2. Repository

Copied from `thecomputerplumbers/starter` at `80b3374`. Tooling is unchanged:
mise, pnpm, turbo, oxlint + oxfmt, TypeScript 7, Drizzle 1.x RC, vinext.

```
apps/web            marketing + dashboard + auth + GitHub endpoints   (starter)
apps/coordinator    Worker: Pool and Listener Durable Objects, tunnel (new)
daemon/             Go module: daemon, menu bar item, VM image build  (new)
packages/auth       Better Auth on D1                                 (starter, reshaped)
packages/db         D1 schema shared by both Workers                  (new, from apps/web/db)
packages/github     App JWT, installation tokens, webhooks, scale set client (new)
packages/protocol   Tunnel message schema; generates the Go types     (new)
packages/pricing    Compute-unit math, floor, competitor rates        (new)
packages/ui*        Unchanged
packages/email      Kept for low-balance and revocation notices
packages/stripe     Kept, reshaped in step 2 (see §9)
packages/mcp        On: the MCP transport; the hive's tools are in apps/web/mcp
```

### What changes from the starter

| Starter piece                                    | Action                                                                              |
| ------------------------------------------------ | ----------------------------------------------------------------------------------- |
| Email + password, sign-up, verify, forgot, reset | Remove. GitHub is the only way in                                                   |
| Invitations, create/select organization, `/team` | Remove. Organizations come from GitHub App installs, members from GitHub            |
| Projects example                                 | `features.projects: false` now; delete once `/machines` exists as the pattern       |
| MCP + OAuth provider                             | On. `/mcp` serves a hive's state, prices and settings to agents, one hive per grant |
| Stripe subscriptions, plans, `/billing`          | `features.billing: false` in step 1. Becomes one-time token purchases in step 2     |
| `apps/web/db/schema`                             | Move to `packages/db` so the coordinator shares it                                  |
| `AppDatabase` Durable Object                     | Keep in web for rate limits                                                         |

`packages/pricing` is the single source for the floor, the rate, and the
GitHub/Blacksmith comparison figures. The coordinator charges from it and the
marketing calculator renders from it, so the site cannot drift from the bill.

## 3. Cloudflare topology

Two Workers, because deploying a Worker drops every WebSocket held by its
Durable Objects. Marketing copy changes must not disconnect the fleet.

| Resource        | Name                     | Notes                                                                |
| --------------- | ------------------------ | -------------------------------------------------------------------- |
| Worker          | `hiveworker-web`         | `<domain>`. Marketing, dashboard, `/api/auth/*`, `/api/github/*`     |
| Worker          | `hiveworker-coordinator` | `connect.<domain>`. Tunnel endpoint, Durable Objects, queue consumer |
| D1              | `hiveworker`             | Bound to both Workers. Migrations are applied by the web deploy only |
| Durable Objects | `Pool`, `Listener`       | Coordinator. SQLite storage, hibernatable WebSockets                 |
| Queue           | `github-events`          | Web produces verified webhooks; coordinator consumes                 |
| R2              | `hiveworker-releases`    | `get.<domain>`. Daemon builds, VM images, install script, manifest   |
| Cron            | Coordinator, daily       | Membership reconcile, stale-machine sweep                            |
| Email Service   | Web                      | Low balance, machine revoked                                         |

`<domain>` is `hiverunner.dev` (staging:
`hiveworkr-staging.thecomputerplumbers.com`). Sibling hosts nest under it
(`connect.hiverunner.dev`, `get.hiverunner.dev`). The old
`hiveworkr.thecomputerplumbers.com` hosts still answer for daemons and route
actions installed before the move. That
is two levels below the zone, which the zone's default certificate does not
cover; Cloudflare issued a certificate for the coordinator's custom domain
on its first deploy, so this works as configured. Hosts come from configuration, never from string-building,
so the move to a real domain is a config change plus new GitHub App URLs. The
daemon reads its coordinator host from the release manifest for the same reason.

Production only for now; there is no staging environment. Web reaches the
coordinator through a service binding for dashboard reads of live presence.

Secrets: `BETTER_AUTH_SECRET`, `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`,
`GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `GITHUB_WEBHOOK_SECRET`,
`RECEIPT_SIGNING_KEY`. GitHub issues the App key as PKCS#1; WebCrypto imports
only PKCS#8, so convert once with `openssl pkcs8 -topk8 -nocrypt` before
`wrangler secret put`.

## 4. Auth (Better Auth)

Better Auth 1.7.6, already in the starter. All three plugins named below ship
in that version.

**Sign-in.** One provider: GitHub, using the Hive Runner GitHub App's own client
ID and secret. One App therefore covers both installation and sign-in. The App
needs the account permission _Email addresses: read_ or GitHub returns no email
and Better Auth rejects the sign-in.

**Organizations.** Keep the `organization` plugin, with
`allowUserToCreateOrganization: false`; its create, invite, role and remove
endpoints are refused at the Worker entry. An organization row is created only
by a GitHub App installation. Its id is derived from the GitHub account id, and
the installation lives beside it in `github_installation`, keyed by that
account so a reinstall returns to the same pool.

**Membership.** GitHub is the source of truth; the `member` table is a cache.

- On sign-in: candidates are the installations the person's own token lists
  (`GET /user/installations`) plus every pool they are already cached in.
  Each is confirmed with `GET /orgs/{org}/memberships/{username}` using the
  _installation_ token. GitHub `admin` maps to `owner`, `member` to `member`.
  The same check runs fresh on device approval, and daily for everyone.
- An error from GitHub leaves the cache alone. Only a definite "not a member"
  removes anyone.
- `organization.member_removed` webhooks delete the member and revoke their
  machines immediately.
- Team narrowing applies to adding machines, not to seeing the dashboard:
  enrollment also checks `GET /orgs/{org}/teams/{slug}/memberships/{username}`.
  Owners are exempt.
- The starter's rule stands: `session.activeOrganizationId` is never an
  authorization decision. Use `requireMembership` on every write.

User GitHub tokens are used once, to claim an installation (§5), and never for
membership. They expire after eight hours and we do not refresh them.

**Daemon sign-in.** `deviceAuthorization` plugin (RFC 8628) plus `bearer`.

1. Daemon requests a device code (client id `hiveworker-daemon`; any other
   is refused) and opens `https://<domain>/device?user_code=…`.
2. The developer signs in with GitHub if needed, checks the code matches, and
   approves.
3. Daemon exchanges the device code for a session token and calls
   `POST /api/machines` with it as a bearer token: hardware facts, benchmark
   result and the pools to serve in; machine ID and **machine token** out.
   Someone in several pools gets the list back and chooses any or all.
4. The server ends that session as it answers. The machine token (opaque,
   stored only as a hash) is the only credential the daemon keeps.

**A machine belongs to a person and can serve several organizations.** Each
pool is authorized separately (`machine_pool`): GitHub membership and that
pool's team limit are checked when it is added, and there is a cap per person
per pool. A machine already running can be added to another of its owner's
pools from the dashboard, or with `hived link` on the machine: a second
device sign-in, then `POST /api/machines/link` with that session as the bearer
token, which the server ends as it answers. The machine token is deliberately
not enough to link, since it would let whoever holds it bring another
organization's jobs to the machine; it is enough to leave one
(`hived unlink`, `POST /api/machines/unlink`). The daemon asks
`GET /api/machines/me` on a timer and opens or closes tunnels to match, so
nothing is reinstalled. `hived login` on a machine already enrolled enrolls
nothing and points at `hived link`. Leaving an
organization on GitHub removes that person's machines from that pool only.
The machine's owner can retire it everywhere; an organization owner can
remove it from their own pool.

Apple Silicon macOS only.

**Passwords.** Enabled on localhost only, for seeded users and the smoke test.

## 5. GitHub App installation

One App per environment, owned by a GitHub organization named for the product
so the install screen reads "Hive Runner". The repository lives under
`thecomputerplumbers`; whether the App does too is **Open**. The App is
defined by a manifest committed at `apps/web/github-app.json`.

### Permissions and events

| Permission                                | Why                                        |
| ----------------------------------------- | ------------------------------------------ |
| Organization → Self-hosted runners: write | Create scale sets, mint JIT runner configs |
| Organization → Members: read              | Membership and team checks                 |
| Repository → Actions: read                | `workflow_job` events                      |
| Repository → Metadata: read               | Mandatory                                  |
| Account → Email addresses: read           | Sign-in                                    |

Events: `workflow_job`, `organization`, `membership`, `team`,
`installation_repositories`. `installation` events are always delivered.

No code, contents, or secrets permission. That is a line worth putting on the
marketing site.

### Flow

1. "Install" on the site goes to `github.com/apps/<slug>/installations/new`.
2. GitHub sends the `installation.created` webhook. This is the source of
   truth and does all the provisioning, with or without a browser present:
   organization row, pool settings, the `hive` scale sets, and the trial grant
   as a ledger entry. Idempotent on installation ID.
3. GitHub redirects the browser to the Setup URL, `/github/setup?installation_id=…`.
   If there is no session, sign in with GitHub and come back.
4. **Claim check.** The page calls `GET /user/installations` with the user's
   token and proceeds only if that installation is listed. The
   `installation_id` in a Setup URL is attacker-controllable, so it is never
   trusted alone.
5. Land on the dashboard with the next step: install the daemon.

If the installer is not an org owner GitHub files a request instead
(`setup_action=request`); show "waiting for an owner to approve". Uninstall and
suspend mark the installation inactive, close its tunnels, and stop its
listeners. Data is kept.

"The org's wallet" in step 1 is a ledger account in D1. It becomes an on-chain
wallet in step 2 (§9).

Personal accounts are refused in v1 with a clear message: org-level scale sets
do not exist for users, and repo-level ones need Administration: write, which
is too much to ask for (**Open**).

### Scale sets and labels

`runs-on: hive` matches a scale set named `hive`. A scale set is one queue, so
a runner cannot be picked for a job by size within it. Therefore one scale set
per size, all created at install: `hive` (2 vCPU), `hive-4cpu`, `hive-8cpu`.
**Verify**: newer scale sets accept multiple labels; if so `hive-2cpu` becomes
an alias of `hive` rather than a fourth set.

Scale sets go in the org's default runner group, which excludes public
repositories by default. That is the right default for a private pool.

## 6. Coordinator

### Durable Objects

**`Pool`**, one per organization. Holds one hibernatable WebSocket per machine
serving it, their presence, and free capacity by size. A machine serving three
organizations holds three tunnels, one to each object, and keeps a single job
slot between them: when two pools offer it a job at once, it takes the first
and declines the second, which stays queued at GitHub for another machine. Single-threaded, so two jobs cannot
be handed the same machine. Its SQLite holds only live state; anything worth
querying is written through to D1.

**`Listener`**, one per scale set. Speaks the same protocol as the Actions
Runner Controller listener, over `fetch`:

1. Installation token → registration token → Actions service admin token.
2. Open a message session; long-poll for messages.
3. On _job available_: ask `Pool` whether a machine of that size is free. If
   so, acquire the job, mint a JIT runner config, and have `Pool` send it down
   the tunnel. If not, leave the job unacquired; it stays queued at GitHub.
4. On _job started_ and _job completed_: write the job row and meter it.

GitHub assigns an acquired job to _any_ idle runner in the scale set, not to
one we choose. So the coordinator reserves capacity, not a specific job, and
learns the machine-to-job binding from the runner name in _job started_. The
JIT runner name encodes the machine ID for that reason.

**Woken, not always on.** A listener that long-polls forever keeps its object
billed around the clock for every org. Instead the `workflow_job.queued`
webhook wakes the `Listener`; it polls while jobs are queued or running and
goes idle after a quiet period, with a slow alarm as a safety net for missed
webhooks. **Verify** (spike 1): that messages queue while no session is
polling, that a 50-second long poll is fine from a Durable Object, and what
session expiry looks like. Fallback, still on Cloudflare: run GitHub's Go
scale set client in a Cloudflare Container fronted by the same object.

### Tunnel

`wss://connect.<domain>/v1/tunnel?pool=<organization>`, machine token in the
`Authorization` header, outbound only, one connection per pool served, JSON
messages defined in `packages/protocol`.

| Direction      | Messages                                                                   |
| -------------- | -------------------------------------------------------------------------- |
| Daemon → coord | `hello`, `presence`, `job.accepted`, `job.exited`, `benchmark`             |
| Coord → daemon | `welcome`, `job.assign` (JIT config, size, time limit), `drain`, `revoked` |

Presence is `offline | paused | online | busy | draining`. Keepalive uses the
WebSocket auto-response so pings do not wake the object. Daemons reconnect with
jittered backoff; a coordinator deploy drops sockets but running jobs carry on,
since the runner talks to GitHub directly.

### Metering

- Time comes from GitHub's _job started_ and _job completed_ messages, not from
  the daemon. The payee does not get to report its own hours.
- Charge = minutes (rounded up) × vCPUs × speed score × rate. The rate is
  fixed at the floor for private pools in step 1 and locked at assignment.
- Each completed job writes three ledger entries: charge to the org, earning to
  the payee (org or developer, per pool setting), tax to the treasury account.
- Amounts are integers in micro-tokens. Step 1 ledger units are defined as 1:1
  with the eventual token, so step 2 changes settlement and not the arithmetic.
- The receipt is the job's priced facts signed with an Ed25519 key held by the
  coordinator, stored on the job row, and downloadable from the dashboard.
- An org may go $5 below zero (`OVERDRAFT_USD`). Past that it gets no new
  jobs acquired; they wait in GitHub's queue and the dashboard says why.

Speed scores come from the enrollment benchmark and are corrected against
observed durations of repeated jobs. Step 1 records the data; automatic
cutting of inflated scores only matters once payees are strangers (step 5).

## 6a. Daemon: virtualization and speed

Go, in `daemon/`. Apple's Virtualization.framework, driven from Go. Not
Docker: on a Mac it is a Linux VM with containers inside, and a container is
not the hardware boundary the goals require. Not Firecracker: it needs Linux
KVM, so it arrives with Linux x64 hosts in step 3. Docker runs _inside_ the
guest for jobs that use `container:` or `services:`.

A cold runner pays for scheduling, an image pull, boot, registration and empty
caches on every job. The daemon pays for none of them at job time:

| Cost         | How it is avoided                                                                                                              |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------ |
| Boot         | One VM is always booted. Direct kernel boot, no firmware or bootloader                                                         |
| Registration | **Verify**: the warm VM holds a runner already registered and idle, so GitHub assigns at once. Falls back to JIT on assignment |
| Disk         | Each job's disk is a copy-on-write clone of a golden image (APFS `clonefile`), discarded after                                 |
| Image pull   | The golden image is on the host already; updates download in the background                                                    |
| Cold caches  | A per-repository cache volume on the host: package stores, Docker layers, `actions/cache`                                      |
| Slow cores   | Jobs run on the machine's performance cores; the speed score prices it                                                         |

The cache volume is the one place a job leaves something behind. It is scoped
to one repository within one private pool, never shared across organizations,
and never attached to public-pool jobs.

x64 container images run under Rosetta in the guest. Memory is ballooned back
to the host when the VM is idle, so a warm VM costs the owner little.

## 7. Data model (D1)

Better Auth tables (`user`, `session`, `account`, `verification`,
`organization`, `member`, `deviceCode`, `jwks`, `rateLimit`) plus:

| Table                 | Key columns                                                                                                                                                           |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `github_installation` | installation ID, account ID + login, organization ID, status, permission set                                                                                          |
| `pool_settings`       | organization ID, allowed team slugs, earnings destination (`org` or `developer`), routing, public overflow                                                            |
| `scale_set`           | organization ID, name, vCPUs, GitHub scale set ID                                                                                                                     |
| `machine`             | user ID, name, model, cores, memory, speed score, token hash, status, daemon version                                                                                  |
| `machine_pool`        | machine ID, organization ID, status, presence, last seen                                                                                                              |
| `job`                 | organization ID, scale set, repo, run ID, job ID, runner name, machine ID, vCPUs, speed score, rate, queued/started/completed, result, compute units, amount, receipt |
| `ledger_entry`        | account (org, user, or treasury), kind (`grant`, `purchase`, `charge`, `earning`, `tax`), amount, job ID                                                              |
| `webhook_delivery`    | GitHub delivery ID, for dedupe                                                                                                                                        |

Balances are sums over `ledger_entry`; nothing stores a mutable balance.

## 8. Marketing site

**Where.** In `apps/web`, same Worker as the dashboard. One deploy, one design
system, and the starter already ships the site header and footer. Marketing
routes read no session and no database, so they prerender and cache at the
edge. A signed-in visitor sees the same page with "Dashboard" in the header.

| Route                | Content                                                                                                                                                                  |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `/`                  | The one-line diff (`ubuntu-latest` → `hive`). Cheaper, faster, simple. How it works in three steps: install, sign in, change one line. Isolation summary. Install button |
| `/pricing`           | Calculator: minutes per month and runner size in; GitHub, Blacksmith, Hive Runner out. Fed by `packages/pricing`. Trial grant                                            |
| `/security`          | Hardware-virtualized VM per job, nothing touches the host, App permissions (no code access), private pool trust model, owner stays in control                            |
| `/docs/*`            | MDX in the repo: quickstart, labels and sizes, the daemon, pools, pricing, FAQ                                                                                           |
| `/download`          | Daemon for Apple Silicon, `curl` installer, system requirements                                                                                                          |
| `/install`           | Redirect to the GitHub App install URL                                                                                                                                   |
| `/terms`, `/privacy` | Legal                                                                                                                                                                    |

No blog, changelog, or status page in v1.

**Token on the site.** Until the legal review listed as open in the goals doc
is done, the site describes the token only as how jobs are paid for and shows
USD estimates. No token price, no purchase call to action, no earnings
promises to developers.

**Launch gate.** The site can go up as soon as step 1 runs end to end for one
outside org; before that, `/install` is a waitlist form.

Dashboard routes, for contrast: `/dashboard`, `/machines`, `/jobs`,
`/jobs/[id]`, `/wallet`, `/settings`, `/account`, `/device`, `/github/setup`.

## 9. Seams for later steps

**Token repo.** Contracts (ERC-20 with the two tax rates, escrow channel,
deploy scripts, tests) live in their own repository with their own audit and
release cadence. This repo consumes published ABIs and addresses through a
small `packages/chain` added in step 2. Nothing in step 1 imports chain code.

**Step 2, x402.** The ledger account becomes an escrow deposit; the per-job
charge becomes a cumulative voucher signed each minute; `earning` entries
become batched claims. The org's signing key must not sit in a Worker secret
per org: use a hosted signer (Coinbase server wallets) called from the
coordinator. Who signs a batch claim is still open and decides whether the
daemon stays keyless.

**Buying tokens.** `packages/stripe` loses subscriptions and keeps Checkout in
payment mode plus a saved card for the doubled trial. Blocked on the on-ramp
question in the goals doc.

**Public pool.** `pool_settings.public_overflow` and a per-repo opt-in table.
A single global pool object alongside the per-org ones.

## 10. Step 1 milestones

| #   | Milestone  | Done when                                                                                         |
| --- | ---------- | ------------------------------------------------------------------------------------------------- |
| 0   | Spike      | A Durable Object holds a scale set session for a test org and a hand-started runner takes a job   |
| 1   | Foundation | `project:init` run, starter trimmed per §2, GitHub-only sign-in, staging deployed                 |
| 2   | GitHub App | Install provisions org, pool, scale sets, grant; Setup URL claim check; uninstall handled         |
| 3   | Tunnel     | Device sign-in, machine enrollment, `Pool` presence; a scripted fake daemon passes the smoke test |
| 4   | Daemon     | Signed Go binary boots a Linux ARM VM, keeps one warm, runs a JIT runner, yields to the owner     |
| 5   | Metering   | Jobs priced, ledger and signed receipts written, dashboard shows machines, jobs, balance          |
| 6   | Site       | Marketing pages, docs, download, install                                                          |

The spike comes first because the woken listener is the one design choice here
that could fail outright, and its fallback changes the coordinator's shape.

## 11. Open

- **GitHub's self-hosted runner fee.** GitHub announced a $0.002 per minute
  charge for self-hosted runners in December 2025 and postponed it with no new
  date; as of 2026-10-03 it is not in effect and not cancelled. If it returns
  it is twice our 2 vCPU gross price and would be paid by the organization on
  top of ours. The "close to free" goal and the site's "no platform fee" line
  both depend on it staying away.
- **Personal accounts** in v1: refused above.
- **Empty pool.** Jobs currently wait in GitHub's queue. The goals doc leaves
  open whether to fail over to GitHub-hosted runners or a cloud fleet.
- **Daemon CI.** Signing and notarizing need a macOS builder, which is the one
  build that cannot run on Cloudflare.
