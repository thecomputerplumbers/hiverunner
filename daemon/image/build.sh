#!/bin/bash
# Builds the guest image: a kernel, an initrd and an ext4 root disk.
#
#   ./build.sh [output-directory]
#
# Needs Docker and nothing else. The default output is where hived looks.
set -euo pipefail
cd "$(dirname "$0")"

# Pinned. The hash is the one GitHub publishes in the runner's release notes.
# Image maintenance can override both the version and its published digest.
RUNNER_VERSION="${RUNNER_VERSION:-2.337.0}"
RUNNER_SHA256="${RUNNER_SHA256:-9b1dc70626422526e3c94767cf024896beb15da5342a3f4819bf2feac13e0393}"
# Bump when anything in this directory changes what ends up in the guest.
IMAGE_VERSION=5

out="${1:-$HOME/Library/Application Support/HiveWorker/image}"
tag="hiveworker-guest:${IMAGE_VERSION}-${RUNNER_VERSION}"
mkdir -p "$out"
work="$out/.build"
rm -rf "$work"
mkdir -p "$work"

echo "Building the guest filesystem..."
docker build --platform linux/arm64 \
  --build-arg "RUNNER_VERSION=$RUNNER_VERSION" \
  --build-arg "RUNNER_SHA256=$RUNNER_SHA256" \
  -t "$tag" .

container=$(docker create --platform linux/arm64 "$tag" /bin/true)
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT

echo "Turning it into a disk..."
# Unpacked and packed as root inside a container, so ownership and device
# nodes survive; macOS's filesystem would not keep them.
docker export "$container" | docker run --rm -i --platform linux/arm64 \
  -v "$work:/out" "$tag" /bin/bash -euc '
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

    # Direct kernel boot wants the uncompressed arm64 Image.
    kernel=$(ls boot/vmlinuz-* | sort -V | tail -1)
    initrd=$(ls boot/initrd.img-* | sort -V | tail -1)
    if gzip -t "$kernel" 2>/dev/null; then gunzip -c "$kernel" > /out/Image; else cp "$kernel" /out/Image; fi
    cp "$initrd" /out/initrd
    rm -rf boot/*

    # Sized to fit with a little room; each job grows its own clone.
    used=$(du -sm . | cut -f1)
    mke2fs -q -t ext4 -L hive-root -d . /out/rootfs.img "$((used + used / 5 + 512))M"
    echo "$used" > /out/used-mb
  '

mv "$work/Image" "$work/initrd" "$work/rootfs.img" "$out/"
cat > "$out/manifest.json" <<JSON
{
  "imageVersion": $IMAGE_VERSION,
  "runnerVersion": "$RUNNER_VERSION",
  "kernel": "Image",
  "initrd": "initrd",
  "disk": "rootfs.img",
  "commandLine": "console=hvc0 root=/dev/vda rw quiet"
}
JSON
rm -rf "$work"
echo "Guest image written to: $out"
ls -lh "$out"
