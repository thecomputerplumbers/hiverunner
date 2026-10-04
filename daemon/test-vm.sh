#!/bin/bash
# Runs the tests that boot real virtual machines. They need the guest image
# (image/build.sh) and a test binary signed for virtualization, which plain
# `go test` cannot produce.
#
# The image is read from HIVE_IMAGE, or the default directory hived uses.
# When two checkouts are being worked on at once, give each its own:
#
#   HIVE_IMAGE=/some/dir ./image/build.sh /some/dir && HIVE_IMAGE=/some/dir ./test-vm.sh
set -euo pipefail
cd "$(dirname "$0")"
binary=$(mktemp -t hive-vm-test)
trap 'rm -f "$binary"' EXIT
go test -c -o "$binary" ./internal/vm
codesign --force --sign - --entitlements vz.entitlements "$binary" 2>/dev/null
HIVE_VM_TEST=1 "$binary" -test.v -test.run 'VM|Share'  -test.timeout 5m "$@"
