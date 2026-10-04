# Licensing

This repository is private today and expected to become public. Treat every
commit as if it already were.

## Our code

AGPL-3.0-only, copyright The Computer Plumbers LLC. The text is in `LICENSE`
and every `package.json` carries the identifier. Anyone may run, modify, and
host HiveWorker, but a modified version offered over a network must publish
its source. The token tax is enforced by the contract, not by keeping code
closed.

This covers everything in this repository, including the daemon when it lands
under `daemon/`. The token contracts live in a separate repository and their
license is a separate decision.

The starter this was copied from is our own code with no license of its own,
so it carries no obligations into this repository.

Because we own all of the copyright, we can also offer the code under other
terms. That stops being true with the first outside contribution unless
contributors grant it, which is why the contribution question below matters.

## Dependencies

`pnpm licenses:check` fails on any dependency whose license is not on the
allowlist in `scripts/check-licenses.mjs`. It runs in `mise run check` and so
in CI. Adding a license to the allowlist, or a package to the exceptions, is a
decision: record the reason in that file.

Everything on the allowlist can be combined into an AGPL-3.0 work. Not allowed
without a deliberate decision: GPL-2.0-only (incompatible with AGPL-3.0), SSPL,
source-available licenses (BSL, FSL, Elastic), and anything with no license.

Current exceptions:

| Package                | License           | Why it is acceptable                                                                              |
| ---------------------- | ----------------- | ------------------------------------------------------------------------------------------------- |
| `@img/sharp-libvips-*` | LGPL-3.0-or-later | Dynamically linked native library used at build time. Not in the Worker bundle, not redistributed |

## What we will ship to other people

These are distributed, not just run by us, so their licenses bind us directly.
Check each when it is added.

| Component                     | License           | Obligation                                                                                                         |
| ----------------------------- | ----------------- | ------------------------------------------------------------------------------------------------------------------ |
| GitHub Actions runner (in VM) | MIT               | Keep the notice in the image                                                                                       |
| Linux guest image             | Mixed             | Publish the package manifest and offer source for GPL parts                                                        |
| Go modules in the daemon      | ISC, BSD-3-Clause | `coder/websocket`, `golang.org/x/sys`, `Code-Hex/vz` (MIT). Ship a third-party notices file with the signed binary |

## Before going public

- Scan the full git history for secrets and for the Cloudflare account ID.
- Confirm no code was pasted in from a repository under a different license.
- Decide whether outside contributions need a sign-off (DCO) or a contributor
  agreement. Only an agreement keeps the option to relicense.
- Check the Go modules the daemon links: AGPL-3.0 cannot absorb GPL-2.0-only code.
- Trademark: the name and symbol are still open in the goals doc.
