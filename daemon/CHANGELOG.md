# hived changes

What an owner needs to know about a release of the daemon. Releases are
numbered in `daemon/VERSION` and published by the Release workflow; machines
with `auto-update` on take them when idle.

## Unreleased

### macOS jobs run again as the hiveworker user

From 0.4.31, a Mac that runs `hive-macos` jobs as the `hiveworker` user ran
none of them (#132). Before each job, the daemon stops every process
`hiveworker` has. It did that with `kill(-1)`, which on a Mac signals the
caller too, so it stopped or killed itself. When stopped, it sat in state T
for good, and the job waited on it. The job's slot stayed taken, so the Mac
took no other macOS job until hived was restarted. Its runner never
started, and the coordinator let the job go three minutes later.

Now each process is stopped by its pid, never the clean's own. The daemon
also gives up on a clean that will not end, so the slot is freed.

### macOS jobs keep running across updates: run `hived macos setup` once more

`hived macos setup` now puts the hived that `hiveworker` runs, and GitHub's
runner, in `/Library/Application Support/Hive Runner`. That folder is
root's, and the `bin` and `runner` folders inside it are the owner's. No job
can change them, and the daemon keeps them current itself as it starts,
with no sudo. An update, even one that brings a new runner, no longer turns
a Mac's macOS jobs off until its owner runs setup again (#130). The sudo
rule now names `…/Hive Runner/bin/hived`, outside the owner's home folder,
so `chmod 700 ~` no longer stops macOS jobs (#131).

A Mac set up by an older hived keeps taking macOS jobs as it did.
`hived macos status`, `hived status` and `hived doctor` ask its owner to run
`hived macos setup` once more, and to close their home folder only after
that.

Also:

- When `macos-jobs` is on and the Mac cannot take macOS jobs, the log says
  so as a warning, with the reason, each time that changes. The hive's
  Machines page shows `macOS jobs off: needs its owner`, with the reason on
  hover.
- A release that a Mac must follow with `hived macos setup` says so in its
  manifest. A Mac taking macOS jobs as `hiveworker` does not install such a
  release by itself. It logs why, and `hived status` says the update waits
  for its owner.
- When `hiveworker` cannot open the hived the sudo rule names, for example
  because the home folder is closed, `hived macos status` says that,
  instead of saying the rule is missing.
- `hived macos setup` again accepts Xcode's license and finishes its first
  launch when those are needed. A bug had skipped that step.

### A released job says why

`job released by the coordinator` now carries the coordinator's reason, for
example `its runner did not connect to GitHub within three minutes`, the
job is over, or the run was cancelled. A job released before it started
also says `started=false`.
### A build Mac can give jobs every core: `priority standard`

On a Mac, the daemon runs in macOS's background band, and so does every job
it runs. That keeps jobs on the efficiency cores with the disk throttled.
It suits a laptop lending idle time, but made builds on a Mac kept for
building many times slower (#120). `hived config set priority standard`
runs the daemon, its jobs and its VMs at standard priority instead.
`background` stays the default. The daemon takes up a change by starting
again through launchd once it has no job. `hived status` and `hived doctor`
say which is on, and the doctor suggests `standard` for a Mac that is
plugged in with `lid on`, or a desktop. A Linux machine runs at standard
priority either way.

### Browser tests find what a headless browser needs already installed

Guest image 14 carries the system libraries, fonts and xvfb that Playwright
installs for Chromium on Ubuntu 24.04, so `playwright install-deps
--dry-run chromium` finds nothing to do and a job need not run apt as root
to get them. That step (`install chromium --with-deps`) was the heaviest
moment of a browser-test job and had crashed or hung on hive VMs. The image
is about 325 MB larger unpacked. The browser itself is still the job's to
download or cache, since its revision follows the Playwright version.

### An update cut off halfway no longer leaves the machine half updated

The installer used to put the new daemon in place before it downloaded the
guest image. A download cut off partway (curl's exit 92, an HTTP/2 stream
reset, partway through a 700 MB image) left the new daemon on disk beside
the old image while the old daemon carried on running. Now nothing is
replaced until the daemon and the image are both downloaded and checked, and
then the image and the daemon are swapped in one after the other, each in a
single step.

The image's pieces are fetched over HTTP/1.1, and a piece that is cut off is
picked up where it stopped, up to six tries. The pieces downloaded so far are
kept on the data disk, so the next run carries on from there instead of
starting again. A failed automatic update is tried again an hour later, then
two, then four, and never more than a day apart, instead of a day later
every time.

### A job that does not quite fit runs with a little less memory

A Linux job whose VM would cut into `reserve-memory` at its full size (2 GB
a core) now runs with what fits instead of being declined, down to three
quarters of its size and never under 4 GB. The reserve is kept either way.
The log says `giving a job less memory than its size, to fit what is free`
with `guest_mb` and `size_mb`. Below that floor the job is declined, as
before. A 4-core job on a host with 12.6 GB free and 4 GB reserved used to
be declined forever; it now runs with 7.5 GB.

### A released job no longer says the daemon was shutting down

When the coordinator backs out of a running job, because GitHub cancelled
it, the job's VM was stopped as if the daemon were shutting down: `VM
stopped reason=daemon_shutdown` in the log, and "daemon shutting down" in
the job's own log. It now says `released`.

### The log says more about a job's memory

A job declined for want of memory is logged with what made up the memory it
needed: `guest_mb` for the job's own VM, `held_mb` for jobs already accepted,
`margin_mb` and the owner's `reserve_mb`. `needed_mb` is their sum, not the
size of the job. On Linux, `job ended` also says `peak_guest_mb`, the most
memory the job's VM held at once; it is left out where it cannot be read,
including on a Mac.

### macOS jobs as the hiveworker user: run `hived macos setup` again

A Mac that takes macOS jobs as the `hiveworker` user declines them after
this update until its owner runs `hived macos setup` once more. Setup now
unpacks GitHub's runner as root's in
`/Library/Application Support/Hive Runner/runner`, where no job can change
it, instead of in `hiveworker`'s home; the next clean removes the old copy.
`hived macos status` says when setup is needed.

Also:

- One macOS job runs at a time on a Mac, however many jobs it may take.
- Before and after each job, every process the `hiveworker` user has is
  stopped, and its crontab removed, so nothing a job started runs on into the
  next. If one will not stop, the Mac declines macOS jobs for ten minutes and
  logs what was left.
- The runner reads its JIT config from its environment rather than its
  command line.
- `hived macos remove` also removes the runner.
- `hived macos setup` writes its sudo rule, checks it and puts it in place
  all inside `/etc/sudoers.d`.
- `hived macos setup`, `hived macos status` and `hived doctor` say when the
  owner's home folder can be read or opened by `hiveworker`, and how to close
  it: `chmod 700 ~`.
- `hived drain`, which the updater waits on, counts a macOS job only when the
  daemon started it, and counts jobs in macOS VMs too.

### Actions cache: saves are held to the cache's size as they arrive

Every save in progress counts toward the cache's size, whether or not it said
its length up front, and one cache is at most 10 GB, as on GitHub. A machine
takes at most 64 saves at once, and drops one that has sent nothing for ten
minutes or is two hours old. A save past what the machine will keep fails as
an ordinary cache-save failure; the job goes on.

### Spotlight is kept out of macOS jobs

A `hive-macos` job run as `hiveworker` no longer has Spotlight indexing
everything it writes, which took a steady share of the CPU for the whole
build (#110). With the new `spotlight-exclude` setting, on by default:

- Each job lives in `~hiveworker/hive.noindex`, which Spotlight skips.
- CocoaPods', npm's, pnpm's, Gradle's and Xcode's DerivedData caches are kept
  in `~hiveworker/hive.noindex/cache`, by variable or by symlink. A job's own
  `env:` and `-derivedDataPath` still win.
- `hived macos setup` says how to add `/Users/hiveworker` to Spotlight's
  privacy list, and `hived doctor` has a `spotlight` note. Neither fails.

`hived config set spotlight-exclude off` puts everything back where it was.

### Jobs are kept off the networks around a Linux machine

A job's VM on Linux is now fenced off the networks around the machine that
lends it (#106). This changes what jobs reach on a machine that updates, with
nothing for its owner to do:

- **A machine that updates** and whose owner has not chosen is at
  `network-isolation lan-allow`. Its jobs still reach its LAN (a registry, a
  cache, a database there keeps working), and no longer reach **the machine
  itself, its tailnet (100.64.0.0/10, fd7a:115c:a1e0::/48), link-local
  addresses or a cloud's metadata service**. A job that used something on the
  machine or its tailnet now gets "administratively prohibited".
- **A machine enrolled from now on** starts at `network-isolation block`:
  its jobs reach the internet only.
- hived logs one warning the first time it starts fenced, and `hived config`
  and `hived status` show the setting and whether it is the default.

To choose:

```sh
hived config set network-isolation block      # internet only
hived config set network-isolation lan-allow  # the default after an update
hived config set network-isolation off        # as before: everything
hived config set allow-network 100.101.102.103:5000,192.168.1.20:5432
hived config set dns public                   # Cloudflare's and Google's, not the LAN's resolver
```

`allow-network` refuses `0.0.0.0/0` and `::/0` (that is
`network-isolation off`) and warns about anything wider than an IPv4 /8 or
an IPv6 /32. A Mac is not fenced yet: its VMs are behind the system's NAT.

### hived doctor says how exposed a machine is

`hived doctor` has a Security box: firewall, ssh, sharing, network and macOS
jobs (#104). It runs nothing with sudo unless asked: `hived doctor --sudo`
says it is about to, and then reads the firewall rules and sshd's settings as
root. Without it, an unread firewall says `could not read the rules (needs
sudo)`.
