<#
.SYNOPSIS
    Build helper for hlab on Windows, where `make` is usually unavailable.

.EXAMPLE
    .\build.ps1                 # build bin\hlab.exe
    .\build.ps1 test            # run the test suite
    .\build.ps1 check           # fmt + vet + test
    .\build.ps1 install         # install for this user and put it on PATH
    .\build.ps1 uninstall       # remove it again
    .\build.ps1 release         # cross-compile every target into dist\
#>
[CmdletBinding()]
param(
    [ValidateSet('build', 'test', 'race', 'vet', 'fmt', 'tidy', 'check', 'install', 'uninstall', 'release', 'clean')]
    [string]$Task = 'build'
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$Binary = 'hlab'
$Pkg    = './cmd/hlab'
$Module = 'github.com/cl-tim/homelab-cli'

function Get-GitValue([string[]]$GitArgs, [string]$Fallback) {
    try {
        # Collected whole before taking the first line: Select-Object -First
        # stops the pipeline early, git's exit code is then never recorded, and
        # every build was silently stamped with the fallback.
        $lines = @(& git @GitArgs 2>$null)
        if ($LASTEXITCODE -eq 0 -and $lines.Count -gt 0 -and $lines[0]) { return $lines[0].Trim() }
    } catch { }
    return $Fallback
}

$Version = Get-GitValue @('describe', '--tags', '--always', '--dirty') 'dev'
$Commit  = Get-GitValue @('rev-parse', 'HEAD') 'unknown'
$Date    = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')

$LdFlags = "-s -w " +
           "-X $Module/internal/buildinfo.Version=$Version " +
           "-X $Module/internal/buildinfo.Commit=$Commit " +
           "-X $Module/internal/buildinfo.Date=$Date"

# Per-user, so installing needs no administrator rights. The weekly backup task
# runs the copy here too, which means it follows every install.
$InstallDir = Join-Path $env:LOCALAPPDATA "Programs\$Binary"

function Get-UserPathEntries {
    $raw = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $raw) { return @() }
    return @($raw -split ';' | Where-Object { $_ })
}

function Test-SamePath([string]$A, [string]$B) {
    return [Environment]::ExpandEnvironmentVariables($A).TrimEnd('\') -ieq $B.TrimEnd('\')
}

function Add-UserPath([string]$Dir) {
    $entries = Get-UserPathEntries
    if ($entries | Where-Object { Test-SamePath $_ $Dir }) { return $false }
    [Environment]::SetEnvironmentVariable('Path', (($entries + $Dir) -join ';'), 'User')
    Write-Host "Added $Dir to your PATH" -ForegroundColor DarkGray
    return $true
}

function Remove-UserPath([string]$Dir) {
    $entries = Get-UserPathEntries
    $kept = @($entries | Where-Object { -not (Test-SamePath $_ $Dir) })
    if ($kept.Count -eq $entries.Count) { return }
    [Environment]::SetEnvironmentVariable('Path', ($kept -join ';'), 'User')
    Write-Host "Removed $Dir from your PATH" -ForegroundColor DarkGray
}

function Invoke-Step([string]$Name, [scriptblock]$Body) {
    Write-Host "==> $Name" -ForegroundColor Cyan
    & $Body
    if ($LASTEXITCODE -ne 0) { throw "$Name failed with exit code $LASTEXITCODE" }
}

switch ($Task) {
    'build' {
        Invoke-Step 'build' { go build -trimpath -ldflags $LdFlags -o "bin/$Binary.exe" $Pkg }
        Write-Host "Built bin\$Binary.exe ($Version)" -ForegroundColor Green
    }
    'test'    { Invoke-Step 'test' { go test ./... } }
    'race'    { Invoke-Step 'test -race' { go test -race ./... } }
    'vet'     { Invoke-Step 'vet' { go vet ./... } }
    'fmt'     { Invoke-Step 'fmt' { go fmt ./... } }
    'tidy'    { Invoke-Step 'tidy' { go mod tidy }; Invoke-Step 'verify' { go mod verify } }
    'install' {
        # Built in a temporary file, then moved into place, so a failed build
        # never leaves a half-written binary where the old one was.
        $staged = Join-Path $env:TEMP "$Binary-install-$PID.exe"
        Invoke-Step 'build' { go build -trimpath -ldflags $LdFlags -o $staged $Pkg }

        New-Item -ItemType Directory -Force $InstallDir | Out-Null
        $target = Join-Path $InstallDir "$Binary.exe"

        # Windows will not overwrite an executable that is running, but it will
        # rename one. Moving the old copy aside lets an install succeed while
        # hlab is in use in another terminal or the backup task is mid-run.
        $old = "$target.old"
        Remove-Item $old -Force -ErrorAction SilentlyContinue
        if (Test-Path $target) { Move-Item $target $old -Force }
        Move-Item $staged $target -Force
        Remove-Item $old -Force -ErrorAction SilentlyContinue

        $pathChanged = Add-UserPath $InstallDir
        Write-Host "Installed $target ($Version)" -ForegroundColor Green

        # Another hlab earlier on PATH (a `go install` into the Go bin dir, say)
        # would win, and the install would look like it did nothing.
        $machine = [Environment]::GetEnvironmentVariable('Path', 'Machine')
        $user = [Environment]::GetEnvironmentVariable('Path', 'User')
        foreach ($dir in ("$machine;$user" -split ';' | Where-Object { $_ })) {
            $candidate = Join-Path ([Environment]::ExpandEnvironmentVariables($dir)) "$Binary.exe"
            if (-not (Test-Path $candidate)) { continue }
            if ((Resolve-Path $candidate).Path -ne (Resolve-Path $target).Path) {
                Write-Warning "$candidate comes earlier on PATH and will run instead. Remove it."
            }
            break
        }
        if ($pathChanged) {
            Write-Host 'Open a new terminal for the PATH change to take effect.' -ForegroundColor DarkGray
        }
    }
    'uninstall' {
        $target = Join-Path $InstallDir "$Binary.exe"
        if (Test-Path $target) { Remove-Item $target -Force }
        Remove-Item "$target.old" -Force -ErrorAction SilentlyContinue
        Remove-UserPath $InstallDir

        # The directory also holds the scheduled backup's script and log, which
        # are not this script's to delete.
        if (-not (Get-ChildItem $InstallDir -ErrorAction SilentlyContinue)) {
            Remove-Item $InstallDir -Force -ErrorAction SilentlyContinue
        }
        Write-Host "Removed $target. Your config and key in hlab's home are untouched." -ForegroundColor Green
    }
    'check' {
        Invoke-Step 'fmt' { go fmt ./... }
        Invoke-Step 'vet' { go vet ./... }
        Invoke-Step 'test' { go test ./... }
        Write-Host 'All checks passed.' -ForegroundColor Green
    }
    'release' {
        if (Test-Path dist) { Remove-Item dist -Recurse -Force }
        New-Item -ItemType Directory dist | Out-Null

        $targets = @(
            @{ os = 'linux';   arch = 'amd64' },
            @{ os = 'linux';   arch = 'arm64' },
            @{ os = 'linux';   arch = 'arm'   },
            @{ os = 'darwin';  arch = 'arm64' },
            @{ os = 'darwin';  arch = 'amd64' },
            @{ os = 'windows'; arch = 'amd64' }
        )

        foreach ($t in $targets) {
            $ext = if ($t.os -eq 'windows') { '.exe' } else { '' }
            $out = "dist/$Binary-$($t.os)-$($t.arch)$ext"
            Write-Host "  building $($t.os)/$($t.arch)" -ForegroundColor DarkGray
            $env:GOOS = $t.os; $env:GOARCH = $t.arch
            go build -trimpath -ldflags $LdFlags -o $out $Pkg
            if ($LASTEXITCODE -ne 0) {
                Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
                throw "build failed for $($t.os)/$($t.arch)"
            }
        }
        Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
        Write-Host 'Release binaries in dist\' -ForegroundColor Green
    }
    'clean' {
        foreach ($p in 'bin', 'dist', 'coverage.out') {
            if (Test-Path $p) { Remove-Item $p -Recurse -Force }
        }
        Write-Host 'Cleaned.' -ForegroundColor Green
    }
}
