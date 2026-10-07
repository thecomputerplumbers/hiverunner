# Checks and deployment

**Live:** production only, in The Computer Plumbers Cloudflare account.

| What        | Where                                           |
| ----------- | ----------------------------------------------- |
| Web         | https://hiveworkr.thecomputerplumbers.com       |
| Coordinator | wss://connect.hiveworkr.thecomputerplumbers.com |
| Database    | D1 `hiveworker`, shared by both Workers         |

There is no staging environment. The sections below still describe how to add
one; until then `pnpm release production` is the only release, and it deploys
the web Worker, applies migrations, then deploys the coordinator.

`pnpm release`, not `pnpm deploy`: the latter is a pnpm built-in.

`.github/workflows/check.yml` runs on main pushes and pull requests: lint,
formatting, typecheck, regression tests, production build and an isolated Worker
smoke test. Tests need no Cloudflare account credentials and send no real email.
Configure the `check` job as a required branch check in each new repository.

## Configure each environment

Run `pnpm project:init` first. Production uses the root Wrangler configuration;
staging uses `env.staging`. They must have different Worker names, D1 database
IDs and canonical origins. Durable Object and email bindings are declared again
for staging because bindings are not inherited. The same object class belongs
to a different Worker namespace in each deployment.

1. Run the printed D1 creation commands and save each returned ID in its own
   environment. Keep database names unique to the new project.
2. Configure the matching custom domain for each Worker, or set APP_URL to its
   actual workers.dev origin. The post-deploy check uses this URL.
3. Verify a sending domain in Cloudflare Email Service and set EMAIL_FROM.
4. Create the GitHub App for the environment and set its six secrets: see
   [github-app.md](github-app.md).
5. Set BETTER_AUTH_SECRET separately for each Worker. If billing is enabled,
   set STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET and the plan price secrets too.
   Use test Stripe credentials for staging, live credentials for production.

```sh
pnpm --filter web exec wrangler secret put BETTER_AUTH_SECRET --env staging
pnpm --filter web exec wrangler secret put BETTER_AUTH_SECRET
pnpm doctor staging
pnpm doctor production
```

Create GitHub environments named `staging` and `production`. Each needs a
`CLOUDFLARE_API_TOKEN` secret and `CLOUDFLARE_ACCOUNT_ID` variable. Scope the token
to the intended account and required Worker/D1 operations. Application secrets
belong in the Worker, not in committed files. Configure a production environment
approval rule if the project requires release approval.

## Release

The Deploy workflow is manually dispatched from main and selects exactly one
environment. It runs the isolated smoke tests first. You can run the same release
command locally from a clean, committed checkout:

```sh
pnpm release staging
pnpm release production
```

The command validates target configuration/secret inventory and code checks,
builds with the selected CLOUDFLARE_ENV, and verifies the generated artifact's
Worker and D1 identities. It then applies D1 migrations, deploys that exact built
artifact, and checks the canonical HTTPS health endpoint for the expected commit
marker and configured dependencies. No production deploy occurs on a plain push.

Use backward-compatible migrations: the old Worker can still serve requests
between migration and deployment. Split destructive schema changes across
releases. Rollback the Worker separately; applied D1 migrations do not roll back
with code. A failed post-deploy health check means the upload may have succeeded:
inspect the deployment before retrying. Keep tested recovery procedures for each
product's data before destructive migrations.

The template's real domains, sending service and credentials are deliberately
unconfigured. This repository's local checks do not certify an actual deployment.

References: [Wrangler environments](https://developers.cloudflare.com/workers/wrangler/environments/)
and [GitHub Actions](https://developers.cloudflare.com/workers/ci-cd/external-cicd/github-actions/).

## Releasing the daemon

The daemon and the Linux image jobs run in are published to the R2 bucket
`hiveworker-releases`, served at `https://get.hiveworkr.thecomputerplumbers.com`.
`curl -fsSL …/install.sh | sh` is the whole of installing on a developer's
Mac or Linux box.

| Object                                      | What it is                                           |
| ------------------------------------------- | ---------------------------------------------------- |
| `latest.json`                               | The current version, with URLs and checksums         |
| `install.sh`                                | `daemon/release/install.sh`                          |
| `hived/<version>/hived-darwin-arm64.zip`    | The daemon, signed and notarized                     |
| `images/<image>/image.aar`                  | The guest image, as an Apple Archive                 |
| `linux-<arch>.txt`                          | The same as `latest.json`, for Linux                 |
| `hived/<version>/hived-linux-<arch>.tar.gz` | The daemon for Linux                                 |
| `images/<image>/image-<arch>-<sum>.tar.gz`  | The guest image, as a tarball named for its checksum |

A release is built and published from a clean checkout on an Apple Silicon
Mac, because signing and notarizing need macOS:

```sh
# Once per Mac: the notarization credentials, kept in the keychain.
xcrun notarytool store-credentials hiveworker --key AuthKey_XXXX.p8 --key-id XXXX --issuer XXXX

# Each release: bump daemon/VERSION, commit, then
HIVE_SIGN_IDENTITY="Developer ID Application: … (TEAMID)" ./daemon/release/publish.sh --upload
```

It needs a **Developer ID Application** certificate in the keychain. An Apple
Distribution or Apple Development certificate will not do: those are for the
App Store and for your own devices. The script refuses to upload a build
that is not signed with one, and the installer refuses to install a daemon
whose signing team is not the one named in `latest.json`.

The same run cross-compiles the Linux daemon and repacks the image as a
tarball. Linux builds are not signed: the installer checks each download
against the checksum in the manifest it fetched over HTTPS, as it does on a
Mac. Linux reads `linux-<arch>.txt`, lines of `name=value`, because a JSON
parser is not on every box.

A version is published once: its files are cached for a year under their
names, so the script refuses to publish a version the bucket already has.
Bump `daemon/VERSION` instead.

`./daemon/release/publish.sh --out DIR` builds the same tree locally without
signing or uploading. Serve `DIR` and point the installer at it to try a
change to either script:

```sh
HIVE_RELEASES=http://127.0.0.1:8000 HIVE_DEV=1 HIVE_NO_START=1 HOME=/tmp/fresh sh DIR/install.sh
```

`HIVE_NO_START=1` matters in that test. The background service is named per
user, not per home directory, so without it the installer would replace the
service of the daemon really installed on that Mac.

After the first release, set `DAEMON_RELEASED` in
`apps/web/components/marketing/site.ts` to `true` and release the web app.
That one constant turns every page from "not released yet" to the working
instructions. `DAEMON_SIGNER` beside it is the name macOS shows.

The image changes only when `daemon/image` does. `publish.sh` uploads it if
that version is not in the bucket yet and otherwise leaves it alone.
