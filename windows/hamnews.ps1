<#
.SYNOPSIS
  hamnews on Windows: page / read / send / receive / status / start.
  The news server runs in WSL; hnlink.exe (beside this script) does the exchange.
#>
param(
    [Parameter(Position = 0)][ValidateSet('page', 'read', 'send', 'receive', 'status', 'start', 'help')]
    [string]$Command = 'help',
    [int]$Days = 14
)
$ErrorActionPreference = 'Stop'
$env:WSL_UTF8 = '1'
$cfg = Get-Content (Join-Path $PSScriptRoot 'hamnews.json') | ConvertFrom-Json
$D = $cfg.Distro
$Flamp = Join-Path $cfg.Nbems 'FLAMP'
$Hn = Join-Path $PSScriptRoot 'hnlink.exe'
$Log = Join-Path $PSScriptRoot 'hnlink.log'

switch ($Command) {
    'page' {
        Start-Process ("http://" + ($cfg.Http -replace '^127\.0\.0\.1', 'localhost') + "/")
    }
    'read' {
        & wsl.exe -d $D --exec env NNTPSERVER=localhost tin -r
    }
    'send' {
        $tx = Join-Path $Flamp 'tx'
        New-Item -ItemType Directory -Force $tx | Out-Null
        $out = Join-Path $tx ("{0}-{1}.rnews" -f $cfg.Call, (Get-Date -Format yyyyMMdd))
        & $Hn batch --days $Days $out
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        Write-Host "Add $out in FLAMP and transmit."
    }
    'receive' {
        $files = @(Get-ChildItem -Path (Join-Path $Flamp 'rx') -Recurse -File -Include '*.rnews', '*.rnews.gz' -ErrorAction SilentlyContinue |
            ForEach-Object { $_.FullName })
        if (-not $files) { Write-Host "No .rnews files under $Flamp\rx"; break }
        & $Hn load @files
        exit $LASTEXITCODE
    }
    'status' {
        Write-Host "== news server (WSL $D)"
        & wsl.exe -d $D -u root --exec sh -c 'systemctl --no-pager status hamnews 2>/dev/null | head -3 || pgrep -a innd'
        if ($cfg.Link) {
            Write-Host "`n== hnlink ($($cfg.LinkCall) via KISS $($cfg.Kiss))"
            $t = Get-ScheduledTask -TaskName 'hnlink' -ErrorAction SilentlyContinue
            Write-Host ("task: " + $(if ($t) { $t.State } else { 'not registered' }) +
                "   process: " + $(if (Get-Process hnlink -ErrorAction SilentlyContinue) { 'running' } else { 'not running' }))
            if (Test-Path $Log) { Get-Content $Log -Tail 12 }
        }
    }
    'start' {
        foreach ($t in 'hamnews', 'hnlink') {
            if (Get-ScheduledTask -TaskName $t -ErrorAction SilentlyContinue) { Start-ScheduledTask -TaskName $t }
        }
        Write-Host "Started."
    }
    default {
        Write-Host @"
hamnews page             status page: stations heard, transfers, messages, posting
hamnews read             tin newsreader (or any newsreader: server localhost, port 119)
hamnews send [-Days N]   FLAMP: write the last N days (default 14) to FLAMP\tx\
hamnews receive          FLAMP: load every .rnews file under FLAMP\rx\ (repeats are harmless)
hamnews status           news server and hnlink state, recent hnlink log
hamnews start            start the news server and hnlink if they were stopped
"@
    }
}
