# hived changes

What an owner needs to know about a release of the daemon. Releases are
numbered in `daemon/VERSION` and published by the Release workflow; machines
with `auto-update` on take them when idle.

## Unreleased

### Spotlight is kept out of macOS jobs

A `hive-macos` job run as `hiveworker` no longer has Spotlight indexing
everything it writes, which took a steady share of the CPU for the whole
build (#110). With the new `spotlight-exclude` setting, on by default:

- The runner and each job live in `~hiveworker/hive.noindex`, which
  Spotlight skips. An existing `~hiveworker/runner` moves there by itself
  when no job is running; nothing needs reinstalling.
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
