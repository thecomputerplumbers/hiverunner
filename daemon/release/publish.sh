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
#   install.ps1                               the same for Windows: sets up WSL, then runs install.sh in it
#   hived/<version>/hived-darwin-arm64.zip    signed and notarized
#   images/<image>/image-<sum>.aar.aa, .ab, …  the Linux image, as an Apple Archive in pieces
#   linux-<arch>.txt                          what install.sh reads on Linux
#   tools/virtiofsd-<version>-linux-<arch>-<sum>  for systems without their own
#   hived/<version>/hived-linux-<arch>.tar.gz
#   images/<image>/image-<arch>-<sum>.tar.gz.aa, .ab, …  the same image, as a tarball in pieces
#
# Linux on arm64 runs the image the Mac does. Linux on x86-64 needs its own,
# built with `GUEST_ARCH=x64 ./image/build.sh DIR`; name that directory in
# HIVE_IMAGE_AMD64 to publish it, or x86-64 is left out of the release.
#
# Signing needs a "Developer ID Application" certificate in the keychain, and
# notarizing needs credentials stored once with:
#
#   xcrun notarytool store-credentials hiveworker --key AuthKey.p8 --key-id … --issuer …
#
#   HIVE_SIGN_IDENTITY   the certificate's name. Required to upload.
#   HIVE_NOTARY_PROFILE  the stored credentials' name. Defaults to hiveworker.
#   HIVE_NOTARY_KEY      instead of stored credentials, as a workflow has none:
#                        the path of an App Store Connect API key, with its id
#                        in HIVE_NOTARY_KEY_ID and HIVE_NOTARY_ISSUER.
#
# Without HIVE_SIGN_IDENTITY the daemon is signed ad hoc, which runs only on
# the Mac that built it. Such a build is marked as a development build, the
# installer refuses it unless told HIVE_DEV=1, and it cannot be uploaded.
#
# An image that is already published is not built or packed again: a release
# that changes only the daemon names the image files the last one did, so it
# needs no image on disk and no Docker. Otherwise the image is built first
# with ./image/build.sh if it is not already in HIVE_IMAGE (or hived's
# default directory).
set -euo pipefail
cd "$(dirname "$0")/.."

BUCKET="hiveworker-releases"
BASE="${HIVE_RELEASES:-https://get.hiverunner.dev}"
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
  if [ -n "${HIVE_NOTARY_KEY:-}" ]; then
    xcrun notarytool submit "$daemon_zip" --key "$HIVE_NOTARY_KEY" --key-id "$HIVE_NOTARY_KEY_ID" --issuer "$HIVE_NOTARY_ISSUER" --wait | tee "$build/notary.log"
  else
    xcrun notarytool submit "$daemon_zip" --keychain-profile "$profile" --wait | tee "$build/notary.log"
  fi
  grep -q "status: Accepted" "$build/notary.log" || { echo "Apple did not accept the build." >&2; exit 1; }
fi
daemon_sha=$(shasum -a 256 "$daemon_zip" | cut -d' ' -f1)
rm -rf "$build"

echo "== Image"
# The image the repository describes, not whichever one happens to be on disk.
wanted="$(sed -n 's/^IMAGE_VERSION=//p' image/build.sh)-$(sed -n 's/^RUNNER_VERSION="${RUNNER_VERSION:-\(.*\)}"/\1/p' image/build.sh)"
# What the last release published, asked past the CDN's memory. If that is
# this image, its files are named again as they are: packing the same image
# twice does not give the same bytes, and a published name is cached for a
# year.
live=$(mktemp -d)
# A published URL moved onto this release's host: files keep their names in
# the bucket whichever host serves them, and the keys are read back from the
# URLs below by stripping $BASE.
rebase() { printf '%s/%s' "$BASE" "${1#https://*/}"; }
for manifest in latest.json linux-arm64.txt linux-amd64.txt; do
  curl -fsS -H "Cache-Control: no-cache" -o "$live/$manifest" "$BASE/$manifest?published=$(date +%s)$$" 2>/dev/null || rm -f "$live/$manifest"
done
image_dir="${HIVE_IMAGE:-$HOME/Library/Application Support/HiveWorker/image}"
# aar_ and not image_: the Linux half below has an image_url of its own.
aar_key=""
if [ "$(plutil -extract image.version raw -o - "$live/latest.json" 2>/dev/null || true)" = "$wanted" ]; then
  image_version="$wanted"
  aar_url=$(rebase "$(plutil -extract image.url raw -o - "$live/latest.json")")
  # None for an image published whole, as they were before one outgrew that.
  aar_parts=$(plutil -extract image.parts raw -o - "$live/latest.json" 2>/dev/null || true)
  image_sha=$(plutil -extract image.sha256 raw -o - "$live/latest.json")
  image_mb=$(plutil -extract image.megabytes raw -o - "$live/latest.json")
  echo "   $image_version, $image_mb MB, as already published"
else
  [ -f "$image_dir/manifest.json" ] || ./image/build.sh "$image_dir"
  image_version="$(plutil -extract imageVersion raw -o - "$image_dir/manifest.json")-$(plutil -extract runnerVersion raw -o - "$image_dir/manifest.json")"
  [ "$image_version" = "$wanted" ] || { echo "The image in $image_dir is $image_version; this commit builds $wanted. Rebuild it with ./image/build.sh" >&2; exit 1; }
  mkdir -p "$out/images/$image_version"
  packing="$out/images/$image_version/image.packing"
  (cd "$image_dir" && aa archive -a lzfse -d . -o "$packing" -include-path-list <(printf 'Image\ninitrd\nmanifest.json\nrootfs.img\n'))
  image_sha=$(shasum -a 256 "$packing" | cut -d' ' -f1)
  image_mb=$(($(stat -f %z "$packing") / 1048576))
  # Named for its contents and in pieces, as the Linux tarballs below are
  # and for their reasons: an archive of the same image is not the same
  # bytes twice, and the bucket takes at most 300 MB in one upload. An
  # image with Docker in it is more than that.
  aar_key="images/$image_version/image-$(printf %.12s "$image_sha").aar"
  aar_url="$BASE/$aar_key"
  split -b 200m "$packing" "$out/$aar_key."
  rm "$packing"
  aar_parts=$(cd "$out/images/$image_version" && ls "$(basename "$aar_key")".* | sed 's/.*\.//' | tr '\n' ' ' | sed 's/ $//')
  echo "   $image_version, $image_mb MB"
fi

echo "== Linux"
# Nothing to sign: Linux has no gatekeeper to satisfy, and the installer
# checks each download against the checksum published beside it.
linux_arches=""
# One value from what the last release published for an architecture.
live_value() { sed -n "s/^$2=//p" "$live/linux-$1.txt" 2>/dev/null; }
virtiofsd_version=$(sed -n 's/^VIRTIOFSD_VERSION="${VIRTIOFSD_VERSION:-\(.*\)}"/\1/p' release/virtiofsd.sh)
# Whether the last release's image and virtiofsd for an architecture are the
# ones this commit describes, and so can be named again.
as_published() { [ "$(live_value "$1" image_version)" = "$wanted" ] && [ "$(live_value "$1" virtiofsd_version)" = "$virtiofsd_version" ]; }
linux() { # arch image-directory
  arch="$1"
  dir="$2"
  mkdir -p "$out/hived/$version"
  binary=$(mktemp -d)
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$binary/hived" ./cmd/hived
  daemon_tar="$out/hived/$version/hived-linux-$arch.tar.gz"
  COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -czf "$daemon_tar" -C "$binary" hived
  rm -rf "$binary"
  this_image="$wanted"
  if as_published "$arch"; then
    image_url=$(rebase "$(live_value "$arch" image_url)")
    image_parts=$(live_value "$arch" image_parts)
    image_tar_sha=$(live_value "$arch" image_sha256)
    image_megabytes=$(live_value "$arch" image_megabytes)
    virtiofsd_url=$(rebase "$(live_value "$arch" virtiofsd_url)")
    virtiofsd_sha=$(live_value "$arch" virtiofsd_sha256)
  else
    [ -f "$dir/manifest.json" ] || { echo "No image in $dir" >&2; exit 1; }
    built_for=$(plutil -extract arch raw -o - "$dir/manifest.json" 2>/dev/null || echo arm64)
    [ "$built_for" = "$arch" ] || { echo "The image in $dir is for $built_for, not $arch." >&2; exit 1; }
    this_image="$(plutil -extract imageVersion raw -o - "$dir/manifest.json")-$(plutil -extract runnerVersion raw -o - "$dir/manifest.json")"
    [ "$this_image" = "$wanted" ] || { echo "The $arch image in $dir is $this_image; this commit builds $wanted." >&2; exit 1; }
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
    image_url="$BASE/$image_key"
    image_megabytes=$(($(stat -f %z "$packing") / 1048576))
    # In pieces: the bucket takes at most 300 MB in one upload, and an image
    # can be more. The installer puts them back together and checks the whole.
    split -b 200m "$packing" "$out/$image_key."
    rm "$packing"
    image_parts=$(cd "$out/images/$this_image" && ls "$(basename "$image_key")".* | sed 's/.*\.//' | tr '\n' ' ' | sed 's/ $//')
    # The program that shares a job's directory with its VM, for systems that
    # do not package the one that runs without root. Built once per version
    # and kept, because the build takes minutes and need not be repeated.
    kept="${HIVE_TOOLS:-$HOME/Library/Caches/HiveWorker/tools}/virtiofsd-$virtiofsd_version-linux-$arch"
    if [ ! -f "$kept" ]; then
      echo "   building virtiofsd $virtiofsd_version for $arch, once"
      mkdir -p "$(dirname "$kept")"
      ./release/virtiofsd.sh "$arch" "$kept"
    fi
    virtiofsd_sha=$(shasum -a 256 "$kept" | cut -d' ' -f1)
    virtiofsd_key="tools/virtiofsd-$virtiofsd_version-linux-$arch-$(printf %.12s "$virtiofsd_sha")"
    virtiofsd_url="$BASE/$virtiofsd_key"
    mkdir -p "$out/tools"
    cp "$kept" "$out/$virtiofsd_key"
  fi
  mkdir -p "$out/tools"
  cp release/virtiofsd.NOTICE.txt "$out/tools/virtiofsd.NOTICE.txt"
  cat > "$out/linux-$arch.txt" <<MANIFEST
version=$version
commit=$(git rev-parse HEAD)
daemon_url=$BASE/hived/$version/hived-linux-$arch.tar.gz
daemon_sha256=$(shasum -a 256 "$daemon_tar" | cut -d' ' -f1)
image_version=$this_image
image_url=$image_url
image_parts=$image_parts
image_sha256=$image_tar_sha
image_megabytes=$image_megabytes
virtiofsd_version=$virtiofsd_version
virtiofsd_url=$virtiofsd_url
virtiofsd_sha256=$virtiofsd_sha
MANIFEST
  linux_arches="$linux_arches $arch"
  echo "   $arch: daemon and image $this_image, $image_megabytes MB"
}
linux arm64 "$image_dir"
if [ -n "${HIVE_IMAGE_AMD64:-}" ] || as_published amd64; then
  linux amd64 "${HIVE_IMAGE_AMD64:-}"
else
  echo "   amd64: left out (HIVE_IMAGE_AMD64 is not set)"
fi

cp release/install.sh "$out/install.sh"
cp release/install.ps1 "$out/install.ps1"
# What a Mac set up for macOS jobs by an older setup needs setup again for.
# A daemon that takes them waits for its owner on a release with a newer one.
macos_setup=$(sed -n 's/^[[:space:]]*SetupVersion = \([0-9][0-9]*\)$/\1/p' internal/native/native.go)
[ -n "$macos_setup" ] || { echo "No SetupVersion in internal/native/native.go" >&2; exit 1; }
cat > "$out/latest.json" <<JSON
{
  "version": "$version",
  "commit": "$(git rev-parse HEAD)",
  "macosSetup": $macos_setup,
  "daemon": {
    "url": "$BASE/hived/$version/hived-darwin-arm64.zip",
    "sha256": "$daemon_sha",
    "teamId": "$team"
  },
  "image": {
    "version": "$image_version",
    "url": "$aar_url",
    "parts": "$aar_parts",
    "sha256": "$image_sha",
    "megabytes": $image_mb
  }
}
JSON
plutil -lint "$out/latest.json" >/dev/null 2>&1 || python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$out/latest.json"

# Nothing here may be more than the bucket takes in one upload. Checked for
# a build that will not be uploaded too, because that is what a rehearsal
# is, and an upload refused is a release that stops halfway.
oversized=$(find "$out" -type f -size +290M)
if [ -n "$oversized" ]; then
  echo "Too large to upload in one piece (the bucket takes 300 MB):" >&2
  echo "$oversized" >&2
  exit 1
fi

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
if [ -n "$aar_key" ]; then
  for part in $aar_parts; do
    # Named for the whole archive's checksum, so a piece already there is
    # the same piece.
    if ! published "$aar_key.$part"; then
      put "$aar_key.$part" "$out/$aar_key.$part" application/octet-stream "public, max-age=31536000, immutable"
    fi
  done
fi
put "hived/$version/hived-darwin-arm64.zip" "$daemon_zip" application/zip "public, max-age=31536000, immutable"
for arch in $linux_arches; do
  image_key=$(sed -n "s|^image_url=$BASE/||p" "$out/linux-$arch.txt")
  for part in $(sed -n 's/^image_parts=//p' "$out/linux-$arch.txt"); do
    # Named for the whole image's checksum, so a piece already there is the
    # same piece.
    if ! published "$image_key.$part"; then
      put "$image_key.$part" "$out/$image_key.$part" application/octet-stream "public, max-age=31536000, immutable"
    fi
  done
  virtiofsd_key=$(sed -n "s|^virtiofsd_url=$BASE/||p" "$out/linux-$arch.txt")
  if ! published "$virtiofsd_key"; then
    put "$virtiofsd_key" "$out/$virtiofsd_key" application/octet-stream "public, max-age=31536000, immutable"
  fi
  put "hived/$version/hived-linux-$arch.tar.gz" "$out/hived/$version/hived-linux-$arch.tar.gz" application/gzip "public, max-age=31536000, immutable"
done
[ -z "$linux_arches" ] || put "tools/virtiofsd.NOTICE.txt" "$out/tools/virtiofsd.NOTICE.txt" "text/plain; charset=utf-8" "public, max-age=3600"
put "install.sh" "$out/install.sh" "text/x-shellscript; charset=utf-8" "public, max-age=60"
put "install.ps1" "$out/install.ps1" "text/plain; charset=utf-8" "public, max-age=60"
put "latest.json" "$out/latest.json" application/json "public, max-age=60"
for arch in $linux_arches; do
  put "linux-$arch.txt" "$out/linux-$arch.txt" "text/plain; charset=utf-8" "public, max-age=60"
done
echo "== Released $version: curl -fsSL $BASE/install.sh | sh"
