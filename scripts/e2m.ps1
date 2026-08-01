param(
  [ValidateSet('help', 'fmt', 'fmt-check', 'lint', 'test', 'test-race', 'web-test', 'build', 'ci', 'security-scan', 'dev-core', 'dev-console')]
  [string]$Task = 'help'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$RepoRoot = Split-Path -Parent $PSScriptRoot
$GoModules = @('app/e2m-core', 'app/e2m-agent', 'packages/e2m-contracts')
$WebDir = Join-Path $RepoRoot 'web/console'

Set-Location $RepoRoot

function Invoke-External {
  param(
    [Parameter(Mandatory = $true)][string]$Command,
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments
  )
  & $Command @Arguments
  if ($LASTEXITCODE -ne 0) {
    throw "Command failed ($LASTEXITCODE): $Command $($Arguments -join ' ')"
  }
}

function Invoke-InDirectory {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][scriptblock]$Script
  )
  Push-Location $Path
  try {
    & $Script
  } finally {
    Pop-Location
  }
}

function Get-GoFiles {
  Get-ChildItem -Path (Join-Path $RepoRoot 'app'), (Join-Path $RepoRoot 'packages') -Recurse -Filter *.go -File |
    ForEach-Object { $_.FullName }
}

function Get-FileBatches {
  param(
    [Parameter(Mandatory = $true)][string[]]$Files,
    [int]$BatchSize = 64
  )
  for ($Offset = 0; $Offset -lt $Files.Count; $Offset += $BatchSize) {
    $Last = [Math]::Min($Offset + $BatchSize - 1, $Files.Count - 1)
    Write-Output -NoEnumerate @($Files[$Offset..$Last])
  }
}

function Invoke-GoModules {
  param([Parameter(Mandatory = $true)][scriptblock]$Script)
  foreach ($Module in $GoModules) {
    $ModulePath = Join-Path $RepoRoot $Module
    Write-Host "==> $Module"
    Invoke-InDirectory $ModulePath $Script
  }
}

function Invoke-Help {
  @'
E2M engineering commands
  .\scripts\e2m.ps1 fmt            Format Go + web code
  .\scripts\e2m.ps1 fmt-check      Check Go + web formatting
  .\scripts\e2m.ps1 lint           Run Go vet + web ESLint
  .\scripts\e2m.ps1 test           Run Go tests
  .\scripts\e2m.ps1 test-race      Run Go tests with race detector
  .\scripts\e2m.ps1 web-test       Run web unit tests
  .\scripts\e2m.ps1 build          Build web console + Docker image smoke target
  .\scripts\e2m.ps1 ci             Run local CI gate
  .\scripts\e2m.ps1 security-scan  Run govulncheck + npm audit
'@ | Write-Host
}

function Invoke-Fmt {
  $GoFiles = @(Get-GoFiles)
  if ($GoFiles.Count -gt 0) {
    foreach ($Batch in @(Get-FileBatches -Files $GoFiles)) {
      Invoke-External gofmt -w @Batch
    }
  }
  Invoke-External npm run format --prefix $WebDir
}

function Invoke-FmtCheck {
  $GoFiles = @(Get-GoFiles)
  if ($GoFiles.Count -gt 0) {
    $Unformatted = @(
      foreach ($Batch in @(Get-FileBatches -Files $GoFiles)) {
        & gofmt -l @Batch
        if ($LASTEXITCODE -ne 0) {
          throw "Command failed ($LASTEXITCODE): gofmt -l"
        }
      }
    )
    if ($Unformatted.Count -gt 0) {
      Write-Error "gofmt needed:`n$($Unformatted -join "`n")"
    }
  }
  Invoke-External npm run format:check --prefix $WebDir
}

function Invoke-Lint {
  Invoke-GoModules { Invoke-External go vet ./... }
  Invoke-External npm run lint --prefix $WebDir
}

function Invoke-Test {
  Invoke-GoModules { Invoke-External go test ./... }
}

function Invoke-TestRace {
  Invoke-GoModules { Invoke-External go test -race ./... }
}

function Invoke-WebTest {
  Invoke-External npm run test --prefix $WebDir
}

function Invoke-Build {
  Invoke-External npm run build --prefix $WebDir
  Invoke-External docker build -f app/e2m-core/Dockerfile -t e2m-core:local .
}

function Invoke-SecurityScan {
  $GovulncheckCommand = Get-Command govulncheck -ErrorAction SilentlyContinue
  $Govulncheck = if ($GovulncheckCommand) { $GovulncheckCommand.Source } else { $null }
  if (-not $Govulncheck) {
    $GoPath = (& go env GOPATH).Trim()
    $Candidate = Join-Path $GoPath 'bin/govulncheck.exe'
    if (Test-Path $Candidate) {
      $Govulncheck = $Candidate
    } else {
      throw 'govulncheck not found. Install it with: go install golang.org/x/vuln/cmd/govulncheck@latest'
    }
  }
  Invoke-GoModules { Invoke-External $Govulncheck ./... }
  Invoke-External npm audit --audit-level=high --registry=https://registry.npmjs.org/
  Invoke-External npm audit --audit-level=high --registry=https://registry.npmjs.org/ --prefix $WebDir
}

switch ($Task) {
  'help' { Invoke-Help }
  'fmt' { Invoke-Fmt }
  'fmt-check' { Invoke-FmtCheck }
  'lint' { Invoke-Lint }
  'test' { Invoke-Test }
  'test-race' { Invoke-TestRace }
  'web-test' { Invoke-WebTest }
  'build' { Invoke-Build }
  'ci' { Invoke-FmtCheck; Invoke-Lint; Invoke-Test; Invoke-WebTest; Invoke-External npm run build --prefix $WebDir }
  'security-scan' { Invoke-SecurityScan }
  'dev-core' { Invoke-External go run ./app/e2m-core/cmd/e2m-core }
  'dev-console' { Invoke-External npm run dev --prefix $WebDir }
}
