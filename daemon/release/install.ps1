# Installs the HiveWorker daemon on this Windows PC and adds it to your hive.
#
#   irm https://get.hiveworkr.thecomputerplumbers.com/install.ps1 | iex
#
# Jobs are Linux, so on Windows the daemon is the Linux one, running in a
# small Linux of its own under Windows' Linux subsystem (WSL 2), which gives
# it the hardware virtualization it needs. This script creates that Linux,
# named HiveWorker and separate from any other you have, installs what the
# daemon needs inside it, runs the ordinary Linux installer there, and
# arranges for it to keep running while you are signed in.
#
# Nothing is installed for other users and nothing here asks for
# administrator rights. If WSL itself is missing, the script says how to add
# it (that one step does need them) and stops.
#
# Run it again to update. To remove everything, run it with
# $env:HIVE_UNINSTALL = '1' set first.

& {
  $ErrorActionPreference = 'Stop'
  $ProgressPreference = 'SilentlyContinue'
  # wsl.exe speaks UTF-16 unless asked not to, which PowerShell cannot read.
  $env:WSL_UTF8 = '1'

  $Distro = 'HiveWorker'
  $User = 'hive'
  $Base = if ($env:HIVE_RELEASES) { $env:HIVE_RELEASES.TrimEnd('/') } else { 'https://get.hiveworkr.thecomputerplumbers.com' }
  $Root = Join-Path $env:LOCALAPPDATA 'HiveWorker'
  $Bin = Join-Path $Root 'bin'
  $RunKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
  # Ubuntu's own image for WSL, checked against the sums published beside it.
  $Ubuntu = 'https://cloud-images.ubuntu.com/wsl/releases/24.04/current'
  $UbuntuFile = 'ubuntu-noble-wsl-amd64-wsl.rootfs.tar.gz'

  function Say([string]$Text) { Write-Host $Text }
  function Step([string]$Text) { Write-Host ''; Write-Host $Text -ForegroundColor White }
  function Fail([string]$Text) { throw "hiveworker: $Text" }

  # Runs a command inside the HiveWorker Linux and returns whether it worked.
  # What it prints goes to the screen, not into the answer.
  function InLinux([string]$As, [string]$Command) {
    & wsl.exe -d $Distro -u $As -- sh -c $Command | Out-Host
    return ($LASTEXITCODE -eq 0)
  }
  # Runs a shell script inside it, from a file the Linux reads. Passed as an
  # argument its quotes and line breaks do not survive the trip, and piped
  # it would arrive with Windows' line endings and break the shell.
  function RunScript([string]$As, [string]$Text) {
    $file = Join-Path $Root 'step.sh'
    [IO.File]::WriteAllText($file, ($Text -replace "`r`n", "`n") + "`n", (New-Object Text.UTF8Encoding($false)))
    try {
      & wsl.exe -d $Distro -u $As --cd $Root -- sh ./step.sh | Out-Host
      return ($LASTEXITCODE -eq 0)
    } finally {
      Remove-Item -Force $file -ErrorAction SilentlyContinue
    }
  }
  # Through cmd where a program's complaints are to be thrown away: Windows
  # PowerShell turns discarded error output into an error of its own.
  function HasDistro {
    $names = (& cmd.exe /c 'wsl.exe --list --quiet 2>nul') -split "`r?`n" | ForEach-Object { $_.Trim() }
    return ($names -contains $Distro)
  }
  # What keeps the Linux running: WSL stops one that nothing is attached to.
  $KeepAliveArgs = "--headless wsl.exe -d $Distro -u $User --exec sleep infinity"
  $KeepAlive = "conhost.exe $KeepAliveArgs"
  function KeepAlives {
    Get-CimInstance Win32_Process -Filter "Name = 'wsl.exe'" -ErrorAction SilentlyContinue |
      Where-Object { $_.CommandLine -like "*-d $Distro*sleep infinity*" }
  }
  function StopKeepAlive {
    KeepAlives | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
  }
  # Starts it unless it is running, and returns whether it is running after.
  # One started from a session with no desktop, as over SSH, ends with that
  # session, so there Windows is asked to start it on the desktop of whoever
  # is signed in, through a task that is removed again at once.
  function StartKeepAlive {
    if (KeepAlives) { return $true }
    if ((Get-Process -Id $PID).SessionId -ne 0) {
      Start-Process -FilePath 'conhost.exe' -ArgumentList $KeepAliveArgs -WindowStyle Hidden
      return $true
    }
    $task = 'HiveWorker keep-alive'
    try {
      $me = [Security.Principal.WindowsIdentity]::GetCurrent().Name
      # Without these Windows would end it after three days, or on battery.
      $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
      Register-ScheduledTask -TaskName $task -Force -Settings $settings `
        -Action (New-ScheduledTaskAction -Execute 'conhost.exe' -Argument $KeepAliveArgs) `
        -Principal (New-ScheduledTaskPrincipal -UserId $me -LogonType Interactive) | Out-Null
      Start-ScheduledTask -TaskName $task
      for ($i = 0; $i -lt 10 -and -not (KeepAlives); $i++) { Start-Sleep -Milliseconds 500 }
    } catch {
    } finally {
      Unregister-ScheduledTask -TaskName $task -Confirm:$false -ErrorAction SilentlyContinue
    }
    return [bool](KeepAlives)
  }

  try {
    if ($env:HIVE_UNINSTALL -eq '1') {
      Step 'Removing HiveWorker'
      StopKeepAlive
      Remove-ItemProperty -Path $RunKey -Name 'HiveWorker' -ErrorAction SilentlyContinue
      if (Get-Command wsl.exe -ErrorAction SilentlyContinue) {
        if (HasDistro) { & wsl.exe --unregister $Distro | Out-Null }
      }
      $path = [Environment]::GetEnvironmentVariable('Path', 'User')
      if ($path) {
        $kept = ($path -split ';' | Where-Object { $_ -and ($_ -ne $Bin) }) -join ';'
        [Environment]::SetEnvironmentVariable('Path', $kept, 'User')
      }
      if (Test-Path $Root) { Remove-Item -Recurse -Force $Root }
      Say 'Removed. This PC still appears in your hive as offline; remove it from the dashboard under Machines.'
      return
    }

    Step 'Checking this PC can run VMs'
    if ([Environment]::OSVersion.Version.Build -lt 22000) {
      Fail 'the daemon needs Windows 11. Windows 10 cannot give a Linux inside WSL the virtualization jobs need.'
    }
    if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
      Fail "the daemon needs a 64-bit Intel or AMD PC, and this is $($env:PROCESSOR_ARCHITECTURE). Windows on Arm cannot run VMs inside WSL."
    }
    $wslWorks = $false
    if (Get-Command wsl.exe -ErrorAction SilentlyContinue) {
      & cmd.exe /c 'wsl.exe --status >nul 2>&1'
      $wslWorks = ($LASTEXITCODE -eq 0)
    }
    if (-not $wslWorks) {
      Say 'Windows'' Linux subsystem (WSL) is not set up on this PC. Add it once, from a'
      Say 'PowerShell window opened with "Run as administrator":'
      Say ''
      Say '  wsl --install --no-distribution'
      Say ''
      Say 'Restart the PC when it asks, then run this script again.'
      return
    }
    Say 'Windows 11 and WSL are here.'

    New-Item -ItemType Directory -Force -Path $Root, $Bin | Out-Null

    if (HasDistro) {
      Step 'Using the HiveWorker Linux already on this PC'
    } else {
      Step 'Creating a Linux for HiveWorker'
      $work = Join-Path $env:TEMP ("hiveworker-" + [Guid]::NewGuid().ToString('N'))
      New-Item -ItemType Directory -Force -Path $work | Out-Null
      try {
        $tar = Join-Path $work $UbuntuFile
        Say 'Downloading Ubuntu 24.04, about 350 MB.'
        & curl.exe -fL --retry 3 --proto '=https' --tlsv1.2 -# -o $tar "$Ubuntu/$UbuntuFile"
        if ($LASTEXITCODE -ne 0) { Fail 'could not download Ubuntu. Check your connection and try again.' }
        $sums = (& curl.exe -fsSL --proto '=https' --tlsv1.2 "$Ubuntu/SHA256SUMS") -join "`n"
        $want = ($sums -split "`n" | Where-Object { $_ -match [regex]::Escape($UbuntuFile) + '$' } | Select-Object -First 1) -replace '\s.*$', ''
        $have = (Get-FileHash -Algorithm SHA256 $tar).Hash.ToLower()
        if (-not $want -or ($have -ne $want.ToLower())) {
          Fail 'the Ubuntu download does not match its published checksum. Nothing was installed.'
        }
        & wsl.exe --import $Distro (Join-Path $Root 'linux') $tar --version 2
        if ($LASTEXITCODE -ne 0) { Fail 'could not create the Linux. Is virtualization switched on in this PC''s firmware?' }
      } finally {
        Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
      }
    }

    Step 'Installing what the daemon needs inside it'
    $setup = @'
set -eu
export DEBIAN_FRONTEND=noninteractive
id hive >/dev/null 2>&1 || useradd --create-home --shell /bin/bash hive
getent group kvm >/dev/null || groupadd --system kvm
usermod -aG kvm hive
apt-get update -qq
apt-get install -y -qq --no-install-recommends \
  qemu-system-x86 qemu-utils passt virtiofsd uidmap curl ca-certificates dbus-user-session >/dev/null
# Each time this Linux starts: the two devices a VM needs, made usable by the
# daemon's user.
cat > /usr/local/sbin/hive-devices <<'DEVICES'
#!/bin/sh
modprobe -a kvm_intel kvm_amd vhost_vsock 2>/dev/null
for device in /dev/kvm /dev/vhost-vsock; do
  [ -e "$device" ] && chgrp kvm "$device" && chmod 0660 "$device"
done
exit 0
DEVICES
chmod 0755 /usr/local/sbin/hive-devices
# systemd, so the daemon is a service like on any Linux; the daemon's user by
# default; Windows' own programs off the PATH.
cat > /etc/wsl.conf.new <<'CONF'
[boot]
systemd=true
command=/usr/local/sbin/hive-devices

[user]
default=hive

[interop]
appendWindowsPath=false
CONF
# Said to the script that called this one, which restarts the Linux for it.
if cmp -s /etc/wsl.conf.new /etc/wsl.conf; then
  rm /etc/wsl.conf.new
else
  mv /etc/wsl.conf.new /etc/wsl.conf
  touch /run/hive-restart
fi
'@
    if (-not (RunScript 'root' $setup)) { Fail 'could not set up the Linux. Run this again to retry.' }

    # Restarted when that changed how it starts, so it comes up with systemd
    # and the devices as configured. Otherwise left running: a restart would
    # end a job it is running, or a sign-in someone is waiting on.
    if (InLinux 'root' 'test -e /run/hive-restart || ! test -d /run/systemd/system') {
      StopKeepAlive
      & wsl.exe --terminate $Distro | Out-Null
    }
    # "degraded" is a Linux with one unit unhappy, which is still running.
    [void](InLinux 'root' 'timeout 90 systemctl is-system-running --wait >/dev/null 2>&1; /usr/local/sbin/hive-devices; loginctl enable-linger hive')
    if (-not (InLinux $User 'test -r /dev/kvm && test -w /dev/kvm')) {
      Say ''
      Say 'This Linux has no hardware virtualization, so it cannot run jobs. Two things to check:'
      Say '  1. Virtualization (Intel VT-x or AMD-V) is switched on in this PC''s firmware.'
      Say '  2. %UserProfile%\.wslconfig does not say nestedVirtualization=false.'
      Say 'Then run "wsl --shutdown" and this script again.'
      return
    }
    Say 'KVM, QEMU, virtiofsd and passt are here.'

    # The ordinary Linux installer does the rest: the daemon, the image jobs
    # run in, signing in with GitHub, and the service.
    # (It waits a moment first for this user's own services to be up, which
    # the installer needs to register the daemon with.)
    $install = @'
set -eu
export XDG_RUNTIME_DIR="/run/user/$(id -u)"
cd
for i in $(seq 30); do
  systemctl --user show-environment >/dev/null 2>&1 && break
  sleep 1
done
curl -fsSL "$HIVE_RELEASES/install.sh" | sh
'@
    if (-not (RunScript $User ("export HIVE_RELEASES='$Base' HIVE_ON_WINDOWS=1`n" + $install))) { Fail 'the install inside the Linux did not finish. Run this again to retry.' }

    Step 'Keeping it running'
    Set-ItemProperty -Path $RunKey -Name 'HiveWorker' -Value $KeepAlive
    if (StartKeepAlive) {
      Say 'It starts when you sign in to Windows, and stops when you sign out.'
    } else {
      Say 'No one is signed in at this PC, so it is not running yet. It starts when'
      Say 'you sign in to Windows, and stops when you sign out.'
    }

    # hived, from any Windows terminal.
    $shim = "@echo off`r`nwsl.exe -d $Distro -u $User -- /home/$User/.local/bin/hived %*`r`n"
    [IO.File]::WriteAllText((Join-Path $Bin 'hived.cmd'), $shim, (New-Object Text.ASCIIEncoding))
    $path = [string][Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (($path -split ';') -contains $Bin)) {
      [Environment]::SetEnvironmentVariable('Path', (($path.TrimEnd(';') + ';' + $Bin).TrimStart(';')), 'User')
      Say ''
      Say 'Open a new terminal to use the hived command from Windows.'
    }

    Say ''
    Say 'Done. This PC now runs jobs for your hive while you are signed in.'
    Say ''
    Say '  hived status     what this PC lends, and whether it is lending'
    Say '  hived pause      stop taking jobs'
    Say '  hived config     how many jobs, how large, and when'
  } catch {
    Write-Host ''
    Write-Host $_.Exception.Message -ForegroundColor Red
  }
}
