# Run Actions on a Mac

The first execution path supports Linux arm64 jobs in one disposable VM per
job on Apple Silicon. Each job gets its own kernel, memory and APFS disk clone.
Only a fresh directory containing the one-time runner configuration is shared
with the guest; the Mac's files and server credentials stay on the host.

While available, the daemon keeps one clean 2-vCPU VM booted. A `hive` job
uses it once, then it is destroyed and replaced. Larger jobs boot their own
VM. Set `HIVE_WARM=0` to disable the idle VM; battery or lid closure also drops
it. Warmth removes guest boot time, not GitHub's runner startup time.

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
services. The coordinator creates/reuses a scale set per organization and
supported label when eligible machines connect. It listens while machines are
available, so a queued job does not depend on webhook timing. The web Worker
continues to change D1 only; pool sweeps notice revocation and uninstall.

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
runner group. The daemon pauses availability on battery or when the lid is
closed. One machine runs one job across all its pools and leaves two cores
for its owner.

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
`services:` are not supported yet. Every job starts from a clean disk; caches
do not survive. x86-only binaries need an arm64 alternative. There is no menu
bar app or Keychain token storage yet. The server imposes a 360-minute VM
limit; GitHub may impose a shorter workflow timeout.

## Dispatch and receipts

The server offers a slot before acquiring a job. After the daemon accepts,
the listener acquires it and sends a JIT runner configuration. Registration
and session credentials remain on the coordinator. Explicit runner names map
GitHub lifecycle messages to machines, including across reconnects.

GitHub's start/finish messages determine charges; `job.exited` never does.
The accepted speed quote is locked, and settlement writes a signed receipt
and three idempotent ledger entries. Empty balances stop new acquisition;
running jobs finish. Malformed lifecycle facts are held for repair in listener
storage and do not block later queue messages.

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
