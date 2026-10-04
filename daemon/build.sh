#!/bin/bash
# Builds hived and signs it so macOS lets it create virtual machines.
#
#   ./build.sh [output-path]
#
# The signature is ad hoc: enough to run on the machine that built it. A
# release build is signed with a Developer ID and notarized instead.
set -euo pipefail
cd "$(dirname "$0")"
out="${1:-./hived}"
version="${HIVE_VERSION:-0.0.0-dev}"
go build -ldflags "-X main.version=$version" -o "$out" ./cmd/hived
if [ "$(uname)" = "Darwin" ]; then
  codesign --force --sign - --entitlements vz.entitlements "$out"
fi
echo "Built $out"
