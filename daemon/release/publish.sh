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
#   linux-<arch>.txt                          what install.sh reads on Linux
#   hived/<version>/hived-linux-<arch>.tar.gz
#   images/<image>/image-<arch>-<sum>.tar.gz  the same image, as a tarball
#
# Linux on arm64 runs the image the Mac does. Linux on x86-64 needs its own,
# built with `./image/build.sh --arch amd64 DIR`; name that directory in
# HIVE_IMAGE_AMD64 to publish it, or x86-64 is left out of the release.
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

echo "== Linux"
# Nothing to sign: Linux has no gatekeeper to satisfy, and the installer
# checks each download against the checksum published beside it.
linux_arches=""
linux() { # arch image-directory
  arch="$1"
  dir="$2"
  [ -f "$dir/manifest.json" ] || { echo "No image in $dir" >&2; exit 1; }
  built_for=$(plutil -extract arch raw -o - "$dir/manifest.json" 2>/dev/null || echo arm64)
  [ "$built_for" = "$arch" ] || { echo "The image in $dir is for $built_for, not $arch." >&2; exit 1; }
  this_image="$(plutil -extract imageVersion raw -o - "$dir/manifest.json")-$(plutil -extract runnerVersion raw -o - "$dir/manifest.json")"
  [ "$this_image" = "$wanted" ] || { echo "The $arch image in $dir is $this_image; this commit builds $wanted." >&2; exit 1; }
  binary=$(mktemp -d)
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$binary/hived" ./cmd/hived
  daemon_tar="$out/hived/$version/hived-linux-$arch.tar.gz"
  COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -czf "$daemon_tar" -C "$binary" hived
  rm -rf "$binary"
  # Named for its contents. A tarball of the same image is not the same
  # bytes twice, and a name is cached for a year: with the checksum in the
  # name, the manifest can only ever point at the file it describes.
  packing="$out/images/$this_image/image-$arch.packing"
  mkdir -p "$out/images/$this_image"
  COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -czf "$packing" -C "$dir" manifest.json \
    "$(plutil -extract kernel raw -o - "$dir/manifest.json")" \
    "$(plutil -extract initrd raw -o - "$dir/manifest.json")" \
    "$(plutil -extract disk raw -o - "$dir/manifest.json")"
  image_tar_sha=$(shasum -a 256 "$packing" | cut -d' ' -f1)
  image_key="images/$this_image/image-$arch-$(printf %.12s "$image_tar_sha").tar.gz"
  mv "$packing" "$out/$image_key"
  cat > "$out/linux-$arch.txt" <<MANIFEST
version=$version
commit=$(git rev-parse HEAD)
daemon_url=$BASE/hived/$version/hived-linux-$arch.tar.gz
daemon_sha256=$(shasum -a 256 "$daemon_tar" | cut -d' ' -f1)
image_version=$this_image
image_url=$BASE/$image_key
image_sha256=$image_tar_sha
image_megabytes=$(($(stat -f %z "$out/$image_key") / 1048576))
MANIFEST
  linux_arches="$linux_arches $arch"
  echo "   $arch: daemon and image $this_image, $(($(stat -f %z "$out/$image_key") / 1048576)) MB"
}
linux arm64 "$image_dir"
if [ -n "${HIVE_IMAGE_AMD64:-}" ]; then
  linux amd64 "$HIVE_IMAGE_AMD64"
else
  echo "   amd64: left out (HIVE_IMAGE_AMD64 is not set)"
fi

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
# Whether the bucket has an object, asked in a way the CDN in front of it
# cannot answer from memory and will not remember: a plain request for a
# missing object leaves "not found" cached at that address for minutes, and
# the installer is then told the file just uploaded does not exist.
published() { curl -fsI -H "Cache-Control: no-cache" "$BASE/$1?published=$(date +%s)$$" >/dev/null 2>&1; }

# A version is published once. Its files are cached for a year under their
# names, so replacing one leaves some people with the old file and a manifest
# whose checksum is for the new.
if published "hived/$version/hived-darwin-arm64.zip"; then
  echo "Version $version is already published. Bump daemon/VERSION, commit, and publish that." >&2
  exit 1
fi

# The large, versioned files first and the manifest that points at them last:
# nobody is ever sent to a file that is not there yet.
if ! published "images/$image_version/image.aar"; then
  put "images/$image_version/image.aar" "$image_aar" application/octet-stream "public, max-age=31536000, immutable"
fi
put "hived/$version/hived-darwin-arm64.zip" "$daemon_zip" application/zip "public, max-age=31536000, immutable"
for arch in $linux_arches; do
  image_key=$(sed -n "s|^image_url=$BASE/||p" "$out/linux-$arch.txt")
  put "$image_key" "$out/$image_key" application/gzip "public, max-age=31536000, immutable"
  put "hived/$version/hived-linux-$arch.tar.gz" "$out/hived/$version/hived-linux-$arch.tar.gz" application/gzip "public, max-age=31536000, immutable"
done
put "install.sh" "$out/install.sh" "text/x-shellscript; charset=utf-8" "public, max-age=60"
put "latest.json" "$out/latest.json" application/json "public, max-age=60"
for arch in $linux_arches; do
  put "linux-$arch.txt" "$out/linux-$arch.txt" "text/plain; charset=utf-8" "public, max-age=60"
done
echo "== Released $version: curl -fsSL $BASE/install.sh | sh"
