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

The daemon keeps itself current. Once a day it reads the release manifest
(one request, cached by ETag) and, when a newer release is out, says so once
in its log, in `hived status` and as a desktop notification. With the default
`auto-update on` it then stops taking jobs, waits for the one in hand, and
runs the installer, which restarts it; `hived update` does the same on
demand, and `hived config set auto-update off` leaves it to the owner. The
dashboard marks machines whose daemon is behind the release. GitHub requires
runners to update within 30 days of a runner release, and the image pins the
runner, so publishing a release is how a fleet stays within that.

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

### Linux x64 (not yet released)

A label names an architecture as well as a size. The labels above are arm64
and stay arm64. Linux x64 jobs have their own, served only by Linux x64
machines:

| `runs-on`       | Guest vCPUs | Minimum host cores |
| --------------- | ----------- | ------------------ |
| `hive-x64`      | 2           | 4                  |
| `hive-x64-4cpu` | 4           | 6                  |
| `hive-x64-8cpu` | 8           | 10                 |

The coordinator already keeps the two apart: a machine says what it is in
`hello` (one that does not is what it enrolled as, and every machine enrolled
before this was an arm64 Mac), and a job is only offered to, and only counts
as capacity on, a machine of its label's architecture. A machine declines an
offer for another architecture as a second check. The scale set for an x64
label is created when the first x64 machine able to serve it comes online.
Savings on the dashboard compare each job with GitHub's price for its own
architecture.

The daemon for Linux is released for both architectures, and real jobs have
run on each. The x64 labels are `X64_RUNNER_SIZES` in `@workspace/pricing`,
listed apart from `RUNNER_SIZES` because the site's prices and examples are
written for arm64. The x64 guest image is built with:

```sh
GUEST_ARCH=x64 ./daemon/image/build.sh /path/to/image
```

It is the same guest with GitHub's x64 runner and an x64 kernel, and its
`manifest.json` says `"arch": "amd64"`, Go's name for it. `hived` refuses an image that is not
for the machine it is running on.

### macOS jobs (iOS builds)

`runs-on: hive-macos` is for what only macOS can do: Xcode, iOS builds and
signing, simulators. Such a job does not run in a VM. It runs on a member's
Mac itself, as a separate local user named `hiveworker`, which keeps the
owner's files and keychain out of reach and nothing more: the job can use the
Xcode and other software installed on that Mac, and its network. It is the
same trust model as any self-hosted macOS runner, and so two people have to
agree before one runs:

- The Mac's owner runs `hived macos setup`. With sudo, it creates the
  `hiveworker` user (hidden, no usable password, in the `_developer` group),
  writes one rule to `/etc/sudoers.d/hiveworker` that lets the owner run
  exactly `hived macos-job` as that user, downloads GitHub's macOS runner
  (`native.RunnerVersion`, pinned with its checksum), and turns on the
  `macos-jobs` setting. `hived macos status` says what is in place;
  `hived macos remove` undoes it all. The daemon says `macos: true` in its
  hello only while the setting is on and the Mac is ready.
- An owner of the hive turns on "Allow macOS jobs" in its settings
  (`pool_settings.macos_jobs`). Until then the coordinator advertises no
  capacity for the label, creates no scale set for it, and answers capacity
  probes for it with no.

The label's kind is `macos` in `@workspace/pricing`, beside the two
architectures, and `serves()` in the coordinator's schedule is the one place
that decides which machine is which kind. A job is billed as 4 vCPUs, the size
of GitHub's standard macOS runner, and compared with GitHub's price for it.

Each job gets an APFS clone of the runner directory, GitHub's runner in it
with a clean environment, and the clone is deleted afterwards. What the job
leaves in the `hiveworker` user's home (CocoaPods, npm and Xcode caches) stays
for the next job. The owner installs what every job needs, typically CocoaPods
and Fastlane with Homebrew. An Expo app:

```yaml
jobs:
  ios:
    runs-on: hive-macos
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-node@v4
        with:
          node-version: 22
      - run: npm ci
      - run: npx eas-cli build --local --platform ios --profile preview --non-interactive --output app.ipa
        env:
          EXPO_TOKEN: ${{ secrets.EXPO_TOKEN }}
      - uses: actions/upload-artifact@v4
        with:
          name: ios
          path: app.ipa
```

Android EAS builds need no Mac: they run on the Linux labels with
`setup-java` and `android-actions/setup-android`.

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

Workflows do this with one step, the public action
[`thecomputerplumbers/hive-route`](https://github.com/thecomputerplumbers/hive-route)
(`uses: thecomputerplumbers/hive-route@v1`, inputs `label`, `fallback` and
`wait`). Its source lives in that repository, not this one. What it does is
what this section describes.

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
