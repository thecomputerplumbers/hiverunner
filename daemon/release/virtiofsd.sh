#!/bin/bash
# Builds virtiofsd, the program that serves a job's directories to its VM,
# as a single static binary for Linux.
#
#   ./release/virtiofsd.sh arm64|amd64 OUTPUT-FILE
#
# The daemon needs the virtiofsd that runs without root, which is its own
# project (https://gitlab.com/virtio-fs/virtiofsd). Recent distributions
# package it. Debian 12 and Raspberry Pi OS 12 do not: theirs is the older
# one that came inside QEMU and must be root. So a release carries a copy
# the installer can fall back on.
#
# Needs Docker. Built in Alpine so it links musl and runs on any Linux.
set -euo pipefail
VIRTIOFSD_VERSION="${VIRTIOFSD_VERSION:-1.13.2}"
arch="$1"
out="$2"
case "$arch" in
  arm64 | amd64) ;;
  *) echo "usage: virtiofsd.sh arm64|amd64 OUTPUT-FILE" >&2; exit 2 ;;
esac
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
docker run --rm --platform "linux/$arch" -v "$dir:/out" rust:alpine sh -euc '
  apk add --no-cache musl-dev libcap-ng-dev libcap-ng-static libseccomp-dev libseccomp-static >/dev/null
  # Named, so the static flags apply to virtiofsd and not to the build
  # scripts and macros compiled on the way.
  target=$(rustc -vV | sed -n "s/host: //p")
  LIBSECCOMP_LINK_TYPE=static LIBSECCOMP_LIB_PATH=/usr/lib \
  LIBCAPNG_LINK_TYPE=static LIBCAPNG_LIB_PATH=/usr/lib \
  RUSTFLAGS="-C target-feature=+crt-static" \
    cargo install virtiofsd --version "'"$VIRTIOFSD_VERSION"'" --locked --target "$target" --root /tmp/built >/dev/null 2>&1
  cp /tmp/built/bin/virtiofsd /out/virtiofsd
  /out/virtiofsd --version
'
install -m 0755 "$dir/virtiofsd" "$out"
