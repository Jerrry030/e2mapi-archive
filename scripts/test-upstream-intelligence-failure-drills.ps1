[CmdletBinding()]
param(
    [switch]$IncludePostgres,
    [switch]$IncludePrometheus,
    [switch]$IncludeRace
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$coreRoot = Join-Path $repositoryRoot 'app/e2m-core'
$agentRoot = Join-Path $repositoryRoot 'app/e2m-agent'
$contractsRoot = Join-Path $repositoryRoot 'packages/e2m-contracts'
$ruleFile = Join-Path $repositoryRoot 'deployments/monitoring/upstream-intelligence-alerts.yml'
$ruleTestFile = Join-Path $repositoryRoot 'deployments/monitoring/upstream-intelligence-alerts.test.yml'

$postgresDSNWasSet = Test-Path Env:E2M_TEST_POSTGRES_DSN
$postgresDSN = if ($postgresDSNWasSet) { $env:E2M_TEST_POSTGRES_DSN } else { $null }
if ($IncludePostgres) {
    if ([string]::IsNullOrWhiteSpace($postgresDSN)) {
        throw 'IncludePostgres requires E2M_TEST_POSTGRES_DSN'
    }
    $postgresP95Budget = 0
    if (-not [int]::TryParse($env:E2M_UI17_PG_P95_MAX_MS, [ref]$postgresP95Budget) -or $postgresP95Budget -le 0) {
        throw 'IncludePostgres requires a positive integer E2M_UI17_PG_P95_MAX_MS'
    }
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string]$WorkingDirectory,
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    Write-Host ("==> {0} {1}" -f $FilePath, ($Arguments -join ' '))
    Push-Location $WorkingDirectory
    try {
        & $FilePath @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Command failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}

$corePattern = 'UI17|Collector|Evaluate|Recommendation|Shadow|DryRun|TrafficShare|Rollout|Connector.*Completion|ReadDTOsExclude|ReadEndpointsDoNotExpose'
$agentPattern = 'TrafficShare|SchedulingFence|WriteReceipt|UnknownWeight|UpstreamIntelligence|IntelligenceClient'
$contractsPattern = 'TrafficShare|Recommendation|UpstreamIntelligence'

Remove-Item Env:E2M_TEST_POSTGRES_DSN -ErrorAction SilentlyContinue
try {
    Invoke-Checked $coreRoot 'go' @('test', './internal/operationalmetrics', './internal/httpapi', './internal/store', './internal/upstreamrecommendation', './internal/upstreamexperiment', './internal/recommendationexecution', './internal/recommendationrollout', './internal/recommendationcoordination', '-run', $corePattern, '-count=1')
}
finally {
    if ($postgresDSNWasSet) {
        $env:E2M_TEST_POSTGRES_DSN = $postgresDSN
    }
}
Invoke-Checked $agentRoot 'go' @('test', './internal/connector', './internal/adapters/newapi', './internal/adapters/sub2api', '-run', $agentPattern, '-count=1')
Invoke-Checked $contractsRoot 'go' @('test', './...', '-run', $contractsPattern, '-count=1')

if ($IncludeRace) {
    $raceEnabled = (& go env CGO_ENABLED).Trim()
    if ($LASTEXITCODE -ne 0 -or $raceEnabled -ne '1') {
        throw 'IncludeRace requires a working CGO_ENABLED=1 Go race toolchain; use the documented Linux Go container when the host toolchain is unavailable'
    }
    Invoke-Checked $coreRoot 'go' @('test', '-race', './internal/store', './internal/httpapi', './internal/recommendationrollout', '-run', $corePattern, '-count=1')
    Invoke-Checked $agentRoot 'go' @('test', '-race', './internal/connector', './internal/adapters/sub2api', '-run', $agentPattern, '-count=1')
}

if ($IncludePostgres) {
    $postgresPattern = '^TestPostgres.*(Upstream|Recommendation|Connector|UI17)'
    Invoke-Checked $coreRoot 'go' @('test', './internal/store', '-run', $postgresPattern, '-count=1', '-v')
}

if ($IncludePrometheus) {
    $promtool = Get-Command promtool -ErrorAction SilentlyContinue
    if ($null -eq $promtool) {
        throw 'IncludePrometheus requires promtool on PATH'
    }
    Invoke-Checked $repositoryRoot $promtool.Source @('check', 'rules', $ruleFile)
    Invoke-Checked $repositoryRoot $promtool.Source @('test', 'rules', $ruleTestFile)
}

Write-Host 'UI-17 local failure drills completed successfully.'
