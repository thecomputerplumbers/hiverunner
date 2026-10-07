#!/bin/bash
# Builds the guest image: a kernel, an initrd and an ext4 root disk.
#
#   ./build.sh [output-directory]
#   GUEST_ARCH=x64 ./build.sh <output-directory>
#
# Needs Docker and nothing else. The default output is where hived looks.
#
# GUEST_ARCH is the architecture of the guest, and so of the jobs it runs:
# arm64 (the default) or x64. A machine boots guests of its own architecture
# only, so an image built for another one must be given somewhere to go.
set -euo pipefail
cd "$(dirname "$0")"

# Pinned. The hashes are the ones GitHub publishes in the runner's release
# notes, one per architecture. Image maintenance can override both the version
# and the published digest of the architecture it is building.
RUNNER_VERSION="${RUNNER_VERSION:-2.337.0}"
RUNNER_SHA256_ARM64="9b1dc70626422526e3c94767cf024896beb15da5342a3f4819bf2feac13e0393"
RUNNER_SHA256_X64="70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613"
# Bump when anything in this directory changes what ends up in the guest.
IMAGE_VERSION=7

# Direct kernel boot wants a different file on each: the uncompressed Image
# on arm64, the compressed bzImage on x64. GitHub's runner calls the second
# x64 and Go, whose name the manifest uses, amd64.
case "${GUEST_ARCH:-arm64}" in
  arm64 | aarch64)
    arch=arm64 goarch=arm64 platform=linux/arm64 kernel=Image
    cmdline="console=hvc0 root=/dev/vda rw quiet"
    sha="${RUNNER_SHA256:-$RUNNER_SHA256_ARM64}"
    tag="hiveworker-guest:${IMAGE_VERSION}-${RUNNER_VERSION}"
    ;;
  x64 | amd64 | x86_64)
    arch=x64 goarch=amd64 platform=linux/amd64 kernel=vmlinuz
    # The initrd is built to look for a hibernation image on the swap device
    # of whatever machine built it. A guest has none, and on x64 the initrd
    # waits half a minute for it to appear unless it is told not to.
    cmdline="console=hvc0 root=/dev/vda rw quiet noresume"
    sha="${RUNNER_SHA256:-$RUNNER_SHA256_X64}"
    tag="hiveworker-guest-x64:${IMAGE_VERSION}-${RUNNER_VERSION}"
    ;;
  *)
    echo "GUEST_ARCH must be arm64 or x64, not ${GUEST_ARCH}." >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  arm64 | aarch64) host=arm64 ;;
  x86_64 | amd64) host=x64 ;;
  *) host=unknown ;;
esac
if [ -n "${1:-}" ]; then
  out="$1"
elif [ "$arch" != "$host" ]; then
  echo "This machine cannot boot an $arch guest. Say where to write the image: GUEST_ARCH=$arch $0 <output-directory>" >&2
  exit 1
elif [ "$(uname -s)" = Darwin ]; then
  out="$HOME/Library/Application Support/HiveWorker/image"
else
  out="${XDG_CONFIG_HOME:-$HOME/.config}/HiveWorker/image"
fi
mkdir -p "$out"
work="$out/.build"
rm -rf "$work"
mkdir -p "$work"

echo "Building the $arch guest filesystem..."
docker build --platform "$platform" \
  --build-arg "RUNNER_VERSION=$RUNNER_VERSION" \
  --build-arg "RUNNER_SHA256=$sha" \
  --build-arg "RUNNER_ARCH=$arch" \
  -t "$tag" .

container=$(docker create --platform "$platform" "$tag" /bin/true)
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT

echo "Turning it into a disk..."
# Unpacked and packed as root inside a container, so ownership and device
# nodes survive; macOS's filesystem would not keep them.
docker export "$container" | docker run --rm -i --platform "$platform" \
  -e "KERNEL=$kernel" -v "$work:/out" "$tag" /bin/bash -euc '
    mkdir /rootfs
    tar -x -C /rootfs
    cd /rootfs

    # Docker manages these inside a container; a real boot needs real ones.
    rm -f etc/resolv.conf etc/hostname etc/hosts .dockerenv
    ln -s /run/systemd/resolve/stub-resolv.conf etc/resolv.conf
    echo hive > etc/hostname
    printf "127.0.0.1 localhost\n127.0.1.1 hive\n" > etc/hosts
    echo "/dev/vda / ext4 defaults,noatime 0 1" > etc/fstab

    # Clones must not share an identity: an empty machine-id is generated
    # afresh on each boot.
    : > etc/machine-id
    rm -f var/lib/dbus/machine-id

    kernel=$(ls boot/vmlinuz-* | sort -V | tail -1)
    initrd=$(ls boot/initrd.img-* | sort -V | tail -1)
    if [ "$KERNEL" = Image ]; then
      # Direct kernel boot wants the uncompressed arm64 Image.
      if gzip -t "$kernel" 2>/dev/null; then gunzip -c "$kernel" > /out/Image; else cp "$kernel" /out/Image; fi
    else
      # On x64 it wants the bzImage as it is. Its header says so at a fixed
      # place; anything else would fail at boot, where it is harder to read.
      [ "$(dd if="$kernel" bs=1 skip=514 count=4 2>/dev/null)" = HdrS ] || { echo "$kernel is not a bzImage" >&2; exit 1; }
      # Ubuntu ships it readable by root alone; hived is not root.
      cat "$kernel" > "/out/$KERNEL"
    fi
    cp "$initrd" /out/initrd
    rm -rf boot/*

    # Sized to fit with a little room; each job grows its own clone.
    used=$(du -sm . | cut -f1)
    mke2fs -q -t ext4 -L hive-root -d . /out/rootfs.img "$((used + used / 5 + 512))M"
    echo "$used" > /out/used-mb
  '

mv "$work/$kernel" "$work/initrd" "$work/rootfs.img" "$out/"
cat > "$out/manifest.json" <<JSON
{
  "imageVersion": $IMAGE_VERSION,
  "runnerVersion": "$RUNNER_VERSION",
  "arch": "$goarch",
  "kernel": "$kernel",
  "initrd": "initrd",
  "disk": "rootfs.img",
  "commandLine": "$cmdline"
}
JSON
rm -rf "$work"
echo "Guest image written to: $out"
ls -lh "$out"
