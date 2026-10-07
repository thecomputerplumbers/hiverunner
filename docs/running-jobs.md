# Run Actions on a Mac

The first execution path supports Linux arm64 jobs in one disposable VM per
job on Apple Silicon. Each job gets its own kernel, memory and APFS disk clone.
Only a fresh directory containing the one-time runner configuration is shared
with the guest; the Mac's files and server credentials stay on the host.

While available, the daemon keeps one clean 2-vCPU VM booted. A `hive` job
uses it once, then it is destroyed and replaced. Larger jobs boot their own
VM. Set `HIVE_WARM=0` to disable the idle VM; lid closure also drops it.
Jobs and the idle VM remain available on battery. Warmth removes guest boot
time, not GitHub's runner startup time.

## Server setup

The installed GitHub App needs organization self-hosted runners write access.
The web Worker owns installation, membership and machine enrollment. The
coordinator uses the same D1 database and has two SQLite Durable Object classes:
`Pool` and `ScaleListener`. Apply web migrations before deploying either Worker.

Set these **coordinator** secrets, separately from the web Worker's secrets:

- `GITHUB_APP_ID` and `GITHUB_APP_PRIVATE_KEY`: the existing App identity.
- `RECEIPT_SIGNING_KEY`: base64 PKCS#8 Ed25519 private key, generated once and
  kept in your secret manager. Preserve old public keys when rotating it.

Never configure `GITHUB_API_URL` in production; it exists only for local fake
services and requires `LOCAL_DEVELOPMENT=1` with a loopback HTTP origin. Invalid
overrides fail before the coordinator sends credentials. The coordinator creates/reuses a scale set per organization and
supported label when eligible machines connect. It listens while machines are
available, so a queued job does not depend on webhook timing. The web Worker
continues to change D1 only; pool sweeps notice revocation and uninstall.
Listeners poll continuously while eligible machines are online. This incurs
Durable Object runtime cost; webhook-driven idle wakeup is a later optimization.

## Machine setup

Build the image once with Docker, then build the daemon with Go:

```sh
./daemon/image/build.sh
./daemon/build.sh /tmp/hived
/tmp/hived selftest
/tmp/hived login
/tmp/hived run
```

`login` opens the browser to approve machine enrollment. The GitHub App must
be installed on the organization and its repository must be accessible to the
runner group. The daemon pauses availability when the lid is closed and
keeps running on battery. One machine runs one job at a time across all its pools unless its
owner raises `max-jobs`, and always leaves two cores for its owner.

Use these labels in a repository's workflow:

| `runs-on`   | Guest vCPUs | Minimum host cores |
| ----------- | ----------- | ------------------ |
| `hive`      | 2           | 4                  |
| `hive-4cpu` | 4           | 6                  |
| `hive-8cpu` | 8           | 10                 |

```yaml
jobs:
  test:
    runs-on: hive
    steps:
      - uses: actions/checkout@v7
      - run: uname -m
```

Jobs install their own toolchains with `setup-*` actions. `container:` and
`services:` are not supported yet. Every job starts from a clean disk. What
`actions/cache` saves is kept on the Mac that ran the job, per repository, and
restored from there: see "The Actions cache" in `daemon/README.md`. x86-only
binaries need an arm64 alternative. There is no menu
bar app or Keychain token storage yet. The server imposes a 360-minute VM
limit; GitHub may impose a shorter workflow timeout.

The image pins a runner version and checksum; automatic runner updates remain
enabled. Keep the image current to avoid repeated updates in disposable guests
or stale-runner failures. GitHub requires runners to update within 30 days of a
new release ([runner update policy](https://docs.github.com/en/actions/reference/runners/self-hosted-runners)).
There is no automatic image updater yet.

## Dispatch and receipts

The listener advertises capacity to GitHub and provisions runners from
`totalAssignedJobs`, including initial session demand with no job messages.
GitHub may assign work directly. For explicit `JobAvailable` offers, the server
waits for a machine's acceptance before calling acquire. Both paths reserve
the machine before minting and sending its JIT configuration. Registration
and session credentials remain on the coordinator. Explicit runner names map
GitHub lifecycle messages to machines, including across reconnects. Direct
messages with request id zero use GitHub's stable job UUID for receipt identity.

GitHub's start/finish messages determine charges; `job.exited` never does.
The accepted speed quote is locked, and settlement writes a signed receipt
and three idempotent ledger entries. Empty balances stop new acquisition;
running jobs finish. Malformed lifecycle facts are held for repair in listener
storage and do not block later queue messages.
Unknown event kinds are ignored and acknowledged alongside known events.
No-start cleanup waits for a successful lifecycle poll and checks the runner's
public GitHub busy status. API failures and busy runners preserve the VM;
confirmed idle or missing runners can be replaced without charging a job.

`GET /v1/receipt-key` on the coordinator publishes the current Ed25519 public
key and key id. Verify the stored receipt's exact bytes against that key;
older receipts require the archived public key matching their key id.

## Checks

`mise run check` covers contracts, scheduling, pricing, receipts, types and
the production build. `pnpm test:worker` adds real local workerd/D1/DO tests
for enrollment, revocation, dispatch, API retries, reconnects and settlement.
`pnpm test:daemon` drives the real daemon through enrollment and tunnels.
`./daemon/test-vm.sh` boots actual VMs and checks cancellation and cleanup.

Tests use fresh local data and generated fixture keys. They never deploy,
read production job data, or send real email. Real GitHub smoke jobs are a
separate coordinated experiment so two listeners do not compete for a session.

The [integrated smoke run](https://github.com/thecomputerplumbers/hiveworker/actions/runs/37178137715)
passed on this Mac using a warm `hive` VM: checkout, arm64/CPU/user checks,
setup-go and Linux daemon tests. The local coordinator verified GitHub's
timestamps, signed the receipt and produced three ledger entries summing to
zero. This experiment used isolated local accounting data, with no deployment.

## Capacity routing with GitHub-hosted fallback

GitHub Actions can run a preflight job on `ubuntu-latest` and pass its selected
runner through a job output to `runs-on`. A workflow can poll
`GET https://connect.hiveworkr.thecomputerplumbers.com/v1/capacity?label=hive-4cpu`
for up to 15 seconds, then select `ubuntu-latest` if no machine is available.
An available machine ends the probe immediately. The polling deadline begins
after the preflight runner starts; it does not include GitHub runner startup.

Authorize the request with a GitHub Actions OIDC bearer token, requested with
audience `https://connect.hiveworkr.thecomputerplumbers.com`. The preflight job
needs `permissions: { id-token: write }`. The coordinator verifies GitHub's
signature, issuer, audience, expiry and immutable repository owner ID, then
checks the owner's active App installation and positive credit. Selected-repo
installations also require repository access through the installation token.

The response is `{ "available": true, "label": "hive-4cpu" }` only when the live
pool has an idle, greeted, sufficiently large, currently enrolled machine.
Assigned, paused, busy (including in another pool), revoked and retired machines
are excluded. Responses are not cached. No machine identities or credentials
are returned. This endpoint reports availability; it does not reserve capacity.
A machine can disconnect or take another job between probing and queuing. There
is no automatic reroute after a job is queued on HiveWorker.
