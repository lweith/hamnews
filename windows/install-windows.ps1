<#
.SYNOPSIS
  Set up a hamnews station on Windows: the news server runs in WSL2 (Ubuntu);
  hnlink, FLAMP, direwolf and your newsreader run on Windows.

.DESCRIPTION
  1. Checks for WSL and the Linux distribution, installing Ubuntu if needed (then
     stops so you can create the Linux user; re-run afterwards).
  2. Turns on systemd in the distribution.
  3. Installs the hamnews .deb (callsign preseeded through debconf) and tin
     inside it, and the net's group list from groups.txt. hnlink runs on
     Windows, next to direwolf.
  4. Leaves Windows FLAMP's settings alone (with -DisableFlampAutoSave, turns off
     "Auto save rx files"; see OPERATIONS.md section 2.1).
  5. Installs hnlink.exe and the 'hamnews' command into %LOCALAPPDATA%\hamnews,
     adds that to your PATH, and registers logon tasks: one keeps the WSL
     distribution running (news on localhost:119), one runs hnlink (automatic
     exchange through direwolf's KISS port), unless -NoAutoStart / -NoLink.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File install-windows.ps1 -Call kn6fln -From 'KN6FLN <you@example.com>'
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Call,
    [string]$From,
    [string]$Distro = 'Ubuntu',
    [int]$Ssid = 5,
    [string]$Kiss = '127.0.0.1:8001',
    [switch]$NoLink,
    [switch]$DisableFlampAutoSave,
    [switch]$NoAutoStart
)
$ErrorActionPreference = 'Stop'
$env:WSL_UTF8 = '1'                      # make wsl.exe print UTF-8, not UTF-16
$Call = $Call.ToLower()
if ($Call -notmatch '^[a-z0-9]{3,10}$') { throw "Callsign '$Call' must be 3-10 letters/digits" }
if (-not $From) { $From = $Call.ToUpper() }
if ($From -match '["\\]') { throw "From address may not contain double quotes or backslashes" }
if ($Ssid -lt 0 -or $Ssid -gt 15) { throw "-Ssid must be 0-15" }
$LinkCall = if ($Ssid -eq 0) { $Call.ToUpper() } else { "$($Call.ToUpper())-$Ssid" }
if ($Call.Length -gt 6 -and -not $NoLink) {
    Write-Warning "A callsign over 6 characters can't be an AX.25 address; not setting up hnlink."
    $NoLink = $true
}
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path   # the windows\ folder of the release

function Step($m) { Write-Host "`n== $m" -ForegroundColor Cyan }
function Warn($m) { Write-Host "`n!! $m" -ForegroundColor Yellow }

# Run a command as root in the distro without a shell (so < > & in arguments are
# literal). Plain functions on purpose: no param block, so '-p' etc. pass through
# in $args instead of being parsed as PowerShell parameters. Arguments must not
# contain double quotes (Windows PowerShell 5.1 mangles them for native commands).
function InWsl {
    & wsl.exe -d $Distro -u root --exec @args
    if ($LASTEXITCODE -ne 0) { throw "in WSL failed ($LASTEXITCODE): $($args -join ' ')" }
}
function InWslOut {
    $o = & wsl.exe -d $Distro -u root --exec @args
    if ($LASTEXITCODE -ne 0) { throw "in WSL failed ($LASTEXITCODE): $($args -join ' ')" }
    ($o -join "`n").Trim()
}

# ------------------------------------------------------------------ 0. hnlink.exe
$sums = Get-Content (Join-Path $Here 'SHA256SUMS')
function Verified($name) {
    $f = Join-Path $Here $name
    if (-not (Test-Path $f)) { throw "$name not found in $Here (incomplete copy of the release?)" }
    $want = (($sums | Where-Object { $_ -match ('\s\*?' + [regex]::Escape($name) + '$') }) -replace '\s.*$', '').ToUpper()
    if (-not $want -or (Get-FileHash $f -Algorithm SHA256).Hash -ne $want) { throw "$name checksum mismatch" }
    $f
}
$HnSrc = Verified 'hnlink.exe'
$debFile = Get-ChildItem $Here -Filter 'hamnews_*_amd64.deb' | Select-Object -First 1
if (-not $debFile) { throw "hamnews_*_amd64.deb not found in $Here (incomplete copy of the release?)" }
$Deb = Verified $debFile.Name

# ------------------------------------------------------------------ 1. WSL + distro
Step "Checking WSL"
if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
    throw "WSL is not available. Run 'wsl --install' in an administrator PowerShell, reboot, then re-run this script."
}
$have = (& wsl.exe -l -q) -replace "`0", '' | ForEach-Object { $_.Trim() } | Where-Object { $_ }
if ($have -notcontains $Distro) {
    Step "Installing $Distro (a window will ask you to create a Linux user name and password)"
    & wsl.exe --install -d $Distro
    Warn "When $Distro has finished setting up (and after a reboot if Windows asks), run this script again."
    exit 0
}
$LinuxUser = (& wsl.exe -d $Distro --exec whoami).Trim()
if (-not $LinuxUser -or $LinuxUser -eq 'root') {
    throw "$Distro has no normal default user. Open '$Distro' once and finish its first-run setup."
}
Step "Using WSL distribution '$Distro', Linux user '$LinuxUser'"

# ------------------------------------------------------------------ 2. systemd
$pid1 = InWslOut ps -p 1 -o comm=
if ($pid1 -ne 'systemd') {
    Step "Turning on systemd in $Distro (restarts the distribution)"
    InWsl sh -c 'grep -qs ^systemd=true /etc/wsl.conf || printf ''\n[boot]\nsystemd=true\n'' >> /etc/wsl.conf'
    & wsl.exe --terminate $Distro | Out-Null
    Start-Sleep -Seconds 3
    $pid1 = InWslOut ps -p 1 -o comm=
    if ($pid1 -ne 'systemd') {
        throw "systemd did not start in $Distro (needs WSL 0.67.6 or later: run 'wsl --update', then re-run)."
    }
}

# ------------------------------------------------------------------ 3. hamnews .deb
Step "Installing the hamnews package and tin in $Distro"
$debName = Split-Path -Leaf $Deb
InWsl cp (InWslOut wslpath -a $Deb) /tmp/$debName
InWsl sh -c "echo 'hamnews hamnews/callsign string $Call' | debconf-set-selections"
InWsl env DEBIAN_FRONTEND=noninteractive apt-get -qq update
InWsl env DEBIAN_FRONTEND=noninteractive apt-get -qq install -y tin /tmp/$debName
InWsl rm -f /tmp/$debName

$Groups = Join-Path $Here 'groups.txt'
if (Test-Path $Groups) {
    Step "Installing the net's group list (groups.txt -> /etc/hamnews/groups)"
    InWsl sh -c "tr -d '\r' < '$(InWslOut wslpath -a $Groups)' > /etc/hamnews/groups"
}
InWsl systemctl restart hamnews
$state = InWslOut sh -c 'systemctl is-active hamnews || true'
if ($state -ne 'active') { throw "hamnews did not start in $Distro (wsl -d $Distro -u root journalctl -u hamnews)" }

# ------------------------------------------------------------------ 4. Windows FLAMP
# FLAMP settings are left alone unless -DisableFlampAutoSave is given.
$Nbems = Join-Path $env:USERPROFILE 'NBEMS.files'
$Prefs = Join-Path $Nbems 'FLAMP\FLAMP.prefs'
if (Test-Path $Prefs) {
    $p = Get-Content $Prefs
    if ($p -match '^auto_rx_save:1') {
        if (-not $DisableFlampAutoSave) {
            Warn "Note: stock FLAMP with 'Auto save rx files' on lets any station write files in your user folder (OPERATIONS.md section 2.1)."
        } elseif (Get-Process flamp -ErrorAction SilentlyContinue) {
            Warn "FLAMP is running; close it and re-run with -DisableFlampAutoSave, or turn off 'Auto save rx files' in FLAMP."
        } else {
            Copy-Item $Prefs "$Prefs.bak-hamnews" -Force
            $p -replace '^auto_rx_save:1', 'auto_rx_save:0' | Set-Content $Prefs -Encoding ASCII
            Step "Turned OFF FLAMP 'Auto save rx files' (backup: FLAMP.prefs.bak-hamnews)."
        }
    }
}

# ------------------------------------------------------------------ 5. commands + tasks
$Dest = Join-Path $env:LOCALAPPDATA 'hamnews'
New-Item -ItemType Directory -Force $Dest | Out-Null
# A running hnlink.exe would block the copy.
if (Get-ScheduledTask -TaskName 'hnlink' -ErrorAction SilentlyContinue) { Stop-ScheduledTask -TaskName 'hnlink' }
Get-Process hnlink -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Copy-Item (Join-Path $Here 'hamnews.ps1'), (Join-Path $Here 'hamnews.cmd'), $HnSrc $Dest -Force
$HnExe = Join-Path $Dest 'hnlink.exe'
$HnLog = Join-Path $Dest 'hnlink.log'
@{ Distro = $Distro; Call = $Call; LinkCall = $LinkCall; Kiss = $Kiss; From = $From; Nbems = $Nbems
   Http = '127.0.0.1:8073'; Link = (-not $NoLink) } |
    ConvertTo-Json | Set-Content (Join-Path $Dest 'hamnews.json') -Encoding UTF8
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $Dest) {
    [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $Dest), 'User')
}

$trg = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$set = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -Hidden `
    -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1)
if (-not $NoAutoStart) {
    Step "Registering logon task 'hamnews' to keep $Distro running"
    # conhost --headless: run with no console window (Windows 10 2004+ / 11)
    $act = New-ScheduledTaskAction -Execute 'conhost.exe' -Argument "--headless wsl.exe -d $Distro -u root --exec /bin/sleep infinity"
    Register-ScheduledTask -TaskName 'hamnews' -Action $act -Trigger $trg -Settings $set -Force | Out-Null
    Start-ScheduledTask -TaskName 'hamnews'
}
if (-not $NoLink) {
    Step "Registering logon task 'hnlink' ($LinkCall via KISS $Kiss)"
    # A task's argument string is a plain command line (no PowerShell native-arg
    # mangling); hnlink.exe parses it with the usual Windows quoting rules.
    $hnArgs = "--headless `"$HnExe`" run --call $LinkCall --kiss $Kiss --nntp localhost:119 --from `"$From`" --log `"$HnLog`""
    $act = New-ScheduledTaskAction -Execute 'conhost.exe' -Argument $hnArgs
    Register-ScheduledTask -TaskName 'hnlink' -Action $act -Trigger $trg -Settings $set -Force | Out-Null
    Start-ScheduledTask -TaskName 'hnlink'
}

Write-Host @"

Done. Station $($Call.ToUpper()): news server in WSL ($Distro) on localhost:119.
Open a NEW terminal so PATH picks up the 'hamnews' command:

  hamnews page        status page and posting (http://localhost:8073)
  hamnews read        tin; or point Thunderbird at news server localhost, port 119
  hamnews send        write a FLAMP batch to $Nbems\FLAMP\tx\
  hamnews receive     load received FLAMP batches from $Nbems\FLAMP\rx\
  hamnews status      news server, hnlink, and recent log

"@
if (-not $NoLink) {
    Write-Host "hnlink transmits as $LinkCall through direwolf's KISS port ($Kiss). Install and"
    Write-Host "configure direwolf for your radio interface (see hnlink\README.md); hnlink keeps"
    Write-Host "retrying until direwolf is up. Log: $HnLog"
}
Write-Host "Operating procedure: $Here\OPERATIONS.md"
