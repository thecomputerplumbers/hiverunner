# hived

The HiveWorker daemon. One static Go binary that enrolls a Mac in its
organization's hive and holds the single outbound tunnel jobs arrive through.

```sh
./image/build.sh    # once: builds the guest image (needs Docker)
./build.sh          # builds ./hived, signed so macOS lets it create VMs
./hived selftest    # boots a throwaway VM and reports what it finds
./hived login       # sign in with GitHub in the browser, enroll this machine
./hived run         # connect to every hive it serves and wait for jobs
./hived status      # what this Mac lends, and whether it is lending
./hived config      # the owner's settings, below
./hived pause       # stop taking jobs; ./hived resume to undo
./hived benchmark
```

Use `./build.sh`, not a bare `go build`: creating a virtual machine needs the
`com.apple.security.virtualization` entitlement, and an unsigned binary is
refused by macOS.

`HIVE_SERVER` points it at another server (`http://localhost:3000` for local
development) and `HIVE_CONFIG` at another machine file.

## One machine, several organizations

A machine belongs to its owner, not to a pool. At `hived login` someone in
several organizations chooses which of them the machine serves, and can add or
remove hives later from the dashboard without touching the machine: the
running daemon asks the server every minute and opens or closes a tunnel per
hive to match. Its job slots, one unless its owner allows more, are one
budget across all of them.

## Installing and removing

Developers install with one command, which is `release/install.sh` served
from the releases bucket:

```sh
curl -fsSL https://get.hiveworkr.thecomputerplumbers.com/install.sh | sh
```

It checks the Mac, downloads the daemon and the guest image and verifies
both, installs under the home directory, runs `hived login`, and starts a
per-user launch agent. Running it again updates. `hived uninstall` removes
the service, the image, the Actions cache, the settings, the sign-in and the
binary, after asking.

Building and publishing a release is in `docs/deployment.md`.

## The owner's settings

The machine's owner decides what it lends. `hived config` shows the settings
and `hived config set <name> <value>` changes one:

| Setting      | Values                | What it does                                     |
| ------------ | --------------------- | ------------------------------------------------ |
| `max-jobs`   | `1` to `8`            | How many jobs run at once                        |
| `max-vcpus`  | `2`, `4`, `8`, `auto` | The largest job this Mac takes                   |
| `warm`       | `on`, `off`           | Keep one VM booted so a `hive` job starts sooner |
| `battery`    | `on`, `off`           | Take jobs while unplugged                        |
| `hours`      | `22:00-07:00`, `any`  | When jobs may start, in local time               |
| `cache`      | `on`, `off`           | Keep an Actions cache on this Mac                |
| `cache-size` | `1` to `2000`, `auto` | The most disk the cache may use, in gigabytes    |

`hived pause` and `hived resume` stop and restart lending altogether. A job
already running always finishes.

The settings live in `settings.json` beside the machine file and can be edited
by hand. A running daemon reads the file again when it changes, so nothing
needs restarting; a file it cannot read is reported and the last good settings
stay in force. Outside its hours, on battery with `battery` off, or paused, the
machine reports `paused` and the hive offers it nothing. The limits on job
size and job count are sent to the hive in `hello`, so the hive neither offers
past them nor counts the machine as capacity it does not have; changing one
reconnects the tunnel once the machine is idle.

Jobs running at once share the Mac: together they never take the last two
cores, and each gets its own VM, memory and disk. A machine serving several
hives is the judge of its own limits, because one hive cannot see another's
jobs: an offer that would not fit is declined. The speed score was measured
on an idle machine, so several jobs at once may each run slower than it says.
The warm VM is only kept while no job is running.

## The Actions cache

A job's `actions/cache` step, and every action built on the same library
(`setup-node`, `setup-go` and the rest), saves to and restores from this Mac
instead of GitHub. Workflows do not change. A restore is a copy from local
disk, and nothing counts against GitHub's cache quota.

```sh
./hived cache                  # what it holds, by repository
./hived cache clear            # delete it all
./hived cache clear 1403685691 # delete one repository's, by its id
./hived config set cache-size 40
./hived config set cache off
```

**Whose cache.** One cache per repository, inside one pool. A job proves
which repository it is with the token GitHub gives it, and hived checks that
token's signature against GitHub's published keys before believing it. The
hive is not taken from the job at all: it is the hive whose job hived put in
that VM. So a job cannot reach another repository's cache, and a machine
serving two organizations keeps their caches apart. Within a repository,
caches are scoped by branch as GitHub scopes them: a job reads its own
branch's caches and the default branch's, and saves only to its own.

`hived cache` lists repositories by GitHub's numeric id: that is all the
job's token says about which repository it is.

**Each Mac builds its own.** The cache is on the machine, not in the pool. A
job that lands on a Mac that has not run that repository starts cold and
leaves a cache behind for the next one.

**How a VM reaches it.** Each VM gets a socket that exists only between it
and the host: on no network, and so not subject to the Mac's firewall. A
relay in the guest gives that socket an address on the guest's own loopback.
The runner tells each action where GitHub's results service is; a shim in
front of the runner's Node swaps that one address for the relay's, and only
when the cache has answered a health check. hived answers the cache calls
and passes everything else, artifacts above all, on to GitHub untouched.

If any part of that is missing, a job uses GitHub's cache exactly as before:
an older guest image, the cache switched off, a runner that updated itself
and replaced its Node, or a results address other than github.com's.

**Disk.** `cache-size` caps it; `auto` is a tenth of the disk, between 10 and
50 GB. Past the cap, the caches wanted longest ago go first. A save that
would leave the Mac under 5 GB free is refused, and the job carries on
without saving. The files are under `~/Library/Caches/HiveWorker`, which
backups skip. Losing any of it is a cache miss and nothing more.

## What it keeps

One file, `~/Library/Application Support/HiveWorker/machine.json`, readable
by its owner only: the server, the machine id and the machine token. Which
hives it serves is not stored; the server is asked. The GitHub sign-in is
used once to obtain that token and is not kept. Moving the token to the
Keychain is planned.

## How a job runs

Each job gets its own Linux virtual machine under Apple's
Virtualization.framework, and the machine is destroyed when the job ends.

1. The coordinator offers a job (`job.assign`). The daemon takes a free job
   slot and accepts, or declines if it has none or not the cores.
2. The coordinator sends GitHub's one-time runner configuration (`job.start`).
3. The daemon clones the guest disk (copy-on-write, instant on APFS), creates
   an empty directory for the job, writes the configuration into it, and boots
   the VM by direct kernel boot. Boot to running is about three seconds.
4. The guest's agent reads the configuration, deletes it, and runs GitHub's
   runner as an unprivileged user. The runner talks to GitHub directly.
5. The guest writes its exit code and powers off. The daemon deletes the disk
   and the directory and reports `job.exited`.

What the guest can reach: the internet, outbound, through NAT. What it can see
of the Mac: that one directory. Nothing listens in the guest and nothing can
log in to it. A released job, or one past its timeout, has its VM stopped at
once.

The guest image is Ubuntu 24.04 arm64 with GitHub's runner, pinned by the hash
GitHub publishes. It has git, curl, sudo and the usual archive tools; jobs
install toolchains with `setup-*` actions, as they would anywhere.

A VM of the default size is kept booted while the lid is open, including on
battery, so a job of that size starts in under a second instead of about
three. It is replaced after each job.

## Keeping the guest image current

The guest pins a version of GitHub's runner. GitHub stops accepting old
versions, and a runner it refuses does not update itself: it exits without
taking the job, which stays queued. Measured on 2026-10-04:

| Guest runner | Released   | What happened                                          |
| ------------ | ---------- | ------------------------------------------------------ |
| 2.337.0      | 2026-08-26 | Current. Ran the job                                   |
| 2.336.0      | 2026-07-20 | Ran the job, without updating itself                   |
| 2.335.0      | 2026-06-08 | Refused. The runner exited in five seconds with no job |

The daemon recognises the refusal (the runner exits 7). It logs that the image
must be rebuilt, reports the machine as paused so the hive stops offering it
jobs, and declines any offer with that reason. It stays that way until it is
restarted on a new image: bump `RUNNER_VERSION` and its hash in
`image/build.sh`, run it, and restart `hived`.

There is no way to know in advance that a version has been retired, and its
age is not a guide: the current release is over a month old and valid. So
rebuild when GitHub publishes a runner release, rather than waiting for the
refusal.

If the daemon is killed, or the Mac loses power, a job's disk and directory
are left behind. The next `hived run` removes them before it starts anything.
It can do that safely because every command that boots a VM (`run`,
`selftest`, `dev-job`) first takes an exclusive lock on the work directory,
which sits beside the guest image and is named after it. A second command on
the same directory exits, saying it is in use; stop the daemon to run a
self-test against its image.

## What it does not do yet

- **Container jobs.** `container:` and `services:` need Docker in the guest.
- **A pre-registered runner.** The runner still registers with GitHub when the
  job arrives, which is most of what is left of the start-up time.
- **Caches between jobs.** Each job starts from the same clean disk.
- **Throttling for the owner.** It pauses with the lid closed,
  but does not yet slow a running job when the owner is busy.
- **A menu bar item, and the Keychain** for the machine token.

## Layout

| Package             | Holds                                                        |
| ------------------- | ------------------------------------------------------------ |
| `cmd/hived`         | The command                                                  |
| `internal/api`      | Device sign-in and enrollment over HTTPS                     |
| `internal/tunnel`   | The reconnecting WebSocket to the coordinator                |
| `internal/protocol` | Tunnel messages, tested against `packages/protocol/fixtures` |
| `internal/host`     | Hardware facts, power and lid state                          |
| `internal/bench`    | The speed score benchmark                                    |
| `internal/config`   | The machine file                                             |

## Tests

`go test ./...` runs everywhere, against stand-in servers. `./test-vm.sh`
boots real VMs and needs the guest image. `pnpm test:daemon`
from the repository root runs the built binary against the real web and
coordinator Workers on a fresh local database (macOS only).
