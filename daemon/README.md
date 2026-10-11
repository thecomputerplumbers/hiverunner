# hived

The HiveWorker daemon. One static Go binary that enrolls a machine in its
organization's hive and holds the single outbound tunnel jobs arrive through.
It runs on Apple Silicon Macs and on Linux with KVM, arm64 or x86-64.

```sh
./image/build.sh    # once: builds the guest image (needs Docker)
./build.sh          # builds ./hived, signed so macOS lets it create VMs
./hived selftest    # boots a throwaway VM and reports what it finds
./hived login       # sign in with GitHub in the browser, enroll this machine
./hived link        # add this machine to another of your hives
./hived run         # connect to every hive it serves and wait for jobs
./hived status      # what this machine lends, whether it is lending, and
                    # whether it is connected; --json for a script
./hived doctor      # whether it keeps taking jobs unattended, and reaches the server
./hived stop        # stop the installed service; start and restart too
./hived config      # the owner's settings, below
./hived pause       # stop taking jobs; ./hived resume to undo
./hived update      # move to the current release; the daemon does this itself when idle
./hived benchmark
```

On a Mac use `./build.sh`, not a bare `go build`: creating a virtual machine
needs the `com.apple.security.virtualization` entitlement, and an unsigned
binary is refused by macOS. On Linux a plain `go build ./cmd/hived` will do.

`HIVE_SERVER` points it at another server (`http://localhost:3000` for local
development) and `HIVE_CONFIG` at another machine file.

## One machine, several organizations

A machine belongs to its owner, not to a pool. `hived login` says who that
is and enrolls the machine, once; someone in several organizations chooses
there which of them it serves. After that `hived link` adds it to another
hive and `hived unlink` takes it out of one, as the dashboard also can,
without touching the machine otherwise: the running daemon asks the server
every minute and opens or closes a tunnel per hive to match. Linking is
approved in a browser like the first sign-in, because the machine token
alone must not be able to bring another organization's jobs to the machine.
Running `hived login` on a machine that is signed in changes nothing and
says to use `hived link`. Its job slots, one unless its owner allows more, are one
budget across all of them.

## Installing and removing

Developers install with one command, which is `release/install.sh` served
from the releases bucket:

```sh
curl -fsSL https://get.hiveworkr.thecomputerplumbers.com/install.sh | sh
```

It checks the machine, downloads the daemon and the guest image and verifies
both, installs under the home directory, runs `hived login`, and starts a
per-user service: a launch agent on a Mac, a systemd user unit on Linux.
Running it again updates. `hived uninstall` removes
the service, the image, the Actions cache, the settings, the sign-in and the
binary, after asking.

Building and publishing a release is in `docs/deployment.md`.

## On Windows

Jobs are Linux, so on Windows the daemon is the Linux x86-64 one, inside a
Linux of its own under WSL 2, which on Windows 11 passes hardware
virtualization through. `release/install.ps1` does the setup:

```powershell
irm https://get.hiveworkr.thecomputerplumbers.com/install.ps1 | iex
```

It imports Ubuntu 24.04 as a distribution named `HiveWorker`, separate from
any other, installs QEMU, virtiofsd and passt in it, turns on systemd, makes
`/dev/kvm` usable by the daemon's user at each start, runs `install.sh`
inside, and adds a per-user Run entry that keeps the distribution alive
while the person is signed in (WSL stops one that nothing is attached to).
It also puts a `hived` command on the Windows PATH that calls the one
inside. It asks for no administrator rights; if WSL itself is missing it
prints the one command that adds it and stops. Setting
`$env:HIVE_UNINSTALL = '1'` first removes everything.

The command is for a PowerShell window. Started from `cmd` or over SSH, as
`powershell -Command "irm ... | iex"`, Microsoft Defender takes that command
line for a downloader and refuses to start it. There, download the script and
run the file:

```powershell
curl.exe -fsSLo install.ps1 https://get.hiveworkr.thecomputerplumbers.com/install.ps1
powershell -ExecutionPolicy Bypass -File install.ps1
```

Over SSH the script cannot keep the distribution alive itself, since what an
SSH session starts ends with it. It has Windows start that on the desktop of
the person signed in, through a scheduled task it removes straight after; if
no one is signed in, the daemon starts at the next sign-in.

## On Linux

The same daemon and the same jobs, on arm64 and on x86-64. An arm64 box runs
the image a Mac does; an x86-64 box runs one built for it
(`GUEST_ARCH=x64 ./image/build.sh DIR`), and takes the jobs whose label asks
for x64. A machine says which it is when it connects, and is only ever
offered jobs of its own architecture. What differs from a Mac is what a VM is
made of. KVM does the virtualization, and three ordinary
programs stand around it, all running as the daemon's own user:

- **QEMU** is the machine. It boots the image's kernel directly, on a qcow2
  overlay of the image's disk: instant on any filesystem, holding only what
  the job writes, deleted with the job.
- **virtiofsd** serves the job's one directory to the guest, from inside a
  namespace that contains nothing else.
- **passt** is the guest's network: out through the user's own sockets,
  nothing in, and no address that reaches the host's loopback.

The installer does not install QEMU or passt and never asks for a password.
It says what is missing and how to get it (`sudo apt install qemu-system
qemu-utils passt` on Debian, Ubuntu or Raspberry Pi OS). virtiofsd is the
exception: Debian 12 and Raspberry Pi OS 12 package only the older one that
came inside QEMU and must be root, so where the system has no virtiofsd that
runs as an ordinary user the installer downloads HiveWorker's own static
build of it (`release/virtiofsd.sh`) into `~/.config/HiveWorker/bin`, and the
daemon looks there first. The user must be able to open
`/dev/kvm`, which usually means being in the `kvm` group. A cloud VM has no
KVM unless its provider offers nested virtualization; bare metal and home
servers do.

The guest reaches the Actions cache over `vhost-vsock`. Every VM on a Linux
host dials the same host socket, so the daemon tells them apart by the
address the kernel assigned each VM, which a guest cannot choose, and refuses
any caller it did not start. That needs the `vhost_vsock` module and a user who may open `/dev/vhost-vsock`,
which is often the `kvm` group's alone even where `/dev/kvm` is lent to
whoever is logged in. Without either, jobs run and use GitHub's cache.

A machine with a battery follows the same rules as a Mac: jobs on battery
unless the owner says otherwise, none with the lid shut unless `lid` is on. A machine with
neither is always available.

Files are where Linux keeps them: `~/.config/HiveWorker` for the image, the
settings and the sign-in, `~/.cache/HiveWorker` for the Actions cache, and
the journal for the log (`journalctl --user -u hiveworker`).

## The owner's settings

The machine's owner decides what it lends. `hived config` shows the settings
and `hived config set <name> <value>` changes one:

| Setting      | Values                | What it does                                     |
| ------------ | --------------------- | ------------------------------------------------ |
| `max-jobs`   | `1` to `8`            | How many jobs run at once                        |
| `max-vcpus`  | `2`, `4`, `8`, `auto` | The largest job this Mac takes                   |
| `warm`       | `on`, `off`           | Keep one VM booted so a `hive` job starts sooner |
| `battery`    | `on`, `off`           | Take jobs while unplugged                        |
| `lid`        | `on`, `off`           | Take jobs with the lid closed                    |
| `hours`      | `22:00-07:00`, `any`  | When jobs may start, in local time               |
| `cache`      | `on`, `off`           | Keep an Actions cache on this Mac                |
| `cache-size` | `1` to `2000`, `auto` | The most disk the cache may use, in gigabytes    |
| `data-dir`   | a path, `default`     | Where the image and each job's disk are kept     |
| `cache-dir`  | a path, `default`     | Where the Actions cache is kept                  |

`hived pause` and `hived resume` stop and restart lending altogether. A job
already running always finishes.

When the machine is not taking jobs, `hived status` says why and what to do
about it: `lid closed`, then `open the lid or run hived config set lid on`.

## On another disk

What takes room can be kept somewhere else, such as an external drive on a
Mac with a small one of its own:

```sh
./hived config set data-dir /Volumes/Big/HiveWorker    # the image, and each job's disk
./hived config set cache-dir /Volumes/Big/HiveWorker   # the Actions cache
./hived config set data-dir default                    # back where it was
```

Each names a home, and hived keeps a directory of its own inside it (`image`
and `image-jobs`, `actions-cache`), so both may name the same place. What is
already there is moved, not downloaded again: at once if no daemon is
running, and otherwise by the daemon, which stops taking jobs, waits for the
ones it has, moves, and starts itself again. Until then everything carries on
from the old place, and `hived config` says a move is waiting. The image and
the job disks go together because a job's disk is a clone of the image, which
only works within one volume. The installer asks the daemon where the image
is kept, so an update lands there too.

The machine's token and its settings do not move. They stay in the home
directory, where the file's permissions mean something; an external disk
often has ownership switched off.

A disk that is not attached is not stood in for. hived makes nothing in its
place, takes no jobs (or runs them on GitHub's cache, if it is the cache that
is missing), says so in `hived status` and `hived doctor`, and starts itself
again when the disk is back. `HIVE_IMAGE` and `HIVE_CACHE` still come first,
for development.

## A machine nobody sits at

`lid` is off unless the owner turns it on, because a closed laptop normally
sleeps, and a sleeping machine takes no jobs and says nothing. With `lid on`
the daemon stops reading the lid, and the machine has to be kept awake:

```sh
./hived config set lid on
sudo pmset -a disablesleep 1   # macOS: pmset -g then shows SleepDisabled 1
```

On Linux the same thing is `HandleLidSwitch=ignore` and
`HandleLidSwitchExternalPower=ignore` in `/etc/systemd/logind.conf`. With
`lid on` and sleep not disabled, `hived config set lid on`, `hived status`
and the daemon's log at start all say so, rather than the machine looking
healthy until its lid closes.

`hived doctor` checks everything that stops a machine taking jobs after a
restart or with its lid closed, says how to fix each, and exits 1 if anything
is wrong:

| Check           | What it wants                                                                                |
| --------------- | -------------------------------------------------------------------------------------------- |
| `sign-in`       | The machine is in a hive                                                                     |
| `service`       | The background service is installed                                                          |
| `jobs`          | Nothing is holding jobs now: not paused, in its hours                                        |
| `power`         | Plugged in                                                                                   |
| `storage`       | A disk named by `data-dir` or `cache-dir` is attached                                        |
| `lid`           | `lid on`, on a machine with a lid                                                            |
| `sleep`         | macOS: `SleepDisabled 1` on a MacBook, `sleep 0` on a desktop. Linux: logind ignores the lid |
| `power cut`     | macOS desktops: starts up again by itself (`autorestart 1`)                                  |
| `auto-login`    | macOS: the user hived runs as is logged in at start-up, since hived is a launch agent        |
| `FileVault`     | macOS: off, since a restart otherwise stops at the unlock screen                             |
| `start at boot` | Linux: lingering is on for the user, so the service starts without a login                   |

FileVault, and the auto-login it rules out, trade the disk's protection for
coming back unattended, and running on battery with `battery on` is allowed.
`hived doctor` says these and does not count them: only what is certainly
wrong makes it exit 1. A MacBook closed with a display and power attached
stays awake without `SleepDisabled`, so there the warning says it is awake
only while they are attached. The daemon repeats the sleep warning in its log
when `lid` is turned on while it runs.

## Updating

Once a day, at a random minute, the daemon reads `latest.json` from the
releases bucket, sending the ETag it remembers so an unchanged manifest costs
one small request. When a newer release is out it says so once per version:
in its log, in `hived status`, and as a desktop notification. Then, unless the
owner has set `auto-update off`, it stops taking new jobs, waits for the ones
it has, and runs the installer in its own session (the service manager stops
the daemon's process group when the installer restarts the service, and the
installer must outlive that). The installer checks every download against its
published checksum, as at first install. `hived update` does the same at once,
from the terminal. A daemon run from anywhere but `~/.local/bin/hived` is a
developer's build: it is told about releases and never replaced by one. The
state is `update.json` beside `machine.json`.

The settings live in `settings.json` beside the machine file and can be edited
by hand. A running daemon reads the file again when it changes, so nothing
needs restarting; a file it cannot read is reported and the last good settings
stay in force. Outside its hours, on battery with `battery` off, with its lid
closed and `lid` off, or paused, the machine reports `paused` and the hive offers it nothing. The limits on job
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
backups skip, unless `cache-dir` says otherwise. Losing any of it is a cache miss and nothing more.

## What it keeps

One file, `~/Library/Application Support/HiveWorker/machine.json` (on Linux,
`~/.config/HiveWorker/machine.json`), readable
by its owner only: the server, the machine id and the machine token. Which
hives it serves is not stored; the server is asked. The GitHub sign-in is
used once to obtain that token and is not kept. Moving the token to the
Keychain is planned.

## How a job runs

Each job gets its own Linux virtual machine, under Apple's
Virtualization.framework on a Mac and under KVM on Linux, and the machine is
destroyed when the job ends.

1. The coordinator offers a job (`job.assign`). The daemon takes a free job
   slot and accepts, or declines if it has none or not the cores.
2. The coordinator sends GitHub's one-time runner configuration (`job.start`).
3. The daemon clones the guest disk (copy-on-write, instant on APFS; an
   overlay on Linux), creates
   an empty directory for the job, writes the configuration into it, and boots
   the VM by direct kernel boot. Boot to running is about three seconds.
4. The guest's agent reads the configuration, deletes it, and runs GitHub's
   runner as an unprivileged user. The runner talks to GitHub directly.
5. The guest writes its exit code and powers off. The daemon deletes the disk
   and the directory and reports `job.exited`.

What the guest can reach: outbound, whatever the machine it runs on can.
That is the internet, and it is also the machine's own network: other
machines on its LAN, and the peers of a VPN or tailnet the machine is on.
Nothing here narrows that to the internet, so a job has the network position
of the machine that lends it. On Linux the guest is given the machine's own
address and routes by passt, which makes each of the guest's connections as
the daemon's user; hived tells it not to map the machine's loopback into the
guest. On a Mac the guest is behind the system's NAT. Nothing reaches in on
either.

Names are resolved by the machine's resolvers, except the ones only the
machine can use. A resolver on its loopback is out of the guest's reach.
Tailscale's (100.100.100.100 and its IPv6 twin) answers only until a job
starts a Tailscale of its own, which takes that address over, and then
public names stop resolving mid-job. In place of either the guest gets the
machine's other resolvers, or the ones behind systemd-resolved's stub, or
failing those Cloudflare's and Google's. On Linux the guest is not given the
machine's search domains: a job resolves the names it wrote. On a Mac the
system's NAT answers the guest's questions itself, at an address no
Tailscale claims, and does pass the Mac's search domains on.

What it can see of the host's files: that one directory. Nothing listens in
the guest and nothing can log in to it. A released job, or one past its
timeout, has its VM stopped at once.

The guest image is Ubuntu 24.04 arm64 with GitHub's runner, pinned by the hash
GitHub publishes. It has git, curl, sudo, jq, the usual archive tools and
the system's `python3` (3.12, with `venv`); jobs install toolchains with
`setup-*` actions, as they would anywhere.

It also has Docker: the engine, `docker buildx` and `docker compose`, from
Docker's own repository, as GitHub's runners have. The runner's user is in
the `docker` group, so a job uses it without `sudo`, and `services:`,
`container:` and actions that are themselves containers have what they need.
Nothing of it runs until it is used: only the socket is there at boot, and
the engine starts when a job first speaks to it, in under a second, so
a job that never does waits for nothing. Images are pulled onto the job's
own disk and go when the VM does. Docker's bridge takes 172.17.0.0/16, or
the next range along when the machine's own network is already there.
`hived selftest` runs a container in the VM and has it reach GitHub, and says
`DOCKER=ok`.

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

- **Job containers without Perl.** In a `container:` job the runner starts
  each JavaScript action with its own Node, which here stands behind
  `image/hive-node`, a Perl script. Debian and Ubuntu images always have
  Perl, and Alpine ones are given a Node of their own that is not wrapped;
  an image with neither, such as Fedora's, cannot run `actions/checkout`.
- **Docker layers between jobs.** Each job pulls and builds from nothing,
  unless it keeps its layers in a registry or in `actions/cache`.
- **A pre-registered runner.** The runner still registers with GitHub when the
  job arrives, which is most of what is left of the start-up time.
- **Caches between jobs.** Each job starts from the same clean disk.
- **Throttling for the owner.** It pauses with the lid closed (unless `lid`
  is on), but does not yet slow a running job when the owner is busy.
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
boots real VMs and needs the guest image; it runs on a Mac and on Linux with
KVM. A Linux host that is itself a VM boots guests several times slower, and
`HIVE_VM_TEST_PATIENCE=4` stretches the tests' time limits to allow for it. `pnpm test:daemon`
from the repository root runs the built binary against the real web and
coordinator Workers on a fresh local database (macOS only).
