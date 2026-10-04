# HiveWorker: Goals and Decisions

As of October 3, 2026. Companion to the HiveWorker Architecture Spec.

## What it is

HiveWorker is GitHub Actions-compatible runner software that lets an organization run its CI on its own developers' machines. Each job is paid with an x402 micropayment in the HiveWorker token. A small daemon on each machine takes jobs, runs them in a throwaway VM, and earns tokens.

## Goals

1. **Cheaper.** A private pool costs the org 10x less than Blacksmith. It should feel close to free.
2. **Faster.** Jobs land on fast hardware the org already owns and start in seconds.
3. **Simple to adopt.** One click to install for the org, one sign-in for a developer, one changed line in a workflow.
4. **Ultra lean to run.** Minimal infrastructure, minimal settlement cost, no billing system.
5. **One revenue source.** The token tax is the entire business model.

## Objectives for v1

- A workflow switches to HiveWorker by changing `runs-on` to `hive`.
- Every job runs in a hardware-virtualized VM and never touches the host.
- The developer stays in control: the daemon yields when the machine is needed.
- Every job has a price, a payer, a payee, and a signed receipt.
- The private pool works with no trust in outsiders. The public pool is opt-in per repo.

Not in v1: replacing GitHub's orchestration, secret-bearing jobs on strangers' machines, Windows hosts, GPU or long-running non-CI workloads.

## Agreed decisions

### Product and pools

| Decision     | Detail                                                                                                                                     |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------ |
| Model        | Follows the Actions Runner Controller pattern: a listener holds the scale set session with GitHub, and ephemeral runners take one job each |
| Runner       | GitHub's official runner binary inside the VM, registered with a one-time JIT config                                                       |
| Pools        | Both. Private pool per org by default, with optional overflow to a public pool                                                             |
| Main product | Private pools. They will be the most common use                                                                                            |
| Public pool  | No-secret jobs only, opt-in per repo, machines post a stake                                                                                |
| Org billing  | None. No org accounts, platform fees, or subscriptions                                                                                     |

### Onboarding

| Decision             | Detail                                                                                                                         |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Org install          | One click: install the HiveWorker GitHub App and authorize                                                                     |
| What install creates | The `hive` scale set, the private pool, and the org's wallet                                                                   |
| Joining a pool       | Developer installs the daemon and signs in with GitHub. No invite codes                                                        |
| Default restriction  | Only members of the GitHub org that installed the app can add machines to its pool. Admins can narrow to specific GitHub teams |
| Trial                | Every new org gets a grant of tokens. Adding a credit card doubles it                                                          |
| Buying more          | The credit card is how an org buys more tokens                                                                                 |

### Daemon

| Decision         | Detail                                                                                    |
| ---------------- | ----------------------------------------------------------------------------------------- |
| Language         | Go, shipped as a single signed binary plus a menu bar item                                |
| Host platform    | Apple Silicon macOS only in v1                                                            |
| Connection       | One outbound tunnel to the coordinator. No inbound ports                                  |
| Presence         | Reports `offline`, `paused`, `online`, `busy`, or `draining`                              |
| Warm VM          | Keeps one VM booted and ready so jobs start in seconds                                    |
| Keys             | The daemon should not hold a private key. It verifies vouchers and names a pay-to address |
| Privileges       | Runs as a per-user agent, not root                                                        |
| Owner returns    | Continue on battery. Pause new jobs and discard the idle VM when the lid closes           |
| Wallet ownership | Configurable per org. Earnings go to an org wallet or to the developer as a perk          |

### Payments

| Decision       | Detail                                                                                                                         |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Protocol       | x402                                                                                                                           |
| Chain          | Base                                                                                                                           |
| Facilitator    | Coinbase's hosted facilitator. HiveWorker does not run its own                                                                 |
| Currency       | The HiveWorker token only, as an ERC-20. No USDC layer                                                                         |
| Display        | Token amounts with a USD estimate beside them for comparison                                                                   |
| Scheme         | Batch settlement: the org deposits into escrow once, signs a cumulative voucher each minute, and the machine claims in batches |
| Charging later | Acceptable. Usage is batched and settled after the fact                                                                        |

### Token and tax

| Decision            | Detail                                                                                            |
| ------------------- | ------------------------------------------------------------------------------------------------- |
| Business model      | The token tax, paid to the HiveWorker treasury wallet, is the only revenue                        |
| Where the tax lives | Inside the token contract                                                                         |
| Rates               | Two: a job rate on claims out of escrow, and a transfer rate on everything else, including trades |
| Escrow handling     | Deposits into escrow and refunds out of it are untaxed                                            |
| Minimum price       | The coordinator enforces a network-wide floor per compute unit and refuses jobs priced below it   |
| Claim rule          | Machines must claim their vouchers. A machine with stale unclaimed vouchers stops receiving jobs  |

### Pricing

| Decision      | Detail                                                                                                         |
| ------------- | -------------------------------------------------------------------------------------------------------------- |
| Target        | Under GitHub Actions and Blacksmith. Private pools 10x cheaper than Blacksmith, backed by the floor            |
| Unit          | Compute unit: one vCPU-minute at reference speed. The reference is one core of a GitHub standard hosted runner |
| Hardware      | Price reflects the machine. A benchmark at enrollment gives each machine a speed score                         |
| Charge        | Minutes x vCPUs x speed score x rate                                                                           |
| Job sizes     | Requested by label, for example `hive-2cpu` or `hive-8cpu`                                                     |
| Honest scores | The coordinator checks scores against real job durations and cuts inflated ones                                |

## Placeholder numbers

These are starting points to tune, not decisions.

| Setting                          | Placeholder                                                        |
| -------------------------------- | ------------------------------------------------------------------ |
| Job rate                         | 25%                                                                |
| Transfer rate                    | 1%                                                                 |
| Floor                            | $0.0005 per compute unit ($0.001 per minute for 2 reference vCPUs) |
| Org's net cost in a private pool | $0.00025 per 2 vCPU minute                                         |
| Trial grant                      | $5 of tokens, $10 with a credit card                               |

What the placeholders imply:

- Blacksmith's cheapest runner is $0.0025 per minute, so the private pool net cost is 10x lower.
- An org running 500,000 minutes a month produces about $125 of tax.
- $10,000 a month takes about 40 million minutes, or roughly 80 orgs of that size.
- A $5 trial grant covers about 20,000 two-vCPU minutes in a private pool.

The 500,000-minute figure is an estimate for a mid-sized engineering org, not a measured number.

## Build order

1. Private pool with metering only: coordinator, daemon, tunnel, Linux ARM jobs on Apple Silicon.
2. x402 on Base Sepolia: escrow channels, per-minute vouchers, batched claims, receipts.
3. More job types: macOS jobs, Linux x64 hosts, always-on anchor machines.
4. Token on mainnet: transfer tax with the escrow exemption, and a funded liquidity pool.
5. Public pool: overflow, staking, spot-check re-runs, reputation.

## Still open

- The exact tax rates, floor, and trial size.
- Whether the token's escrow exemption works with Coinbase's facilitator. Fallback: collect the job rate in a separate contract at claim time.
- Who must sign a batch claim, which decides whether the daemon can stay keyless.
- Whether selling tokens for card payments needs a licensed on-ramp partner.
- Legal review of the token before any sale or listing, and of paying developers in tokens.
- What happens when both pools are empty: a HiveWorker cloud fleet, or fail over to GitHub-hosted runners.
- Whether macOS jobs (iOS builds) make v1.
- The token's name and symbol. HIVE is already taken by another blockchain.
