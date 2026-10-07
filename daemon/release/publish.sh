#!/bin/bash
# Builds a release of the daemon and the guest image, and publishes them.
#
#   ./release/publish.sh --out DIR      build everything into DIR and stop
#   ./release/publish.sh --upload       build, then publish to the releases bucket
#
# What a release is:
#
#   latest.json                               what install.sh reads
#   install.sh
#   hived/<version>/hived-darwin-arm64.zip    signed and notarized
#   images/<image>/image.aar                  the Linux image, as an Apple Archive
#
# Signing needs a "Developer ID Application" certificate in the keychain, and
# notarizing needs credentials stored once with:
#
#   xcrun notarytool store-credentials hiveworker --key AuthKey.p8 --key-id … --issuer …
#
#   HIVE_SIGN_IDENTITY   the certificate's name. Required to upload.
#   HIVE_NOTARY_PROFILE  the stored credentials' name. Defaults to hiveworker.
#
# Without HIVE_SIGN_IDENTITY the daemon is signed ad hoc, which runs only on
# the Mac that built it. Such a build is marked as a development build, the
# installer refuses it unless told HIVE_DEV=1, and it cannot be uploaded.
#
# The image is built first with ./image/build.sh if it is not already in
# HIVE_IMAGE (or hived's default directory).
set -euo pipefail
cd "$(dirname "$0")/.."

BUCKET="hiveworker-releases"
BASE="${HIVE_RELEASES:-https://get.hiveworkr.thecomputerplumbers.com}"
mode=""
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --out) mode="out"; out="$2"; shift 2 ;;
    --upload) mode="upload"; shift ;;
    *) echo "usage: publish.sh --out DIR | --upload" >&2; exit 2 ;;
  esac
done
[ -n "$mode" ] || { echo "usage: publish.sh --out DIR | --upload" >&2; exit 2; }
[ "$(uname -sm)" = "Darwin arm64" ] || { echo "Releases are built on an Apple Silicon Mac." >&2; exit 1; }

version=$(tr -d '[:space:]' < VERSION)
identity="${HIVE_SIGN_IDENTITY:-}"
profile="${HIVE_NOTARY_PROFILE:-hiveworker}"
if [ "$mode" = "upload" ]; then
  [ -n "$identity" ] || { echo "Set HIVE_SIGN_IDENTITY to a Developer ID Application certificate to publish." >&2; exit 1; }
  [ -z "$(git status --porcelain)" ] || { echo "Commit your changes first: a release is built from a commit." >&2; exit 1; }
  out=$(mktemp -d)
  trap 'rm -rf "$out"' EXIT
fi
mkdir -p "$out"
out=$(cd "$out" && pwd)

echo "== Daemon $version"
build=$(mktemp -d)
go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$build/hived" ./cmd/hived
team=""
if [ -n "$identity" ]; then
  # Hardened runtime and a secure timestamp: both are required to notarize.
  codesign --force --timestamp --options runtime --entitlements vz.entitlements --sign "$identity" "$build/hived"
  team=$(codesign -dv "$build/hived" 2>&1 | sed -n 's/^TeamIdentifier=//p')
  [ -n "$team" ] && [ "$team" != "not set" ] || { echo "The signature carries no team: is that a Developer ID certificate?" >&2; exit 1; }
else
  codesign --force --sign - --entitlements vz.entitlements "$build/hived"
  echo "   signed ad hoc: a development build"
fi
codesign --verify --strict "$build/hived"
mkdir -p "$out/hived/$version"
daemon_zip="$out/hived/$version/hived-darwin-arm64.zip"
rm -f "$daemon_zip"
ditto -c -k "$build/hived" "$daemon_zip"
if [ -n "$identity" ]; then
  echo "   notarizing with Apple, which takes a minute or two"
  xcrun notarytool submit "$daemon_zip" --keychain-profile "$profile" --wait | tee "$build/notary.log"
  grep -q "status: Accepted" "$build/notary.log" || { echo "Apple did not accept the build." >&2; exit 1; }
fi
daemon_sha=$(shasum -a 256 "$daemon_zip" | cut -d' ' -f1)
rm -rf "$build"

echo "== Image"
image_dir="${HIVE_IMAGE:-$HOME/Library/Application Support/HiveWorker/image}"
[ -f "$image_dir/manifest.json" ] || ./image/build.sh "$image_dir"
image_version="$(plutil -extract imageVersion raw -o - "$image_dir/manifest.json")-$(plutil -extract runnerVersion raw -o - "$image_dir/manifest.json")"
# The image the repository describes, not whichever one happens to be on disk.
wanted="$(sed -n 's/^IMAGE_VERSION=//p' image/build.sh)-$(sed -n 's/^RUNNER_VERSION="${RUNNER_VERSION:-\(.*\)}"/\1/p' image/build.sh)"
[ "$image_version" = "$wanted" ] || { echo "The image in $image_dir is $image_version; this commit builds $wanted. Rebuild it with ./image/build.sh" >&2; exit 1; }
mkdir -p "$out/images/$image_version"
image_aar="$out/images/$image_version/image.aar"
if [ ! -f "$image_aar" ]; then
  (cd "$image_dir" && aa archive -a lzfse -d . -o "$image_aar" -include-path-list <(printf 'Image\ninitrd\nmanifest.json\nrootfs.img\n'))
fi
image_sha=$(shasum -a 256 "$image_aar" | cut -d' ' -f1)
image_mb=$(($(stat -f %z "$image_aar") / 1048576))
echo "   $image_version, $image_mb MB"

cp release/install.sh "$out/install.sh"
cat > "$out/latest.json" <<JSON
{
  "version": "$version",
  "commit": "$(git rev-parse HEAD)",
  "daemon": {
    "url": "$BASE/hived/$version/hived-darwin-arm64.zip",
    "sha256": "$daemon_sha",
    "teamId": "$team"
  },
  "image": {
    "version": "$image_version",
    "url": "$BASE/images/$image_version/image.aar",
    "sha256": "$image_sha",
    "megabytes": $image_mb
  }
}
JSON
plutil -lint "$out/latest.json" >/dev/null 2>&1 || python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$out/latest.json"

if [ "$mode" = "out" ]; then
  echo "== Built into $out"
  exit 0
fi

echo "== Publishing to $BUCKET"
put() { # key file content-type cache-control
  (cd ../apps/web && pnpm exec wrangler r2 object put "$BUCKET/$1" --file "$2" --content-type "$3" --cache-control "$4" --remote >/dev/null)
  echo "   $1"
}
# The large, versioned files first and the manifest that points at them last:
# nobody is ever sent to a file that is not there yet.
if ! curl -fsI "$BASE/images/$image_version/image.aar" >/dev/null 2>&1; then
  put "images/$image_version/image.aar" "$image_aar" application/octet-stream "public, max-age=31536000, immutable"
fi
put "hived/$version/hived-darwin-arm64.zip" "$daemon_zip" application/zip "public, max-age=31536000, immutable"
put "install.sh" "$out/install.sh" "text/x-shellscript; charset=utf-8" "public, max-age=60"
put "latest.json" "$out/latest.json" application/json "public, max-age=60"
echo "== Released $version: curl -fsSL $BASE/install.sh | sh"
