param(
  [string]$ComposeFile = "",
  [int]$TimeoutSeconds = 600,
  [ValidateSet("release", "test-only")]
  [string]$ObservationProfile = "test-only",
  [switch]$SourceFrozen,
  [string]$EvidenceDir = ""
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RepoRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$ExpectedComposeFile = [IO.Path]::GetFullPath((Join-Path $RepoRoot "deployments/templates/compose/e2m-ui17-disposable-intelligence.compose.yml"))
if ($ComposeFile -eq "") { $ComposeFile = $ExpectedComposeFile }
$ComposeFile = [IO.Path]::GetFullPath($ComposeFile)
if ($ComposeFile -ne $ExpectedComposeFile -or -not (Test-Path -LiteralPath $ComposeFile -PathType Leaf)) {
  throw "UI-17 must use the repository's disposable intelligence Compose file"
}
if ($TimeoutSeconds -lt 180 -or $TimeoutSeconds -gt 1800) {
  throw "TimeoutSeconds must be between 180 and 1800"
}
if ($ObservationProfile -eq "release" -and -not $SourceFrozen.IsPresent) {
  throw "release observation requires explicit -SourceFrozen acknowledgement"
}
if ($ObservationProfile -eq "test-only" -and $SourceFrozen.IsPresent) {
  throw "-SourceFrozen is only valid with -ObservationProfile release"
}

$RandomHex = ([guid]::NewGuid().ToString("N")).Substring(0, 12)
$ProjectName = "e2m-ui17-intel-{0}-{1}" -f $PID, $RandomHex
$ProjectPattern = '^e2m-ui17-intel-[1-9][0-9]{0,9}-[a-f0-9]{12}$'
$SystemTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
$RuntimeDir = [IO.Path]::GetFullPath((Join-Path $SystemTemp $ProjectName))
$MarkerFile = Join-Path $RuntimeDir ".e2m-ui17-disposable.json"
if ([string]::IsNullOrWhiteSpace($EvidenceDir)) {
  $EvidenceDir = Join-Path $RepoRoot ".tmp/ui17-evidence"
}
$EvidenceDir = [IO.Path]::GetFullPath($EvidenceDir)
$EvidenceRunDir = [IO.Path]::GetFullPath((Join-Path $EvidenceDir $ProjectName))
$EvidenceFile = Join-Path $EvidenceRunDir "redacted-evidence.json"
$MarkerCreated = $false
$StackMayExist = $false
$PrimaryFailure = $null
$PrimaryFailureDetail = ""
$PrimaryFailureStage = ""
$PrimaryFailureCategory = ""
$FinalizationFailure = $null
$CleanupFailure = $null
$BusinessPassed = $false
$Succeeded = $false
$SourceFrozenAcknowledged = [bool]$SourceFrozen.IsPresent
$ReleaseEligible = $ObservationProfile -eq "release" -and $SourceFrozenAcknowledged
$RunEvidence = $null
$ProtectedBefore = $null
$ProtectedAfter = $null
$ProtectedUnchanged = $false
$DisposableRemoved = $false
$RuntimeRemoved = $false
$EnvironmentRestored = $false
$ComposeSHA256Before = ""
$ComposeSHA256AfterStart = ""
$ComposeSHA256AfterCleanup = ""
$RunnerFile = [IO.Path]::GetFullPath($PSCommandPath)
$RunnerSHA256Before = (Get-FileHash -Algorithm SHA256 -LiteralPath $RunnerFile).Hash.ToLowerInvariant()
$RunnerSHA256AfterCleanup = ""
$RunnerUnchanged = $false
$ComposeUnchanged = $false
$BuildInputManifestBefore = $null
$BuildInputManifestAfterStart = $null
$BuildInputManifestAfterCleanup = $null
$BuildInputsUnchanged = $false
$ImagesVerified = $false
$BuiltImageProvenance = @()
$BuiltImagesProvenanceBound = $false
$ProtectedBeforeCaptured = $false
$CleanupEvidence = $null
$ImageEvidence = @()
$Timeline = [Collections.Generic.List[object]]::new()
$CurrentStage = "preflight"
$AllowedDiagnosticStages = @(
  "preflight", "compose_start", "connector_setup", "credential_boundary",
  "core_browser_snapshot", "core_source_cardinality", "core_source_details", "core_overview_metrics",
  "postgres_source_identity", "postgres_run_manifest", "postgres_observations", "connector_outbox_drain",
  "durable_outbox_core_stop", "durable_outbox_queue", "durable_outbox_connector_restart", "durable_outbox_replay",
  "failure_isolation_recovery", "confirmed_change", "final_browser_snapshot", "security_scan"
)
$CoreReadBodies = [Collections.Generic.List[string]]::new()
$CredentialValues = [Collections.Generic.List[string]]::new()
$CoreOnlySensitiveValues = [Collections.Generic.List[string]]::new()
$EnvironmentNames = @(
  "UI17_CORE_PORT",
  "UI17_OWNED_SUB2API_PORT", "UI17_EXTERNAL_A_SUB2API_PORT", "UI17_EXTERNAL_B_SUB2API_PORT",
  "UI17_OWNED_CONNECTOR_PORT", "UI17_EXTERNAL_A_CONNECTOR_PORT", "UI17_EXTERNAL_B_CONNECTOR_PORT",
  "UI17_OWNED_CONNECTOR_ID", "UI17_EXTERNAL_A_CONNECTOR_ID", "UI17_EXTERNAL_B_CONNECTOR_ID",
  "UI17_OWNED_INSTANCE_ID", "UI17_EXTERNAL_A_INSTANCE_ID", "UI17_EXTERNAL_B_INSTANCE_ID",
  "UI17_OWNED_ENROLLMENT_FILE", "UI17_EXTERNAL_A_ENROLLMENT_FILE", "UI17_EXTERNAL_B_ENROLLMENT_FILE"
)
$PreviousEnvironment = @{}
foreach ($Name in $EnvironmentNames) {
  $PreviousEnvironment[$Name] = [Environment]::GetEnvironmentVariable($Name, "Process")
}

$ExpectedServices = @(
  "core-postgres", "e2m-core",
  "owned-postgres", "owned-redis", "owned-sub2api",
  "external-a-postgres", "external-a-redis", "external-a-sub2api",
  "external-b-postgres", "external-b-redis", "external-b-sub2api",
  "connector-owned", "connector-external-a", "connector-external-b"
)
$PostgresImage = "postgres:16-alpine@sha256:e013e867e712fec275706a6c51c966f0bb0c93cfa8f51000f85a15f9865a28cb"
$RedisImage = "redis:7-alpine@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99"
$Sub2APIImage = "ghcr.io/wei-shaw/sub2api:0.1.164@sha256:a94c25fb4c50c3bf21155142d745ff11a8d9199e4cf72d9a2424d75ccbfc1659"
$NodeImage = "node:24-alpine@sha256:a0b9bf06e4e6193cf7a0f58816cc935ff8c2a908f81e6f1a95432d679c54fbfd"
$ExpectedPinnedImages = @{
  "core-postgres" = @{ config_image = $PostgresImage; repo_digest = "postgres@sha256:e013e867e712fec275706a6c51c966f0bb0c93cfa8f51000f85a15f9865a28cb" }
  "owned-postgres" = @{ config_image = $PostgresImage; repo_digest = "postgres@sha256:e013e867e712fec275706a6c51c966f0bb0c93cfa8f51000f85a15f9865a28cb" }
  "external-a-postgres" = @{ config_image = $PostgresImage; repo_digest = "postgres@sha256:e013e867e712fec275706a6c51c966f0bb0c93cfa8f51000f85a15f9865a28cb" }
  "external-b-postgres" = @{ config_image = $PostgresImage; repo_digest = "postgres@sha256:e013e867e712fec275706a6c51c966f0bb0c93cfa8f51000f85a15f9865a28cb" }
  "owned-redis" = @{ config_image = $RedisImage; repo_digest = "redis@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99" }
  "external-a-redis" = @{ config_image = $RedisImage; repo_digest = "redis@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99" }
  "external-b-redis" = @{ config_image = $RedisImage; repo_digest = "redis@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99" }
  "owned-sub2api" = @{ config_image = $Sub2APIImage; repo_digest = "ghcr.io/wei-shaw/sub2api@sha256:a94c25fb4c50c3bf21155142d745ff11a8d9199e4cf72d9a2424d75ccbfc1659" }
  "external-a-sub2api" = @{ config_image = $Sub2APIImage; repo_digest = "ghcr.io/wei-shaw/sub2api@sha256:a94c25fb4c50c3bf21155142d745ff11a8d9199e4cf72d9a2424d75ccbfc1659" }
  "external-b-sub2api" = @{ config_image = $Sub2APIImage; repo_digest = "ghcr.io/wei-shaw/sub2api@sha256:a94c25fb4c50c3bf21155142d745ff11a8d9199e4cf72d9a2424d75ccbfc1659" }
}
$ExpectedBuiltImages = @{
  "e2m-core" = "$ProjectName-e2m-core"
  "connector-owned" = "$ProjectName-connector-owned"
  "connector-external-a" = "$ProjectName-connector-external-a"
  "connector-external-b" = "$ProjectName-connector-external-b"
}
$ExpectedBuiltServices = @("e2m-core", "connector-owned", "connector-external-a", "connector-external-b")
$ExpectedGoProxy = "https://proxy.golang.org,direct"
$OutboxPresentMarker = "E2M_UI17_OUTBOX_PRESENT"
$OutboxMissingMarker = "E2M_UI17_OUTBOX_MISSING"
$BuildInputRoots = @(
  ".dockerignore", "go.work", "go.work.sum", "app/e2m-core", "app/e2m-agent",
  "packages/e2m-contracts", "web/console"
)

function Write-Step {
  param([Parameter(Mandatory = $true)][string]$Message)
  Write-Host "==> $Message"
}

function Get-JsonValue {
  param($Object, [Parameter(Mandatory = $true)][string]$Name)
  if ($null -eq $Object) { return $null }
  if ($Object -is [Collections.IDictionary] -and $Object.Contains($Name)) { return $Object[$Name] }
  $Property = $Object.PSObject.Properties[$Name]
  if ($null -ne $Property) { return $Property.Value }
  return $null
}

function Assert-True {
  param([bool]$Condition, [Parameter(Mandatory = $true)][string]$Message)
  if (-not $Condition) {
    $Exception = [InvalidOperationException]::new($Message)
    $Exception.Data["e2m_ui17_local_assertion"] = $true
    throw $Exception
  }
}

function Get-SanitizedPrimaryFailureDetail {
  param($Failure)
  if ($null -eq $Failure) { return "" }
  $Exception = if ($Failure -is [Management.Automation.ErrorRecord]) {
    $Failure.Exception
  } elseif ($Failure -is [Exception]) {
    $Failure
  } else {
    $null
  }
  if ($null -eq $Exception -or -not $Exception.Data.Contains("e2m_ui17_local_assertion") -or
      $Exception.Data["e2m_ui17_local_assertion"] -ne $true) {
    return ""
  }
  $Message = [string]$Exception.Message
  if ([string]::IsNullOrWhiteSpace($Message)) { return "" }
  foreach ($Sensitive in @($CredentialValues) + @($CoreOnlySensitiveValues)) {
    if (-not [string]::IsNullOrWhiteSpace($Sensitive) -and
        $Message.IndexOf($Sensitive, [StringComparison]::Ordinal) -ge 0) {
      return ""
    }
  }
  if ($Message -match '[/\\]' -or
      $Message -match '(?i)(?:token|secret|password|passwd|authorization|bearer|credential|enrollment|api[_-]?key|cookie|session|private[_-]?key|client[_-]?secret|header|url|uri|path)') {
    return ""
  }
  $FirstLine = [string](@($Message -split '[\r\n]+', 2)[0]).Trim()
  if ($FirstLine -notmatch "^[A-Za-z0-9][A-Za-z0-9 .,:;_()'-]*$") { return "" }
  if ($FirstLine.Length -gt 200) { $FirstLine = $FirstLine.Substring(0, 200) }
  return $FirstLine
}

function ConvertTo-CanonicalDecimalText {
  param([AllowEmptyString()][string]$Value, [Parameter(Mandatory = $true)][string]$Label)
  Assert-True ($Value -match '^-?[0-9]+(?:\.[0-9]+)?$') "$Label was not plain decimal text"
  $Negative = $Value.StartsWith("-", [StringComparison]::Ordinal)
  $Unsigned = if ($Negative) { $Value.Substring(1) } else { $Value }
  $Dot = $Unsigned.IndexOf('.', [StringComparison]::Ordinal)
  if ($Dot -ge 0) {
    $Whole = $Unsigned.Substring(0, $Dot)
    $Fractional = $Unsigned.Substring($Dot + 1)
  } else {
    $Whole = $Unsigned
    $Fractional = ""
  }
  $Whole = [regex]::Replace($Whole, '^0+', '')
  if ($Whole -eq "") { $Whole = "0" }
  $Fractional = [regex]::Replace($Fractional, '0+$', '')
  if ($Whole -eq "0" -and $Fractional -eq "") { return "0" }
  $Canonical = $Whole
  if ($Fractional -ne "") { $Canonical += ".$Fractional" }
  if ($Negative) { $Canonical = "-$Canonical" }
  Assert-True ($Canonical -match '^(?:0|-?[1-9][0-9]*|-?(?:0\.[0-9]*[1-9]|[1-9][0-9]*\.[0-9]*[1-9]))$') "$Label was not canonical"
  Assert-True ($Whole.Length -le 20 -and $Fractional.Length -le 18 -and
    ($Whole.Length + $Fractional.Length) -le 38) "$Label exceeded NUMERIC bounds"
  return $Canonical
}

function Assert-JsonNumber {
  param($Value, [Parameter(Mandatory = $true)][string]$Label)
  $Numeric = $Value -is [byte] -or $Value -is [sbyte] -or $Value -is [int16] -or
    $Value -is [uint16] -or $Value -is [int32] -or $Value -is [uint32] -or
    $Value -is [int64] -or $Value -is [uint64] -or $Value -is [single] -or
    $Value -is [double] -or $Value -is [decimal]
  if (-not $Numeric) { throw "$Label must be a JSON number"
  }
}

function Assert-ExactProject {
  if ($ProjectName -notmatch $ProjectPattern -or $ProjectName -eq "e2m-real-gateways" -or
      $ProjectName -eq ([IO.Path]::GetFileName($RepoRoot)).ToLowerInvariant()) {
    throw "unsafe UI-17 Compose project name"
  }
  if ($ProjectName -match 'real-gateways') { throw "UI-17 project name overlaps the protected real gateway stack" }
}

function Assert-RuntimePath {
  $Resolved = [IO.Path]::GetFullPath($RuntimeDir)
  if ([IO.Path]::GetDirectoryName($Resolved) -ne $SystemTemp -or [IO.Path]::GetFileName($Resolved) -ne $ProjectName) {
    throw "UI-17 runtime path escaped the system temporary directory"
  }
  if ($Resolved -like "*$([IO.Path]::DirectorySeparatorChar)e2m-real-gateways*") {
    throw "UI-17 runtime path overlaps the protected real gateway runtime"
  }
  if ($MarkerCreated) {
    if (-not (Test-Path -LiteralPath $MarkerFile -PathType Leaf)) { throw "UI-17 cleanup marker is missing" }
    $Marker = Get-Content -Raw -LiteralPath $MarkerFile | ConvertFrom-Json
    if ((Get-JsonValue $Marker "project") -ne $ProjectName -or
        [IO.Path]::GetFullPath([string](Get-JsonValue $Marker "compose_file")) -ne $ComposeFile -or
        [IO.Path]::GetFullPath([string](Get-JsonValue $Marker "runtime_dir")) -ne $Resolved) {
      throw "UI-17 cleanup marker does not match the disposable project"
    }
  }
}

function Invoke-Compose {
  $Arguments = @($args)
  Assert-ExactProject
  $DockerArguments = @("compose", "-p", $ProjectName, "-f", $ComposeFile) + @($Arguments)
  & docker @DockerArguments
  if ($LASTEXITCODE -ne 0) { throw "docker compose failed: exit_code=$LASTEXITCODE" }
}

function Invoke-ComposeCapture {
  $Arguments = @($args)
  Assert-ExactProject
  $DockerArguments = @("compose", "-p", $ProjectName, "-f", $ComposeFile) + @($Arguments)
  $Output = & docker @DockerArguments 2>&1
  if ($LASTEXITCODE -ne 0) { throw "docker compose failed: exit_code=$LASTEXITCODE" }
  return ($Output -join "`n")
}

function Get-ComposeContainerIDs {
  Assert-ExactProject
  $Lines = (Invoke-ComposeCapture ps --all --quiet @args) -split '[\r\n]+'
  foreach ($Line in $Lines) {
    $Value = $Line.Trim()
    if ($Value -match '^[0-9a-f]{12,64}$') { Write-Output $Value }
  }
}

function Assert-ComposeProjectLabels {
  param([Parameter(Mandatory = $true)][string[]]$ExpectedServiceNames)
  $ContainerIDs = @(Get-ComposeContainerIDs)
  $Expected = @($ExpectedServiceNames | Sort-Object -Unique)
  Assert-True ($Expected.Count -eq $ExpectedServiceNames.Count) "expected Compose service list contained duplicates"
  Assert-True ($ContainerIDs.Count -eq $Expected.Count) "Compose project did not contain exactly $($Expected.Count) containers"
  $SeenServices = @{}
  foreach ($ContainerID in $ContainerIDs) {
    $DockerArguments = @("inspect", $ContainerID)
    $Output = & docker @DockerArguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "docker inspect failed while verifying Compose project labels: exit_code=$LASTEXITCODE" }
    $Inspected = @(($Output -join "`n") | ConvertFrom-Json)
    Assert-True ($Inspected.Count -eq 1) "container $ContainerID inspection was ambiguous"
    $ProjectLabel = [string]$Inspected[0].Config.Labels.'com.docker.compose.project'
    $ServiceLabel = [string]$Inspected[0].Config.Labels.'com.docker.compose.service'
    Assert-True ($ProjectLabel -eq $ProjectName) "container $ContainerID belongs to unexpected Compose project $ProjectLabel"
    Assert-True ($ServiceLabel -in $Expected) "container $ContainerID has unexpected Compose service label $ServiceLabel"
    Assert-True (-not $SeenServices.ContainsKey($ServiceLabel)) "Compose service $ServiceLabel had more than one container"
    $SeenServices[$ServiceLabel] = $true
  }
  Assert-True ($SeenServices.Count -eq $Expected.Count) "Compose project omitted one or more expected services"
  foreach ($Service in $Expected) {
    Assert-True ($SeenServices.ContainsKey($Service)) "Compose project omitted expected service $Service"
  }
}

function ConvertTo-JsonBytes {
  param([Parameter(Mandatory = $true)]$Value)
  return ,([Text.Encoding]::UTF8.GetBytes(($Value | ConvertTo-Json -Depth 30 -Compress)))
}

function Invoke-Json {
  param(
    [Parameter(Mandatory = $true)][string]$Method,
    [Parameter(Mandatory = $true)][string]$Uri,
    $Body = $null,
    [hashtable]$Headers = @{},
    [switch]$CaptureCoreRead,
    [switch]$AllowNotFound,
    [int]$ExpectedStatus = 0
  )
  $Request = @{ Method = $Method; Uri = $Uri; Headers = $Headers; TimeoutSec = 45; UseBasicParsing = $true }
  if ($null -ne $Body) {
    $Request.Body = ConvertTo-JsonBytes $Body
    $Request.ContentType = "application/json; charset=utf-8"
  }
  try {
    $Response = Invoke-WebRequest @Request
  } catch {
    $Status = 0
    if ($null -ne $_.Exception.Response -and $null -ne $_.Exception.Response.StatusCode) {
      $Status = [int]$_.Exception.Response.StatusCode
    }
    if ($AllowNotFound -and $Status -eq 404) { return $null }
    $Route = ([uri]$Uri).AbsolutePath
    throw "HTTP request failed: method=$Method route=$Route status=$Status"
  }
  if ($ExpectedStatus -gt 0 -and [int]$Response.StatusCode -ne $ExpectedStatus) {
    throw "HTTP request returned unexpected status: method=$Method route=$(([uri]$Uri).AbsolutePath)"
  }
  $Raw = [string]$Response.Content
  if ($CaptureCoreRead) { $CoreReadBodies.Add($Raw) }
  if ([string]::IsNullOrWhiteSpace($Raw)) { return $null }
  try { return ($Raw | ConvertFrom-Json) }
  catch { throw "HTTP response was not valid JSON: method=$Method route=$(([uri]$Uri).AbsolutePath)" }
}

function Get-EnvelopeData {
  param($Response, [Parameter(Mandatory = $true)][string]$Name)
  if ($null -eq $Response) { throw "$Name returned an empty response" }
  $Code = Get-JsonValue $Response "code"
  if ($null -eq $Code) { throw "$Name did not return the required envelope code" }
  Assert-JsonNumber $Code "$Name code"
  if ([int]$Code -ne 0) { throw "$Name returned a non-success envelope" }
  if ($null -eq $Response.PSObject.Properties["data"]) { throw "$Name did not return envelope data" }
  return (Get-JsonValue $Response "data")
}

function Wait-Http {
  param(
    [Parameter(Mandatory = $true)][string]$Uri,
    [Parameter(Mandatory = $true)][string]$Name,
    [hashtable]$Headers = @{}
  )
  $Deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $Deadline) {
    try {
      $Response = Invoke-WebRequest -Method GET -Uri $Uri -Headers $Headers -UseBasicParsing -TimeoutSec 5
      if ($Response.StatusCode -ge 200 -and $Response.StatusCode -lt 300) { return }
    } catch {}
    Start-Sleep -Seconds 2
  }
  throw "timed out waiting for $Name"
}

function Get-FreeTcpPort {
  $Listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
  try {
    $Listener.Start()
    return ([Net.IPEndPoint]$Listener.LocalEndpoint).Port
  } finally {
    $Listener.Stop()
  }
}

function Get-UniqueFreePorts {
  param([int]$Count)
  $Ports = [Collections.Generic.HashSet[int]]::new()
  while ($Ports.Count -lt $Count) { [void]$Ports.Add((Get-FreeTcpPort)) }
  return @($Ports)
}

function Write-PrivateTextFile {
  param([Parameter(Mandatory = $true)][string]$Path, [AllowEmptyString()][string]$Value)
  $Parent = Split-Path -Parent $Path
  New-Item -ItemType Directory -Force -Path $Parent | Out-Null
  [IO.File]::WriteAllText($Path, $Value + "`n", [Text.UTF8Encoding]::new($false))
}

function Add-SensitiveValue {
  param([AllowEmptyString()][string]$Value)
  if (-not [string]::IsNullOrWhiteSpace($Value) -and $Value.Length -ge 6 -and -not $CredentialValues.Contains($Value)) {
    $CredentialValues.Add($Value)
  }
}

function Add-CoreOnlySensitiveValue {
  param([AllowEmptyString()][string]$Value)
  if (-not [string]::IsNullOrWhiteSpace($Value) -and $Value.Length -ge 6 -and -not $CoreOnlySensitiveValues.Contains($Value)) {
    $CoreOnlySensitiveValues.Add($Value)
  }
}

foreach ($StaticSecret in @(
    "ui17-admin-password", "ui17-core-admin-password", "ui17-owner-password",
    "ui17-sub2api-only", "ui17-core-only",
    "ui17-owned-jwt-secret-not-production", "ui17-external-a-jwt-secret-not-production", "ui17-external-b-jwt-secret-not-production",
    "65326d2d756931372d646973706f7361626c652d637572736f722d6b65792d31",
    "ui17-owned-user-password", "ui17-external-a-user-password", "ui17-external-b-user-password"
  )) {
  Add-SensitiveValue $StaticSecret
}

function Assert-NoKnownSensitiveValue {
  param([Parameter(Mandatory = $true)][string]$Text, [Parameter(Mandatory = $true)][string]$Surface)
  foreach ($Secret in @($CredentialValues) + @($CoreOnlySensitiveValues)) {
    if ($Text.IndexOf($Secret, [StringComparison]::Ordinal) -ge 0) { throw "$Surface disclosed a sensitive sentinel" }
  }
}

function Assert-NoBrowserSensitiveValue {
  param([Parameter(Mandatory = $true)][string]$Text, [Parameter(Mandatory = $true)][string]$Surface)
  Assert-NoKnownSensitiveValue $Text $Surface
  foreach ($Forbidden in @('authorization', 'x-api-key', 'user_bearer_token', 'gateway_url', 'local_ref')) {
    if ($Text.IndexOf($Forbidden, [StringComparison]::OrdinalIgnoreCase) -ge 0) { throw "$Surface disclosed forbidden field $Forbidden" }
  }
}

function Add-TimelineEvent {
  param([Parameter(Mandatory = $true)][string]$Event)
  $Timeline.Add([ordered]@{ event = $Event; at = (Get-Date).ToUniversalTime().ToString("o") })
}

function Set-DiagnosticStage {
  param([Parameter(Mandatory = $true)][string]$Stage)
  Assert-True ($AllowedDiagnosticStages -contains $Stage) "diagnostic stage was not allowlisted"
  $script:CurrentStage = $Stage
}

function Get-SafeFailureCategory {
  param($Failure)
  $Exception = if ($Failure -is [Management.Automation.ErrorRecord]) {
    $Failure.Exception
  } elseif ($Failure -is [Exception]) {
    $Failure
  } else {
    $null
  }
  if ($null -eq $Exception) { return "unknown_failure" }
  if ($Exception.Data.Contains("e2m_ui17_local_assertion") -and
      $Exception.Data["e2m_ui17_local_assertion"] -eq $true) {
    return "local_assertion"
  }
  if ($Exception -is [Net.WebException]) { return "http_transport" }
  if ($Exception -is [Management.Automation.RuntimeException]) { return "powershell_runtime" }
  return "unexpected_exception"
}

function Get-ProtectedStackSnapshot {
  $Names = @(
    "e2m-real-gateways-postgres-1",
    "e2m-real-gateways-connector-sub2api-1",
    "e2m-real-gateways-connector-newapi-1",
    "e2m-real-gateways-connector-cpa-1"
  )
  $Snapshot = @()
  foreach ($Name in $Names) {
    $Raw = & docker inspect $Name 2>$null
    if ($LASTEXITCODE -ne 0) {
      throw "protected container was not inspectable: $Name"
    }
    $Inspect = @(($Raw -join "`n") | ConvertFrom-Json)
    Assert-True ($Inspect.Count -eq 1) "protected container inspection was ambiguous"
    $Snapshot += [pscustomobject]@{
      name = $Name
      id = [string]$Inspect[0].Id
      started_at = [string]$Inspect[0].State.StartedAt
      status = [string]$Inspect[0].State.Status
      image = [string]$Inspect[0].Image
    }
  }
  foreach ($Item in $Snapshot) { Write-Output $Item }
}

function Assert-ProtectedStackUnchanged {
  param([object[]]$Before, [object[]]$After)
  Assert-True ($Before.Count -eq 4 -and $After.Count -eq 4) "protected stack snapshot was incomplete"
  foreach ($Expected in $Before) {
    $Actual = @($After | Where-Object { $_.name -eq $Expected.name })
    Assert-True ($Actual.Count -eq 1) "protected stack after-snapshot omitted a container"
    foreach ($Field in @("id", "started_at", "status", "image")) {
      Assert-True ($Actual[0].$Field -eq $Expected.$Field) "protected stack changed field $Field for $($Expected.name)"
    }
  }
}

function Get-ComposeSHA256 {
  return (Get-FileHash -Algorithm SHA256 -LiteralPath $ComposeFile).Hash.ToLowerInvariant()
}

function Get-SHA256Hex {
  param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Value)
  $SHA256 = [Security.Cryptography.SHA256]::Create()
  try {
    return ([BitConverter]::ToString($SHA256.ComputeHash([Text.UTF8Encoding]::new($false).GetBytes($Value)))).Replace("-", "").ToLowerInvariant()
  } finally {
    $SHA256.Dispose()
  }
}

function Get-BuildInputManifest {
  $FilesByPath = [Collections.Generic.Dictionary[string, object]]::new([StringComparer]::Ordinal)
  $RepositoryPrefix = $RepoRoot.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
  foreach ($RelativeRoot in $BuildInputRoots) {
    $FullRoot = [IO.Path]::GetFullPath((Join-Path $RepoRoot $RelativeRoot))
    $InsideRepository = $FullRoot -eq $RepoRoot -or
      $FullRoot.StartsWith($RepoRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)
    Assert-True $InsideRepository "build input root escaped repository"
    if (Test-Path -LiteralPath $FullRoot -PathType Leaf) {
      $Candidates = @(Get-Item -LiteralPath $FullRoot -Force)
    } elseif (Test-Path -LiteralPath $FullRoot -PathType Container) {
      $Candidates = @(Get-ChildItem -LiteralPath $FullRoot -Recurse -Force -File)
    } elseif ($RelativeRoot -eq "go.work.sum") {
      continue
    } else {
      throw "required build input was missing: $RelativeRoot"
    }
    foreach ($File in $Candidates) {
      $FullFilePath = [IO.Path]::GetFullPath($File.FullName)
      Assert-True ($FullFilePath.StartsWith($RepositoryPrefix, [StringComparison]::OrdinalIgnoreCase)) "build input file escaped repository"
      $RelativePath = $FullFilePath.Substring($RepositoryPrefix.Length).Replace('\', '/')
      # Mirror the repository .dockerignore for the selected COPY roots, while
      # also excluding UI-17 runtime artifacts that can be produced in-tree.
      if ($RelativePath -match '(^|/)(\.git|\.agents|\.codex|\.tmp|tmp|node_modules|dist|build|coverage)(/|$)' -or
          $RelativePath -match '(^|/)\.DS_Store$' -or
          $RelativePath -match '\.(exe|test|log|tsbuildinfo)$') {
        continue
      }
      $FilesByPath[$RelativePath] = $File
    }
  }
  [string[]]$SortedPaths = @($FilesByPath.Keys)
  [Array]::Sort($SortedPaths, [StringComparer]::Ordinal)
  $Entries = @($SortedPaths | ForEach-Object {
    $RelativePath = $_
    $File = $FilesByPath[$RelativePath]
    $Stream = [IO.File]::Open($File.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $SHA256 = [Security.Cryptography.SHA256]::Create()
    try {
      $FileSize = $Stream.Length
      $ContentSHA256 = ([BitConverter]::ToString($SHA256.ComputeHash($Stream))).Replace("-", "").ToLowerInvariant()
    } finally {
      $SHA256.Dispose()
      $Stream.Dispose()
    }
    "$RelativePath`t$FileSize`t$ContentSHA256"
  })
  Assert-True ($Entries.Count -gt 0) "build input manifest was empty"
  $SchemaHeader = "e2m-build-input-manifest-v1`tpath-size-content-sha256"
  $Canonical = $SchemaHeader + "`n" + ($Entries -join "`n") + "`n"
  return [ordered]@{
    schema = "e2m-build-input-manifest-v1"
    canonical_format = "schema-header LF; path TAB size TAB content_sha256 LF; ordinal path order"
    file_count = $Entries.Count
    canonical_sha256 = Get-SHA256Hex $Canonical
    roots = @($BuildInputRoots)
    exclusions = @(".git", ".agents", ".codex", ".tmp", "tmp", ".DS_Store", "node_modules", "dist", "build", "coverage", "*.exe", "*.test", "*.log", "*.tsbuildinfo")
  }
}

function Assert-BuildInputManifestEqual {
  param($Actual, $Expected, [Parameter(Mandatory = $true)][string]$Label)
  Assert-True ($null -ne $Actual -and $null -ne $Expected) "$Label build input manifest was missing"
  Assert-True ([string]$Actual.schema -eq [string]$Expected.schema) "$Label build input manifest schema changed"
  Assert-True ([int]$Actual.file_count -eq [int]$Expected.file_count) "$Label build input file count changed"
  Assert-True ([string]$Actual.canonical_sha256 -eq [string]$Expected.canonical_sha256) "$Label build input digest changed"
}

function Assert-ComposeImagePins {
  $Raw = Get-Content -Raw -LiteralPath $ComposeFile
  foreach ($Expected in @($PostgresImage, $RedisImage, $Sub2APIImage, $NodeImage)) {
    Assert-True ($Raw.IndexOf($Expected, [StringComparison]::Ordinal) -ge 0) "Compose file omitted a required immutable release image"
  }
  foreach ($ForbiddenName in @("E2M_UI17_CORE_IMAGE", "E2M_UI17_CONNECTOR_IMAGE", "UI17_CORE_IMAGE", "UI17_CONNECTOR_IMAGE", "NODE_IMAGE", "GOPROXY")) {
    Assert-True ($Raw.IndexOf(('${' + $ForbiddenName), [StringComparison]::OrdinalIgnoreCase) -lt 0) "Compose release image allowed environment override $ForbiddenName"
  }
  $AllNodeImageArguments = [regex]::Matches($Raw, '(?m)^\s*NODE_IMAGE:\s*[^\r\n#]+\s*$')
  $ExpectedNodeImageArguments = [regex]::Matches($Raw, ('(?m)^\s*NODE_IMAGE:\s*' + [regex]::Escape($NodeImage) + '\s*$'))
  Assert-True ($AllNodeImageArguments.Count -eq 1 -and $ExpectedNodeImageArguments.Count -eq 1) "Compose did not pin the vetted Node image exactly once"
  $AllGoProxyArguments = [regex]::Matches($Raw, '(?m)^\s*GOPROXY:\s*[^\r\n#]+\s*$')
  $ExpectedGoProxyArguments = [regex]::Matches($Raw, ('(?m)^\s*GOPROXY:\s*' + [regex]::Escape($ExpectedGoProxy) + '\s*$'))
  Assert-True ($AllGoProxyArguments.Count -eq $ExpectedBuiltServices.Count -and $ExpectedGoProxyArguments.Count -eq $ExpectedBuiltServices.Count) "Compose did not pin the expected GOPROXY exactly once for every built service"
}

function Get-DisposableImageEvidence {
  param([string[]]$ExpectedServiceNames = $ExpectedServices)
  $ContainerIDs = @(Get-ComposeContainerIDs @ExpectedServiceNames)
  Assert-True ($ContainerIDs.Count -eq $ExpectedServiceNames.Count) "image evidence did not inspect exactly $($ExpectedServiceNames.Count) containers"
  $Rows = @()
  $SeenServices = @{}
  foreach ($ContainerID in $ContainerIDs) {
    $RawContainer = & docker inspect $ContainerID 2>$null
    if ($LASTEXITCODE -ne 0) { throw "container image inspection failed" }
    $Container = @(($RawContainer -join "`n") | ConvertFrom-Json)[0]
    Assert-True ($null -ne $Container) "container image inspection returned no record"
    $ProjectLabel = [string]$Container.Config.Labels.'com.docker.compose.project'
    $Service = [string]$Container.Config.Labels.'com.docker.compose.service'
    Assert-True ($ProjectLabel -eq $ProjectName) "container image evidence had unexpected project label for $Service"
    Assert-True ($Service -in $ExpectedServiceNames) "container image evidence had unexpected service label $Service"
    Assert-True (-not $SeenServices.ContainsKey($Service)) "container image evidence duplicated service $Service"
    $SeenServices[$Service] = $true
    $ConfigImage = [string]$Container.Config.Image
    Assert-True (-not [string]::IsNullOrWhiteSpace($ConfigImage)) "$Service Config.Image was empty"
    $ImageID = [string]$Container.Image
    Assert-True ($ImageID -match '^sha256:[0-9a-f]{64}$') "$Service container Image ID was not immutable"
    $RawImage = & docker image inspect $ImageID 2>$null
    if ($LASTEXITCODE -ne 0) { throw "Docker image metadata inspection failed" }
    $Image = @(($RawImage -join "`n") | ConvertFrom-Json)[0]
    Assert-True ([string]$Image.Id -eq $ImageID) "$Service container Image ID differed from inspected image identity"
    $RepoDigests = @($Image.RepoDigests | ForEach-Object { [string]$_ } | Sort-Object -Unique)
    $Kind = "pinned"
    $BuiltRepository = ""
    if ($ExpectedPinnedImages.ContainsKey($Service)) {
      $Expected = $ExpectedPinnedImages[$Service]
      Assert-True ($ConfigImage -eq [string]$Expected.config_image) "$Service Config.Image differed from the Compose-pinned digest"
      Assert-True ($RepoDigests -contains [string]$Expected.repo_digest) "$Service RepoDigests omitted the exact Compose-pinned repository digest"
    } else {
      $Kind = "built"
      Assert-True ($ExpectedBuiltImages.ContainsKey($Service)) "$Service had no declared image identity policy"
      $BuiltRepository = [string]$ExpectedBuiltImages[$Service]
      $RepoTags = @($Image.RepoTags | ForEach-Object { [string]$_ } | Sort-Object -Unique)
      $ExpectedBuiltTag = "$BuiltRepository`:latest"
      Assert-True ($ConfigImage -eq $BuiltRepository) "$Service Config.Image differed from its exact Compose-built repository"
      Assert-True (@($RepoTags | Where-Object { $_ -eq $ExpectedBuiltTag }).Count -eq 1) "$Service image metadata omitted its exact Compose-built repository tag"
    }
    $Rows += [ordered]@{
      service = $Service; project_label = $ProjectLabel; service_label = $Service
      config_image = $ConfigImage; image_id = $ImageID; inspected_image_id = [string]$Image.Id
      kind = $Kind; built_repository = $BuiltRepository; repo_digests = $RepoDigests
    }
  }
  Assert-True ($SeenServices.Count -eq $ExpectedServiceNames.Count) "image evidence omitted one or more disposable services"
  foreach ($Service in $ExpectedServiceNames) { Assert-True ($SeenServices.ContainsKey($Service)) "image evidence omitted service $Service" }
  foreach ($Row in @($Rows | Sort-Object service)) { Write-Output $Row }
}

function Get-BuiltImageProvenance {
  param([object[]]$Images)
  Assert-True ($RunnerSHA256Before -match '^[0-9a-f]{64}$') "built image provenance lacked the runner digest"
  Assert-True ($ComposeSHA256Before -match '^[0-9a-f]{64}$') "built image provenance lacked the Compose digest"
  Assert-True ($null -ne $BuildInputManifestBefore -and [string]$BuildInputManifestBefore.canonical_sha256 -match '^[0-9a-f]{64}$') "built image provenance lacked the build input digest"
  $BuiltRows = @($Images | Where-Object { [string]$_.kind -eq "built" } | Sort-Object service)
  Assert-True ($BuiltRows.Count -eq $ExpectedBuiltServices.Count) "built image provenance had the wrong service count"
  $ExpectedServiceSet = @($ExpectedBuiltServices | Sort-Object)
  $ActualServiceSet = @($BuiltRows | ForEach-Object { [string]$_.service } | Sort-Object -Unique)
  Assert-True ($ActualServiceSet.Count -eq $ExpectedServiceSet.Count) "built image provenance duplicated a service"
  for ($Index = 0; $Index -lt $ExpectedServiceSet.Count; $Index++) {
    Assert-True ($ActualServiceSet[$Index] -eq $ExpectedServiceSet[$Index]) "built image provenance service set was not exact"
  }
  $Rows = @($BuiltRows | ForEach-Object {
    Assert-True ([string]$_.image_id -match '^sha256:[0-9a-f]{64}$' -and [string]$_.image_id -eq [string]$_.inspected_image_id) "built image provenance contained an invalid image identity"
    [ordered]@{
      service = [string]$_.service
      config_image = [string]$_.config_image
      image_id = [string]$_.image_id
      inspected_image_id = [string]$_.inspected_image_id
      built_repository = [string]$_.built_repository
      runner_sha256 = $RunnerSHA256Before
      compose_sha256 = $ComposeSHA256Before
      build_input_sha256 = [string]$BuildInputManifestBefore.canonical_sha256
      build_input_file_count = [int]$BuildInputManifestBefore.file_count
      go_proxy = $ExpectedGoProxy
    }
  })
  Assert-True (@($Rows | ForEach-Object { [string]$_.service } | Sort-Object -Unique).Count -eq $ExpectedBuiltServices.Count) "built image provenance service uniqueness failed"
  Assert-True (@($Rows | Where-Object {
    $_.runner_sha256 -ne $RunnerSHA256Before -or $_.compose_sha256 -ne $ComposeSHA256Before -or
    $_.build_input_sha256 -ne [string]$BuildInputManifestBefore.canonical_sha256 -or
    $_.build_input_file_count -ne [int]$BuildInputManifestBefore.file_count -or
    $_.go_proxy -ne $ExpectedGoProxy
  }).Count -eq 0) "built image provenance bindings were inconsistent"
  foreach ($Row in $Rows) { Write-Output $Row }
}

function Assert-ConnectorPublicResponse {
  param($Response, [Parameter(Mandatory = $true)][string]$Name)
  function Assert-CredentialConfiguredObject {
    param($Value)
    Assert-True ($null -ne $Value -and ($Value -is [Collections.IDictionary] -or $Value -is [pscustomobject])) "$Name credential configured field was not an object"
    $HasXAPIKey = if ($Value -is [Collections.IDictionary]) {
      $Value.Contains("x_api_key")
    } else {
      $null -ne $Value.PSObject.Properties["x_api_key"]
    }
    if ($HasXAPIKey) {
      $XAPIKeyValue = if ($Value -is [Collections.IDictionary]) { $Value["x_api_key"] } else { $Value.PSObject.Properties["x_api_key"].Value }
      Assert-True ($XAPIKeyValue -is [bool]) "$Name credential configured x api key was not boolean"
    }
  }
  function Assert-CredentialConfiguredShape {
    param($Value)
    if ($null -eq $Value) { return }
    if ($Value -is [Collections.IDictionary]) {
      foreach ($Key in @($Value.Keys)) {
        $Child = $Value[$Key]
        if ([string]$Key -eq "credential_configured") {
          Assert-CredentialConfiguredObject $Child
        }
        Assert-CredentialConfiguredShape $Child
      }
      return
    }
    if ($Value -is [pscustomobject]) {
      foreach ($Property in @($Value.PSObject.Properties)) {
        $Child = $Property.Value
        if ($Property.Name -eq "credential_configured") {
          Assert-CredentialConfiguredObject $Child
        }
        Assert-CredentialConfiguredShape $Child
      }
      return
    }
    if ($Value -is [Collections.IEnumerable] -and -not ($Value -is [string])) {
      foreach ($Child in @($Value)) { Assert-CredentialConfiguredShape $Child }
    }
  }
  Assert-CredentialConfiguredShape $Response
  $Raw = $Response | ConvertTo-Json -Depth 30 -Compress
  foreach ($Secret in $CredentialValues) {
    if ($Raw.IndexOf($Secret, [StringComparison]::Ordinal) -ge 0) { throw "$Name local public response disclosed a credential" }
  }
  if ($Raw -match '(?i)"credentials"\s*:|"x_api_key"\s*:\s*"|"authorization"\s*:') {
    throw "$Name local public response serialized credential material"
  }
}

function Login-Sub2API {
  param([Parameter(Mandatory = $true)][string]$BaseUrl, [Parameter(Mandatory = $true)][string]$Email,
    [Parameter(Mandatory = $true)][string]$Password, [Parameter(Mandatory = $true)][string]$Name)
  $Login = Invoke-Json POST "$BaseUrl/api/v1/auth/login" @{
    email = $Email; password = $Password; turnstile_token = ""
  }
  $Data = Get-EnvelopeData $Login "$Name login"
  $Token = [string](Get-JsonValue $Data "access_token")
  if ([string]::IsNullOrWhiteSpace($Token)) { throw "$Name login did not return access_token" }
  return $Token
}

function Assert-Sub2APIUserDTOs {
  param([Parameter(Mandatory = $true)][string]$BaseUrl, [Parameter(Mandatory = $true)][string]$Bearer,
    [Parameter(Mandatory = $true)][long]$GroupID, [Parameter(Mandatory = $true)][string]$Name,
    [Parameter(Mandatory = $true)][double]$ExpectedBalance, [Parameter(Mandatory = $true)][double]$ExpectedGroupRate,
    [Parameter(Mandatory = $true)][double]$ExpectedUserRate, [Parameter(Mandatory = $true)][double]$ExpectedInputPrice,
    [Parameter(Mandatory = $true)][double]$ExpectedOutputPrice)
  $Headers = @{ Authorization = "Bearer $Bearer" }
  $Profile = Get-EnvelopeData (Invoke-Json GET "$BaseUrl/api/v1/user/profile" $null $Headers) "$Name profile"
  Assert-JsonNumber (Get-JsonValue $Profile "balance") "$Name profile balance"
  Assert-True ([math]::Abs([double](Get-JsonValue $Profile "balance") - $ExpectedBalance) -lt 0.000000001) "$Name profile balance differed from its seed"
  $Groups = @(Get-EnvelopeData (Invoke-Json GET "$BaseUrl/api/v1/groups/available" $null $Headers) "$Name groups")
  Assert-True ($Groups.Count -gt 0) "$Name groups endpoint returned no groups"
  $Group = @($Groups | Where-Object { [long](Get-JsonValue $_ "id") -eq $GroupID })[0]
  Assert-True ($null -ne $Group) "$Name groups endpoint omitted the seeded group"
  Assert-JsonNumber (Get-JsonValue $Group "id") "$Name group id"
  Assert-JsonNumber (Get-JsonValue $Group "rate_multiplier") "$Name group multiplier"
  Assert-True ([math]::Abs([double](Get-JsonValue $Group "rate_multiplier") - $ExpectedGroupRate) -lt 0.000000001) "$Name group default multiplier differed from its seed"
  $Rates = Get-EnvelopeData (Invoke-Json GET "$BaseUrl/api/v1/groups/rates" $null $Headers) "$Name rates"
  Assert-True ($null -ne $Rates) "$Name rates endpoint returned null after seeding a group override"
  $RateProperty = $Rates.PSObject.Properties[[string]$GroupID]
  Assert-True ($null -ne $RateProperty) "$Name rates endpoint omitted the seeded group override"
  Assert-JsonNumber $RateProperty.Value "$Name user group rate"
  Assert-True ([math]::Abs([double]$RateProperty.Value - $ExpectedUserRate) -lt 0.000000001) "$Name user multiplier differed from its seed"
  $Channels = @(Get-EnvelopeData (Invoke-Json GET "$BaseUrl/api/v1/channels/available" $null $Headers) "$Name channels")
  Assert-True ($Channels.Count -gt 0) "$Name channels endpoint returned no channels"
  $Channel = @($Channels | Where-Object { (Get-JsonValue $_ "name") -eq "UI17 $Name channel" })[0]
  Assert-True ($null -ne $Channel) "$Name channels endpoint omitted the seeded channel"
  foreach ($Field in @("name", "description", "platforms")) {
    Assert-True ($null -ne $Channel.PSObject.Properties[$Field]) "$Name channel omitted $Field"
  }
  $Platforms = @(Get-JsonValue $Channel "platforms")
  Assert-True ($Platforms.Count -gt 0) "$Name channel returned no platforms"
  $Platform = @($Platforms | Where-Object { (Get-JsonValue $_ "platform") -eq "anthropic" })[0]
  Assert-True ($null -ne $Platform) "$Name channel omitted the seeded anthropic platform"
  foreach ($Field in @("platform", "groups", "supported_models")) {
    Assert-True ($null -ne $Platform.PSObject.Properties[$Field]) "$Name channel platform omitted $Field"
  }
  $Models = @(Get-JsonValue $Platform "supported_models")
  Assert-True ($Models.Count -gt 0) "$Name channel returned no supported models"
  $Model = @($Models | Where-Object { (Get-JsonValue $_ "name") -eq "claude-sonnet-4" -and (Get-JsonValue $_ "platform") -eq "anthropic" })[0]
  Assert-True ($null -ne $Model) "$Name channel omitted the exact seeded model"
  # Sub2API v0.1.164 exposes its configured station price only as `pricing`.
  # The Connector's pinned-version compatibility mapping treats that field as
  # the published settlement price only when `site_pricing` is absent. Assert
  # the real upstream contract here; Core/PG assertions below prove the mapped
  # offers and the verified default per_tokens=1 instead of inventing a field.
  $Pricing = Get-JsonValue $Model "pricing"
  $SitePricing = Get-JsonValue $Model "site_pricing"
  Assert-True ($null -ne $Pricing) "$Name model omitted pricing"
  Assert-True ($null -eq $Model.PSObject.Properties["site_pricing"] -and $null -eq $SitePricing) "$Name Sub2API v0.1.164 unexpectedly exposed site_pricing"
  Assert-True ((Get-JsonValue $Pricing "billing_mode") -eq "token") "$Name pricing billing mode was not token"
  Assert-True ($null -ne $Pricing.PSObject.Properties["intervals"]) "$Name pricing omitted intervals"
  foreach ($Pair in @(
      @{ field = "input_price"; expected = $ExpectedInputPrice },
      @{ field = "output_price"; expected = $ExpectedOutputPrice },
      @{ field = "cache_read_price"; expected = ($ExpectedInputPrice / 10) }
    )) {
    $Value = Get-JsonValue $Pricing $Pair.field
    Assert-JsonNumber $Value "$Name pricing $($Pair.field)"
    Assert-True ([math]::Abs([double]$Value - [double]$Pair.expected) -lt 0.000000000001) "$Name pricing $($Pair.field) differed from its seed"
  }
}

function Initialize-Sub2APIStation {
  param(
    [Parameter(Mandatory = $true)][string]$Name,
    [Parameter(Mandatory = $true)][string]$BaseUrl,
    [Parameter(Mandatory = $true)][double]$Balance,
    [Parameter(Mandatory = $true)][double]$GroupRate,
    [Parameter(Mandatory = $true)][double]$UserRate,
    [Parameter(Mandatory = $true)][double]$InputPrice,
    [Parameter(Mandatory = $true)][double]$OutputPrice
  )
  Write-Step "Bootstrapping real Sub2API station $Name"
  $AdminToken = Login-Sub2API $BaseUrl "admin@ui17.invalid" "ui17-admin-password" "$Name admin"
  Add-SensitiveValue $AdminToken
  $AdminHeaders = @{ Authorization = "Bearer $AdminToken" }
  $Compliance = Invoke-Json GET "$BaseUrl/api/v1/admin/compliance" $null $AdminHeaders -AllowNotFound
  if ($null -ne $Compliance) {
    $ComplianceData = Get-EnvelopeData $Compliance "$Name compliance"
    if ([bool](Get-JsonValue $ComplianceData "required")) {
      [void](Invoke-Json POST "$BaseUrl/api/v1/admin/compliance/accept" @{
        phrase = "I have read, understood, and agree to the Sub2API Deployment and Operation Compliance Commitment"
        language = "en"
      } $AdminHeaders)
    }
  }
  [void](Get-EnvelopeData (Invoke-Json PUT "$BaseUrl/api/v1/admin/settings" @{ available_channels_enabled = $true } $AdminHeaders) "$Name available channels setting")
  $KeyData = Get-EnvelopeData (Invoke-Json POST "$BaseUrl/api/v1/admin/settings/admin-api-key/regenerate" @{} $AdminHeaders) "$Name admin key"
  $AdminAPIKey = [string](Get-JsonValue $KeyData "key")
  if ([string]::IsNullOrWhiteSpace($AdminAPIKey)) { throw "$Name did not return a generated admin API key" }
  Add-SensitiveValue $AdminAPIKey

  $GroupData = Get-EnvelopeData (Invoke-Json POST "$BaseUrl/api/v1/admin/groups" @{
    name = "UI17 $Name group"; description = "Disposable UI-17 acceptance group"
    platform = "anthropic"; rate_multiplier = $GroupRate; subscription_type = "standard"
  } $AdminHeaders) "$Name group create"
  $GroupID = [long](Get-JsonValue $GroupData "id")
  Assert-True ($GroupID -gt 0) "$Name group create did not return data.id"

  $ChannelBody = @{
    name = "UI17 $Name channel"; description = "Disposable UI-17 acceptance pricing"
    group_ids = @($GroupID); billing_model_source = "channel_mapped"; restrict_models = $true
    model_pricing = @(@{
      platform = "anthropic"; models = @("claude-sonnet-4"); billing_mode = "token"
      input_price = $InputPrice; output_price = $OutputPrice; cache_read_price = ($InputPrice / 10)
    })
  }
  $ChannelData = Get-EnvelopeData (Invoke-Json POST "$BaseUrl/api/v1/admin/channels" $ChannelBody $AdminHeaders) "$Name channel create"
  $ChannelID = [long](Get-JsonValue $ChannelData "id")
  Assert-True ($ChannelID -gt 0) "$Name channel create did not return data.id"

  $UserEmail = "ui17-$Name-user@invalid.test"
  $UserPassword = "ui17-$Name-user-password"
  $UserData = Get-EnvelopeData (Invoke-Json POST "$BaseUrl/api/v1/admin/users" @{
    email = $UserEmail; password = $UserPassword; username = "ui17-$Name-user"; role = "user"
    balance = $Balance; concurrency = 5; rpm_limit = 60; allowed_groups = @($GroupID)
  } $AdminHeaders) "$Name user create"
  $UserID = [long](Get-JsonValue $UserData "id")
  Assert-True ($UserID -gt 0) "$Name user create did not return data.id"
  $RateMap = @{}; $RateMap[[string]$GroupID] = $UserRate
  [void](Get-EnvelopeData (Invoke-Json PUT "$BaseUrl/api/v1/admin/users/$UserID" @{ group_rates = $RateMap } $AdminHeaders) "$Name user rate update")
  $UserBearer = Login-Sub2API $BaseUrl $UserEmail $UserPassword "$Name ordinary user"
  Add-SensitiveValue $UserBearer
  Assert-Sub2APIUserDTOs $BaseUrl $UserBearer $GroupID $Name $Balance $GroupRate $UserRate $InputPrice $OutputPrice
  return [pscustomobject]@{
    Name = $Name; BaseUrl = $BaseUrl; AdminToken = $AdminToken; AdminAPIKey = $AdminAPIKey
    UserBearer = $UserBearer; UserID = $UserID; GroupID = $GroupID; ChannelID = $ChannelID; ChannelBody = $ChannelBody
    Balance = $Balance; GroupRate = $GroupRate; UserRate = $UserRate; InputPrice = $InputPrice; OutputPrice = $OutputPrice
  }
}

function Login-Core {
  param([Parameter(Mandatory = $true)][string]$CoreBaseUrl)
  $Response = Invoke-Json POST "$CoreBaseUrl/api/v1/auth/login" @{
    email = "admin@ui17.invalid"; password = "ui17-core-admin-password"
  }
  $Token = [string](Get-JsonValue $Response "token")
  if ([string]::IsNullOrWhiteSpace($Token)) { throw "Core login did not return a token" }
  Add-SensitiveValue $Token
  return $Token
}

function New-CoreFixture {
  param([Parameter(Mandatory = $true)][string]$CoreBaseUrl)
  Write-Step "Creating the Core owner, three instances, and three one-time Connector enrollments"
  $Token = Login-Core $CoreBaseUrl
  $Headers = @{ Authorization = "Bearer $Token" }
  $Owner = Invoke-Json POST "$CoreBaseUrl/api/v1/users" @{
    email = "ui17-owner@invalid.test"; password = "ui17-owner-password"; display_name = "UI-17 disposable owner"; roles = @("client")
  } $Headers
  $OwnerID = [long](Get-JsonValue $Owner "id")
  Assert-True ($OwnerID -gt 0) "Core owner create did not return an id"
  $Specs = @()
  foreach ($Name in @("owned", "external-a", "external-b")) {
    $Created = Invoke-Json POST "$CoreBaseUrl/api/v1/instances" @{
      user_id = $OwnerID; name = "UI17 $Name Sub2API"; kind = "sub2api"
    } $Headers
    $InstanceID = [string](Get-JsonValue $Created "id")
    $Install = Get-JsonValue $Created "connector_install"
    if ($null -eq $Install) { $Install = Invoke-Json POST "$CoreBaseUrl/api/v1/instances/$InstanceID/connector-install" $null $Headers }
    $Enrollment = Get-JsonValue $Install "enrollment"
    $ConnectorID = [string](Get-JsonValue $Enrollment "connector_id")
    $EnrollmentToken = [string](Get-JsonValue $Install "token")
    if ($InstanceID -eq "" -or $ConnectorID -eq "" -or $EnrollmentToken -eq "") {
      throw "Core did not return a complete $Name Connector install"
    }
    Add-SensitiveValue $EnrollmentToken
    $Specs += [pscustomobject]@{
      Name = $Name; InstanceID = $InstanceID; ConnectorID = $ConnectorID; EnrollmentToken = $EnrollmentToken
    }
  }
  return [pscustomobject]@{ Token = $Token; Headers = $Headers; OwnerID = $OwnerID; Specs = $Specs }
}

function Get-ConnectorLocalUIToken {
  param([Parameter(Mandatory = $true)][string]$Service)
  $Deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $Deadline) {
    try {
      $Token = (Invoke-ComposeCapture exec --no-TTY $Service sh -c "cat /var/lib/e2m-agent/local-ui.token").Trim()
      if ($Token -ne "") { Add-SensitiveValue $Token; return $Token }
    } catch {}
    Start-Sleep -Seconds 1
  }
  throw "timed out waiting for $Service local UI token"
}

function Wait-ConnectorRuntimeToken {
  param([Parameter(Mandatory = $true)][string]$Service)
  $Deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $Deadline) {
    try {
      $Token = (Invoke-ComposeCapture exec --no-TTY $Service sh -c "cat /var/lib/e2m-agent/connector.token").Trim()
      if ($Token -ne "") { Add-SensitiveValue $Token; return }
    } catch {}
    Start-Sleep -Seconds 1
  }
  throw "timed out waiting for $Service runtime Connector token"
}

function Initialize-ConnectorSource {
  param(
    [Parameter(Mandatory = $true)]$Spec,
    [Parameter(Mandatory = $true)]$Station,
    [Parameter(Mandatory = $true)][int]$LocalPort,
    [Parameter(Mandatory = $true)][string]$InternalGatewayUrl,
    [Parameter(Mandatory = $true)][ValidateSet("owned", "external")][string]$Mode
  )
  $Service = "connector-$($Spec.Name)"
  $LocalBase = "http://127.0.0.1:$LocalPort"
  $LocalToken = Get-ConnectorLocalUIToken $Service
  Wait-ConnectorRuntimeToken $Service
  $Headers = @{ "X-E2M-Local-Token" = $LocalToken; Origin = $LocalBase }
  Wait-Http "$LocalBase/api/local/connector/config" "$Service local API" $Headers
  $Saved = Invoke-Json POST "$LocalBase/api/local/connector/config" @{
    gateway_kind = "sub2api"; gateway_url = $InternalGatewayUrl; auth = "x-api-key"
    credentials = @{ x_api_key = $Station.AdminAPIKey }
  } $Headers
  Assert-True ([bool](Get-JsonValue (Get-JsonValue $Saved "config") "gateway_configured")) "$Service did not save its managed gateway"
  Assert-ConnectorPublicResponse $Saved "$Service config"
  $Credentials = @{ user_bearer_token = $Station.UserBearer }
  $SourceBody = @{
    mode = $Mode; provider = "sub2api"; display_name = "UI17 $($Spec.Name) source"
    credentials = $Credentials; currency = "USD"; poll_interval_seconds = 60; status = "active"
  }
  if ($Mode -eq "external") {
    $SourceBody.gateway_url = $InternalGatewayUrl
    $Credentials.x_api_key = $Station.AdminAPIKey
  }
  $Created = Invoke-Json POST "$LocalBase/api/local/upstream-intelligence/sources" $SourceBody $Headers
  Assert-ConnectorPublicResponse $Created "$Service source"
  $PublicSource = Get-JsonValue $Created "source"
  $LocalRef = [string](Get-JsonValue $PublicSource "local_ref")
  Assert-True ($LocalRef -ne "") "$Service source create did not return local_ref"
  $Tested = Invoke-Json POST "$LocalBase/api/local/upstream-intelligence/sources/$LocalRef/test" @{} $Headers
  Assert-ConnectorPublicResponse $Tested "$Service reachability test"
  Assert-True ((Get-JsonValue $Tested "status") -ge 200 -and (Get-JsonValue $Tested "status") -lt 300 -and
    -not [bool](Get-JsonValue $Tested "collected")) "$Service reachability test did not prove a non-collecting authenticated test"
  return [pscustomobject]@{
    Name = $Spec.Name; Service = $Service; LocalBase = $LocalBase; LocalToken = $LocalToken
    LocalRef = $LocalRef; InstanceID = $Spec.InstanceID; ConnectorID = $Spec.ConnectorID; Mode = $Mode
  }
}

function Rotate-Sub2APIAdminKey {
  param($Station)
  $Headers = @{ Authorization = "Bearer $($Station.AdminToken)" }
  $Data = Get-EnvelopeData (Invoke-Json POST "$($Station.BaseUrl)/api/v1/admin/settings/admin-api-key/regenerate" @{} $Headers) "$($Station.Name) rotated admin key"
  $Rotated = [string](Get-JsonValue $Data "key")
  Assert-True ($Rotated -ne "" -and $Rotated -ne $Station.AdminAPIKey) "$($Station.Name) admin key rotation did not invalidate the configured key"
  Add-SensitiveValue $Rotated
  try {
    [void](Invoke-WebRequest -Method GET -Uri "$($Station.BaseUrl)/api/v1/admin/accounts" -Headers @{ "x-api-key" = $Station.AdminAPIKey } -UseBasicParsing -TimeoutSec 15)
    throw "$($Station.Name) old administrator key remained valid after rotation"
  } catch {
    if ($_.Exception.Message -like "*remained valid*") { throw }
    $Status = 0
    if ($null -ne $_.Exception.Response -and $null -ne $_.Exception.Response.StatusCode) { $Status = [int]$_.Exception.Response.StatusCode }
    Assert-True ($Status -in @(401, 403)) "$($Station.Name) old administrator key rejection was not an authentication failure"
  }
  $Station | Add-Member -NotePropertyName RotatedAdminAPIKey -NotePropertyValue $Rotated -Force
}

function Invoke-ConnectorCollect {
  param([Parameter(Mandatory = $true)]$Connector, [Parameter(Mandatory = $true)][ValidateSet("succeeded", "failed")][string]$ExpectedStatus)
  $Headers = @{ "X-E2M-Local-Token" = $Connector.LocalToken; Origin = $Connector.LocalBase }
  $Response = Invoke-Json POST "$($Connector.LocalBase)/api/local/upstream-intelligence/sources/$($Connector.LocalRef)/collect" @{} $Headers
  Assert-ConnectorPublicResponse $Response "$($Connector.Service) collect"
  $Summary = Get-JsonValue $Response "summary"
  Assert-True ($null -ne $Summary) "$($Connector.Service) collect omitted summary"
  Assert-True (([string](Get-JsonValue $Summary "status")) -eq $ExpectedStatus) "$($Connector.Service) collect returned an unexpected status"
  if ($ExpectedStatus -eq "succeeded") {
    Assert-True (([string](Get-JsonValue $Summary "coverage")) -eq "complete") "$($Connector.Service) collection was not complete"
    Assert-True ([int](Get-JsonValue $Summary "fact_count") -gt 1) "$($Connector.Service) collection returned too few real facts"
    Assert-True ([string]::IsNullOrWhiteSpace([string](Get-JsonValue $Summary "error_code"))) "$($Connector.Service) collection returned an error code"
  } else {
    Assert-True (([string](Get-JsonValue $Summary "coverage")) -eq "unavailable") "$($Connector.Service) failure drill was not unavailable"
    Assert-True ([int](Get-JsonValue $Summary "fact_count") -eq 0) "$($Connector.Service) failure drill persisted ambiguous facts"
    Assert-True (([string](Get-JsonValue $Summary "error_code")) -eq "upstream_unavailable") "$($Connector.Service) failure drill did not use the allowlisted upstream_unavailable code"
  }
  Assert-True ([bool](Get-JsonValue $Response "uploaded")) "$($Connector.Service) collection did not upload its durable batch"
  Assert-True (-not [bool](Get-JsonValue $Response "queued")) "$($Connector.Service) collection left a pending batch"
  Assert-True ([string]::IsNullOrWhiteSpace([string](Get-JsonValue $Response "error_code"))) "$($Connector.Service) local upload returned an error"
  return $Response
}

function Invoke-CoreCursorPages {
  param(
    $CoreContext, [Parameter(Mandatory = $true)][string]$CoreBaseUrl,
    [Parameter(Mandatory = $true)][ValidateSet("sources", "rates", "changes")][string]$Kind,
    [Parameter(Mandatory = $true)][long]$FactVersion
  )
  $Items = @(); $Seen = @{}; $Cursor = ""; $PageCount = 0
  do {
    $Uri = "$CoreBaseUrl/api/v1/upstream-intelligence/$Kind`?user_id=$($CoreContext.OwnerID)&fact_version=$FactVersion&limit=1"
    if ($Cursor -ne "") { $Uri += "&cursor=$([uri]::EscapeDataString($Cursor))" }
    $Page = Invoke-Json GET $Uri $null $CoreContext.Headers -CaptureCoreRead
    $PageCount++
    Assert-True ([long](Get-JsonValue $Page "fact_version") -eq $FactVersion) "$Kind pagination changed fact_version"
    $PageItems = @((Get-JsonValue $Page "items"))
    Assert-True ($PageItems.Count -le 1) "$Kind pagination ignored limit=1"
    foreach ($Item in $PageItems) {
      $ID = if ($Kind -eq "rates") { [string](Get-JsonValue $Item "observation_id") } else { [string](Get-JsonValue $Item "id") }
      Assert-True ($ID -ne "" -and -not $Seen.ContainsKey($ID)) "$Kind pagination returned an empty or duplicate identity"
      $Seen[$ID] = $true; $Items += $Item
    }
    $Cursor = [string](Get-JsonValue $Page "next_cursor")
    Assert-True ($PageCount -le 10000) "$Kind pagination exceeded its safety bound"
  } while ($Cursor -ne "")
  return [pscustomobject]@{ Items = @($Items); PageCount = $PageCount; FactVersion = $FactVersion }
}

function Get-CoreBrowserSnapshot {
  param($CoreContext, [Parameter(Mandatory = $true)][string]$CoreBaseUrl)
  for ($Attempt = 1; $Attempt -le 4; $Attempt++) {
    try {
      $Overview = Invoke-Json GET "$CoreBaseUrl/api/v1/upstream-intelligence/overview?user_id=$($CoreContext.OwnerID)" $null $CoreContext.Headers -CaptureCoreRead
      $FactVersion = [long](Get-JsonValue $Overview "fact_version")
      Assert-True ($FactVersion -gt 0) "Core overview omitted a finalized fact_version"
      $Sources = Invoke-CoreCursorPages $CoreContext $CoreBaseUrl "sources" $FactVersion
      $Rates = Invoke-CoreCursorPages $CoreContext $CoreBaseUrl "rates" $FactVersion
      $Changes = Invoke-CoreCursorPages $CoreContext $CoreBaseUrl "changes" $FactVersion
      $Wallets = @((Get-JsonValue $Overview "wallets"))
      Assert-True ($Wallets.Count -gt 0 -and $Rates.Items.Count -gt 0) "Core snapshot omitted wallet or offer evidence candidates"
      $WalletID = [string](Get-JsonValue $Wallets[0] "observation_id")
      $OfferID = [string](Get-JsonValue $Rates.Items[0] "observation_id")
      $WalletEvidence = Invoke-Json GET "$CoreBaseUrl/api/v1/upstream-intelligence/evidence/$([uri]::EscapeDataString($WalletID))`?user_id=$($CoreContext.OwnerID)&fact_version=$FactVersion" $null $CoreContext.Headers -CaptureCoreRead
      $OfferEvidence = Invoke-Json GET "$CoreBaseUrl/api/v1/upstream-intelligence/evidence/$([uri]::EscapeDataString($OfferID))`?user_id=$($CoreContext.OwnerID)&fact_version=$FactVersion" $null $CoreContext.Headers -CaptureCoreRead
      Assert-True ([long](Get-JsonValue $WalletEvidence "fact_version") -eq $FactVersion -and (Get-JsonValue $WalletEvidence "kind") -eq "wallet") "wallet evidence did not share the browser snapshot"
      Assert-True ([long](Get-JsonValue $OfferEvidence "fact_version") -eq $FactVersion -and (Get-JsonValue $OfferEvidence "kind") -eq "offer") "offer evidence did not share the browser snapshot"
      $ChangeEvidence = $null
      if ($Changes.Items.Count -gt 0) {
        $ChangeID = [string](Get-JsonValue $Changes.Items[0] "id")
        $ChangeEvidence = Invoke-Json GET "$CoreBaseUrl/api/v1/upstream-intelligence/evidence/$([uri]::EscapeDataString($ChangeID))`?user_id=$($CoreContext.OwnerID)&fact_version=$FactVersion" $null $CoreContext.Headers -CaptureCoreRead
        Assert-True ([long](Get-JsonValue $ChangeEvidence "fact_version") -eq $FactVersion -and (Get-JsonValue $ChangeEvidence "kind") -eq "change") "change evidence did not share the browser snapshot"
      }
      return [pscustomobject]@{
        FactVersion = $FactVersion; Overview = $Overview; Sources = @($Sources.Items); Rates = @($Rates.Items)
        Changes = @($Changes.Items); WalletEvidence = $WalletEvidence; OfferEvidence = $OfferEvidence; ChangeEvidence = $ChangeEvidence
        Pages = [ordered]@{ sources = $Sources.PageCount; rates = $Rates.PageCount; changes = $Changes.PageCount }
      }
    } catch {
      if ($Attempt -eq 4) { throw }
      Start-Sleep -Milliseconds 500
    }
  }
  throw "Core browser snapshot retry bound was exhausted"
}

function Get-CoreSources {
  param($CoreContext, [string]$CoreBaseUrl)
  for ($Attempt = 1; $Attempt -le 4; $Attempt++) {
    try {
      $Head = Invoke-Json GET "$CoreBaseUrl/api/v1/upstream-intelligence/sources?user_id=$($CoreContext.OwnerID)&limit=1" $null $CoreContext.Headers -CaptureCoreRead
      $FactVersion = [long](Get-JsonValue $Head "fact_version")
      foreach ($Item in @((Invoke-CoreCursorPages $CoreContext $CoreBaseUrl "sources" $FactVersion).Items)) { Write-Output $Item }
      return
    } catch {
      if ($Attempt -eq 4) { throw }
    }
  }
}

function Assert-CoreIntelligence {
  param($CoreContext, [string]$CoreBaseUrl)
  Write-Step "Verifying browser-safe Core intelligence DTOs"
  Set-DiagnosticStage "core_browser_snapshot"
  $Browser = Get-CoreBrowserSnapshot $CoreContext $CoreBaseUrl
  Add-TimelineEvent "core_browser_snapshot_verified"
  Set-DiagnosticStage "core_source_cardinality"
  $Sources = @($Browser.Sources)
  Assert-True ($Sources.Count -eq 3) "Core did not expose exactly three intelligence sources"
  Assert-True (@($Sources | Where-Object { (Get-JsonValue $_ "mode") -eq "owned" }).Count -eq 1) "Core source modes did not contain one owned source"
  Assert-True (@($Sources | Where-Object { (Get-JsonValue $_ "mode") -eq "external" }).Count -eq 2) "Core source modes did not contain two external sources"
  Set-DiagnosticStage "core_source_details"
  foreach ($Source in $Sources) {
    Assert-True ((Get-JsonValue $Source "provider") -eq "sub2api") "Core exposed a non-Sub2API source"
    Assert-True ((Get-JsonValue $Source "freshness") -eq "current") "Core source was not current"
    Assert-True ((Get-JsonValue $Source "last_coverage") -eq "complete") "Core source coverage was not complete"
    Assert-True ([string]::IsNullOrWhiteSpace([string](Get-JsonValue $Source "last_error_code"))) "Core source retained an error after success"
    $SourceID = [uri]::EscapeDataString([string](Get-JsonValue $Source "id"))
    $Detail = Invoke-Json GET "$CoreBaseUrl/api/v1/upstream-intelligence/sources/$SourceID`?user_id=$($CoreContext.OwnerID)" $null $CoreContext.Headers -CaptureCoreRead
    $Wallet = Get-JsonValue $Detail "wallet"
    Assert-True ($null -ne $Wallet -and $null -ne (Get-JsonValue $Wallet "balance_amount")) "Core source detail omitted its exact wallet balance"
    $WalletEvidence = Get-JsonValue $Wallet "evidence"
    Assert-True ((Get-JsonValue $WalletEvidence "accuracy") -eq "exact") "Core wallet evidence was not exact"
    Assert-True ((Get-JsonValue $WalletEvidence "coverage") -eq "complete") "Core wallet evidence was not complete"
    Assert-True ((Get-JsonValue $WalletEvidence "freshness") -eq "current") "Core wallet evidence was not current"
    $Rates = @((Get-JsonValue $Detail "current_rates"))
    Assert-True ($Rates.Count -gt 0) "Core source detail omitted current rates"
    foreach ($Rate in $Rates) {
      Assert-True ($null -ne (Get-JsonValue $Rate "published_unit_price")) "Core rate omitted published_unit_price"
      Assert-True ($null -ne (Get-JsonValue $Rate "group_multiplier")) "Core rate omitted group_multiplier"
    }
  }
  Add-TimelineEvent "core_source_details_verified"
  Set-DiagnosticStage "core_overview_metrics"
  $Overview = $Browser.Overview
  Assert-True ([int](Get-JsonValue (Get-JsonValue $Overview "metrics") "source_count") -eq 3) "Core overview source_count was not three"
  Assert-True ($Browser.Rates.Count -gt 0) "Core rates pagination was empty"
  return $Sources
}

function Get-PostgresScalar {
  param([Parameter(Mandatory = $true)][string]$SQL)
  $Value = (Invoke-ComposeCapture exec --no-TTY core-postgres psql -v ON_ERROR_STOP=1 -U e2m -d e2m -Atc $SQL).Trim()
  if ($Value -notmatch '^[0-9]+$') { throw "PostgreSQL verification did not return an integer" }
  return [long]$Value
}

function Get-PostgresText {
  param([Parameter(Mandatory = $true)][string]$SQL)
  return (Invoke-ComposeCapture exec --no-TTY core-postgres psql -v ON_ERROR_STOP=1 -U e2m -d e2m -Atc $SQL).Trim()
}

function ConvertFrom-OutboxProbe {
  param(
    [Parameter(Mandatory = $true)][AllowEmptyString()][string]$Raw,
    [Parameter(Mandatory = $true)][string]$Label
  )
  $Normalized = $Raw.Replace("`r`n", "`n").Replace("`r", "`n")
  $Newline = $Normalized.IndexOf("`n", [StringComparison]::Ordinal)
  if ($Newline -ge 0) {
    $Marker = $Normalized.Substring(0, $Newline).Trim()
    $Payload = $Normalized.Substring($Newline + 1).Trim()
  } else {
    $Marker = $Normalized.Trim()
    $Payload = ""
  }
  if ($Marker -eq $OutboxMissingMarker) {
    Assert-True ($Payload -eq "") "$Label missing marker contained an unexpected payload"
    return [pscustomobject]@{
      present = $false; version = 0; pending = @(); checksum = ""; payload_sha256 = ""
    }
  }
  Assert-True ($Marker -eq $OutboxPresentMarker) "$Label returned an unknown outbox probe marker"
  Assert-True ($Payload -ne "") "$Label present marker omitted its outbox payload"
  try { $Outbox = $Payload | ConvertFrom-Json }
  catch { throw "$Label outbox was not valid JSON" }
  $Version = Get-JsonValue $Outbox "version"
  Assert-True ($Version -is [int] -and [int]$Version -in @(1, 2)) "$Label outbox version was invalid"
  $Checksum = [string](Get-JsonValue $Outbox "checksum")
  Assert-True ($Checksum -match '^[0-9a-f]{64}$') "$Label outbox checksum was missing"
  $StoredPending = @((Get-JsonValue $Outbox "pending"))
  $Pending = @()
  if ([int]$Version -eq 1) {
    foreach ($Request in $StoredPending) {
      Assert-True ($null -ne $Request) "$Label legacy outbox contained an empty request"
      $Pending += $Request
    }
  } else {
    foreach ($Entry in $StoredPending) {
      $Request = Get-JsonValue $Entry "request"
      Assert-True ($null -ne $Request) "$Label current outbox entry omitted its request"
      $Pending += $Request
    }
  }
  return [pscustomobject]@{
    present = $true
    version = [int]$Version
    pending = @($Pending)
    checksum = $Checksum
    payload_sha256 = Get-SHA256Hex $Payload
  }
}

function Get-ConnectorOutboxSnapshot {
  param(
    [Parameter(Mandatory = $true)][string]$Service,
    [Parameter(Mandatory = $true)][string]$Label
  )
  # Keep the native-process argument free of quotes, escapes, variables, and
  # substitutions. Windows PowerShell 5.1 rewrites those before Docker passes
  # the argument to `sh -c`, while these fixed tokens round-trip byte-for-byte.
  $Command = 'if [ -f /var/lib/e2m-agent/upstream-intelligence-outbox.json ]; then echo E2M_UI17_OUTBOX_PRESENT; cat /var/lib/e2m-agent/upstream-intelligence-outbox.json; else echo E2M_UI17_OUTBOX_MISSING; fi'
  $Raw = Invoke-ComposeCapture exec --no-TTY $Service sh -c $Command
  return ConvertFrom-OutboxProbe $Raw $Label
}

function Assert-OutboxProbeParser {
  $Missing = ConvertFrom-OutboxProbe "$OutboxMissingMarker`n" "missing fixture"
  Assert-True (-not [bool]$Missing.present -and @($Missing.pending).Count -eq 0 -and
    [int]$Missing.version -eq 0 -and [string]$Missing.checksum -eq "" -and
    [string]$Missing.payload_sha256 -eq "") "missing outbox fixture was not exactly empty"
  $FixtureChecksum = "a" * 64
  $Present = ConvertFrom-OutboxProbe "$OutboxPresentMarker`n{`"version`":2,`"pending`":[],`"checksum`":`"$FixtureChecksum`"}`n" "present fixture"
  Assert-True ([bool]$Present.present -and [int]$Present.version -eq 2 -and
    @($Present.pending).Count -eq 0 -and [string]$Present.checksum -eq $FixtureChecksum -and
    [string]$Present.payload_sha256 -match '^[0-9a-f]{64}$') "present outbox fixture was not parsed exactly"
  $V2 = ConvertFrom-OutboxProbe "$OutboxPresentMarker`n{`"version`":2,`"pending`":[{`"enqueued_at`":`"2026-07-27T00:00:00Z`",`"request`":{`"run`":{`"id`":`"uirun_v2`"}}}],`"checksum`":`"$FixtureChecksum`"}`n" "v2 fixture"
  Assert-True ([int]$V2.version -eq 2 -and @($V2.pending).Count -eq 1 -and
    [string](Get-JsonValue (Get-JsonValue $V2.pending[0] "run") "id") -eq "uirun_v2") "v2 outbox request was not normalized"
  $V1 = ConvertFrom-OutboxProbe "$OutboxPresentMarker`n{`"version`":1,`"pending`":[{`"run`":{`"id`":`"uirun_v1`"}}],`"checksum`":`"$FixtureChecksum`"}`n" "v1 fixture"
  Assert-True ([int]$V1.version -eq 1 -and @($V1.pending).Count -eq 1 -and
    [string](Get-JsonValue (Get-JsonValue $V1.pending[0] "run") "id") -eq "uirun_v1") "v1 outbox request was not normalized"
  foreach ($Invalid in @(
    "E2M_UI17_OUTBOX_UNKNOWN`n",
    "$OutboxMissingMarker`n{}`n",
    "$OutboxPresentMarker`nnot-json`n",
    "$OutboxPresentMarker`n{`"version`":3,`"pending`":[],`"checksum`":`"$FixtureChecksum`"}`n",
    "$OutboxPresentMarker`n{`"version`":2,`"pending`":[{}],`"checksum`":`"$FixtureChecksum`"}`n"
  )) {
    $Rejected = $false
    try { [void](ConvertFrom-OutboxProbe $Invalid "invalid fixture") }
    catch { $Rejected = $true }
    Assert-True $Rejected "invalid outbox probe fixture was accepted"
  }
}

function Assert-PostgresEvidence {
  param([long]$OwnerID)
  Write-Step "Verifying finalized PostgreSQL facts and durable Connector outboxes"
  Set-DiagnosticStage "postgres_source_identity"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_intelligence_sources WHERE user_id=$OwnerID") -eq 3) "PostgreSQL source count was not three"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_intelligence_sources WHERE user_id=$OwnerID AND id ~ '^uisrc-[0-9a-f]+$' AND BTRIM(local_ref)<>''") -eq 3) "PostgreSQL source identities were not three opaque uisrc IDs with local refs"
  Assert-True ((Get-PostgresScalar "SELECT count(DISTINCT connector_id) FROM upstream_intelligence_sources WHERE user_id=$OwnerID") -eq 3) "PostgreSQL sources did not have three connector identities"
  Assert-True ((Get-PostgresScalar "SELECT count(DISTINCT instance_id) FROM upstream_intelligence_sources WHERE user_id=$OwnerID") -eq 3) "PostgreSQL sources did not have three instance identities"
  Assert-True ((Get-PostgresScalar "SELECT count(DISTINCT local_ref) FROM upstream_intelligence_sources WHERE user_id=$OwnerID") -eq 3) "PostgreSQL sources did not have three local refs"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_intelligence_sources WHERE user_id=$OwnerID AND mode='owned'") -eq 1) "PostgreSQL did not contain exactly one owned source"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_intelligence_sources WHERE user_id=$OwnerID AND mode='external'") -eq 2) "PostgreSQL did not contain exactly two external sources"
  Add-TimelineEvent "postgres_source_identity_verified"
  Set-DiagnosticStage "postgres_run_manifest"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_collection_runs WHERE user_id=$OwnerID AND status='succeeded' AND coverage='complete' AND batch_count>0 AND fact_count>0 AND page_count>0 AND finalized_fact_version>0") -ge 3) "PostgreSQL lacked complete positive-count finalized runs"
  Assert-True ((Get-PostgresScalar "SELECT count(DISTINCT source_id) FROM upstream_collection_runs WHERE user_id=$OwnerID AND status='succeeded' AND coverage='complete' AND finalized_fact_version>0") -eq 3) "PostgreSQL successful runs did not cover all sources"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_ingest_batches WHERE user_id=$OwnerID") -ge 3) "PostgreSQL ingest batches were incomplete"
  Add-TimelineEvent "postgres_run_manifest_verified"
  Set-DiagnosticStage "postgres_observations"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_wallet_observations WHERE user_id=$OwnerID") -ge 3) "PostgreSQL wallet observations were incomplete"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_offer_observations WHERE user_id=$OwnerID") -gt 3) "PostgreSQL offer observations were incomplete"
  Assert-True ((Get-PostgresScalar "SELECT count(DISTINCT source_id) FROM upstream_offer_observations WHERE user_id=$OwnerID") -eq 3) "PostgreSQL offers did not cover all sources"
  Assert-True ((Get-PostgresScalar "SELECT count(DISTINCT source_id) FROM upstream_wallet_observations WHERE user_id=$OwnerID") -eq 3) "PostgreSQL wallets did not cover all sources"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_offer_observations WHERE user_id=$OwnerID AND per_tokens<>1") -eq 0) "Sub2API v0.1.164 offers did not preserve the verified per_tokens=1 mapping"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_collection_runs r WHERE r.user_id=$OwnerID AND r.finalized_fact_version>0 AND (SELECT count(*) FROM upstream_ingest_batches b WHERE b.user_id=r.user_id AND b.run_id=r.id)<>r.batch_count") -eq 0) "an ingest batch count differed from its finalized manifest"
  Add-TimelineEvent "postgres_observations_verified"
  Set-DiagnosticStage "connector_outbox_drain"
  foreach ($Service in @("connector-owned", "connector-external-a", "connector-external-b")) {
    $Deadline = (Get-Date).AddSeconds([Math]::Min($TimeoutSeconds, 60))
    $PendingCount = -1
    do {
      $Outbox = Get-ConnectorOutboxSnapshot $Service "$Service successful-upload outbox"
      $PendingCount = @($Outbox.pending).Count
      if ($PendingCount -eq 0) { break }
      Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $Deadline)
    Assert-True ($PendingCount -eq 0) "$Service outbox did not drain after successful upload"
  }
  Add-TimelineEvent "connector_outboxes_drained"
}

function Invoke-DurableOutboxDrill {
  param($CoreContext, [string]$CoreBaseUrl, $Connector)
  Write-Step "Proving durable pending state across Connector restart and automatic replay"
  Set-DiagnosticStage "durable_outbox_core_stop"
  Invoke-Compose stop e2m-core
  Set-DiagnosticStage "durable_outbox_queue"
  $Headers = @{ "X-E2M-Local-Token" = $Connector.LocalToken; Origin = $Connector.LocalBase }
  $PendingResponse = Invoke-Json POST "$($Connector.LocalBase)/api/local/upstream-intelligence/sources/$($Connector.LocalRef)/collect" @{} $Headers -ExpectedStatus 202
  $PendingSummary = Get-JsonValue $PendingResponse "summary"
  $RunID = [string](Get-JsonValue $PendingSummary "run_id")
  Assert-True ($RunID -ne "" -and [bool](Get-JsonValue $PendingResponse "queued") -and -not [bool](Get-JsonValue $PendingResponse "uploaded")) "offline collect did not return a durable pending run"
  Assert-True ((Get-JsonValue $PendingResponse "error_code") -eq "upload_failed") "offline collect did not fail with upload_failed"
  $Before = Get-ConnectorOutboxSnapshot $Connector.Service "pending outbox"
  Assert-True ([bool]$Before.present) "offline collect did not create a durable outbox file"
  Assert-True ([int]$Before.version -eq 2) "offline collect did not persist the current outbox version"
  $Pending = @($Before.pending)
  Assert-True ($Pending.Count -gt 0 -and @($Pending | Where-Object { (Get-JsonValue (Get-JsonValue $_ "run") "id") -eq $RunID }).Count -gt 0) "pending outbox omitted the offline run"
  Set-DiagnosticStage "durable_outbox_connector_restart"
  Invoke-Compose restart $Connector.Service
  Wait-Http "$($Connector.LocalBase)/api/local/connector/config" "$($Connector.Service) after restart" $Headers
  $AfterRestart = Get-ConnectorOutboxSnapshot $Connector.Service "restarted pending outbox"
  Assert-True ([bool]$AfterRestart.present) "Connector restart removed the pending outbox file"
  Assert-True ([int]$AfterRestart.version -eq [int]$Before.version) "Connector restart changed the pending outbox version"
  Assert-True ([string]$AfterRestart.checksum -eq [string]$Before.checksum) "Connector restart changed the pending outbox checksum while Core was stopped"
  Assert-True ([string]$AfterRestart.payload_sha256 -eq [string]$Before.payload_sha256) "Connector restart changed the pending outbox bytes while Core was stopped"
  Assert-True (@($AfterRestart.pending).Count -eq $Pending.Count) "Connector restart lost pending batches"
  Assert-True (@($AfterRestart.pending | Where-Object { (Get-JsonValue (Get-JsonValue $_ "run") "id") -eq $RunID }).Count -gt 0) "Connector restart lost the pending run identity"
  Set-DiagnosticStage "durable_outbox_replay"
  Invoke-Compose start e2m-core
  Wait-Http "$CoreBaseUrl/healthz" "restarted E2M Core"
  $Deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $Replayed = $false
  $OutboxFileRemoved = $false
  while ((Get-Date) -lt $Deadline) {
    try {
      $Current = Get-ConnectorOutboxSnapshot $Connector.Service "replayed outbox"
      if (@($Current.pending).Count -eq 0 -and
          (Get-PostgresScalar "SELECT count(*) FROM upstream_collection_runs WHERE user_id=$($CoreContext.OwnerID) AND id='$RunID' AND finalized_fact_version>0") -eq 1) {
        $OutboxFileRemoved = -not [bool]$Current.present
        $Replayed = $true; break
      }
    } catch {}
    Start-Sleep -Seconds 2
  }
  Assert-True $Replayed "automatic Connector polling did not replay and finalize the durable pending run"
  return [ordered]@{
    run_id = $RunID; pending_batches = $Pending.Count; restart_preserved = $true
    replay_finalized = $true; outbox_drained = $true; outbox_file_removed = $OutboxFileRemoved
  }
}

function Find-CoreSource {
  param([object[]]$Sources, [Parameter(Mandatory = $true)][string]$DisplayName)
  return @($Sources | Where-Object { (Get-JsonValue $_ "display_name") -eq $DisplayName })[0]
}

function Invoke-FailureIsolationAndRecovery {
  param($CoreContext, [string]$CoreBaseUrl, [object[]]$Connectors, [int]$ExternalBPort)
  Write-Step "Proving one-station failure isolation, stale evidence, and recovery"
  $Owned = @($Connectors | Where-Object Name -eq "owned")[0]
  $ExternalA = @($Connectors | Where-Object Name -eq "external-a")[0]
  $ExternalB = @($Connectors | Where-Object Name -eq "external-b")[0]
  $BeforeRuns = @{
    owned = Get-PostgresScalar "SELECT count(*) FROM upstream_collection_runs r JOIN upstream_intelligence_sources s ON s.user_id=r.user_id AND s.id=r.source_id WHERE r.user_id=$($CoreContext.OwnerID) AND s.display_name='UI17 owned source' AND r.status='succeeded'"
    external_a = Get-PostgresScalar "SELECT count(*) FROM upstream_collection_runs r JOIN upstream_intelligence_sources s ON s.user_id=r.user_id AND s.id=r.source_id WHERE r.user_id=$($CoreContext.OwnerID) AND s.display_name='UI17 external-a source' AND r.status='succeeded'"
  }
  $BeforeB = Find-CoreSource @(Get-CoreSources $CoreContext $CoreBaseUrl) "UI17 external-b source"
  $BeforeBID = [string](Get-JsonValue $BeforeB "id")
  $BeforeWalletID = Get-PostgresText "SELECT id FROM upstream_wallet_observations WHERE user_id=$($CoreContext.OwnerID) AND source_id='$BeforeBID' ORDER BY observed_at DESC LIMIT 1"
  $BeforeOfferID = Get-PostgresText "SELECT id FROM upstream_offer_observations WHERE user_id=$($CoreContext.OwnerID) AND source_id='$BeforeBID' ORDER BY observed_at DESC LIMIT 1"
  $FreshUntilRaw = Get-PostgresText "SELECT GREATEST((SELECT fresh_until FROM upstream_wallet_observations WHERE user_id=$($CoreContext.OwnerID) AND source_id='$BeforeBID' ORDER BY observed_at DESC LIMIT 1),(SELECT fresh_until FROM upstream_offer_observations WHERE user_id=$($CoreContext.OwnerID) AND source_id='$BeforeBID' ORDER BY observed_at DESC LIMIT 1))::text"
  $FreshUntil = [DateTimeOffset]::MinValue
  Assert-True ([DateTimeOffset]::TryParse($FreshUntilRaw, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::AssumeUniversal, [ref]$FreshUntil)) "stale drill did not load a valid fresh_until boundary"
  Invoke-Compose stop external-b-sub2api
  [void](Invoke-ConnectorCollect $ExternalB "failed")
  $Deadline = (Get-Date).AddSeconds([Math]::Min($TimeoutSeconds, 150))
  $LastHealthyRefresh = [datetime]::MinValue
  $StaleProved = $false
  while ((Get-Date) -lt $Deadline) {
    if (((Get-Date) - $LastHealthyRefresh).TotalSeconds -ge 25) {
      [void](Invoke-ConnectorCollect $Owned "succeeded")
      [void](Invoke-ConnectorCollect $ExternalA "succeeded")
      $LastHealthyRefresh = Get-Date
    }
    $Sources = @(Get-CoreSources $CoreContext $CoreBaseUrl)
    $B = Find-CoreSource $Sources "UI17 external-b source"
    $Healthy = @($Sources | Where-Object { (Get-JsonValue $_ "display_name") -in @("UI17 owned source", "UI17 external-a source") })
    if ($null -ne $B -and (Get-JsonValue $B "freshness") -eq "stale" -and
        $Healthy.Count -eq 2 -and @($Healthy | Where-Object { (Get-JsonValue $_ "freshness") -ne "current" }).Count -eq 0) {
      $StaleProved = $true; break
    }
    Start-Sleep -Seconds 5
  }
  Assert-True $StaleProved "failed source did not become stale while healthy sources remained current"
  Assert-True ([DateTimeOffset]::UtcNow -gt $FreshUntil.ToUniversalTime()) "stale drill did not cross the persisted freshness boundary"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_wallet_observations WHERE user_id=$($CoreContext.OwnerID) AND id='$BeforeWalletID'") -eq 1) "failure drill discarded the last successful wallet"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_offer_observations WHERE user_id=$($CoreContext.OwnerID) AND id='$BeforeOfferID'") -eq 1) "failure drill discarded the last successful rate"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_collection_runs r JOIN upstream_intelligence_sources s ON s.user_id=r.user_id AND s.id=r.source_id WHERE r.user_id=$($CoreContext.OwnerID) AND s.display_name='UI17 owned source' AND r.status='succeeded'") -gt $BeforeRuns.owned) "owned source did not continue succeeding during peer failure"
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_collection_runs r JOIN upstream_intelligence_sources s ON s.user_id=r.user_id AND s.id=r.source_id WHERE r.user_id=$($CoreContext.OwnerID) AND s.display_name='UI17 external-a source' AND r.status='succeeded'") -gt $BeforeRuns.external_a) "external-a did not continue succeeding during peer failure"
  Invoke-Compose start external-b-sub2api
  Wait-Http "http://127.0.0.1:$ExternalBPort/health" "restarted external-b Sub2API"
  $RecoveryResponse = Invoke-ConnectorCollect $ExternalB "succeeded"
  $RecoveryRunID = [string](Get-JsonValue (Get-JsonValue $RecoveryResponse "summary") "run_id")
  Assert-True ((Get-PostgresScalar "SELECT count(*) FROM upstream_collection_runs WHERE user_id=$($CoreContext.OwnerID) AND id='$RecoveryRunID' AND status='succeeded' AND finalized_fact_version>0") -eq 1) "recovery did not create a new finalized successful run"
  $Recovered = Find-CoreSource @(Get-CoreSources $CoreContext $CoreBaseUrl) "UI17 external-b source"
  Assert-True ((Get-JsonValue $Recovered "freshness") -eq "current" -and
    (Get-JsonValue $Recovered "last_coverage") -eq "complete" -and
    [string]::IsNullOrWhiteSpace([string](Get-JsonValue $Recovered "last_error_code"))) "external-b source did not recover cleanly"
}

function Invoke-ConfirmedChangeDrill {
  param($CoreContext, [string]$CoreBaseUrl, $Station, $Connector)
  Write-Step "Proving real price/rate history, two-snapshot removal confirmation, evidence, and recovery"
  $Headers = @{ Authorization = "Bearer $($Station.AdminToken)" }
  $Source = Find-CoreSource @(Get-CoreSources $CoreContext $CoreBaseUrl) "UI17 external-a source"
  $SourceID = [string](Get-JsonValue $Source "id")
  $BeforePrice = Get-PostgresText "SELECT published_unit_price::text FROM upstream_offer_observations WHERE user_id=$($CoreContext.OwnerID) AND source_id='$SourceID' AND price_dimension='input' ORDER BY observed_at DESC LIMIT 1"
  $BeforeRate = Get-PostgresText "SELECT group_multiplier::text FROM upstream_offer_observations WHERE user_id=$($CoreContext.OwnerID) AND source_id='$SourceID' ORDER BY observed_at DESC LIMIT 1"
  $BeforePriceCanonical = ConvertTo-CanonicalDecimalText $BeforePrice "price before change"
  $BeforeRateCanonical = ConvertTo-CanonicalDecimalText $BeforeRate "multiplier before change"
  $ChangedInputPrice = [double]$Station.InputPrice * 1.25
  $ChangedOutputPrice = [double]$Station.OutputPrice * 1.20
  $ChangedUserRate = [double]$Station.UserRate * 1.10
  $ChangedChannelBody = @{
    model_pricing = @(@{
      platform = "anthropic"; models = @("claude-sonnet-4"); billing_mode = "token"
      input_price = $ChangedInputPrice; output_price = $ChangedOutputPrice; cache_read_price = ($ChangedInputPrice / 10)
    })
  }
  [void](Get-EnvelopeData (Invoke-Json PUT "$($Station.BaseUrl)/api/v1/admin/channels/$($Station.ChannelID)" $ChangedChannelBody $Headers) "external-a real price update")
  $RateMap = @{}; $RateMap[[string]$Station.GroupID] = $ChangedUserRate
  [void](Get-EnvelopeData (Invoke-Json PUT "$($Station.BaseUrl)/api/v1/admin/users/$($Station.UserID)" @{ group_rates = $RateMap } $Headers) "external-a real user rate update")
  $ChangedResponse = Invoke-ConnectorCollect $Connector "succeeded"
  $ChangedRunID = [string](Get-JsonValue (Get-JsonValue $ChangedResponse "summary") "run_id")
  $AfterPrice = Get-PostgresText "SELECT published_unit_price::text FROM upstream_offer_observations WHERE user_id=$($CoreContext.OwnerID) AND source_id='$SourceID' AND run_id='$ChangedRunID' AND price_dimension='input' LIMIT 1"
  $AfterRate = Get-PostgresText "SELECT group_multiplier::text FROM upstream_offer_observations WHERE user_id=$($CoreContext.OwnerID) AND source_id='$SourceID' AND run_id='$ChangedRunID' LIMIT 1"
  $AfterPriceCanonical = ConvertTo-CanonicalDecimalText $AfterPrice "price after change"
  $AfterRateCanonical = ConvertTo-CanonicalDecimalText $AfterRate "multiplier after change"
  Assert-True ($AfterPriceCanonical -ne $BeforePriceCanonical) "real Sub2API price update did not create a changed observation"
  Assert-True ($AfterRateCanonical -ne $BeforeRateCanonical) "real Sub2API user multiplier update did not create a changed observation"
  $RemovalBefore = Get-PostgresScalar "SELECT count(*) FROM upstream_change_events WHERE user_id=$($CoreContext.OwnerID) AND source_id='$SourceID' AND event_type IN ('group_removed','model_removed')"
  [void](Get-EnvelopeData (Invoke-Json DELETE "$($Station.BaseUrl)/api/v1/admin/channels/$($Station.ChannelID)" $null $Headers) "external-a channel delete")
  $AbsenceSnapshots = @()
  foreach ($Attempt in 1..2) {
    $Response = Invoke-Json POST "$($Connector.LocalBase)/api/local/upstream-intelligence/sources/$($Connector.LocalRef)/collect" @{} @{
      "X-E2M-Local-Token" = $Connector.LocalToken; Origin = $Connector.LocalBase
    }
    $Summary = Get-JsonValue $Response "summary"
    Assert-True ((Get-JsonValue $Summary "status") -eq "succeeded" -and (Get-JsonValue $Summary "coverage") -eq "complete") "change drill collection was not complete"
    Assert-True ([bool](Get-JsonValue $Response "uploaded") -and -not [bool](Get-JsonValue $Response "queued")) "change drill collection did not finalize"
    $AbsenceRunID = [string](Get-JsonValue $Summary "run_id")
    $AbsenceCoverage = [string](Get-JsonValue $Summary "coverage")
    $AbsenceFactCount = [int](Get-JsonValue $Summary "fact_count")
    Assert-True ($AbsenceRunID -match '^uirun_[0-9a-f]{32}$') "absence snapshot run identity was invalid"
    if ($AbsenceSnapshots.Count -gt 0) {
      Assert-True ($AbsenceRunID -ne [string]$AbsenceSnapshots[-1].run_id) "absence snapshots reused a collection run identity"
    }
    $RemovalCount = Get-PostgresScalar "SELECT count(*) FROM upstream_change_events WHERE user_id=$($CoreContext.OwnerID) AND source_id='$SourceID' AND event_type IN ('group_removed','model_removed')"
    $AbsenceSnapshots += [ordered]@{
      round = $Attempt; run_id = $AbsenceRunID; coverage = $AbsenceCoverage
      fact_count = $AbsenceFactCount; removal_count = $RemovalCount
    }
    if ($Attempt -eq 1) {
      Assert-True ($RemovalCount -eq $RemovalBefore) "first absent complete snapshot prematurely confirmed removal"
    } else {
      Assert-True ($RemovalCount -gt $RemovalBefore) "second distinct complete snapshot did not confirm removal"
    }
  }
  Assert-True ($AbsenceSnapshots.Count -eq 2) "change drill did not retain exactly two absence snapshots"
  $Changes = Invoke-Json GET "$CoreBaseUrl/api/v1/upstream-intelligence/changes?user_id=$($CoreContext.OwnerID)&source_id=$([uri]::EscapeDataString($SourceID))&limit=100" $null $CoreContext.Headers -CaptureCoreRead
  Assert-True (@((Get-JsonValue $Changes "items")).Count -gt 0) "Core browser change feed omitted the confirmed removal"
  $Change = @((Get-JsonValue $Changes "items") | Where-Object { (Get-JsonValue $_ "event_type") -in @("group_removed", "model_removed") })[0]
  Assert-True ($null -ne $Change) "Core change feed omitted the removal event"
  $ChangeID = [string](Get-JsonValue $Change "id")
  $ChangeEvidence = Invoke-Json GET "$CoreBaseUrl/api/v1/upstream-intelligence/evidence/$([uri]::EscapeDataString($ChangeID))`?user_id=$($CoreContext.OwnerID)" $null $CoreContext.Headers -CaptureCoreRead
  Assert-True ((Get-JsonValue $ChangeEvidence "kind") -eq "change" -and (Get-JsonValue $ChangeEvidence "id") -eq $ChangeID) "removal change evidence was not browser-readable"
  $RecoveryChannelBody = @{}
  foreach ($Entry in $Station.ChannelBody.GetEnumerator()) { $RecoveryChannelBody[$Entry.Key] = $Entry.Value }
  $RecoveryChannelBody.name = "UI17 external-a recovery channel"
  $NewChannel = Get-EnvelopeData (Invoke-Json POST "$($Station.BaseUrl)/api/v1/admin/channels" $RecoveryChannelBody $Headers) "external-a channel recovery"
  Assert-True ([long](Get-JsonValue $NewChannel "id") -gt 0) "external-a channel recovery did not return data.id"
  [void](Invoke-ConnectorCollect $Connector "succeeded")
  $Detail = Invoke-Json GET "$CoreBaseUrl/api/v1/upstream-intelligence/sources/$([uri]::EscapeDataString($SourceID))`?user_id=$($CoreContext.OwnerID)" $null $CoreContext.Headers -CaptureCoreRead
  Assert-True (@((Get-JsonValue $Detail "current_rates")).Count -gt 0) "recovered catalog did not restore current rates"
  return [ordered]@{
    changed_run_id = $ChangedRunID; removal_change_id = $ChangeID
    price_changed = $true; multiplier_changed = $true
    price = [ordered]@{ before = $BeforePriceCanonical; after = $AfterPriceCanonical }
    multiplier = [ordered]@{ before = $BeforeRateCanonical; after = $AfterRateCanonical }
    absence_snapshots = @($AbsenceSnapshots)
    two_snapshot_confirmation = $true
  }
}

function Assert-SensitiveBoundaries {
  param([long]$OwnerID)
  Write-Step "Scanning browser DTOs, intelligence tables, evidence surfaces, and all 14 service logs"
  $CoreSurface = $CoreReadBodies -join "`n"
  Assert-NoBrowserSensitiveValue $CoreSurface "Core browser responses"
  $DatabaseSurface = Get-PostgresText "SELECT jsonb_build_object('sources',(SELECT jsonb_agg(to_jsonb(s)) FROM upstream_intelligence_sources s WHERE user_id=$OwnerID),'runs',(SELECT jsonb_agg(to_jsonb(r)) FROM upstream_collection_runs r WHERE user_id=$OwnerID),'batches',(SELECT jsonb_agg(to_jsonb(b)) FROM upstream_ingest_batches b WHERE user_id=$OwnerID),'wallets',(SELECT jsonb_agg(to_jsonb(w)) FROM upstream_wallet_observations w WHERE user_id=$OwnerID),'offers',(SELECT jsonb_agg(to_jsonb(o)) FROM upstream_offer_observations o WHERE user_id=$OwnerID),'changes',(SELECT jsonb_agg(to_jsonb(c)) FROM upstream_change_events c WHERE user_id=$OwnerID))::text"
  Assert-NoKnownSensitiveValue $DatabaseSurface "Core intelligence PostgreSQL tables"
  $AllLogs = Invoke-ComposeCapture logs --no-color
  Assert-NoKnownSensitiveValue $AllLogs "disposable service logs"
  return [ordered]@{ browser_bodies = $CoreReadBodies.Count; postgres_tables = 6; service_logs = 14; known_secret_scan = "passed"; forbidden_browser_fields = "passed" }
}

function Remove-DisposableStack {
  Assert-ExactProject
  Assert-RuntimePath
  # This is the only stack deletion command in this runner. The unique project
  # name and exact Compose file prevent label/glob cleanup from touching peers.
  Invoke-Compose down --volumes --remove-orphans
}


function Assert-DisposableProjectRemoved {
  $Containers = @(Get-ComposeContainerIDs)
  Assert-True ($Containers.Count -eq 0) "disposable Compose containers remained after exact down"
  $Volumes = @(& docker volume ls --quiet --filter "label=com.docker.compose.project=$ProjectName" 2>$null | Where-Object { $_.Trim() -ne "" })
  if ($LASTEXITCODE -ne 0) { throw "Docker volume cleanup verification failed" }
  Assert-True ($Volumes.Count -eq 0) "disposable Compose volumes remained after exact down"
  $Networks = @(& docker network ls --quiet --filter "label=com.docker.compose.project=$ProjectName" 2>$null | Where-Object { $_.Trim() -ne "" })
  if ($LASTEXITCODE -ne 0) { throw "Docker network cleanup verification failed" }
  Assert-True ($Networks.Count -eq 0) "disposable Compose networks remained after exact down"
  return [ordered]@{ containers = 0; volumes = 0; networks = 0; verified_empty = $true }
}

function Write-RedactedEvidence {
  param([bool]$ReleasePass, [string]$FailureCode, [AllowEmptyString()][string]$PrimaryFailureDetail)
  if ($ReleasePass) {
    Assert-True ($BusinessPassed -and $DisposableRemoved -and $RuntimeRemoved -and $EnvironmentRestored) "PASS evidence lacked business or cleanup proof"
    Assert-True ($ProtectedUnchanged -and $ComposeUnchanged -and $RunnerUnchanged -and $BuildInputsUnchanged -and $ImagesVerified -and $BuiltImagesProvenanceBound) "PASS evidence lacked protected-stack, source, Compose, or image proof"
    Assert-True ($null -eq $PrimaryFailure -and $null -eq $CleanupFailure) "PASS evidence was attempted after a failure"
    Assert-True ($PrimaryFailureDetail -eq "") "PASS evidence contained a primary failure detail"
  }
  New-Item -ItemType Directory -Force -Path $EvidenceRunDir | Out-Null
  $Evidence = [ordered]@{
    schema = 4; project = $ProjectName; observation_profile = $ObservationProfile
    source_frozen_acknowledged = $SourceFrozenAcknowledged
    runner_sha256 = $RunnerSHA256Before
    release_eligible = $ReleaseEligible; release_pass = ($ReleaseEligible -and $ReleasePass)
    test_pass = $ReleasePass; failure_code = $FailureCode; primary_failure_detail = $PrimaryFailureDetail
    failed_stage = $PrimaryFailureStage; failure_category = $PrimaryFailureCategory
    compose = [ordered]@{
      sha256_before = $ComposeSHA256Before; sha256_after_start = $ComposeSHA256AfterStart
      sha256_after_cleanup = $ComposeSHA256AfterCleanup; unchanged = $ComposeUnchanged
    }
    provenance = [ordered]@{
      runner = [ordered]@{
        sha256_before = $RunnerSHA256Before; sha256_after_cleanup = $RunnerSHA256AfterCleanup
        unchanged = $RunnerUnchanged; path_kind = "repository_relative"; path = "scripts/test-ui17-disposable-intelligence.ps1"
      }
      compose = [ordered]@{
        sha256 = $ComposeSHA256Before; path_kind = "repository_relative"
        path = "deployments/templates/compose/e2m-ui17-disposable-intelligence.compose.yml"
      }
      build_input = [ordered]@{
        before = $BuildInputManifestBefore; after_start = $BuildInputManifestAfterStart
        after_cleanup = $BuildInputManifestAfterCleanup; unchanged = $BuildInputsUnchanged
        go_proxy_policy = "proxy_with_direct_fallback"; go_proxy = $ExpectedGoProxy
      }
      built_images_bound = $BuiltImagesProvenanceBound; built_images = @($BuiltImageProvenance)
    }
    images_verified = $ImagesVerified; images = @($ImageEvidence); timeline = @($Timeline)
    protected_stack_before = @($ProtectedBefore); protected_stack_after = @($ProtectedAfter)
    protected_stack_unchanged = $ProtectedUnchanged
    disposable_cleanup = $CleanupEvidence
    runtime_directory_removed = $RuntimeRemoved
    environment_restored = $EnvironmentRestored
    acceptance = $RunEvidence
  }
  $Raw = $Evidence | ConvertTo-Json -Depth 30
  Assert-NoKnownSensitiveValue $Raw "redacted evidence"
  $SafetyRaw = $Raw.Replace(('"' + $ExpectedGoProxy + '"'), '"pinned-goproxy"')
  Assert-True ($SafetyRaw -notmatch '(?i)https?://|[A-Z]:\\|/var/lib/e2m-agent|docker[_-]?environment') "evidence contained a URL other than the pinned GOPROXY, an absolute runtime path, or Docker environment"
  [IO.File]::WriteAllText($EvidenceFile, $Raw, [Text.UTF8Encoding]::new($false))
  return $Evidence
}

try {
  Set-DiagnosticStage "preflight"
  Assert-OutboxProbeParser
  Assert-ExactProject
  Assert-RuntimePath
  if ($ProjectName -eq "e2m-real-gateways") { throw "protected project name rejected" }
  if ($null -eq (Get-Command docker -ErrorAction SilentlyContinue)) { throw "docker is required" }
  $DockerVersionArguments = @("compose", "version")
  & docker @DockerVersionArguments
  if ($LASTEXITCODE -ne 0) { throw "docker compose version failed: exit_code=$LASTEXITCODE" }
  $BuildInputManifestBefore = Get-BuildInputManifest
  $ComposeSHA256Before = Get-ComposeSHA256
  Assert-True ($ComposeSHA256Before -match '^[0-9a-f]{64}$') "initial Compose SHA256 was invalid"
  Assert-ComposeImagePins
  $ProtectedBefore = @(Get-ProtectedStackSnapshot)
  $ProtectedBeforeCaptured = $true
  Add-TimelineEvent "protected_stack_captured"

  New-Item -ItemType Directory -Path $RuntimeDir | Out-Null
  New-Item -ItemType Directory -Force -Path $EvidenceRunDir | Out-Null
  @{ schema = 1; project = $ProjectName; compose_file = $ComposeFile; runtime_dir = $RuntimeDir } |
    ConvertTo-Json -Compress | Set-Content -NoNewline -Encoding utf8 -LiteralPath $MarkerFile
  $MarkerCreated = $true
  Assert-RuntimePath

  $EnrollmentFiles = @{}
  foreach ($Name in @("owned", "external-a", "external-b")) {
    $Path = Join-Path $RuntimeDir "$Name/enrollment.token"
    Write-PrivateTextFile $Path ""
    $EnrollmentFiles[$Name] = $Path
  }
  $Ports = @(Get-UniqueFreePorts 7)
  Assert-True ($Ports.Count -eq 7 -and @($Ports | Select-Object -Unique).Count -eq 7) "failed to allocate seven distinct loopback ports"
  $PortMap = @{
    Core = $Ports[0]; OwnedSub2API = $Ports[1]; ExternalASub2API = $Ports[2]; ExternalBSub2API = $Ports[3]
    OwnedConnector = $Ports[4]; ExternalAConnector = $Ports[5]; ExternalBConnector = $Ports[6]
  }
  $InitialEnvironment = @{
    UI17_CORE_PORT = [string]$PortMap.Core
    UI17_OWNED_SUB2API_PORT = [string]$PortMap.OwnedSub2API
    UI17_EXTERNAL_A_SUB2API_PORT = [string]$PortMap.ExternalASub2API
    UI17_EXTERNAL_B_SUB2API_PORT = [string]$PortMap.ExternalBSub2API
    UI17_OWNED_CONNECTOR_PORT = [string]$PortMap.OwnedConnector
    UI17_EXTERNAL_A_CONNECTOR_PORT = [string]$PortMap.ExternalAConnector
    UI17_EXTERNAL_B_CONNECTOR_PORT = [string]$PortMap.ExternalBConnector
    UI17_OWNED_CONNECTOR_ID = "pending-owned"; UI17_EXTERNAL_A_CONNECTOR_ID = "pending-external-a"; UI17_EXTERNAL_B_CONNECTOR_ID = "pending-external-b"
    UI17_OWNED_INSTANCE_ID = "pending-owned"; UI17_EXTERNAL_A_INSTANCE_ID = "pending-external-a"; UI17_EXTERNAL_B_INSTANCE_ID = "pending-external-b"
    UI17_OWNED_ENROLLMENT_FILE = $EnrollmentFiles["owned"]
    UI17_EXTERNAL_A_ENROLLMENT_FILE = $EnrollmentFiles["external-a"]
    UI17_EXTERNAL_B_ENROLLMENT_FILE = $EnrollmentFiles["external-b"]
  }
  foreach ($Entry in $InitialEnvironment.GetEnumerator()) { [Environment]::SetEnvironmentVariable($Entry.Key, $Entry.Value, "Process") }

  Write-Step "Validating and starting isolated 14-service project $ProjectName"
  Set-DiagnosticStage "compose_start"
  Invoke-Compose config --quiet
  $StackMayExist = $true
  $BaseServices = @(
    "core-postgres", "e2m-core",
    "owned-postgres", "owned-redis", "owned-sub2api",
    "external-a-postgres", "external-a-redis", "external-a-sub2api",
    "external-b-postgres", "external-b-redis", "external-b-sub2api"
  )
  Invoke-Compose up --build --detach @BaseServices
  Assert-ComposeProjectLabels $BaseServices
  $CoreBaseUrl = "http://127.0.0.1:$($PortMap.Core)"
  Wait-Http "$CoreBaseUrl/healthz" "E2M Core"
  Wait-Http "http://127.0.0.1:$($PortMap.OwnedSub2API)/health" "owned Sub2API"
  Wait-Http "http://127.0.0.1:$($PortMap.ExternalASub2API)/health" "external-a Sub2API"
  Wait-Http "http://127.0.0.1:$($PortMap.ExternalBSub2API)/health" "external-b Sub2API"

  $Stations = @(
    Initialize-Sub2APIStation "owned" "http://127.0.0.1:$($PortMap.OwnedSub2API)" 141.25 0.72 0.68 0.0000031 0.0000152
    Initialize-Sub2APIStation "external-a" "http://127.0.0.1:$($PortMap.ExternalASub2API)" 84.50 0.83 0.79 0.0000028 0.0000140
    Initialize-Sub2APIStation "external-b" "http://127.0.0.1:$($PortMap.ExternalBSub2API)" 260.00 1.12 1.05 0.0000036 0.0000170
  )
  foreach ($GatewayUrl in @("http://owned-sub2api:8080", "http://external-a-sub2api:8080", "http://external-b-sub2api:8080")) {
    Add-CoreOnlySensitiveValue $GatewayUrl
  }

  $CoreContext = New-CoreFixture $CoreBaseUrl
  foreach ($Spec in $CoreContext.Specs) {
    $Prefix = $Spec.Name.ToUpperInvariant().Replace("-", "_")
    [Environment]::SetEnvironmentVariable("UI17_${Prefix}_CONNECTOR_ID", $Spec.ConnectorID, "Process")
    [Environment]::SetEnvironmentVariable("UI17_${Prefix}_INSTANCE_ID", $Spec.InstanceID, "Process")
    Write-PrivateTextFile $EnrollmentFiles[$Spec.Name] $Spec.EnrollmentToken
  }
  Invoke-Compose up --build --force-recreate --detach connector-owned connector-external-a connector-external-b
  Assert-ComposeProjectLabels $ExpectedServices
  $ComposeSHA256AfterStart = Get-ComposeSHA256
  Assert-True ($ComposeSHA256AfterStart -eq $ComposeSHA256Before) "Compose file changed between initial validation and full stack startup"
  $BuildInputManifestAfterStart = Get-BuildInputManifest
  Assert-BuildInputManifestEqual $BuildInputManifestAfterStart $BuildInputManifestBefore "post-start"
  $ImageEvidence = @(Get-DisposableImageEvidence)
  $ImagesVerified = $true
  $BuiltImageProvenance = @(Get-BuiltImageProvenance $ImageEvidence)
  $BuiltImagesProvenanceBound = $true
  Add-TimelineEvent "disposable_images_verified"

  $ConnectorInputs = @(
    @{ Name = "owned"; Port = $PortMap.OwnedConnector; Gateway = "http://owned-sub2api:8080"; Mode = "owned" }
    @{ Name = "external-a"; Port = $PortMap.ExternalAConnector; Gateway = "http://external-a-sub2api:8080"; Mode = "external" }
    @{ Name = "external-b"; Port = $PortMap.ExternalBConnector; Gateway = "http://external-b-sub2api:8080"; Mode = "external" }
  )
  Set-DiagnosticStage "connector_setup"
  $Connectors = @()
  foreach ($Input in $ConnectorInputs) {
    $Spec = @($CoreContext.Specs | Where-Object Name -eq $Input.Name)[0]
    $Station = @($Stations | Where-Object Name -eq $Input.Name)[0]
    $Connector = Initialize-ConnectorSource $Spec $Station $Input.Port $Input.Gateway $Input.Mode
    Write-PrivateTextFile $EnrollmentFiles[$Input.Name] ""
    $Connectors += $Connector
  }
  # Rotate every administrator key after its formal /test. Successful
  # collection below therefore proves ordinary user bearer credentials alone
  # drive intelligence reads. Owned source creation never contains a URL or
  # x-api-key; external sources retain those only for explicit reachability.
  Set-DiagnosticStage "credential_boundary"
  foreach ($Station in $Stations) { Rotate-Sub2APIAdminKey $Station }
  foreach ($Connector in $Connectors) { [void](Invoke-ConnectorCollect $Connector "succeeded") }
  Add-TimelineEvent "credential_boundary_proved"

  [void](Assert-CoreIntelligence $CoreContext $CoreBaseUrl)
  Assert-PostgresEvidence $CoreContext.OwnerID
  $OwnedConnector = @($Connectors | Where-Object Name -eq "owned")[0]
  $OutboxEvidence = Invoke-DurableOutboxDrill $CoreContext $CoreBaseUrl $OwnedConnector
  Add-TimelineEvent "durable_outbox_replayed"
  Set-DiagnosticStage "failure_isolation_recovery"
  Invoke-FailureIsolationAndRecovery $CoreContext $CoreBaseUrl $Connectors $PortMap.ExternalBSub2API
  Add-TimelineEvent "failure_stale_recovery_proved"
  $ExternalAStation = @($Stations | Where-Object Name -eq "external-a")[0]
  $ExternalAConnector = @($Connectors | Where-Object Name -eq "external-a")[0]
  Set-DiagnosticStage "confirmed_change"
  $ChangeEvidence = Invoke-ConfirmedChangeDrill $CoreContext $CoreBaseUrl $ExternalAStation $ExternalAConnector
  Add-TimelineEvent "real_change_and_removal_proved"
  [void](Assert-CoreIntelligence $CoreContext $CoreBaseUrl)
  Assert-PostgresEvidence $CoreContext.OwnerID
  Set-DiagnosticStage "final_browser_snapshot"
  $BrowserEvidence = Get-CoreBrowserSnapshot $CoreContext $CoreBaseUrl
  Assert-True ($BrowserEvidence.ChangeEvidence -ne $null) "complete browser pass did not exercise change evidence"
  Set-DiagnosticStage "security_scan"
  $SecurityEvidence = Assert-SensitiveBoundaries $CoreContext.OwnerID
  Add-TimelineEvent "security_scan_passed"

  $RunEvidence = [ordered]@{
    real_sub2api_instances = 3
    source_modes = [ordered]@{ owned = 1; external = 2 }
    upstream_contract = [ordered]@{ version = "0.1.164"; pricing = "exposed"; site_pricing = "not_exposed"; mapped_per_tokens = 1 }
    finalized_sources = 3; failure_isolation = "passed"; stale_transition = "passed"
    recovery = "passed"; confirmed_change = $ChangeEvidence; durable_outbox = $OutboxEvidence
    credential_boundary = [ordered]@{ formal_tests = 3; administrator_keys_rotated = 3; bearer_only_collects = 3 }
    browser = [ordered]@{ fact_version = $BrowserEvidence.FactVersion; source_count = $BrowserEvidence.Sources.Count; rate_count = $BrowserEvidence.Rates.Count; change_count = $BrowserEvidence.Changes.Count; pages = $BrowserEvidence.Pages; evidence_kinds = @("wallet", "offer", "change") }
    security = $SecurityEvidence
  }
  $BusinessPassed = $true
} catch {
  $PrimaryFailure = $_
  $PrimaryFailureStage = $CurrentStage
  $PrimaryFailureCategory = Get-SafeFailureCategory $_
  Write-Warning "UI-17 acceptance diagnostic: stage=$PrimaryFailureStage category=$PrimaryFailureCategory"
} finally {
  if ($MarkerCreated) {
    try {
      Assert-ExactProject
      Assert-RuntimePath
      if ($StackMayExist) {
        Remove-DisposableStack
        $CleanupEvidence = Assert-DisposableProjectRemoved
        $DisposableRemoved = $true
        Add-TimelineEvent "disposable_stack_removed"
      } else {
        $CleanupEvidence = [ordered]@{ containers = 0; volumes = 0; networks = 0; verified_empty = $true }
        $DisposableRemoved = $true
      }
    } catch {
      $CleanupFailure = $_
    }
    if ($DisposableRemoved) {
      try {
        Assert-RuntimePath
        Remove-Item -LiteralPath $RuntimeDir -Recurse -Force
        Assert-True (-not (Test-Path -LiteralPath $RuntimeDir)) "runtime directory remained after exact deletion"
        $RuntimeRemoved = $true
      } catch {
        if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
      }
    }
  } else {
    $RuntimeRemoved = -not (Test-Path -LiteralPath $RuntimeDir)
  }
  try {
    foreach ($Name in $EnvironmentNames) {
      [Environment]::SetEnvironmentVariable($Name, $PreviousEnvironment[$Name], "Process")
    }
    foreach ($Name in $EnvironmentNames) {
      Assert-True ([Environment]::GetEnvironmentVariable($Name, "Process") -eq $PreviousEnvironment[$Name]) "process environment was not restored for $Name"
    }
    $EnvironmentRestored = $true
  } catch {
    if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
  }
  try {
    if ($ProtectedBeforeCaptured) {
      $ProtectedAfter = @(Get-ProtectedStackSnapshot)
      Assert-ProtectedStackUnchanged $ProtectedBefore $ProtectedAfter
      $ProtectedUnchanged = $true
      Add-TimelineEvent "protected_stack_unchanged"
    }
  } catch {
    if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
  }
  try {
    $ComposeSHA256AfterCleanup = Get-ComposeSHA256
    Assert-True ($ComposeSHA256Before -match '^[0-9a-f]{64}$') "initial Compose SHA256 was not captured"
    Assert-True ($ComposeSHA256AfterStart -match '^[0-9a-f]{64}$') "post-start Compose SHA256 was not captured"
    Assert-True ($ComposeSHA256AfterCleanup -eq $ComposeSHA256Before -and $ComposeSHA256AfterStart -eq $ComposeSHA256Before) "Compose file changed during disposable acceptance"
    $ComposeUnchanged = $true
    Add-TimelineEvent "compose_file_unchanged"
  } catch {
    if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
  }
  try {
    $RunnerSHA256AfterCleanup = (Get-FileHash -Algorithm SHA256 -LiteralPath $RunnerFile).Hash.ToLowerInvariant()
    Assert-True ($RunnerSHA256Before -match '^[0-9a-f]{64}$' -and $RunnerSHA256AfterCleanup -match '^[0-9a-f]{64}$') "runner SHA256 evidence was incomplete"
    Assert-True ($RunnerSHA256AfterCleanup -eq $RunnerSHA256Before) "runner changed during disposable acceptance"
    $RunnerUnchanged = $true
    Add-TimelineEvent "runner_file_unchanged"
  } catch {
    if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
  }
  try {
    $BuildInputManifestAfterCleanup = Get-BuildInputManifest
    Assert-BuildInputManifestEqual $BuildInputManifestAfterStart $BuildInputManifestBefore "post-start"
    Assert-BuildInputManifestEqual $BuildInputManifestAfterCleanup $BuildInputManifestBefore "post-cleanup"
    $BuildInputsUnchanged = $true
    Add-TimelineEvent "build_inputs_unchanged"
  } catch {
    if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
  }
  try {
    $FailureCode = ""
    if ($null -ne $PrimaryFailure) { $FailureCode = "acceptance_failed" }
    elseif ($null -ne $CleanupFailure) { $FailureCode = "finalization_failed" }
    $PrimaryFailureDetail = Get-SanitizedPrimaryFailureDetail $PrimaryFailure
    $Succeeded = $BusinessPassed -and $DisposableRemoved -and $RuntimeRemoved -and $EnvironmentRestored -and
      $ProtectedUnchanged -and $ComposeUnchanged -and $RunnerUnchanged -and $BuildInputsUnchanged -and
      $ImagesVerified -and $BuiltImagesProvenanceBound -and
      $null -eq $PrimaryFailure -and $null -eq $CleanupFailure
    $FinalEvidence = Write-RedactedEvidence $Succeeded $FailureCode $PrimaryFailureDetail
  } catch {
    $FinalizationFailure = $_
    $Succeeded = $false
  }
}

if ($null -ne $PrimaryFailure) { throw $PrimaryFailure }
if ($null -ne $CleanupFailure) { throw $CleanupFailure }
if ($null -ne $FinalizationFailure) { throw $FinalizationFailure }
if (-not $Succeeded) { throw "UI-17 disposable Sub2API acceptance did not complete" }
if ($ReleaseEligible) { Write-Step "UI-17 disposable Sub2API intelligence release acceptance passed" }
else { Write-Step "UI-17 disposable Sub2API intelligence test-only acceptance passed (not release eligible)" }
Write-Host ($FinalEvidence | ConvertTo-Json -Depth 30 -Compress)
