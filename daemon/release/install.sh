#!/bin/sh
# Installs the HiveWorker daemon on this machine and adds it to your hive.
# It runs on Apple Silicon Macs and on Linux with KVM (arm64 or x86-64).
#
#   curl -fsSL https://get.hiveworkr.thecomputerplumbers.com/install.sh | sh
#
# It downloads the daemon and the Linux image jobs run in, checks both
# against the checksums published with them, installs them under your home
# directory, signs this Mac in with GitHub, and starts the daemon as a
# background service for your user. Nothing is installed system-wide and
# nothing asks for your password.
#
# Run it again to update. To remove everything: `hived uninstall`.
set -eu

BASE="${HIVE_RELEASES:-https://get.hiveworkr.thecomputerplumbers.com}"
LABEL="com.thecomputerplumbers.hiveworker"
BIN_DIR="$HOME/.local/bin"
DATA_DIR="$HOME/Library/Application Support/HiveWorker"
LOG_DIR="$HOME/Library/Logs/HiveWorker"
AGENT="$HOME/Library/LaunchAgents/$LABEL.plist"

say() { printf '%s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }
fail() {
  printf '\nhiveworker: %s\n' "$*" >&2
  exit 1
}

# What to type next, said at the end on either system.
tips() { # what this machine is called, the shell's startup file
  say ""
  say "  hived status     what $1 lends, and whether it is lending"
  say "  hived pause      stop taking jobs"
  say "  hived config     how many jobs, how large, and when"
  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *)
      say ""
      say "$BIN_DIR is not on your PATH yet. To use the commands above, add it:"
      say "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> $2"
      ;;
  esac
}

# Linux is installed by its own half of this script, at the end; what is in
# between is the Mac.
[ "$(uname -s)" = "Linux" ] && LINUX=1 || LINUX=""
if [ -z "$LINUX" ]; then
[ "$(uname -s)" = "Darwin" ] || fail "the daemon runs on macOS and Linux, and this is $(uname -s)."
[ "$(uname -m)" = "arm64" ] || fail "the daemon needs a Mac with Apple Silicon. Intel Macs are not supported."
macos=$(sw_vers -productVersion)
[ "${macos%%.*}" -ge 13 ] || fail "the daemon needs macOS 13 or later. This Mac has $macos."

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Downloads over HTTPS only, unless pointed at a local server for testing.
fetch() {
  case "$BASE" in
    https://*) curl -fL --proto '=https' --tlsv1.2 --retry 3 "$@" ;;
    *) curl -fL --retry 3 "$@" ;;
  esac
}
field() { plutil -extract "$1" raw -o - "$work/latest.json" 2>/dev/null || true; }
check() {
  # $1 file, $2 expected sha256
  actual=$(shasum -a 256 "$1" | cut -d' ' -f1)
  [ "$actual" = "$2" ] || fail "$(basename "$1") does not match its published checksum. Nothing was installed."
}

step "Finding the current release"
fetch -sS -o "$work/latest.json" "$BASE/latest.json" || fail "could not reach $BASE. Check your connection and try again."
version=$(field version)
daemon_url=$(field daemon.url)
daemon_sha=$(field daemon.sha256)
team=$(field daemon.teamId)
image_version=$(field image.version)
image_url=$(field image.url)
image_sha=$(field image.sha256)
image_mb=$(field image.megabytes)
[ -n "$version" ] && [ -n "$daemon_url" ] && [ -n "$daemon_sha" ] && [ -n "$image_url" ] && [ -n "$image_sha" ] ||
  fail "the release manifest is incomplete. Try again in a few minutes."
say "HiveWorker $version"

step "Installing the daemon"
fetch -sS -o "$work/hived.zip" "$daemon_url"
check "$work/hived.zip" "$daemon_sha"
ditto -x -k "$work/hived.zip" "$work/daemon"
[ -f "$work/daemon/hived" ] || fail "the download did not contain the daemon."
# The checksum says this is the file that was published. The signature says
# who built it, and that macOS will let it create virtual machines.
codesign --verify --strict "$work/daemon/hived" 2>/dev/null || fail "the daemon's code signature is not valid."
signer=$(codesign -dv "$work/daemon/hived" 2>&1 | sed -n 's/^TeamIdentifier=//p')
if [ -n "$team" ]; then
  [ "$signer" = "$team" ] || fail "the daemon is not signed by HiveWorker's developer ID."
elif [ "${HIVE_DEV:-}" != "1" ]; then
  fail "this release is a development build and is not signed for distribution."
fi
mkdir -p "$BIN_DIR"
install -m 0755 "$work/daemon/hived" "$BIN_DIR/hived"
say "Installed $BIN_DIR/hived"

step "Installing the Linux image jobs run in"
image_dir="$DATA_DIR/image"
have=""
if [ -f "$image_dir/manifest.json" ]; then
  have="$(plutil -extract imageVersion raw -o - "$image_dir/manifest.json" 2>/dev/null || true)-$(plutil -extract runnerVersion raw -o - "$image_dir/manifest.json" 2>/dev/null || true)"
fi
if [ "$have" = "$image_version" ]; then
  say "Already up to date ($image_version)."
else
  say "Downloading ${image_mb:-about 300} MB. This is the slow part."
  fetch -# -o "$work/image.aar" "$image_url"
  check "$work/image.aar" "$image_sha"
  mkdir -p "$DATA_DIR"
  rm -rf "$DATA_DIR/image.new"
  mkdir "$DATA_DIR/image.new"
  aa extract -d "$DATA_DIR/image.new" -i "$work/image.aar" || fail "could not unpack the image."
  [ -f "$DATA_DIR/image.new/manifest.json" ] || fail "the image download is incomplete."
  # Swapped in whole. A daemon running jobs keeps using the image it has
  # open until it is restarted below.
  rm -rf "$DATA_DIR/image.old"
  [ -d "$image_dir" ] && mv "$image_dir" "$DATA_DIR/image.old"
  mv "$DATA_DIR/image.new" "$image_dir"
  rm -rf "$DATA_DIR/image.old"
  say "Installed image $image_version"
fi

if [ "${HIVE_NO_START:-}" = "1" ]; then
  say ""
  say "Installed. Not signing in or starting the daemon, as asked."
  exit 0
fi

step "Adding this Mac to your hive"
if "$BIN_DIR/hived" status >/dev/null 2>&1; then
  say "This Mac is already signed in."
else
  # Read from the terminal: when this script is piped from curl, its own
  # input is the script.
  "$BIN_DIR/hived" login </dev/tty || fail "sign-in did not finish. Run this again to retry."
fi

step "Starting the daemon"
mkdir -p "$LOG_DIR" "$(dirname "$AGENT")"
cat >"$AGENT" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$LABEL</string>
	<key>ProgramArguments</key>
	<array>
		<string>$BIN_DIR/hived</string>
		<string>run</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>$LOG_DIR/hived.log</string>
	<key>StandardErrorPath</key>
	<string>$LOG_DIR/hived.log</string>
</dict>
</plist>
PLIST
domain="gui/$(id -u)"
# Stopping an older daemon first. A job it was running is cancelled and
# GitHub re-queues nothing by itself, so update when the Mac is idle.
if launchctl print "$domain/$LABEL" >/dev/null 2>&1; then
  launchctl bootout "$domain/$LABEL" 2>/dev/null || true
  # The old daemon destroys its VM on the way out, which takes a few
  # seconds, and the service cannot be started again until it is gone.
  waited=0
  while launchctl print "$domain/$LABEL" >/dev/null 2>&1; do
    [ "$waited" -lt 60 ] || fail "the old daemon did not stop. Run this again in a minute."
    sleep 1
    waited=$((waited + 1))
  done
fi
started=""
for attempt in 1 2 3 4 5; do
  if launchctl bootstrap "$domain" "$AGENT" 2>/dev/null; then
    started=yes
    break
  fi
  sleep 2
done
[ -n "$started" ] || fail "could not start the daemon. See $LOG_DIR/hived.log"

say ""
say "Done. This Mac now runs jobs for your hive whenever it is awake."
tips "this Mac" "~/.zshrc && exec zsh"
exit 0
fi

# ---------------------------------------------------------------- Linux ----
#
# The same thing, with the pieces Linux has instead: KVM for the VMs, a
# tarball for the daemon, and a systemd user service to keep it running.

case "$(uname -m)" in
  aarch64 | arm64) arch=arm64 qemu=qemu-system-aarch64 ;;
  x86_64 | amd64) arch=amd64 qemu=qemu-system-x86_64 ;;
  *) fail "the daemon runs on arm64 and x86-64, and this is $(uname -m)." ;;
esac
DATA_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/HiveWorker"
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
UNIT="hiveworker.service"

step "Checking this machine can run VMs"
missing=""
command -v "$qemu" >/dev/null 2>&1 || missing="$missing $qemu"
command -v qemu-img >/dev/null 2>&1 || missing="$missing qemu-img"
command -v passt >/dev/null 2>&1 || missing="$missing passt"
# The program that shares a job's directory with its VM. It must be the one
# that runs without root, which says "virtiofsd 1.x" for its version; the
# older one that came inside QEMU does not, and is no use here. A system
# without it is given a copy further down, so it is never "missing".
virtiofsd=""
for candidate in "$(command -v virtiofsd 2>/dev/null || true)" /usr/libexec/virtiofsd /usr/lib/virtiofsd; do
  if [ -n "$candidate" ] && [ -x "$candidate" ] && "$candidate" --version 2>/dev/null | grep -q '^virtiofsd [0-9]'; then
    virtiofsd="$candidate"
    break
  fi
done
command -v curl >/dev/null 2>&1 || missing="$missing curl"
command -v sha256sum >/dev/null 2>&1 || missing="$missing sha256sum"
if [ -n "$missing" ]; then
  say "Missing:$missing"
  say ""
  say "Install them, then run this again. This script never asks for your password."
  say "  Debian, Ubuntu, Raspberry Pi OS:  sudo apt install qemu-system qemu-utils passt curl"
  say "  Fedora:                           sudo dnf install qemu-kvm qemu-img passt curl"
  say "  Arch:                             sudo pacman -S qemu-base passt curl"
  exit 1
fi
[ -e /dev/kvm ] || fail "there is no /dev/kvm. Virtualization is switched off in this machine's firmware, or this is a cloud VM without nested virtualization."
if ! [ -r /dev/kvm ] || ! [ -w /dev/kvm ]; then
  say "Your user cannot use /dev/kvm yet. Run this, log out and back in, then run this script again:"
  say "  sudo usermod -aG kvm $(id -un)"
  exit 1
fi
say "KVM, QEMU and passt are here."

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fetch() {
  case "$BASE" in
    https://*) curl -fL --proto '=https' --tlsv1.2 --retry 3 "$@" ;;
    *) curl -fL --retry 3 "$@" ;;
  esac
}
# The Linux manifest is lines of name=value: no JSON parser is on every box.
field() { sed -n "s/^$1=//p" "$work/latest" | head -1; }
check() {
  actual=$(sha256sum "$1" | cut -d' ' -f1)
  [ "$actual" = "$2" ] || fail "$(basename "$1") does not match its published checksum. Nothing was installed."
}

step "Finding the current release"
fetch -sS -o "$work/latest" "$BASE/linux-$arch.txt" || fail "there is no release for Linux on $arch yet, or $BASE could not be reached."
version=$(field version)
daemon_url=$(field daemon_url)
daemon_sha=$(field daemon_sha256)
image_version=$(field image_version)
image_url=$(field image_url)
image_parts=$(field image_parts)
image_sha=$(field image_sha256)
image_mb=$(field image_megabytes)
[ -n "$version" ] && [ -n "$daemon_url" ] && [ -n "$daemon_sha" ] && [ -n "$image_url" ] && [ -n "$image_parts" ] && [ -n "$image_sha" ] ||
  fail "the release manifest is incomplete. Try again in a few minutes."
say "HiveWorker $version"

step "Installing the daemon"
fetch -sS -o "$work/hived.tar.gz" "$daemon_url"
check "$work/hived.tar.gz" "$daemon_sha"
mkdir "$work/daemon"
tar -xzf "$work/hived.tar.gz" -C "$work/daemon"
[ -f "$work/daemon/hived" ] || fail "the download did not contain the daemon."
mkdir -p "$BIN_DIR"
install -m 0755 "$work/daemon/hived" "$BIN_DIR/hived"
say "Installed $BIN_DIR/hived"

if [ -z "$virtiofsd" ]; then
  # This system packages no virtiofsd that runs without root. HiveWorker's
  # own build of it goes beside the image, where the daemon looks first.
  virtiofsd_url=$(field virtiofsd_url)
  virtiofsd_sha=$(field virtiofsd_sha256)
  [ -n "$virtiofsd_url" ] && [ -n "$virtiofsd_sha" ] || fail "this system has no virtiofsd that runs without root, and this release carries none. Install your distribution's virtiofsd package if it has one."
  if [ "$(sha256sum "$DATA_DIR/bin/virtiofsd" 2>/dev/null | cut -d' ' -f1)" != "$virtiofsd_sha" ]; then
    fetch -sS -o "$work/virtiofsd" "$virtiofsd_url"
    check "$work/virtiofsd" "$virtiofsd_sha"
    mkdir -p "$DATA_DIR/bin"
    install -m 0755 "$work/virtiofsd" "$DATA_DIR/bin/virtiofsd"
    fetch -sS -o "$DATA_DIR/bin/virtiofsd.NOTICE.txt" "$BASE/tools/virtiofsd.NOTICE.txt" || true
  fi
  say "Installed virtiofsd $(field virtiofsd_version), which this system does not package"
fi

step "Installing the Linux image jobs run in"
image_dir="$DATA_DIR/image"
have=""
if [ -f "$image_dir/manifest.json" ]; then
  number() { sed -n "s/.*\"$1\": *\"\{0,1\}\([^\",]*\).*/\1/p" "$image_dir/manifest.json" | head -1; }
  have="$(number imageVersion)-$(number runnerVersion)"
fi
if [ "$have" = "$image_version" ]; then
  say "Already up to date ($image_version)."
else
  say "Downloading ${image_mb:-about 300} MB. This is the slow part."
  # The image is published in pieces. The checksum is of the whole.
  : >"$work/image.tar.gz"
  for part in $image_parts; do
    fetch -# -o "$work/image.part" "$image_url.$part"
    cat "$work/image.part" >>"$work/image.tar.gz"
    rm -f "$work/image.part"
  done
  check "$work/image.tar.gz" "$image_sha"
  mkdir -p "$DATA_DIR"
  rm -rf "$DATA_DIR/image.new"
  mkdir "$DATA_DIR/image.new"
  # Sparse, so the disk takes the room its contents do and no more.
  tar -xzSf "$work/image.tar.gz" -C "$DATA_DIR/image.new" || fail "could not unpack the image."
  [ -f "$DATA_DIR/image.new/manifest.json" ] || fail "the image download is incomplete."
  rm -rf "$DATA_DIR/image.old"
  [ -d "$image_dir" ] && mv "$image_dir" "$DATA_DIR/image.old"
  mv "$DATA_DIR/image.new" "$image_dir"
  rm -rf "$DATA_DIR/image.old"
  say "Installed image $image_version"
fi

if [ "${HIVE_NO_START:-}" = "1" ]; then
  say ""
  say "Installed. Not signing in or starting the daemon, as asked."
  exit 0
fi

step "Adding this machine to your hive"
if "$BIN_DIR/hived" status >/dev/null 2>&1; then
  say "This machine is already signed in."
else
  "$BIN_DIR/hived" login </dev/tty || fail "sign-in did not finish. Run this again to retry."
fi

step "Starting the daemon"
if ! systemctl --user show-environment >/dev/null 2>&1; then
  say "This login has no systemd user session, so no service was set up."
  say "Start the daemon yourself, and keep it running however you keep things running:"
  say "  $BIN_DIR/hived run"
  exit 0
fi
mkdir -p "$UNIT_DIR"
cat >"$UNIT_DIR/$UNIT" <<SERVICE
[Unit]
Description=HiveWorker daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$BIN_DIR/hived run
Restart=on-failure
RestartSec=30
# Long enough for a job's VM to be destroyed and its files removed.
TimeoutStopSec=60

[Install]
WantedBy=default.target
SERVICE
systemctl --user daemon-reload
systemctl --user enable "$UNIT" >/dev/null 2>&1 || true
# Restart, not start: an older daemon is replaced. A job it was running is
# cancelled and GitHub re-queues nothing by itself, so update when idle.
systemctl --user restart "$UNIT" || fail "could not start the daemon. See: journalctl --user -u hiveworker"
# Without this the service stops when you log out.
if ! loginctl enable-linger "$(id -un)" >/dev/null 2>&1; then
  say "To keep it running after you log out:  sudo loginctl enable-linger $(id -un)"
fi
if ! [ -e /dev/vhost-vsock ]; then
  say ""
  say "Jobs will run, but without this machine's own Actions cache. To turn it on:"
  say "  echo vhost_vsock | sudo tee /etc/modules-load.d/hiveworker.conf && sudo modprobe vhost_vsock"
elif ! [ -r /dev/vhost-vsock ] || ! [ -w /dev/vhost-vsock ]; then
  say ""
  say "Jobs will run, but without this machine's own Actions cache. To turn it on,"
  say "join the kvm group, then log out and back in:"
  say "  sudo usermod -aG kvm $(id -un)"
fi

# The service belongs to this user's systemd, which took its groups when it
# started. If the kvm group was added since, the shell has it and the
# service does not yet, and the daemon says so in its first lines.
sleep 3
started=$(journalctl --user -u hiveworker -n 20 --no-pager 2>/dev/null || true)
if printf '%s' "$started" | grep -q "cannot open /dev/kvm"; then
  say ""
  say "The daemon is running but cannot use KVM yet: your login got the kvm group"
  say "after your user's services started. Restart them (or reboot), and it will:"
  say "  sudo systemctl restart user@$(id -u)"
elif printf '%s' "$started" | grep -q "cannot open /dev/vhost-vsock" && [ -r /dev/vhost-vsock ] && [ -w /dev/vhost-vsock ]; then
  say ""
  say "Jobs will run, but without this machine's own Actions cache until your"
  say "user's services are restarted: they started before your login got the"
  say "kvm group. Restart them (or reboot):"
  say "  sudo systemctl restart user@$(id -u)"
fi

say ""
say "Done. This machine now runs jobs for your hive. Logs: journalctl --user -u hiveworker"
tips "this machine" "~/.profile"
