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
runner group. The daemon pauses availability when the lid is closed, unless
its owner sets `lid on`, and keeps running on battery. One machine runs one job at a time across all its pools unless its
owner raises `max-jobs`, and always leaves two cores for its owner.

The daemon keeps itself current. Once a day it reads the release manifest
(one request, cached by ETag) and, when a newer release is out, says so once
in its log, in `hived status` and as a desktop notification. With the default
`auto-update on` it then stops taking jobs, waits for the one in hand, and
runs the installer, which restarts it; `hived update` does the same on
demand (`--now` to not wait for the job), including when only the guest image
is behind. A failed update puts the machine back as it was. `hived config set auto-update off` leaves it to the owner. The
dashboard marks machines whose daemon is behind the release. GitHub requires
runners to update within 30 days of a runner release, and the image pins the
runner, so publishing a release is how a fleet stays within that.

Use these labels in a repository's workflow:

| `runs-on`   | Guest vCPUs | Guest RAM | zram + swap file | Minimum host cores |
| ----------- | ----------- | --------- | ---------------- | ------------------ |
| `hive`      | 2           | 4 GB      | 2 GB + 2 GB      | 4                  |
| `hive-4cpu` | 4           | 8 GB      | 4 GB + 4 GB      | 6                  |
| `hive-8cpu` | 8           | 16 GB     | 8 GB + 4 GB      | 10                 |

Guest RAM is 2 GiB a core (at least 4), and never more than half the host.
Each guest also gets compressed zram (half of RAM) and a disk-backed swap
file (half of RAM, at most 4 GiB), so memory pressure slows the job instead
of the OOM killer cutting it. `/dev/shm` stays at the kernel default (half
of RAM); tmpfs pages can swap. Soft memory limits only warn in the GitHub
job log; they never kill the job.

When a runner or VM is still killed or shut down, the daemon classifies why
(`guest_oom`, `host_killed`, `disk_full`, `timeout`, `daemon_shutdown`,
`vm_crash`, `released`) and writes one sentence such as `runner killed: guest
OOM (guest used 7.9 of 8 GB) on MacBook-Pro-5`. The memory figures are the
guest's own use against its own RAM. When the host kills a job's VM the
sentence adds the host's `MemAvailable` at that moment: `runner killed: host
ran out of memory (guest used 5.5 of 7.8 GB, 1.2 GB available on host)`. A job
that stops because the host killed a sibling's VM while it ran (the shortage
may take hived down too, so these would otherwise read `daemon_shutdown` or
`vm_crash`) is reported as `host_killed` with `stopped: host ran out of memory
(sibling job killed)`. The daemon notes the kill in a `host-oom` file in its
work directory. That sentence is injected into the
GitHub job log when the guest is still alive, logged by hived, stored on the
job in D1 (`end_reason` / `end_detail`), and shown on the job page and
`get_job` / `list_jobs`. The virtio balloon device is present but is not
used to shrink a running job.

```yaml
jobs:
  test:
    runs-on: hive
    steps:
      - uses: actions/checkout@v7
      - run: uname -m
```

These are labels, not runner groups. `runs-on` takes the label as a plain
string, and these four are the whole list for an arm64 hive:

```yaml
runs-on: hive # 2 vCPU Linux
runs-on: hive-4cpu # 4 vCPU Linux
runs-on: hive-8cpu # 8 vCPU Linux
runs-on: hive-macos # a member's Mac itself; see macOS jobs below
```

The `group:` form does not work:

```yaml
runs-on:
  group: hive-4cpu # fails: Required runner group 'hive-4cpu' not found
```

There is no runner group of that name, and none is needed. Hive Runner's
runners are in the organization's Default runner group, which is the only
group an organization on GitHub Free has, so nothing here depends on the
organization's plan.

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

When several machines could take a job, the hive's routing setting
(`pool_settings.routing`) picks one. `fastest`, the default, sends it to the
machine with the highest speed score. `spread` sends it to the machine holding
the fewest jobs, so an idle one is used before a working one gets a second.
`round-robin` sends it to the machine that has waited longest since it was
last given one, so every machine gets a share of the work and of what it
earns. An owner of the hive chooses in its settings; `pickMachine` in the
coordinator's schedule is where each is defined, and no policy offers a job
to a machine that `room()` says cannot take it.

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
signing, simulators. Unless the Mac's owner chooses a VM for each job (see
"A macOS VM per job" below), such a job does not run in a VM. It runs on a member's
Mac itself, as a separate local user named `hiveworker`, which keeps the
owner's files and keychain out of reach and nothing more: the job can use the
Xcode and other software installed on that Mac, and its network. It is the
same trust model as any self-hosted macOS runner, and so two people have to
agree before one runs:

- The Mac's owner runs `hived macos setup`. With sudo, it creates the
  `hiveworker` user (hidden, no usable password, in the `_developer` group),
  writes one rule to `/etc/sudoers.d/hiveworker` that lets the owner run
  exactly `hived macos-job` as that user, downloads GitHub's macOS runner
  (`native.RunnerVersion`, pinned with its checksum) and unpacks it as
  root's in `/Library/Application Support/Hive Runner/runner`, and turns on
  the `macos-jobs` setting. `hived macos status` says what is in place;
  `hived macos remove` undoes it all. The daemon says `macos: true` in its
  hello only while the setting is on and the Mac is ready.
- An owner of the hive turns on "Allow macOS jobs" in its settings
  (`pool_settings.macos_jobs`). Until then the coordinator advertises no
  capacity for the label, creates no scale set for it, and answers capacity
  probes for it with no.

`hiveworker` is in the `staff` group, as every user on a Mac is, so the
owner's files are out of its reach only while the owner's home folder is
closed to `staff` and to everyone. `hived macos setup`, `hived macos status`
and `hived doctor` say when it is not; `chmod 700 ~` closes it.

The label's kind is `macos` in `@workspace/pricing`, beside the two
architectures, and `machineServes` there is the one place that decides which
machine is which kind; the coordinator's `serves()` calls it. A job is billed as 4 vCPUs, the size
of GitHub's standard macOS runner, and compared with GitHub's price for it.

One macOS job runs at a time. Each gets an APFS clone of the runner
directory, GitHub's runner in it with a clean environment and its JIT config
in `ACTIONS_RUNNER_INPUT_JITCONFIG` rather than on its command line, and the
clone is deleted afterwards. No job can change the runner the next one
clones: it is root's. Before and after each job every process of the
`hiveworker` user's is stopped and its crontab removed, so nothing a job
started runs on into the next; if one will not stop, the Mac declines macOS
jobs for ten minutes. What the job leaves in the `hiveworker` user's home
(CocoaPods, npm and Xcode caches) stays for the next job. With the owner's
`spotlight-exclude` on, the default, the jobs and those caches are in
`~hiveworker/hive.noindex`, which Spotlight skips; `daemon/README.md` has the
layout. The owner installs what every job needs, typically CocoaPods
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

#### A macOS VM per job (experimental: not yet run on real hardware)

> **Experimental.** None of this has yet booted on a real Mac, and it is off
> unless the owner turns it on: `macos-isolation` is `user` by default. Two
> known gaps remain before it is more than that: the VMs share one vmnet
> network (so one runs at a time unless the owner sets `max-macos-vms 2`),
> and results come back through a writable shared folder rather than a
> virtio-vsock socket.

> **Secrets in the image.** Every job is root in a clone of the image, so
> anything left in it is every job's to read. Never sign in to an Apple
> Account or iCloud in the image; get Xcode through `--share` from a `.xip`,
> not the App Store. Give `runner` a random password you use nowhere else,
> since its hash is in every clone. Leave no keys, tokens or keychain items
> in it. `hived macos image open` and `seal` say this too.

A Mac's owner can have each `hive-macos` job run in a throwaway macOS VM
instead, so nothing one job leaves behind, a process still running or a
file in a home folder, is there when the next one holds a signing key:

```sh
hived config set macos-isolation vm    # user, the default, is the hiveworker user
```

The label, the hive's "Allow macOS jobs" switch and the coordinator are the
same; only the Mac decides, and it can change its mind for the jobs it is
offered next. The VM
is a clone of an image the owner makes once, from a macOS restore image
(IPSW) they already have on the Mac. Nothing is downloaded but GitHub's
runner:

1. `hived macos image create --ipsw <file>` installs macOS into a new image
   (a sparse disk, 150 GB unless `--disk-gb` says otherwise, the auxiliary
   storage and the hardware model the IPSW asks for), beside the Linux
   image in `data-dir`.
2. `hived macos image open --share <folder>` starts the image itself in a
   window. In it the owner goes through Setup Assistant with an admin user
   named `runner`, with a random password, no Apple Account, and FileVault
   off, has `runner` log in automatically,
   installs Xcode (from a `.xip` in the shared folder, or copied from the
   Mac's own `/Applications`), accepts its licence and finishes its first
   launch, downloads the iOS platform, installs what every job needs
   (CocoaPods, Node 24 with corepack, Java 17), runs the guest agent's
   installer from `/Volumes/My Shared Files/hive`, and shuts the VM down.
   The installer also turns Remote Login, Screen Sharing, Remote Management
   and File Sharing off and has the firewall block every incoming
   connection. `hived macos image help` lists the commands.
3. `hived macos image seal` reads the whole image and takes its digest
   (sha256 over the disk, the auxiliary storage and the hardware model),
   and puts GitHub's runner, the version `hived` pins, beside it. A job
   runs only from a sealed image. Its files' sizes and modification times
   are kept with the digest, and a job refuses an image whose files no
   longer match them until it is sealed again: that catches the image
   opened and not sealed after, not a change made to look like none.
   Moving `data-dir` to another disk changes them too, so seal it again
   after.
4. `hived config set macos-jobs on`.

For each job hived makes an APFS clone of the disk and the auxiliary
storage, gives the VM a machine identifier of its own, and shares two
directories with it. One it can only read: GitHub's runner, checked against
the checksum `hived` pins, the job's one-time runner configuration, removed
once the agent has read it, and the image's digest. Results are read
through the other, without following links, as regular files with a small
size cap, and a VM that leaves more than a few small files there is
stopped. That is looked at once a second; macOS has no cheap quota for one
directory, so the free space on the Mac's disk is the real limit. The agent, a launch daemon in the
image, waits for `runner`'s automatic login, unpacks the runner onto the
VM's own disk and runs it as `runner` in that login session (which
simulators, the keychain and Xcode want), with `HIVE_IMAGE_DIGEST` set to
the digest, so a job's log can record which image built and signed it.

The digest names an image; it does not vouch for one. The Mac computes it
itself, and its owner, or anything running as them, can change the image
and seal it again. It tells an honest Mac's jobs apart by image, and no
more. The runner is not part of it. When the runner exits the agent
writes its exit code back and shuts the VM down; hived then deletes the
clone, whether the job passed, failed, timed out or was released, and logs
`macOS VM deleted` with the digest. A VM whose agent has not said it is
ready within five minutes is stopped and the job fails, rather than holding
the Mac until its timeout.

A VM has 4 vCPUs and 8 GB, or half the Mac's memory if that is less, and
the Mac's NAT network: out to the internet and the Mac's own network. The
two VMs that can run at once are on the same NAT subnet, with each other and
with the Mac, and every clone has the same `runner` user and password. So
the image keeps nothing listening, as its installer leaves it: no sharing
services, and a firewall that blocks every incoming connection. Nothing on
the host keeps the VMs apart on the network yet.
Apple's macOS licence allows two macOS VMs on a Mac besides its own macOS.
A Mac runs one at a time unless its owner says
`hived config set max-macos-vms 2`, because the two would share the
network above, on which one job, root in its VM, can put itself between the
other and its gateway. It declines one past that with that reason, whatever
`max-jobs` says. A Mac cannot say how much memory is free, so where it runs
macOS VMs, or its owner keeps a `reserve-memory`, jobs are admitted against
a plan instead: the Mac's memory, less the reserve and the margin, must hold
every job's guest, running or accepted. Whether a job runs in a VM is decided when
it is offered: changing `macos-isolation` changes the jobs offered after,
not one already accepted. A job can report any exit code, so one saying
GitHub refused its runner as too old (exit 7) is believed only when
GitHub's latest runner is newer than the one `hived` pins; then the Mac
declines macOS jobs for an hour, and asks again the next time. A daemon that starts clears any clone one that died
left behind, whichever way it runs macOS jobs. A job is declined, too, when the Mac does
not have the VM's memory free beside its reserve. Simulators run inside the
VM; an Apple Silicon Mac needs no nested virtualization for them.

Not done yet: the Actions cache is not offered to these VMs (their jobs use
GitHub's), there is no read-only shared cache of pnpm, CocoaPods or Gradle
stores, the VM's console and a stop's reason are not written into the job's
log, and no image is published: each owner makes their own.

Jobs install their own toolchains with `setup-*` actions; the system's
`python3` is there for scripts, and gcc, g++ and make for cgo, Rust's
linker and native modules, as on GitHub's runners. Docker is in the
VM, with buildx and compose, for `docker` in a step, `services:` and
`container:`; a `container:` image needs Perl or to be Alpine (see "What it
does not do yet" in `daemon/README.md`). Every job starts from a clean disk,
with no Docker images on it. What
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
and three idempotent ledger entries. A balance $5 below zero (the overdraft) stops new acquisition;
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

The [integrated smoke run](https://github.com/thecomputerplumbers/hiverunner/actions/runs/37178137715)
passed on this Mac using a warm `hive` VM: checkout, arm64/CPU/user checks,
setup-go and Linux daemon tests. The local coordinator verified GitHub's
timestamps, signed the receipt and produced three ledger entries summing to
zero. This experiment used isolated local accounting data, with no deployment.

## Capacity routing with GitHub-hosted fallback

Workflows do this with one step, the public action
[`route`](../route)
(`uses: thecomputerplumbers/hiverunner/route@v1`, inputs `label`, `fallback` and
`wait`). Its source lives in that repository, not this one. What it does is
what this section describes.

GitHub Actions can run a preflight job on `ubuntu-latest` and pass its selected
runner through a job output to `runs-on`. A workflow can poll
`GET https://connect.hiverunner.dev/v1/capacity?label=hive-4cpu`
for up to 15 seconds, then select `ubuntu-latest` if no machine is available.
An available machine ends the probe immediately. The polling deadline begins
after the preflight runner starts; it does not include GitHub runner startup.

Authorize the request with a GitHub Actions OIDC bearer token, requested with
audience `https://connect.hiverunner.dev`. The preflight job
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
is no automatic reroute after a job is queued on Hive Runner.
