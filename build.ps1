<#
.SYNOPSIS
    Build helper for hlab on Windows, where `make` is usually unavailable.

.EXAMPLE
    .\build.ps1                 # build bin\hlab.exe
    .\build.ps1 test            # run the test suite
    .\build.ps1 check           # fmt + vet + test
    .\build.ps1 install         # install into the Go bin dir
    .\build.ps1 release         # cross-compile every target into dist\
#>
[CmdletBinding()]
param(
    [ValidateSet('build', 'test', 'race', 'vet', 'fmt', 'tidy', 'check', 'install', 'release', 'clean')]
    [string]$Task = 'build'
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$Binary = 'hlab'
$Pkg    = './cmd/hlab'
$Module = 'github.com/cl-tim/homelab-cli'

function Get-GitValue([string[]]$GitArgs, [string]$Fallback) {
    try {
        $value = (& git @GitArgs 2>$null | Select-Object -First 1)
        if ($LASTEXITCODE -eq 0 -and $value) { return $value.Trim() }
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
    'install' { Invoke-Step 'install' { go install -trimpath -ldflags $LdFlags $Pkg } }
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
