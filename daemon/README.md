# hived

The HiveWorker daemon. One static Go binary that enrolls a Mac in its
organization's pool and holds the single outbound tunnel jobs arrive through.

```sh
./image/build.sh    # once: builds the guest image (needs Docker)
./build.sh          # builds ./hived, signed so macOS lets it create VMs
./hived selftest    # boots a throwaway VM and reports what it finds
./hived login       # sign in with GitHub in the browser, enroll this machine
./hived run         # connect to every pool it serves and wait for jobs
./hived status
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
remove pools later from the dashboard without touching the machine: the
running daemon asks the server every minute and opens or closes a tunnel per
pool to match. It runs one job at a time across all of them.

## What it keeps

One file, `~/Library/Application Support/HiveWorker/machine.json`, readable
by its owner only: the server, the machine id and the machine token. Which
pools it serves is not stored; the server is asked. The GitHub sign-in is
used once to obtain that token and is not kept. Moving the token to the
Keychain is planned.

## How a job runs

Each job gets its own Linux virtual machine under Apple's
Virtualization.framework, and the machine is destroyed when the job ends.

1. The coordinator offers a job (`job.assign`). The daemon takes its one job
   slot and accepts, or declines if it is busy with another pool's job.
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

## What it does not do yet

- **Container jobs.** `container:` and `services:` need Docker in the guest.
- **A warm VM.** Every job cold-boots. A VM kept booted, or restored from a
  saved state, would cut the three seconds further.
- **Caches between jobs.** Each job starts from the same clean disk.
- **Throttling for the owner.** It pauses on battery and with the lid closed,
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
