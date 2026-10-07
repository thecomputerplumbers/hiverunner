#!/bin/sh
# Installs the HiveWorker daemon on this Mac and adds it to your hive.
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

[ "$(uname -s)" = "Darwin" ] || fail "the daemon runs on macOS, and this is $(uname -s)."
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
say ""
say "  hived status     what this Mac lends, and whether it is lending"
say "  hived pause      stop taking jobs"
say "  hived config     how many jobs, how large, and when"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    say ""
    say "$BIN_DIR is not on your PATH yet. To use the commands above, add it:"
    say "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.zshrc && exec zsh"
    ;;
esac
