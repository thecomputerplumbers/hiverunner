#!/bin/sh
# Installs the Hive Runner daemon on this machine and adds it to your hive.
# It runs on Apple Silicon Macs and on Linux with KVM (arm64 or x86-64).
#
#   curl -fsSL https://get.hiverunner.dev/install.sh | sh
#
# With no one at a terminal, as over ssh with a command or from an agent,
# name the hive this machine serves, as GitHub names the organization:
#
#   curl -fsSL https://get.hiverunner.dev/install.sh | sh -s -- --hive acme
#
# (or HIVE=acme; "all" for every hive you are in). The sign-in link is
# printed for someone to approve anywhere; nothing waits on a question.
# With an enroll key from a hive owner or admin there is no sign-in at all:
#
#   curl -fsSL https://get.hiverunner.dev/install.sh | HIVE_ENROLL_KEY=hwe_... sh -s -- --hive acme
#
# It downloads the daemon and the Linux image jobs run in, checks both
# against the checksums published with them, installs them under your home
# directory, signs this Mac in with GitHub, and starts the daemon as a
# background service for your user. Nothing is installed system-wide and
# nothing asks for your password.
#
# Run it again to update. A daemon that is running jobs is not restarted
# under them: it stops taking new ones, and the update waits for the ones it
# has. HIVE_NOW=1 does not wait, and cancels them. The guest image is staged
# on the data disk, not in /tmp. If the update fails, the machine is put
# back as it was. To remove everything: `hived uninstall`.
set -eu

# Which hives to add this machine to, if the person signing in is in more
# than one. hived checks the names, so they are only passed on here.
HIVE="${HIVE:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --hive)
      [ $# -ge 2 ] || { printf 'hiveworker: --hive needs the name of a hive\n' >&2; exit 2; }
      HIVE=$2
      shift 2
      ;;
    --hive=*)
      HIVE=${1#--hive=}
      shift
      ;;
    # hived reads the key from its environment, which keeps it out of the
    # process list; it is never printed.
    --enroll-key)
      [ $# -ge 2 ] || { printf 'hiveworker: --enroll-key needs a key\n' >&2; exit 2; }
      HIVE_ENROLL_KEY=$2
      export HIVE_ENROLL_KEY
      shift 2
      ;;
    --enroll-key=*)
      HIVE_ENROLL_KEY=${1#--enroll-key=}
      export HIVE_ENROLL_KEY
      shift
      ;;
    *)
      printf 'hiveworker: %s is not something this installer takes. It takes --hive <name> and --enroll-key <key>.\n' "$1" >&2
      exit 2
      ;;
  esac
done

BASE="${HIVE_RELEASES:-https://get.hiverunner.dev}"
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

# A failed install must not leave the machine paused. drain records that it
# paused this machine; cleanup lifts that pause unless a new daemon started
# to. The guest image is staged on the data disk, never in /tmp.
work=""
stage=""
drained=""
restarted=""
cleanup() {
  status=$?
  if [ -n "$drained" ] && [ -z "$restarted" ]; then
    if "$BIN_DIR/hived" drain --end >/dev/null 2>&1; then
      printf 'hiveworker: the update did not finish, so this machine was put back as it was.\n' >&2
    else
      printf 'hiveworker: the update did not finish. If this machine is not taking jobs, run: hived drain --end\n' >&2
    fi
  fi
  [ -n "$stage" ] && rm -rf "$stage"
  [ -n "$work" ] && rm -rf "$work"
  exit "$status"
}
trap 'cleanup' EXIT

# Before a running daemon is replaced: it is told to take no more jobs, and
# the ones it has are waited for. Restarting it at once would cancel them,
# and GitHub runs nothing again by itself. $1 is the hived that does the
# asking, which is the new one: an older daemon does as it is asked all the
# same, and the daemon that starts afterwards takes jobs again.
drain() {
  [ "${HIVE_NOW:-}" = "1" ] && return 0
  "$1" drain || fail "the daemon was left running as it was. To update now, and cancel the jobs it is running: hived update --now"
  drained=yes
}

# Free 1024-byte blocks on the filesystem of $1.
free_kb() {
  df -Pk "$1" | awk 'NR==2 { print $4 }'
}

# The guest image is staged beside where it will be installed. $1 is that
# home, $2 the published size in megabytes.
prepare_image_download() {
  home=$1
  mb=$2
  mkdir -p "$home" || fail "could not make $home"
  case $mb in
    '' | *[!0-9]*) mb=800 ;;
  esac
  # The archive, an unpacked copy beside the old image, and a little room.
  need_mb=$((mb * 3 + 64))
  free=$(free_kb "$home")
  case $free in
    '' | *[!0-9]*) fail "could not see how much room is in $home" ;;
  esac
  free_mb=$((free / 1024))
  if [ "$free_mb" -lt "$need_mb" ]; then
    fail "there is not enough room in $home for the guest image (${mb} MB to download; about $need_mb MB free is needed, and $free_mb MB is free). Free some space and run this again."
  fi
  stage="$home/staging"
  rm -rf "$stage"
  mkdir "$stage" || fail "could not make $stage"
}

# Said in place of signing in, to a machine that already is. Someone running
# this again for a second hive is told it did not add one, and what does.
signed_in() {
  hives=$("$BIN_DIR/hived" hives 2>/dev/null | paste -sd, - | sed 's/,/, /g') || hives=""
  say "This $1 is already signed in${hives:+, and serves: $hives}."
  say "To add it to another hive, run: hived link"
}

# Signs this machine in with GitHub and adds it to a hive; fails if it was
# not added. hived reads the terminal when there is one: piped from curl,
# this script's own input is the script. When there is none it is given
# nothing to read, and says the command to run instead of asking.
# HIVE_INSTALLING tells it the daemon is started here, not by it.
enroll() {
  set -- login
  [ -z "$HIVE" ] || set -- "$@" --hive "$HIVE"
  if (: </dev/tty) 2>/dev/null; then
    HIVE_INSTALLING=1 "$BIN_DIR/hived" "$@" </dev/tty
  else
    HIVE_INSTALLING=1 "$BIN_DIR/hived" "$@" </dev/null
  fi
}

# Said at the end when signing in did not add this machine to a hive.
# Everything else is in place, so one command is left, and it starts the
# daemon too.
not_enrolled() { # what this machine is called
  say ""
  say "Installed, but $1 is not in a hive yet, so the daemon was not started."
  say "See what hived said above. One command is left, and it starts the daemon:"
  say "  ${HIVE_ENROLL_KEY:+HIVE_ENROLL_KEY=<the key> }$BIN_DIR/hived login${HIVE:+ --hive $HIVE}"
  exit 1
}

# Where the image goes: beside the settings, unless its owner keeps it on
# another disk (hived config set data-dir). The daemon just installed is
# asked, since the settings file is its to read. A disk that is not attached
# stops the install: the image is not put somewhere its owner did not choose.
image_home() {
  chosen=$("$BIN_DIR/hived" config get data-dir 2>/dev/null) || chosen=""
  [ -n "$chosen" ] || chosen="$DATA_DIR"
  [ "$chosen" = "$DATA_DIR" ] || [ -d "$chosen" ] ||
    fail "the image is kept in $chosen, which is not there. Attach its disk and run this again, or run: hived config set data-dir default"
  printf '%s\n' "$chosen"
}

# What to type next, said at the end on either system.
tips() { # what this machine is called, the shell's startup file
  say ""
  say "  hived status     what $1 lends, and whether it is lending"
  say "  hived pause      stop taking jobs"
  say "  hived config     how many jobs, how large, and when"
  # Under Windows the commands are typed in Windows, where install.ps1 puts
  # a hived of its own on the PATH.
  case ":$PATH:${HIVE_ON_WINDOWS:+$BIN_DIR:}" in
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
image_parts=$(field image.parts)
image_sha=$(field image.sha256)
image_mb=$(field image.megabytes)
[ -n "$version" ] && [ -n "$daemon_url" ] && [ -n "$daemon_sha" ] && [ -n "$image_url" ] && [ -n "$image_sha" ] ||
  fail "the release manifest is incomplete. Try again in a few minutes."
say "Hive Runner $version"

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
  [ "$signer" = "$team" ] || fail "the daemon is not signed by Hive Runner's developer ID."
elif [ "${HIVE_DEV:-}" != "1" ]; then
  fail "this release is a development build and is not signed for distribution."
fi
mkdir -p "$BIN_DIR"
# Whether anything this installs is new is remembered as it goes: a daemon
# that is already all of it is left running at the end, and one that is not
# is drained before the first thing under it is replaced.
changed=""
running=""
launchctl print "gui/$(id -u)/$LABEL" >/dev/null 2>&1 && running=yes
if ! cmp -s "$work/daemon/hived" "$BIN_DIR/hived"; then
  changed=yes
  [ -z "$running" ] || drain "$work/daemon/hived"
fi
install -m 0755 "$work/daemon/hived" "$BIN_DIR/hived"
say "Installed $BIN_DIR/hived"

step "Installing the Linux image jobs run in"
image_home=$(image_home)
image_dir="$image_home/image"
have=""
if [ -f "$image_dir/manifest.json" ]; then
  have="$(plutil -extract imageVersion raw -o - "$image_dir/manifest.json" 2>/dev/null || true)-$(plutil -extract runnerVersion raw -o - "$image_dir/manifest.json" 2>/dev/null || true)"
fi
if [ "$have" = "$image_version" ]; then
  say "Already up to date ($image_version)."
else
  changed=yes
  prepare_image_download "$image_home" "$image_mb"
  [ -z "$running" ] || drain "$BIN_DIR/hived"
  say "Downloading ${image_mb:-about 300} MB. This is the slow part."
  # The image is published in pieces. The checksum is of the whole. A
  # manifest that names no pieces is of an image published whole.
  if [ -n "$image_parts" ]; then
    : >"$stage/image.aar"
    for part in $image_parts; do
      fetch -# -o "$stage/image.part" "$image_url.$part"
      cat "$stage/image.part" >>"$stage/image.aar"
      rm -f "$stage/image.part"
    done
  else
    fetch -# -o "$stage/image.aar" "$image_url"
  fi
  check "$stage/image.aar" "$image_sha"
  rm -rf "$image_home/image.new"
  mkdir "$image_home/image.new"
  aa extract -d "$image_home/image.new" -i "$stage/image.aar" || fail "could not unpack the image."
  [ -f "$image_home/image.new/manifest.json" ] || fail "the image download is incomplete."
  rm -rf "$stage"
  stage=""
  # Swapped in whole. A daemon running jobs keeps using the image it has
  # open until it is restarted below.
  rm -rf "$image_home/image.old"
  [ -d "$image_dir" ] && mv "$image_dir" "$image_home/image.old"
  mv "$image_home/image.new" "$image_dir"
  rm -rf "$image_home/image.old"
  say "Installed image $image_version"
fi

if [ "${HIVE_NO_START:-}" = "1" ]; then
  say ""
  say "Installed. Not signing in or starting the daemon, as asked."
  exit 0
fi

step "Adding this Mac to your hive"
enrolled=yes
if "$BIN_DIR/hived" status >/dev/null 2>&1; then
  signed_in Mac
else
  enroll || enrolled=""
fi

step "Starting the daemon"
mkdir -p "$LOG_DIR" "$(dirname "$AGENT")"
cat >"$work/agent.plist" <<PLIST
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
cmp -s "$work/agent.plist" "$AGENT" || changed=yes
cp "$work/agent.plist" "$AGENT"
domain="gui/$(id -u)"
# Not in a hive, the daemon would only exit and be started again. It is
# left for hived login to start.
[ -n "$enrolled" ] || not_enrolled "this Mac"
# Nothing new, and the daemon is running: it is left alone. Restarting it
# would cancel a job it is in the middle of, for the same daemon again.
if [ -z "$changed" ] && launchctl print "$domain/$LABEL" >/dev/null 2>&1; then
  say "hived $version is already running, and was left as it is."
  say ""
  say "Done. Nothing needed updating."
  tips "this Mac" "~/.zshrc && exec zsh"
  exit 0
fi
# Stopping an older daemon first, once it has no job. (It was drained
# above if the daemon or the image is new; this is for a change to nothing
# but how it is started.)
if launchctl print "$domain/$LABEL" >/dev/null 2>&1; then
  drain "$BIN_DIR/hived"
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
if [ -z "$started" ]; then
  fail "could not start the daemon. See $LOG_DIR/hived.log"
fi
restarted=yes

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

# Ubuntu 24.04 and later let no unprivileged program make a user namespace
# unless an AppArmor profile allows it, and the virtiofsd this installs
# needs one to share a job's directory with its VM. Without it every job
# fails, with "Error entering sandbox: CleanMount(PermissionDenied)". This
# says what to run, since this script never asks for a password; hived
# doctor says the same. Only for the copy installed here: a distribution's
# own virtiofsd is its packager's to allow.
apparmor() {
  [ -x "$DATA_DIR/bin/virtiofsd" ] && [ "$virtiofsd" = "" ] || return 0
  [ "$(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns 2>/dev/null)" = "1" ] || return 0
  profile=/etc/apparmor.d/hiverunner-virtiofsd
  [ -e "$profile" ] && return 0
  grep -qsF "$DATA_DIR/bin/virtiofsd" /etc/apparmor.d/* 2>/dev/null && return 0
  say ""
  say "AppArmor stops virtiofsd here, so no job can start until this is run. It"
  say "allows that one program a user namespace and nothing else:"
  say "  printf 'abi <abi/4.0>,\\ninclude <tunables/global>\\nprofile hiverunner-virtiofsd \"$DATA_DIR/bin/virtiofsd\" flags=(unconfined) {\\n  userns,\\n  include if exists <local/hiverunner-virtiofsd>\\n}\\n' | sudo tee $profile >/dev/null && sudo apparmor_parser -r $profile"
}

# Ubuntu's 73-seat-late.rules (uaccess) runs on a change event, which a VM
# starting sends, and wipes a user ACL on /dev/kvm. A one-shot setfacl is
# wiped the same way. A udev rule named 99-hiverunner-$user.rules runs after
# uaccess and puts the ACL back. 70-hiverunner-$user.rules is ours from
# before this and is removed: it ran too early. Printed, not run: this
# script never asks for a password; hived doctor says the same.
kvm_acl_cmd() {
  user=$(id -un)
  printf '%s' "printf 'KERNEL==\"kvm\", ACTION==\"add|change\", RUN+=\"/usr/bin/setfacl -m u:${user}:rw /dev/kvm\"\\nKERNEL==\"vhost-vsock\", ACTION==\"add|change\", RUN+=\"/usr/bin/setfacl -m u:${user}:rw /dev/vhost-vsock\"\\n' | sudo tee /etc/udev/rules.d/99-hiverunner-${user}.rules >/dev/null && sudo rm -f /etc/udev/rules.d/70-hiverunner-${user}.rules && sudo setfacl -m u:${user}:rw /dev/kvm /dev/vhost-vsock"
}

# A 70- rule of ours, from before this, still on disk: the installer owns
# that name, and moving its contents after 73-seat-late.rules is the fix.
kvm_acl() {
  user=$(id -un)
  early=/etc/udev/rules.d/70-hiverunner-$user.rules
  late=/etc/udev/rules.d/99-hiverunner-$user.rules
  [ -e "$early" ] && [ ! -e "$late" ] || return 0
  say ""
  say "A udev rule at $early grants you /dev/kvm, but Ubuntu's"
  say "73-seat-late.rules (uaccess) runs after it and wipes that ACL when a VM"
  say "starts. This writes the same rule so it runs after uaccess:"
  say "  $(kvm_acl_cmd); systemctl --user restart hiveworker"
}

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
  # The group is for good, from the next login. The udev rule is for now,
  # for this shell and the daemon's service alike, so nobody has to log out,
  # and it survives the change event a VM starting sends.
  say "Your user cannot use /dev/kvm yet. Run this, then this script again:"
  say "  sudo usermod -aG kvm $(id -un) && $(kvm_acl_cmd)"
  exit 1
fi
say "KVM, QEMU and passt are here."

work=$(mktemp -d)
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
say "Hive Runner $version"

step "Installing the daemon"
fetch -sS -o "$work/hived.tar.gz" "$daemon_url"
check "$work/hived.tar.gz" "$daemon_sha"
mkdir "$work/daemon"
tar -xzf "$work/hived.tar.gz" -C "$work/daemon"
[ -f "$work/daemon/hived" ] || fail "the download did not contain the daemon."
mkdir -p "$BIN_DIR"
# Whether anything this installs is new is remembered as it goes: a daemon
# that is already all of it is left running at the end.
changed=""
running=""
systemctl --user is-active --quiet "$UNIT" 2>/dev/null && running=yes
if ! cmp -s "$work/daemon/hived" "$BIN_DIR/hived"; then
  changed=yes
  [ -z "$running" ] || drain "$work/daemon/hived"
fi
install -m 0755 "$work/daemon/hived" "$BIN_DIR/hived"
say "Installed $BIN_DIR/hived"

if [ -z "$virtiofsd" ]; then
  # This system packages no virtiofsd that runs without root. Hive Runner's
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
image_home=$(image_home)
image_dir="$image_home/image"
have=""
if [ -f "$image_dir/manifest.json" ]; then
  number() { sed -n "s/.*\"$1\": *\"\{0,1\}\([^\",]*\).*/\1/p" "$image_dir/manifest.json" | head -1; }
  have="$(number imageVersion)-$(number runnerVersion)"
fi
if [ "$have" = "$image_version" ]; then
  say "Already up to date ($image_version)."
else
  changed=yes
  prepare_image_download "$image_home" "$image_mb"
  [ -z "$running" ] || drain "$BIN_DIR/hived"
  say "Downloading ${image_mb:-about 300} MB. This is the slow part."
  # The image is published in pieces. The checksum is of the whole.
  : >"$stage/image.tar.gz"
  for part in $image_parts; do
    fetch -# -o "$stage/image.part" "$image_url.$part"
    cat "$stage/image.part" >>"$stage/image.tar.gz"
    rm -f "$stage/image.part"
  done
  check "$stage/image.tar.gz" "$image_sha"
  rm -rf "$image_home/image.new"
  mkdir "$image_home/image.new"
  # Sparse, so the disk takes the room its contents do and no more.
  tar -xzSf "$stage/image.tar.gz" -C "$image_home/image.new" || fail "could not unpack the image."
  [ -f "$image_home/image.new/manifest.json" ] || fail "the image download is incomplete."
  rm -rf "$stage"
  stage=""
  rm -rf "$image_home/image.old"
  [ -d "$image_dir" ] && mv "$image_dir" "$image_home/image.old"
  mv "$image_home/image.new" "$image_dir"
  rm -rf "$image_home/image.old"
  say "Installed image $image_version"
fi

if [ "${HIVE_NO_START:-}" = "1" ]; then
  say ""
  say "Installed. Not signing in or starting the daemon, as asked."
  exit 0
fi

step "Adding this machine to your hive"
enrolled=yes
if "$BIN_DIR/hived" status >/dev/null 2>&1; then
  signed_in machine
else
  enroll || enrolled=""
fi

step "Starting the daemon"
if ! systemctl --user show-environment >/dev/null 2>&1; then
  say "This login has no systemd user session, so no service was set up."
  say "Start the daemon yourself, and keep it running however you keep things running:"
  say "  $BIN_DIR/hived run"
  kvm_acl
  apparmor
  [ -n "$enrolled" ] || not_enrolled "this machine"
  exit 0
fi
mkdir -p "$UNIT_DIR"
cat >"$work/$UNIT" <<SERVICE
[Unit]
Description=Hive Runner daemon
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
cmp -s "$work/$UNIT" "$UNIT_DIR/$UNIT" || changed=yes
cp "$work/$UNIT" "$UNIT_DIR/$UNIT"
if [ -z "$enrolled" ]; then
  # Set up to start at boot, and not started now: not in a hive, it would
  # only exit. hived login starts it.
  systemctl --user daemon-reload
  systemctl --user enable "$UNIT" >/dev/null 2>&1 || true
  loginctl enable-linger "$(id -un)" >/dev/null 2>&1 || true
  kvm_acl
  apparmor
  not_enrolled "this machine"
fi
# Nothing new, and the daemon is running: it is left alone. Restarting it
# would cancel a job it is in the middle of, for the same daemon again.
if [ -z "$changed" ] && systemctl --user is-active --quiet "$UNIT"; then
  say "hived $version is already running, and was left as it is."
  kvm_acl
  apparmor
  say ""
  say "Done. Nothing needed updating."
  tips "this machine" "~/.profile"
  exit 0
fi
# A running daemon is replaced once it has no job. (It was drained above if
# the daemon or the image is new; this is for a change to nothing but how it
# is started.)
if systemctl --user is-active --quiet "$UNIT"; then
  drain "$BIN_DIR/hived"
fi
systemctl --user daemon-reload
systemctl --user enable "$UNIT" >/dev/null 2>&1 || true
if ! systemctl --user restart "$UNIT"; then
  fail "could not start the daemon. See: journalctl --user -u hiveworker"
fi
restarted=yes
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
# service does not yet, and the daemon says so in its first lines. A udev
# rule after Ubuntu's 73-seat-late.rules lends the devices without ending
# any session; a one-shot setfacl, or a 70- rule of ours, is wiped when a
# VM starts. Restarting the user's systemd would also do it, and would end
# every session they have, an agent's in tmux included.
sleep 3
started=$(journalctl --user -u hiveworker -n 20 --no-pager 2>/dev/null || true)
user=$(id -un)
early=/etc/udev/rules.d/70-hiverunner-$user.rules
late=/etc/udev/rules.d/99-hiverunner-$user.rules
lend="$(kvm_acl_cmd); systemctl --user restart hiveworker"
holds=""; id -nG | grep -qw kvm && holds=yes
listed=""; id -nG "$user" | grep -qw kvm && listed=yes
if [ -e "$early" ] && [ ! -e "$late" ]; then
  : # kvm_acl, next to apparmor, says to replace the 70- rule.
elif printf '%s' "$started" | grep -q "cannot open /dev/kvm"; then
  say ""
  say "The daemon is running but cannot use KVM yet: your login got the kvm group"
  say "after your user's services started. This writes a udev rule that lasts"
  say "through a VM starting, without ending any of your sessions:"
  say "  $lend"
  say "(sudo systemctl restart user@$(id -u) works too, but ends every session of yours.)"
elif printf '%s' "$started" | grep -q "cannot open /dev/vhost-vsock" && [ -r /dev/vhost-vsock ] && [ -w /dev/vhost-vsock ]; then
  say ""
  say "Jobs will run, but without this machine's own Actions cache: your user's"
  say "services started before your login got the kvm group. This writes a udev"
  say "rule that lasts through a VM starting, without ending any of your sessions:"
  say "  $lend"
elif [ -n "$listed" ] && [ -z "$holds" ] && [ ! -e "$late" ]; then
  say ""
  say "Your login is in the kvm group, but this session's services are not, so"
  say "/dev/kvm is only an ACL that Ubuntu's 73-seat-late.rules (uaccess) wipes"
  say "when a VM starts. This writes a udev rule that puts it back:"
  say "  $lend"
  say "(sudo systemctl restart user@$(id -u) works too, but ends every session of yours.)"
fi
kvm_acl
apparmor

say ""
say "Done. This machine now runs jobs for your hive. Logs: journalctl --user -u hiveworker"
tips "this machine" "~/.profile"
