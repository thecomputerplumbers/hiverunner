# HiveWorker

GitHub Actions-compatible runners on your own developers' machines. Licensed
under [AGPL-3.0](LICENSE).

| Read                                               | For                                     |
| -------------------------------------------------- | --------------------------------------- |
| [Goals and decisions](docs/goals-and-decisions.md) | What it is and what has been agreed     |
| [Build spec](docs/spec.md)                         | What gets built, on what, in what order |
| [The GitHub App](docs/github-app.md)               | Creating it and how installation works  |
| [Deployment](docs/deployment.md)                   | Environments, secrets, release          |
| [Licensing](docs/licensing.md)                     | Ours and our dependencies'              |

## Get started

```sh
mise install
mise run setup
cp apps/web/.dev.vars.example apps/web/.dev.vars
pnpm db:seed
pnpm run doctor
mise run dev
```

The web app runs at [http://localhost:3000](http://localhost:3000). Sign in
with `owner@example.test` / `Local-demo-only-2026!`. Password sign-in exists on
localhost only; everywhere else GitHub is the only way in.

## Commands

```sh
mise run dev         # Start the development server
mise run check       # Lint, format, licenses, types, tests, production build
mise run format      # Format the workspace with oxfmt
pnpm test:worker     # Install, tunnels, job dispatch and signed settlement on local Workers
pnpm test:daemon     # The real daemon against both local Workers (macOS)
pnpm run doctor      # Check local setup; also accepts staging or production
pnpm db:seed         # Repeatable local pools and users
pnpm db:reset --yes  # Reset local state; stop the dev server first
pnpm cf:typegen      # Regenerate Cloudflare binding and runtime types
```

## Layout

| Path                                                | What it holds                                                       |
| --------------------------------------------------- | ------------------------------------------------------------------- |
| `apps/web`                                          | Marketing site, dashboard, sign-in, GitHub App endpoints (vinext)   |
| `apps/coordinator`                                  | Machine tunnels, scale-set listeners, dispatch and job metering     |
| `daemon`                                            | macOS daemon and isolated Linux VM runner                           |
| `packages/db`                                       | HiveWorker's D1 tables and the operations on them                   |
| `packages/github`                                   | GitHub App over `fetch` and WebCrypto                               |
| `packages/pricing`                                  | Compute-unit math, the floor, comparison rates. Every price is here |
| `packages/auth`                                     | Better Auth on D1: GitHub sign-in, sessions, pool membership        |
| `packages/ui*`                                      | Primitives, app chrome, data display, forms                         |
| `packages/stripe`, `packages/mcp`, `packages/email` | From the starter; switched off until needed                         |

## Status

Working end to end locally, with tests: GitHub App installation, GitHub
sign-in and membership, pool settings, the ledger and trial grant, daemon
sign-in and enrollment, the tunnel, presence, and revocation.

The scale-set listener, offer/accept/JIT dispatch, isolated VM runner and
GitHub-timed metering with signed receipts are implemented. The VM runner has
also run a real Actions job on Apple Silicon. See [running jobs](docs/running-jobs.md)
for setup and current limits. The web app and tunnel coordinator are deployed;
the new execution path still requires release and coordinator secrets.

The daemon is released for Apple Silicon Macs: `curl -fsSL
https://get.hiveworkr.thecomputerplumbers.com/install.sh | sh`. Building and
publishing a release is in [deployment](docs/deployment.md).

Still planned: container jobs, the menu bar item, Keychain storage, and
on-chain payments.

`AGENTS.md` has the rules for working in this codebase: the two databases,
package boundaries, auth, and the checks.
