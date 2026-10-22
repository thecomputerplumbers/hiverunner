#!/bin/bash
# Makes this macOS VM a Hive Runner image. Run it once inside the VM that
# `hived macos image open` shows, from the folder it shares:
#
#   sudo "/Volumes/My Shared Files/hive/install.sh"
#
# It installs the guest agent, which runs each job at boot, and lets the
# runner user use sudo without a password, as on GitHub's own macOS
# runners: every job's VM is thrown away after it. The runner user and its
# automatic login come first, made in System Settings.
#
# Two jobs' VMs run at once on one network, the host's NAT, and every clone
# has the same user and password. So nothing in the image listens: Remote
# Login, Screen Sharing, Remote Management and File Sharing are turned off,
# and the firewall blocks every incoming connection.
#
# NOT YET RUN ON REAL HARDWARE.
set -eu

user=runner
here=$(cd "$(dirname "$0")" && pwd)

if [ "$(id -u)" != 0 ]; then
  echo "Run it with sudo." >&2
  exit 1
fi
if ! id -u "$user" >/dev/null 2>&1; then
  echo "There is no user named $user. Make one in System Settings > Users & Groups, and turn on automatic login for it." >&2
  exit 1
fi

install -d -o root -g wheel -m 0755 /usr/local/libexec
install -o root -g wheel -m 0755 "$here/hive-agent" /usr/local/libexec/hive-agent
install -o root -g wheel -m 0644 "$here/dev.hiverunner.agent.plist" /Library/LaunchDaemons/dev.hiverunner.agent.plist

rule=$(mktemp)
echo "$user ALL=(ALL) NOPASSWD: ALL" > "$rule"
visudo -cf "$rule" >/dev/null
install -o root -g wheel -m 0440 "$rule" /etc/sudoers.d/hive-runner
rm -f "$rule"

# Nothing listens for another VM, or the host, to reach.
for service in com.openssh.sshd com.apple.screensharing com.apple.smbd; do
  launchctl bootout "system/$service" 2>/dev/null || true
  launchctl disable "system/$service" || true
done
/System/Library/CoreServices/RemoteManagement/ARDAgent.app/Contents/Resources/kickstart -deactivate -stop >/dev/null 2>&1 || true
/usr/libexec/ApplicationFirewall/socketfilterfw --setglobalstate on >/dev/null
/usr/libexec/ApplicationFirewall/socketfilterfw --setblockall on >/dev/null

# A VM that sleeps runs nothing until the host gives up on it.
pmset -a sleep 0 displaysleep 0 disksleep 0

if [ "$(defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>/dev/null || true)" != "$user" ]; then
  echo "Warning: $user does not log in automatically, and a job waits for that login. Turn it on in System Settings > Users & Groups." >&2
fi
echo "Installed. Shut this VM down from the Apple menu, then on the Mac run: hived macos image seal"
