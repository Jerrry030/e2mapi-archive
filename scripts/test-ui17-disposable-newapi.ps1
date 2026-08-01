param(
  [string]$ComposeFile = "",
  [string]$RuntimeRoot = "",
  [int]$CorePort = 28080,
  [int]$NewAPIPort = 23000,
  [int]$ConnectorPort = 28082,
  [int]$PostgresPort = 25432,
  [int]$TimeoutSeconds = 2700,
  [ValidateSet("release", "test-only")]
  [string]$ObservationProfile = "test-only",
  [switch]$SourceFrozen,
  [switch]$KeepOnFailure
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RepoRoot = [System.IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$ExpectedComposeFile = [System.IO.Path]::GetFullPath((Join-Path $RepoRoot "deployments/templates/compose/e2m-ui17-disposable-newapi.compose.yml"))
if ($ComposeFile -eq "") { $ComposeFile = $ExpectedComposeFile }
if ($RuntimeRoot -eq "") {
  $RuntimeRoot = Join-Path $RepoRoot ".tmp/ui17-disposable"
}
$ComposeFile = [System.IO.Path]::GetFullPath($ComposeFile)
$RuntimeRoot = [System.IO.Path]::GetFullPath($RuntimeRoot)
$RunnerFile = [System.IO.Path]::GetFullPath($PSCommandPath)
if ($ComposeFile -ne $ExpectedComposeFile -or -not (Test-Path -LiteralPath $ComposeFile -PathType Leaf)) {
  throw "UI-17 must use the repository's disposable NewAPI Compose file"
}
if ($TimeoutSeconds -lt 180 -or $TimeoutSeconds -gt 3600) {
  throw "TimeoutSeconds must be between 180 and 3600"
}
if ($ObservationProfile -eq "release" -and -not $SourceFrozen.IsPresent) {
  throw "release observation requires explicit -SourceFrozen acknowledgement"
}
if ($ObservationProfile -eq "test-only" -and $SourceFrozen.IsPresent) {
  throw "-SourceFrozen is only valid with -ObservationProfile release"
}
$Project = "e2m-ui17-newapi-{0}-{1}" -f (Get-Date -Format "yyyyMMddHHmmss"), ([guid]::NewGuid().ToString("N").Substring(0, 8))
$ProjectPattern = '^e2m-ui17-newapi-[0-9]{14}-[a-f0-9]{8}$'
$RuntimeDir = Join-Path $RuntimeRoot $Project
$MarkerFile = Join-Path $RuntimeDir ".e2m-ui17-project.json"
$EvidenceFile = Join-Path $RuntimeDir "evidence.json"
$CoreImage = "e2m-ui17-core:$Project"
$ConnectorImage = "e2m-ui17-connector:$Project"
$MockOpenAIImage = "e2m-ui17-mock-openai:$Project"
$BusinessPassed = $false
$Succeeded = $false
$StackMayExist = $false
$SourceFrozenAcknowledged = [bool]$SourceFrozen.IsPresent
$ReleaseEligible = $ObservationProfile -eq "release" -and $SourceFrozenAcknowledged
$PrimaryFailure = $null
$CleanupFailure = $null
$FinalizationFailure = $null
$ProtectedBefore = @()
$ProtectedAfter = @()
$ProtectedBeforeCaptured = $false
$ProtectedUnchanged = $false
$ComposeSHA256Before = ""
$ComposeSHA256AfterStart = ""
$ComposeSHA256AfterRestart = ""
$ComposeSHA256AfterCleanup = ""
$ComposeUnchanged = $false
$RunnerSHA256Before = (Get-FileHash -Algorithm SHA256 -LiteralPath $RunnerFile).Hash.ToLowerInvariant()
$RunnerSHA256AfterCleanup = ""
$RunnerUnchanged = $false
$BuildInputBefore = $null
$BuildInputAfterStart = $null
$BuildInputAfterRestart = $null
$BuildInputAfterCleanup = $null
$BuildInputUnchanged = $false
$ImageEvidence = @()
$RestartImageEvidence = @()
$ImageInspectionRuns = @()
$ImagesVerified = $false
$BuiltImageProvenance = @()
$BuiltImagesProvenanceBound = $false
$DisposableRemoved = $false
$CleanupEvidence = $null
$RuntimeRemoved = $false
$EnvironmentRestored = $false
$Context = $null
$ScenarioA = $null
$ScenarioB = $null
$ScenarioC = $null
$ProtocolV3Heartbeat = $null
$ProtocolV3Drills = $null
$UncertainGatewayProof = $null
$ExecutionPolicyID = ""
$ConnectorPausedByRunner = $false
$StartupClaimGateInstalled = $false
$script:newAPI = $null
$FinalEvidence = $null
$PersistentEvidencePath = Join-Path (Join-Path $RepoRoot ".tmp/ui17-evidence") "$Project-newapi-redacted-evidence.json"
$EnvironmentNames = @(
  "E2M_UI17_RUNTIME_DIR", "E2M_UI17_CORE_PORT", "E2M_UI17_NEWAPI_PORT",
  "E2M_UI17_CONNECTOR_PORT", "E2M_UI17_POSTGRES_PORT", "E2M_UI17_CORE_IMAGE",
  "E2M_UI17_CONNECTOR_IMAGE", "E2M_UI17_MOCK_OPENAI_IMAGE", "E2M_UI17_HEALTH_METRICS_INTERVAL",
  "E2M_UI17_CONNECTOR_ID", "E2M_UI17_INSTANCE_ID",
  "E2M_UI17_ROLLOUT_OBSERVATION", "E2M_UI17_ROLLOUT_RUNNER_INTERVAL",
  "E2M_UI17_ROLLOUT_WORKER_INTERVAL"
)
$PreviousEnvironment = @{}
foreach ($Name in $EnvironmentNames) {
  $PreviousEnvironment[$Name] = [Environment]::GetEnvironmentVariable($Name, "Process")
}
$ExpectedServices = @("core-postgres", "newapi-postgres", "newapi-redis", "mock-openai", "newapi", "e2m-core", "connector-newapi")
$PostgresImage = "postgres:16-alpine@sha256:e013e867e712fec275706a6c51c966f0bb0c93cfa8f51000f85a15f9865a28cb"
$RedisImage = "redis:7-alpine@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99"
$NewAPIImage = "calciumion/new-api:latest@sha256:e8c18fdf4a94cc49979fe210dd39ca7643b03431675fd7de1097285750d3e46c"
$NodeImage = "node:24-alpine@sha256:a0b9bf06e4e6193cf7a0f58816cc935ff8c2a908f81e6f1a95432d679c54fbfd"
$ExpectedPinnedImages = @{
  "core-postgres" = @{ config_image = $PostgresImage; repo_digest = "postgres@sha256:e013e867e712fec275706a6c51c966f0bb0c93cfa8f51000f85a15f9865a28cb" }
  "newapi-postgres" = @{ config_image = $PostgresImage; repo_digest = "postgres@sha256:e013e867e712fec275706a6c51c966f0bb0c93cfa8f51000f85a15f9865a28cb" }
  "newapi-redis" = @{ config_image = $RedisImage; repo_digest = "redis@sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99" }
  "newapi" = @{ config_image = $NewAPIImage; repo_digest = "calciumion/new-api@sha256:e8c18fdf4a94cc49979fe210dd39ca7643b03431675fd7de1097285750d3e46c" }
}
$ExpectedBuiltImages = @{ "e2m-core" = $CoreImage; "connector-newapi" = $ConnectorImage; "mock-openai" = $MockOpenAIImage }
$ExpectedBuiltServices = @("connector-newapi", "e2m-core", "mock-openai")
$FixedGoProxy = "https://goproxy.cn,direct"
$BuildInputRoots = @(
  ".dockerignore", "go.work", "go.work.sum", "app/e2m-core", "app/e2m-agent",
  "packages/e2m-contracts", "web/console"
)

function Write-Step { param([string]$Message) Write-Host "==> $Message" }
function Invoke-External {
  param([string]$Command, [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
  & $Command @Arguments
  if ($LASTEXITCODE -ne 0) { throw "external command failed with exit code $LASTEXITCODE" }
}
function Invoke-Compose {
  $Arguments = @($args)
  # Pass native arguments as one array. Routing `-p` through Invoke-External's
  # PowerShell parameter binder can consume it as an abbreviated common
  # parameter and silently fall back to Compose's directory-derived project.
  $composeArguments = @("compose", "-p", $Project, "-f", $ComposeFile) + @($Arguments)
  # Keep human-readable Compose progress visible without returning it through
  # the PowerShell success pipeline. Callers that return typed objects must not
  # be widened into Object[] by incidental native stdout.
  & docker @composeArguments | Out-Host
  if ($LASTEXITCODE -ne 0) { throw "compose command failed with exit code $LASTEXITCODE" }
}
function Invoke-ComposeCapture {
  $Arguments = @($args)
  $composeArguments = @("compose", "-p", $Project, "-f", $ComposeFile) + @($Arguments)
  $raw = & docker @composeArguments 2>$null
  if ($LASTEXITCODE -ne 0) { throw "compose command failed with exit code $LASTEXITCODE" }
  return ($raw -join "`n").Trim()
}
function Invoke-Postgres {
  param([string]$Sql, [string[]]$Variables = @())
  # Feed SQL over stdin. Windows native argument binding can remove embedded
  # double quotes from --command values, corrupting JSON literals and quoted
  # identifiers such as "window" before PostgreSQL sees them.
  # Mark this controlled fixture writer at session scope before caller SQL. The
  # false set_config scope survives any BEGIN/COMMIT owned by the caller, while
  # \g redirects the marker result so it cannot enter evidence or parsed output.
  $sessionSql = @(
    "\set VERBOSITY verbose"
    # PowerShell 5.1 promotes native stderr NOTICE records to terminating
    # errors under ErrorActionPreference=Stop. Suppress informational NOTICE
    # only; ON_ERROR_STOP and PostgreSQL ERROR output remain fail-closed.
    "SET client_min_messages = warning"
    "\g /dev/null"
    "SELECT set_config('e2m.operational_counter_writer','incremental-v1',false)"
    "\g /dev/null"
    $Sql
  ) -join "`n"
  $composeArguments = @(
    "compose", "-p", $Project, "-f", $ComposeFile,
    "exec", "--no-TTY", "core-postgres", "psql", "--no-psqlrc",
    "--set", "ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--quiet"
  )
  foreach ($variable in $Variables) { $composeArguments += @("--set", $variable) }
  $composeArguments += @("--username", "e2m_ui17", "--dbname", "e2m_ui17")
  $raw = $sessionSql | & docker @composeArguments 2>&1
  if ($LASTEXITCODE -ne 0) {
    $detail = (($raw | ForEach-Object { [string]$_ }) -join "`n").Trim()
    throw "postgres command failed: $detail"
  }
  return (($raw | ForEach-Object { [string]$_ }) -join "`n").Trim()
}
function ConvertTo-SqlLiteral {
  param([AllowEmptyString()][string]$Value)
  return "'" + $Value.Replace("'", "''") + "'"
}
function Invoke-NewAPIPostgres {
  param([string]$Sql)
  # NewAPI has no Core 0069 operational-counter tables, so it intentionally
  # does not use the Core writer marker. Keep one controlled psql session and
  # quiet informational command tags without suppressing errors or row output.
  $newAPISessionSql = @(
    "\set VERBOSITY verbose"
    $Sql
  ) -join "`n"
  $composeArguments = @(
    "compose", "-p", $Project, "-f", $ComposeFile,
    "exec", "--no-TTY", "newapi-postgres", "psql", "--no-psqlrc",
    "--set", "ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--quiet",
    "--username", "newapi_ui17", "--dbname", "newapi_ui17"
  )
  $raw = $newAPISessionSql | & docker @composeArguments 2>&1
  if ($LASTEXITCODE -ne 0) {
    $detail = (($raw | ForEach-Object { [string]$_ }) -join "`n").Trim()
    throw "NewAPI postgres command failed: $detail"
  }
  return (($raw | ForEach-Object { [string]$_ }) -join "`n").Trim()
}
function Invoke-NewAPIPostgresClock {
  param([string]$Sql)
  if ([string]::IsNullOrWhiteSpace($Sql)) { throw "NewAPI observer SQL was empty" }
  if ($Sql -notmatch ';\s*$') { throw "NewAPI observer SQL must be a complete statement" }
  # Serialize observer reads with the trigger in the same transaction. Redirect
  # transaction and lock results explicitly; only caller SQL may reach parsers.
  $clockSql = @(
    "BEGIN"
    "\g /dev/null"
    "SELECT pg_advisory_xact_lock(913747201)"
    "\g /dev/null"
    $Sql
    "COMMIT"
    "\g /dev/null"
  ) -join "`n"
  return Invoke-NewAPIPostgres $clockSql
}
function Get-Sha256Hex {
  param([string]$Value)
  $sha = [Security.Cryptography.SHA256]::Create()
  try { return ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Value)))).Replace("-", "").ToLowerInvariant() }
  finally { $sha.Dispose() }
}
function Get-BuildInputManifest {
  $filesByPath = [Collections.Generic.Dictionary[string, object]]::new([StringComparer]::Ordinal)
  $repositoryPrefix = $RepoRoot.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
  foreach ($relativeRoot in $BuildInputRoots) {
    $fullRoot = [IO.Path]::GetFullPath((Join-Path $RepoRoot $relativeRoot))
    Assert-True ($fullRoot -eq $RepoRoot -or $fullRoot.StartsWith($RepoRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) "build input root escaped repository"
    if (Test-Path -LiteralPath $fullRoot -PathType Leaf) {
      $candidates = @(Get-Item -LiteralPath $fullRoot -Force)
    } elseif (Test-Path -LiteralPath $fullRoot -PathType Container) {
      $candidates = @(Get-ChildItem -LiteralPath $fullRoot -Recurse -Force -File)
    } elseif ($relativeRoot -eq "go.work.sum") {
      continue
    } else {
      throw "required build input was missing: $relativeRoot"
    }
    foreach ($file in $candidates) {
      $fullFilePath = [IO.Path]::GetFullPath($file.FullName)
      Assert-True ($fullFilePath.StartsWith($repositoryPrefix, [StringComparison]::OrdinalIgnoreCase)) "build input file escaped repository"
      $relative = $fullFilePath.Substring($repositoryPrefix.Length).Replace('\', '/')
      if ($relative -match '(^|/)(\.git|\.agents|\.codex|\.tmp|tmp|node_modules|dist|build|coverage)(/|$)' -or
          $relative -match '(^|/)\.DS_Store$' -or
          $relative -match '\.(exe|test|log|tsbuildinfo)$') {
        continue
      }
      $filesByPath[$relative] = $file
    }
  }
  [string[]]$sortedPaths = @($filesByPath.Keys)
  [Array]::Sort($sortedPaths, [StringComparer]::Ordinal)
  $entries = @($sortedPaths | ForEach-Object {
    $relative = $_
    $file = $filesByPath[$relative]
    $stream = [IO.File]::Open($file.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
      $fileSize = $stream.Length
      $contentSHA256 = ([BitConverter]::ToString($sha256.ComputeHash($stream))).Replace("-", "").ToLowerInvariant()
    } finally {
      $sha256.Dispose()
      $stream.Dispose()
    }
    "$relative`t$fileSize`t$contentSHA256"
  })
  Assert-True ($entries.Count -gt 0) "build input manifest was empty"
  $schemaHeader = "e2m-build-input-manifest-v1`tpath-size-content-sha256"
  $canonical = $schemaHeader + "`n" + ($entries -join "`n") + "`n"
  return [ordered]@{
    schema = "e2m-build-input-manifest-v1"
    canonical_format = "schema-header LF; path TAB size TAB content_sha256 LF; ordinal path order"
    file_count = $entries.Count
    canonical_sha256 = Get-Sha256Hex $canonical; roots = @($BuildInputRoots)
    exclusions = @(".git", ".agents", ".codex", ".tmp", "tmp", ".DS_Store", "node_modules", "dist", "build", "coverage", "*.exe", "*.test", "*.log", "*.tsbuildinfo")
  }
}
function Assert-BuildInputManifestEqual {
  param($Actual, $Expected, [string]$Label)
  Assert-True ($null -ne $Actual -and $null -ne $Expected) "$Label build input manifest was missing"
  Assert-Equal ([string]$Actual.schema) ([string]$Expected.schema) "$Label build input schema"
  Assert-Equal ([int]$Actual.file_count) ([int]$Expected.file_count) "$Label build input file count"
  Assert-Equal ([string]$Actual.canonical_sha256) ([string]$Expected.canonical_sha256) "$Label build input digest"
}
function New-PublicRoleWeightProof {
  param([int]$From, [int]$To, [int]$Unrelated)
  Assert-Equal ($From + $To + $Unrelated) 100 "public role weight proof total"
  foreach ($weight in @($From, $To, $Unrelated)) {
    Assert-True ($weight -ge 0 -and $weight -le 100) "public role weight proof contained an invalid weight"
  }
  $canonicalPayload = "from=$From`nto=$To`nunrelated=$Unrelated`n"
  return [ordered]@{
    schema = "e2m-public-role-weight-set-v1"
    hash_algorithm = "sha256"
    canonical_format = "from={integer}\nto={integer}\nunrelated={integer}\n"
    canonical_payload = $canonicalPayload
    canonical_sha256 = Get-Sha256Hex $canonicalPayload
    rows = @(
      [ordered]@{ role = "from"; weight = $From }
      [ordered]@{ role = "to"; weight = $To }
      [ordered]@{ role = "unrelated"; weight = $Unrelated }
    )
    total_weight = 100
    identity_scope = "public_roles_only"
  }
}
function ConvertFrom-UtcTimestamp {
  param($Value, [string]$Label)
  $parsed = [DateTimeOffset]::MinValue
  if (-not [DateTimeOffset]::TryParse([string]$Value, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::AssumeUniversal, [ref]$parsed)) {
    throw "$Label omitted a valid UTC timestamp"
  }
  return $parsed.ToUniversalTime()
}
function Assert-True { param([bool]$Condition, [string]$Message) if (-not $Condition) { throw $Message } }
function Get-ComposeContainerIDs {
  $lines = (Invoke-ComposeCapture ps --all --quiet @args) -split '[\r\n]+'
  foreach ($line in $lines) {
    $value = $line.Trim()
    if ($value -match '^[0-9a-f]{12,64}$') { Write-Output $value }
  }
}
function Assert-ComposeProjectLabels {
  param([string[]]$ExpectedServiceNames)
  $containerIDs = @(Get-ComposeContainerIDs)
  $expected = @($ExpectedServiceNames | Sort-Object -Unique)
  Assert-True ($expected.Count -eq $ExpectedServiceNames.Count) "expected Compose service list contained duplicates"
  Assert-True ($containerIDs.Count -eq $expected.Count) "Compose project did not contain exactly $($expected.Count) containers"
  $seen = @{}
  foreach ($containerID in $containerIDs) {
    $raw = & docker inspect $containerID 2>$null
    if ($LASTEXITCODE -ne 0) { throw "disposable Compose project inspection failed" }
    $item = @(($raw -join "`n") | ConvertFrom-Json)[0]
    $projectLabel = [string]$item.Config.Labels.'com.docker.compose.project'
    $serviceLabel = [string]$item.Config.Labels.'com.docker.compose.service'
    Assert-True ($projectLabel -eq $Project) "disposable container had an unexpected project label"
    Assert-True ($serviceLabel -in $expected) "disposable container had an unexpected service label"
    Assert-True (-not $seen.ContainsKey($serviceLabel)) "disposable Compose service was duplicated"
    $seen[$serviceLabel] = $true
  }
  Assert-True ($seen.Count -eq $expected.Count) "disposable Compose project omitted an expected service"
}
function Get-ComposeSHA256 { return (Get-FileHash -Algorithm SHA256 -LiteralPath $ComposeFile).Hash.ToLowerInvariant() }
function Get-ProtectedStackSnapshot {
  foreach ($name in @(
    "e2m-real-gateways-postgres-1", "e2m-real-gateways-connector-sub2api-1",
    "e2m-real-gateways-connector-newapi-1", "e2m-real-gateways-connector-cpa-1"
  )) {
    $raw = & docker inspect $name 2>$null
    if ($LASTEXITCODE -ne 0) { throw "protected container was not inspectable: $name" }
    $item = @(($raw -join "`n") | ConvertFrom-Json)[0]
    [pscustomobject]@{
      name = $name; id = [string]$item.Id; started_at = [string]$item.State.StartedAt
      status = [string]$item.State.Status; image = [string]$item.Image
    }
  }
}
function Assert-ProtectedStackUnchanged {
  param([object[]]$Before, [object[]]$After)
  Assert-True ($Before.Count -eq 4 -and $After.Count -eq 4) "protected stack snapshot was incomplete"
  foreach ($expected in $Before) {
    $actual = @($After | Where-Object { $_.name -eq $expected.name })
    Assert-True ($actual.Count -eq 1) "protected stack after-snapshot omitted a container"
    foreach ($field in @("id", "started_at", "status", "image")) {
      Assert-True ($actual[0].$field -eq $expected.$field) "protected stack changed field $field for $($expected.name)"
    }
  }
}
function Assert-ComposeImagePins {
  $raw = Get-Content -Raw -LiteralPath $ComposeFile
  foreach ($expected in @($PostgresImage, $RedisImage, $NewAPIImage, $NodeImage)) {
    Assert-True ($raw.IndexOf($expected, [StringComparison]::Ordinal) -ge 0) "Compose omitted a required pinned runtime image"
  }
  foreach ($name in @("POSTGRES_IMAGE", "REDIS_IMAGE", "NEWAPI_IMAGE", "GO_IMAGE", "NODE_IMAGE", "RUNTIME_IMAGE", "GOPROXY", "E2M_UI17_ADMIN_EMAIL", "E2M_UI17_ADMIN_PASSWORD", "E2M_UI17_VAULT_KEY")) { # gitleaks:allow -- environment variable name
    Assert-True ($raw.IndexOf(('${' + $name), [StringComparison]::OrdinalIgnoreCase) -lt 0) "Compose allowed release image override $name"
  }
  $allNodeImageArguments = [regex]::Matches($raw, '(?m)^\s*NODE_IMAGE:\s*[^\r\n#]+\s*$')
  $expectedNodeImageArguments = [regex]::Matches($raw, ('(?m)^\s*NODE_IMAGE:\s*' + [regex]::Escape($NodeImage) + '\s*$'))
  Assert-True ($allNodeImageArguments.Count -eq 1 -and $expectedNodeImageArguments.Count -eq 1) "Compose did not pin the vetted Node image exactly once"
  $allGoProxyArguments = [regex]::Matches($raw, '(?m)^\s*GOPROXY:\s*[^\r\n#]+\s*$')
  $expectedGoProxyArguments = [regex]::Matches($raw, ('(?m)^\s*GOPROXY:\s*' + [regex]::Escape($FixedGoProxy) + '\s*$'))
  Assert-True ($allGoProxyArguments.Count -eq $ExpectedBuiltServices.Count -and
    $expectedGoProxyArguments.Count -eq $ExpectedBuiltServices.Count) "Compose did not pin the expected GOPROXY exactly once for every built service"
  Assert-True ($raw.IndexOf('E2M_ADMIN_EMAIL: "admin@ui17.local"', [StringComparison]::Ordinal) -ge 0) "Compose omitted its fixed Core administrator email"
  Assert-True ($raw.IndexOf('E2M_ADMIN_PASSWORD: "ui17-admin-password"', [StringComparison]::Ordinal) -ge 0) "Compose omitted its fixed Core administrator password"
}
function Get-DisposableImageEvidence {
  param(
    [string[]]$ExpectedServiceNames = $ExpectedServices,
    [string]$InspectionReason = "complete-stack"
  )
  $containerIDs = @(Get-ComposeContainerIDs)
  Assert-True ($containerIDs.Count -eq $ExpectedServiceNames.Count) "image evidence inspected the wrong container count"
  $rows = @(); $seen = @{}; $containerInspectCount = 0; $imageInspectCount = 0
  foreach ($containerID in $containerIDs) {
    $rawContainer = & docker inspect $containerID 2>$null
    if ($LASTEXITCODE -ne 0) { throw "container image inspection failed" }
    $containerInspectCount++
    $container = @(($rawContainer -join "`n") | ConvertFrom-Json)[0]
    $service = [string]$container.Config.Labels.'com.docker.compose.service'
    $projectLabel = [string]$container.Config.Labels.'com.docker.compose.project'
    Assert-True ($projectLabel -eq $Project -and $service -in $ExpectedServiceNames) "container image evidence crossed the project boundary"
    Assert-True (-not $seen.ContainsKey($service)) "container image evidence duplicated a service"
    $seen[$service] = $true
    $configImage = [string]$container.Config.Image
    $imageID = [string]$container.Image
    Assert-True ($imageID -match '^sha256:[0-9a-f]{64}$') "$service image ID was not immutable"
    $rawImage = & docker image inspect $imageID 2>$null
    if ($LASTEXITCODE -ne 0) { throw "Docker image metadata inspection failed" }
    $imageInspectCount++
    $image = @(($rawImage -join "`n") | ConvertFrom-Json)[0]
    Assert-True ([string]$image.Id -eq $imageID) "$service image identity was inconsistent"
    $repoDigests = @($image.RepoDigests | ForEach-Object { [string]$_ } | Sort-Object -Unique)
    $kind = "pinned"
    if ($ExpectedPinnedImages.ContainsKey($service)) {
      $expected = $ExpectedPinnedImages[$service]
      Assert-True ($configImage -eq [string]$expected.config_image) "$service Config.Image differed from the pinned Compose image"
      Assert-True ($repoDigests -contains [string]$expected.repo_digest) "$service omitted its exact RepoDigest"
    } else {
      $kind = "built"
      Assert-True ($ExpectedBuiltImages.ContainsKey($service)) "$service had no image identity policy"
      $expectedTag = [string]$ExpectedBuiltImages[$service]
      Assert-True ($configImage -eq $expectedTag) "$service Config.Image differed from its unique build tag"
      Assert-True (@($image.RepoTags | Where-Object { [string]$_ -eq $expectedTag }).Count -eq 1) "$service image metadata omitted its unique build tag"
    }
    $rows += [ordered]@{
      service = $service; project_label = $projectLabel; config_image = $configImage
      image_id = $imageID; inspected_image_id = [string]$image.Id; kind = $kind; repo_digests = $repoDigests
    }
  }
  Assert-True ($seen.Count -eq $ExpectedServiceNames.Count) "image evidence omitted a disposable service"
  Assert-Equal $containerInspectCount $ExpectedServiceNames.Count "container image inspection count"
  Assert-Equal $imageInspectCount $ExpectedServiceNames.Count "image metadata inspection count"
  $script:ImageInspectionRuns += [ordered]@{
    reason = $InspectionReason
    expected_service_count = $ExpectedServiceNames.Count
    unique_service_count = $seen.Count
    container_inspect_count = $containerInspectCount
    image_metadata_inspect_count = $imageInspectCount
    verified = $true
  }
  foreach ($row in @($rows | Sort-Object service)) { Write-Output $row }
}
function Get-BuiltImageProvenance {
  param([object[]]$Images)
  Assert-True ($RunnerSHA256Before -match '^[0-9a-f]{64}$') "built image provenance lacked the runner digest"
  Assert-True ($ComposeSHA256Before -match '^[0-9a-f]{64}$') "built image provenance lacked the Compose digest"
  Assert-True ($null -ne $BuildInputBefore -and [string]$BuildInputBefore.canonical_sha256 -match '^[0-9a-f]{64}$') "built image provenance lacked the build input digest"
  $builtRows = @($Images | Where-Object { [string]$_.kind -eq "built" } | Sort-Object service)
  Assert-Equal $builtRows.Count $ExpectedBuiltServices.Count "built image provenance service count"
  $expectedServices = @($ExpectedBuiltServices | Sort-Object)
  $actualServices = @($builtRows | ForEach-Object { [string]$_.service } | Sort-Object -Unique)
  Assert-Equal $actualServices.Count $expectedServices.Count "built image provenance unique service count"
  for ($index = 0; $index -lt $expectedServices.Count; $index++) {
    Assert-Equal $actualServices[$index] $expectedServices[$index] "built image provenance service identity"
  }
  $rows = @($builtRows | ForEach-Object {
    Assert-True ([string]$_.image_id -match '^sha256:[0-9a-f]{64}$' -and [string]$_.image_id -eq [string]$_.inspected_image_id) "built image provenance contained an invalid image identity"
    [ordered]@{
      service = [string]$_.service; config_image = [string]$_.config_image
      image_id = [string]$_.image_id; inspected_image_id = [string]$_.inspected_image_id
      runner_sha256 = $RunnerSHA256Before; compose_sha256 = $ComposeSHA256Before
      build_input_sha256 = [string]$BuildInputBefore.canonical_sha256
      build_input_file_count = [int]$BuildInputBefore.file_count; go_proxy = $FixedGoProxy
    }
  })
  Assert-True (@($rows | Where-Object {
    $_.runner_sha256 -ne $RunnerSHA256Before -or $_.compose_sha256 -ne $ComposeSHA256Before -or
    $_.build_input_sha256 -ne [string]$BuildInputBefore.canonical_sha256 -or
    $_.build_input_file_count -ne [int]$BuildInputBefore.file_count -or $_.go_proxy -ne $FixedGoProxy
  }).Count -eq 0) "built image provenance bindings were inconsistent"
  foreach ($row in $rows) { Write-Output $row }
}
function ConvertTo-JsonBytes { param($Value) return ,([Text.Encoding]::UTF8.GetBytes(($Value | ConvertTo-Json -Depth 30 -Compress))) }
function Get-JsonValue {
  param($Object, [string]$Name)
  if ($null -eq $Object) { return $null }
  if ($Object -is [Collections.IDictionary] -and $Object.Contains($Name)) { return $Object[$Name] }
  $property = $Object.PSObject.Properties[$Name]
  if ($null -ne $property) { return $property.Value }
  return $null
}
function Get-ImageInspectionCountTotal {
  param([object[]]$Runs, [string]$Field)
  Assert-True ($Field -in @("container_inspect_count", "image_metadata_inspect_count")) "image inspection total requested an unknown field"
  [long]$total = 0
  foreach ($run in @($Runs)) {
    $value = Get-JsonValue $run $Field
    Assert-True ($value -is [int] -and [int]$value -ge 0) "image inspection run omitted a non-negative integer $Field"
    Assert-True ([long]$value -le [long]::MaxValue - $total) "image inspection total overflowed"
    $total += [long]$value
  }
  return $total
}
function Assert-ImageInspectionCountAggregation {
  # Windows PowerShell 5.1 does not expose OrderedDictionary keys as
  # Measure-Object properties. Exercise the exact evidence representation so
  # a schema-only release failure is caught before the disposable stack starts.
  $fixture = @()
  $fixture += [ordered]@{ container_inspect_count = 6; image_metadata_inspect_count = 6 }
  $fixture += [ordered]@{ container_inspect_count = 6; image_metadata_inspect_count = 6 }
  Assert-Equal (Get-ImageInspectionCountTotal $fixture "container_inspect_count") 12 "image inspection container count aggregation self-test"
  Assert-Equal (Get-ImageInspectionCountTotal $fixture "image_metadata_inspect_count") 12 "image inspection metadata count aggregation self-test"
  $missingRejected = $false
  try {
    [void](Get-ImageInspectionCountTotal @([ordered]@{ container_inspect_count = 1 }) "image_metadata_inspect_count")
  } catch {
    $missingRejected = $true
  }
  Assert-True $missingRejected "image inspection aggregation accepted a missing field"
}
function Assert-CorePostgresSessionContract {
  $runner = Get-Content -Raw -LiteralPath $RunnerFile
  $helperStart = $runner.IndexOf('function Invoke-Postgres {', [StringComparison]::Ordinal)
  $helperEnd = $runner.IndexOf('function ConvertTo-SqlLiteral {', $helperStart, [StringComparison]::Ordinal)
  Assert-True ($helperStart -ge 0 -and $helperEnd -gt $helperStart) "Core PostgreSQL helper boundaries were invalid"
  $helper = $runner.Substring($helperStart, $helperEnd - $helperStart)
  foreach ($required in @('"--set", "ON_ERROR_STOP=1"', '"--quiet"', '2>&1', '$sessionSql | & docker', '"SET client_min_messages = warning"')) {
    Assert-True ($helper.IndexOf($required, [StringComparison]::Ordinal) -ge 0) "Core PostgreSQL helper omitted $required"
  }
  $messagesAt = $helper.IndexOf('"SET client_min_messages = warning"', [StringComparison]::Ordinal)
  $messagesDiscardAt = $helper.IndexOf('"\g /dev/null"', $messagesAt, [StringComparison]::Ordinal)
  $markerAt = $helper.IndexOf("SELECT set_config('e2m.operational_counter_writer','incremental-v1',false)", [StringComparison]::Ordinal)
  $discardAt = $helper.IndexOf('"\g /dev/null"', $markerAt, [StringComparison]::Ordinal)
  $callerAt = $helper.IndexOf('    $Sql', [StringComparison]::Ordinal)
  $pipeAt = $helper.IndexOf('$raw = $sessionSql | & docker', [StringComparison]::Ordinal)
  Assert-True ($messagesAt -ge 0 -and $messagesDiscardAt -gt $messagesAt -and $markerAt -gt $messagesDiscardAt -and
    $discardAt -gt $markerAt -and $callerAt -gt $discardAt -and $pipeAt -gt $callerAt) "Core PostgreSQL session did not set message safety, mark, redirect, run caller SQL, and pipe one session in order"
  # Model `UPDATE ... RETURNING` under tuples-only/no-align/quiet: command tags
  # are suppressed while all returned rows remain visible to the caller.
  $modeledOutput = @('channel-a', 'channel-b', 'channel-c')
  Assert-Equal $modeledOutput.Count 3 "Core PostgreSQL quiet UPDATE RETURNING row count"
  Assert-True (@($modeledOutput | Where-Object { $_ -match '^UPDATE\s+[0-9]+$' }).Count -eq 0) "Core PostgreSQL quiet output leaked a command tag"
}
function Assert-NewAPIPostgresClockContract {
  $runner = Get-Content -Raw -LiteralPath $RunnerFile
  $helperStart = $runner.IndexOf('function Invoke-NewAPIPostgres {', [StringComparison]::Ordinal)
  $clockStart = $runner.IndexOf('function Invoke-NewAPIPostgresClock {', [StringComparison]::Ordinal)
  $clockEnd = $runner.IndexOf('function Get-Sha256Hex {', [StringComparison]::Ordinal)
  Assert-True ($helperStart -ge 0 -and $clockStart -gt $helperStart -and $clockEnd -gt $clockStart) "NewAPI PostgreSQL helper boundaries were invalid"
  $helper = $runner.Substring($helperStart, $clockStart - $helperStart)
  $clock = $runner.Substring($clockStart, $clockEnd - $clockStart)
  foreach ($requiredHelper in @('"--set", "ON_ERROR_STOP=1"', '"--quiet"', '2>&1', '$newAPISessionSql | & docker')) {
    Assert-True ($helper.IndexOf($requiredHelper, [StringComparison]::Ordinal) -ge 0) "NewAPI PostgreSQL helper omitted $requiredHelper"
  }
  $beginAt = $clock.IndexOf('"BEGIN"', [StringComparison]::Ordinal)
  $lockAt = $clock.IndexOf('"SELECT pg_advisory_xact_lock(913747201)"', [StringComparison]::Ordinal)
  $callerAt = $clock.IndexOf('    $Sql', [StringComparison]::Ordinal)
  $commitAt = $clock.IndexOf('"COMMIT"', [StringComparison]::Ordinal)
  Assert-True ($beginAt -ge 0 -and $lockAt -gt $beginAt -and $callerAt -gt $lockAt -and $commitAt -gt $callerAt) "NewAPI observer clock did not serialize BEGIN, lock, caller SQL, and COMMIT"
  Assert-Equal ([regex]::Matches($clock, [regex]::Escape('"\g /dev/null"')).Count) 3 "NewAPI observer clock redirected control-statement results"
  Assert-True ($clock.IndexOf('return Invoke-NewAPIPostgres $clockSql', [StringComparison]::Ordinal) -gt $commitAt) "NewAPI observer clock did not use one PostgreSQL session"
  # Model psql's quiet/redirect contract without requiring a database: all
  # control statements are redirected, while the caller statement is not.
  $modeledClock = @(
    [pscustomobject]@{ Name = "BEGIN"; Redirected = $true }
    [pscustomobject]@{ Name = "LOCK"; Redirected = $true }
    [pscustomobject]@{ Name = "CALLER_SQL"; Redirected = $false }
    [pscustomobject]@{ Name = "COMMIT"; Redirected = $true }
  )
  $visible = @($modeledClock | Where-Object { -not $_.Redirected } | ForEach-Object { $_.Name })
  Assert-Equal $visible.Count 1 "NewAPI observer clock visible result count"
  Assert-Equal $visible[0] "CALLER_SQL" "NewAPI observer clock visible result identity"
}
function Assert-Schema6ProtocolEvidenceContract {
  $runner = Get-Content -Raw -LiteralPath $RunnerFile
  $contractStart = $runner.IndexOf("`nfunction Assert-Schema6ProtocolEvidenceContract {", [StringComparison]::Ordinal)
  $contractEnd = $runner.IndexOf("`nfunction Get-JsonArrayItems {", [StringComparison]::Ordinal)
  Assert-True ($contractStart -ge 0 -and $contractEnd -gt $contractStart) "schema-6 self-check boundaries were invalid"
  $implementation = $runner.Remove($contractStart, $contractEnd - $contractStart)
  foreach ($required in @(
    'schema = 6', 'protocol_v3 =', 'Get-ProtocolV3HeartbeatEvidence', 'Get-ProtocolV3StageTaskEvidence',
    'Invoke-ProtocolV3ConflictAndResolutionDrills', 'task_execution_conflict', 'scheduling_fence_stale',
    'confirmed_not_applied', 'connector_task.resolve_execution', 'unit_no_bypass',
    'pending,leased,executing,succeeded', 'source_frozen_acknowledged',
    'Stop-DisposableCoreForStartupClaim', 'scenario-b-arm-startup-claim-window',
    'core_stopped_before_startup_claim', 'startup_run_once_claimed_forward',
    'scenario-c-enable-health-before-workflow', 'pre_workflow_health_restart'
  )) {
    Assert-True ($implementation.IndexOf($required, [StringComparison]::Ordinal) -ge 0) "schema-6 protocol evidence contract omitted $required"
  }
  $writerStart = $runner.IndexOf("`nfunction Write-RedactedEvidence {", $contractEnd, [StringComparison]::Ordinal)
  $gateStart = $runner.IndexOf('  if ($ReleasePass) {', $writerStart, [StringComparison]::Ordinal)
  $evidenceStart = $runner.IndexOf('  $evidence = [ordered]@{', $gateStart, [StringComparison]::Ordinal)
  Assert-True ($writerStart -ge 0 -and $gateStart -gt $writerStart -and $evidenceStart -gt $gateStart) "schema-6 PASS gate boundaries were invalid"
  $gate = $runner.Substring($gateStart, $evidenceStart - $gateStart)
  foreach ($requiredGate in @('$ProtocolV3Heartbeat', '$ProtocolV3Drills', '$UncertainGatewayProof')) {
    Assert-True ($gate.IndexOf($requiredGate, [StringComparison]::Ordinal) -ge 0) "schema-6 PASS gate omitted $requiredGate"
  }
  # Anchor function boundaries to real top-level definitions. Unanchored
  # markers also occur as string literals in this self-check and would select
  # this function instead of the workflow being verified.
  $workflowStart = $runner.IndexOf("`nfunction Invoke-RecommendationWorkflow {", [StringComparison]::Ordinal)
  $workflowEnd = $runner.IndexOf("`nfunction Wait-RolloutState {", [StringComparison]::Ordinal)
  $takeoverStart = $runner.IndexOf("`nfunction Invoke-OperatorTakeoverScenario {", [StringComparison]::Ordinal)
  $takeoverEnd = $runner.IndexOf("`nfunction Invoke-AutomaticRollbackScenario {", [StringComparison]::Ordinal)
  Assert-True ($workflowStart -ge 0 -and $workflowEnd -gt $workflowStart -and $takeoverStart -ge 0 -and $takeoverEnd -gt $takeoverStart) "scenario B startup-claim function boundaries were invalid"
  $workflow = $runner.Substring($workflowStart, $workflowEnd - $workflowStart)
  $armAt = $workflow.IndexOf('$startupClaimRestartReason = "scenario-b-arm-startup-claim-window"', [StringComparison]::Ordinal)
  $postAt = $workflow.IndexOf('$started = Invoke-SafeJson "POST"', [StringComparison]::Ordinal)
  $gateAt = $workflow.IndexOf('$startupClaimGate = Install-StartupClaimGate', [StringComparison]::Ordinal)
  $stopAt = $workflow.IndexOf('$startupClaimArm = Stop-DisposableCoreForStartupClaim $rolloutID $startupClaimGate', [StringComparison]::Ordinal)
  $ungateAt = $workflow.IndexOf('$startupClaimArm.gate_removal = Remove-StartupClaimGate', [StringComparison]::Ordinal)
  Assert-True ($armAt -ge 0 -and $gateAt -gt $armAt -and $postAt -gt $gateAt -and
    $stopAt -gt $postAt -and $ungateAt -gt $stopAt) "scenario B did not gate, create, stop, then remove the temporary claim gate"
  $takeover = $runner.Substring($takeoverStart, $takeoverEnd - $takeoverStart)
  $preClaimAt = $takeover.IndexOf('$preClaim = Invoke-Postgres', [StringComparison]::Ordinal)
  $pauseAt = $takeover.IndexOf('Invoke-Compose pause connector-newapi', [StringComparison]::Ordinal)
  $startAt = $takeover.IndexOf('Invoke-Compose up --detach --force-recreate --no-deps e2m-core', [StringComparison]::Ordinal)
  $leaseAt = $takeover.IndexOf('$lease = Wait-RunningForwardLease', [StringComparison]::Ordinal)
  $rollbackAt = $takeover.IndexOf('$requested = Invoke-SafeJson "POST"', [StringComparison]::Ordinal)
  Assert-True ($preClaimAt -ge 0 -and $pauseAt -gt $preClaimAt -and $startAt -gt $pauseAt -and
    $leaseAt -gt $startAt -and $rollbackAt -gt $leaseAt) "scenario B did not preserve preclaim, startup RunOnce, live lease, and takeover ordering"
  $mainStart = $runner.IndexOf('  [void](Prepare-ScenarioBaseline $Context $channels "C")', $takeoverEnd, [StringComparison]::Ordinal)
  Assert-True ($mainStart -ge 0) "scenario C baseline workflow marker was missing"
  $healthRestartAt = $runner.IndexOf('  Restart-DisposableCoreWithIntervals $Context "1s" "30m" "1m" $scenarioCPreWorkflowRestartReason', $mainStart, [StringComparison]::Ordinal)
  $scenarioCWorkflowAt = $runner.IndexOf('  $workflowC = Invoke-RecommendationWorkflow', $mainStart, [StringComparison]::Ordinal)
  Assert-True ($healthRestartAt -gt $mainStart -and $scenarioCWorkflowAt -gt $healthRestartAt) "scenario C did not restore fast health aggregation before its baseline workflow"
}
function Get-JsonArrayItems {
  param($Object, [string]$Name)
  if ($null -eq $Object) { throw "JSON object was missing array property $Name" }
  $property = $Object.PSObject.Properties[$Name]
  if ($null -eq $property -and $Object -is [Collections.IDictionary] -and $Object.Contains($Name)) {
    $value = $Object[$Name]
  } elseif ($null -ne $property) {
    $value = $property.Value
  } else {
    throw "JSON object was missing array property $Name"
  }
  if ($null -eq $value -or -not ($value -is [array])) { throw "JSON property $Name was not an array" }
  foreach ($item in $value) { Write-Output $item }
}
function Invoke-SafeJson {
  param([string]$Method, [string]$Uri, $Body = $null, [hashtable]$Headers = @{}, [Microsoft.PowerShell.Commands.WebRequestSession]$WebSession)
  $request = @{ Method = $Method; Uri = $Uri; Headers = $Headers; TimeoutSec = 30 }
  if ($null -ne $Body) { $request.Body = ConvertTo-JsonBytes $Body; $request.ContentType = "application/json; charset=utf-8" }
  if ($null -ne $WebSession) { $request.WebSession = $WebSession }
  try { return Invoke-RestMethod @request }
  catch {
    $status = 0
    if ($_.Exception.Response -and $_.Exception.Response.StatusCode) { $status = [int]$_.Exception.Response.StatusCode }
    $route = ([uri]$Uri).AbsolutePath
    $code = "request_failed"
    $reasonCodes = @()
    $errorBody = [string]$_.ErrorDetails.Message
    if ($errorBody -ne "") {
      try {
        $parsedError = $errorBody | ConvertFrom-Json
        $candidateCode = [string](Get-JsonValue $parsedError "code")
        if ($candidateCode -match '^[a-z0-9_]{1,64}$') { $code = $candidateCode }
        foreach ($candidateReason in @(Get-JsonArrayItems $parsedError "reasons")) {
          $candidateReason = [string]$candidateReason
          if ($candidateReason -match '^[a-z0-9_]{1,64}$') { $reasonCodes += $candidateReason }
        }
      } catch {}
    }
    $reasonSummary = if ($reasonCodes.Count -eq 0) { "" } else { " reasons=" + ($reasonCodes -join ",") }
    throw "HTTP failure: method=$Method route=$route status=$status code=$code$reasonSummary"
  }
}
function Invoke-JsonStatus {
  param(
    [string]$Method,
    [string]$Uri,
    $Body = $null,
    [hashtable]$Headers = @{},
    [int]$ExpectedStatus
  )
  $request = @{ Method = $Method; Uri = $Uri; Headers = $Headers; TimeoutSec = 30; UseBasicParsing = $true }
  if ($null -ne $Body) {
    $request.Body = ConvertTo-JsonBytes $Body
    $request.ContentType = "application/json; charset=utf-8"
  }
  $response = $null
  try {
    $response = Invoke-WebRequest @request
  } catch {
    $status = 0
    if ($null -ne $_.Exception.Response -and $null -ne $_.Exception.Response.StatusCode) {
      $status = [int]$_.Exception.Response.StatusCode
    }
    if ($status -ne $ExpectedStatus) {
      throw "HTTP request returned unexpected status: method=$Method route=$(([uri]$Uri).AbsolutePath) expected=$ExpectedStatus actual=$status"
    }
    $raw = [string]$_.ErrorDetails.Message
    $value = $null
    if (-not [string]::IsNullOrWhiteSpace($raw)) {
      try { $value = $raw | ConvertFrom-Json }
      catch { throw "HTTP error response was not valid JSON: method=$Method route=$(([uri]$Uri).AbsolutePath)" }
    }
    return [pscustomobject]@{ Status = $status; Value = $value }
  }
  if ([int]$response.StatusCode -ne $ExpectedStatus) {
    throw "HTTP request returned unexpected status: method=$Method route=$(([uri]$Uri).AbsolutePath) expected=$ExpectedStatus actual=$([int]$response.StatusCode)"
  }
  $raw = [string]$response.Content
  $value = $null
  if (-not [string]::IsNullOrWhiteSpace($raw)) {
    try { $value = $raw | ConvertFrom-Json }
    catch { throw "HTTP response was not valid JSON: method=$Method route=$(([uri]$Uri).AbsolutePath)" }
  }
  return [pscustomobject]@{ Status = [int]$response.StatusCode; Value = $value }
}
function Wait-Http { param([string]$Uri, [string]$Name)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    try { $response = Invoke-WebRequest -Uri $Uri -UseBasicParsing -TimeoutSec 5; if ($response.StatusCode -lt 300) { return } } catch {}
    Start-Sleep -Seconds 2
  }
  throw "timed out waiting for $Name"
}
function Assert-Equal { param($Actual, $Expected, [string]$Label)
  if ($Actual -ne $Expected) { throw "$Label assertion failed: expected=$Expected actual=$Actual" }
}
function Assert-ProjectBoundary {
  if ($Project -notmatch $ProjectPattern -or $Project -match 'real-gateways') { throw "unsafe compose project name" }
  if (-not (Test-Path -LiteralPath $MarkerFile -PathType Leaf)) { throw "UI-17 cleanup marker is missing" }
  $marker = Get-Content -Raw -LiteralPath $MarkerFile | ConvertFrom-Json
  if ($marker.project -ne $Project -or [System.IO.Path]::GetFullPath([string]$marker.compose_file) -ne $ComposeFile -or
      [System.IO.Path]::GetFullPath([string]$marker.runtime_dir) -ne $RuntimeDir) { throw "UI-17 cleanup marker mismatch" }
  $expectedParent = $RuntimeRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
  if ([System.IO.Path]::GetDirectoryName($RuntimeDir) -ne $expectedParent -or [System.IO.Path]::GetFileName($RuntimeDir) -ne $Project) {
    throw "runtime path escaped its dedicated root"
  }
}
function Remove-DisposableStack {
  Assert-ProjectBoundary
  Invoke-Compose down --volumes --remove-orphans
}
function Assert-DisposableProjectRemoved {
  $containers = @(Get-ComposeContainerIDs)
  Assert-True ($containers.Count -eq 0) "disposable Compose containers remained after exact down"
  $volumes = @(& docker volume ls --quiet --filter "label=com.docker.compose.project=$Project" 2>$null | Where-Object { $_.Trim() -ne "" })
  if ($LASTEXITCODE -ne 0) { throw "Docker volume cleanup verification failed" }
  Assert-True ($volumes.Count -eq 0) "disposable Compose volumes remained after exact down"
  $networks = @(& docker network ls --quiet --filter "label=com.docker.compose.project=$Project" 2>$null | Where-Object { $_.Trim() -ne "" })
  if ($LASTEXITCODE -ne 0) { throw "Docker network cleanup verification failed" }
  Assert-True ($networks.Count -eq 0) "disposable Compose networks remained after exact down"
  return [ordered]@{
    containers = $containers.Count; volumes = $volumes.Count; networks = $networks.Count; verified_empty = $true
    inspection = [ordered]@{
      compose_container_query_count = 1; volume_project_label_query_count = 1; network_project_label_query_count = 1
      total_resource_query_count = 3; exact_project_scope = $true; verified = $true
    }
  }
}

# The implementation below intentionally separates fixture preparation from
# business actions. Fixture-only state is inserted into this disposable Core
# PostgreSQL after exact schema assertions. Recommendation generation, shadow,
# dry-run, policy authorization, rollout, NewAPI writes/read-back and rollback
# all traverse the production HTTP/Connector paths.
function Get-EnvelopeData {
  param($Response, [string]$Label)
  if ($null -eq $Response) { throw "$Label returned an empty response" }
  $success = Get-JsonValue $Response "success"
  if ($null -ne $success -and -not [bool]$success) { throw "$Label returned code=gateway_rejected" }
  $code = Get-JsonValue $Response "code"
  if ($null -ne $code -and [int]$code -ne 0) { throw "$Label returned code=gateway_rejected" }
  $data = Get-JsonValue $Response "data"
  if ($null -ne $data) { return $data }
  return $Response
}
function Get-NewAPIHeaders { param($NewAPI)
  return @{ Authorization = "Bearer $($NewAPI.Token)"; "New-Api-User" = [string]$NewAPI.UID }
}
function New-NewAPIRelayToken {
  param($NewAPI)
  Write-Step "Issuing a runtime-only NewAPI relay token"
  $headers = Get-NewAPIHeaders $NewAPI
  $name = "UI17 relay " + [guid]::NewGuid().ToString("N")
  $created = Invoke-SafeJson "POST" "$($NewAPI.BaseUrl)/api/token/" @{
    name = $name; expired_time = -1; remain_quota = 0; unlimited_quota = $true
    # The setup administrator belongs to NewAPI's built-in default group. A
    # runtime token may only select one of that user's usable groups; keeping
    # the disposable relay and channels in default avoids relying on mutable
    # site-specific group configuration.
    model_limits_enabled = $true; model_limits = "gpt-test"; allow_ips = ""; group = "default"
    cross_group_retry = $false
  } $headers
  if (-not [bool](Get-JsonValue $created "success")) { throw "NewAPI relay token create returned code=create_failed" }
  $listed = Invoke-SafeJson "GET" "$($NewAPI.BaseUrl)/api/token/?p=1&page_size=100" $null $headers
  $data = Get-EnvelopeData $listed "NewAPI relay token list"
  $items = @(Get-JsonArrayItems $data "items")
  $matches = @($items | Where-Object { [string](Get-JsonValue $_ "name") -eq $name })
  if ($matches.Count -ne 1) { throw "NewAPI relay token did not resolve uniquely" }
  $id = [string](Get-JsonValue $matches[0] "id")
  if ($id -notmatch '^[1-9][0-9]*$') { throw "NewAPI relay token omitted a valid id" }
  $revealed = Get-EnvelopeData (Invoke-SafeJson "POST" "$($NewAPI.BaseUrl)/api/token/$id/key" @{} $headers) "NewAPI relay token reveal"
  $key = [string](Get-JsonValue $revealed "key")
  if ($key -eq "") { $key = [string]$revealed }
  $key = $key.Trim()
  if ($key -eq "" -or $key -match '\s') { throw "NewAPI relay token reveal returned an invalid key" }
  return [pscustomobject]@{ ID = $id; Key = $key }
}
function Get-NewAPIChannelsPaginated {
  param($NewAPI)
  $headers = Get-NewAPIHeaders $NewAPI
  $items = @()
  $seen = @{}
  $reportedTotal = $null
  for ($page = 1; $page -le 4097; $page++) {
    $response = Invoke-SafeJson "GET" "$($NewAPI.BaseUrl)/api/channel/?p=$page&page_size=1" $null $headers
    $data = Get-EnvelopeData $response "NewAPI channel page"
    # Inspect the property itself before materializing its value. Returning an
    # empty JSON array through a normal PowerShell function enumerates it into
    # zero pipeline objects and is indistinguishable from a missing property.
    # Direct property access preserves [] and page_size=1 as exact arrays.
    $itemsProperty = $data.PSObject.Properties["items"]
    if ($null -eq $itemsProperty) {
      if ($data -is [array]) { $pageItems = @($data) }
      else { throw "NewAPI pagination returned code=response_shape_invalid" }
    } else {
      # Assign inside the branch. Emitting @() from an `if` expression would
      # itself be enumerated into zero pipeline objects before assignment.
      $pageItems = @($itemsProperty.Value)
    }
    $total = Get-JsonValue $data "total"
    if ($null -eq $total) { throw "NewAPI pagination omitted total" }
    if ($null -eq $reportedTotal) { $reportedTotal = [int]$total }
    elseif ([int]$total -ne $reportedTotal) { throw "NewAPI pagination total changed during read" }
    foreach ($item in @($pageItems)) {
      $id = [string](Get-JsonValue $item "id")
      if ($id -notmatch '^[1-9][0-9]*$' -or $seen.ContainsKey($id)) { throw "NewAPI pagination returned an invalid or duplicate channel id" }
      $seen[$id] = $true
      $items += $item
    }
    if ($items.Count -ge $reportedTotal) { break }
    if (@($pageItems).Count -ne 1) { throw "NewAPI pagination ended before reported total" }
  }
  if ($null -eq $reportedTotal -or $items.Count -ne $reportedTotal) { throw "NewAPI pagination did not produce its exact reported total" }
  # Let the caller's @(...)-materialization define the collection boundary.
  # Unary comma would turn an empty result into one nested Object[] element.
  return $items
}
function Initialize-NewAPI {
  Write-Step "Bootstrapping disposable NewAPI"
  $baseUrl = "http://127.0.0.1:$NewAPIPort"
  $username = "ui17admin"
  $password = "Ui17!" + [guid]::NewGuid().ToString("N")
  $setup = Get-EnvelopeData (Invoke-SafeJson "GET" "$baseUrl/api/setup") "NewAPI setup status"
  if (-not [bool](Get-JsonValue $setup "status")) {
    $created = Invoke-SafeJson "POST" "$baseUrl/api/setup" @{
      username = $username; password = $password; confirmPassword = $password
      SelfUseModeEnabled = $true; DemoSiteEnabled = $false
    }
    if (-not [bool](Get-JsonValue $created "success")) { throw "NewAPI setup returned code=setup_failed" }
  }
  try {
    $login = Invoke-RestMethod -Method POST -Uri "$baseUrl/api/user/login" -Body (ConvertTo-JsonBytes @{ username = $username; password = $password }) `
      -ContentType "application/json; charset=utf-8" -TimeoutSec 30 -SessionVariable newApiSession
  } catch { throw "HTTP failure: method=POST route=/api/user/login status=0 code=request_failed" }
  $loginData = Get-EnvelopeData $login "NewAPI login"
  $uid = [string](Get-JsonValue $loginData "id")
  if ($uid -notmatch '^[1-9][0-9]*$') { throw "NewAPI login omitted a valid user id" }
  $token = ""
  for ($attempt = 1; $attempt -le 20; $attempt++) {
    try {
      $tokenResponse = Invoke-RestMethod -Method GET -Uri "$baseUrl/api/user/token" -WebSession $newApiSession `
        -Headers @{ "New-Api-User" = $uid } -TimeoutSec 30
      $candidate = ([string](Get-EnvelopeData $tokenResponse "NewAPI access token")).Trim()
      if ($candidate -eq "") { continue }
      $probe = Invoke-RestMethod -Method GET -Uri "$baseUrl/api/channel/?p=1&page_size=1" `
        -Headers @{ Authorization = "Bearer $candidate"; "New-Api-User" = $uid } -TimeoutSec 30
      if ([bool](Get-JsonValue $probe "success")) { $token = $candidate; break }
    } catch {}
  }
  if ($token -eq "") { throw "NewAPI did not issue a normalized usable access token" }
  $context = [pscustomobject]@{ BaseUrl = $baseUrl; UID = $uid; Token = $token }
  $existing = @(Get-NewAPIChannelsPaginated $context)
  if ($existing.Count -ne 0) { throw "disposable NewAPI was not empty; refusing to mutate it" }
  $context | Add-Member -NotePropertyName RelayToken -NotePropertyValue (New-NewAPIRelayToken $context)
  return $context
}
function New-ThreeNewAPIChannels {
  param($NewAPI)
  Write-Step "Creating the exact disposable NewAPI baseline"
  $specs = @(
    [pscustomobject]@{ Role = "from"; Tag = "e2m:ui17-from"; Name = "UI17 from"; Weight = 80 },
    [pscustomobject]@{ Role = "to"; Tag = "e2m:ui17-to"; Name = "UI17 to"; Weight = 10 },
    [pscustomobject]@{ Role = "unrelated"; Tag = "e2m:ui17-unrelated"; Name = "UI17 unrelated"; Weight = 10 }
  )
  $headers = Get-NewAPIHeaders $NewAPI
  foreach ($spec in $specs) {
    $key = "sk-ui17-disposable-" + [guid]::NewGuid().ToString("N") # gitleaks:allow -- generated test credential
    $response = Invoke-SafeJson "POST" "$($NewAPI.BaseUrl)/api/channel/" @{
      mode = "single"
      channel = @{
        id = 0; name = $spec.Name; type = 1; status = 1; group = "default"
        priority = 0; weight = $spec.Weight; models = "gpt-test"; tag = $spec.Tag; key = $key
        base_url = "http://mock-openai:8093"
        # The pinned NewAPI build only forwards an incoming correlation value
        # when the channel explicitly resolves the full-value client_header
        # placeholder. The disposable mock therefore proves that traffic
        # crossed NewAPI's real relay path instead of merely reaching NewAPI.
        header_override = '{"x-e2m-correlation":"{client_header:X-E2M-Correlation}"}'
      }
    } $headers
    if (-not [bool](Get-JsonValue $response "success")) { throw "NewAPI channel create returned code=create_failed" }
  }
  $actual = @(Get-NewAPIChannelsPaginated $NewAPI)
  if ($actual.Count -ne 3) { throw "NewAPI baseline must contain exactly three channels" }
  $result = @{}
  foreach ($spec in $specs) {
    $matches = @($actual | Where-Object { [string](Get-JsonValue $_ "tag") -eq $spec.Tag })
    if ($matches.Count -ne 1) { throw "NewAPI channel tag did not resolve uniquely" }
    $id = [string](Get-JsonValue $matches[0] "id")
    $weight = [int](Get-JsonValue $matches[0] "weight")
    if ($id -notmatch '^[1-9][0-9]*$' -or $weight -ne $spec.Weight) { throw "NewAPI baseline channel did not match its requested identity and weight" }
    $result[$spec.Role] = [pscustomobject]@{ ID = $id; Weight = $weight; Tag = $spec.Tag }
  }
  return $result
}
function Initialize-E2MConnector {
  param($NewAPI)
  Write-Step "Creating disposable owner, instance and Connector"
  $core = "http://127.0.0.1:$CorePort"
  $login = Invoke-SafeJson "POST" "$core/api/v1/auth/login" @{ email = "admin@ui17.local"; password = "ui17-admin-password" }
  $adminToken = [string](Get-JsonValue $login "token")
  if ($adminToken -eq "") { throw "Core login omitted its session token" }
  $adminHeaders = @{ Authorization = "Bearer $adminToken" }
  $admin = Invoke-SafeJson "GET" "$core/api/v1/auth/me" $null $adminHeaders
  Assert-Equal ([string](Get-JsonValue $admin "email")) "admin@ui17.local" "Core bootstrap administrator email"
  $adminRoles = @(Get-JsonArrayItems $admin "roles" | ForEach-Object { [string]$_ } | Sort-Object -Unique)
  Assert-True ($adminRoles.Count -eq 1 -and $adminRoles[0] -eq "admin") "Core bootstrap identity was not the unique administrator role"
  $initialUsers = @(Invoke-SafeJson "GET" "$core/api/v1/users" $null $adminHeaders | ForEach-Object { $_ })
  Assert-True ($initialUsers.Count -eq 1 -and [string](Get-JsonValue $initialUsers[0] "email") -eq "admin@ui17.local") "Core bootstrap did not contain exactly its fixed administrator"
  $owner = Invoke-SafeJson "POST" "$core/api/v1/users" @{
    email = "owner@ui17.local"; password = "ui17-owner-password"; display_name = "UI17 disposable owner"; roles = @("client")
  } $adminHeaders
  $userID = [long](Get-JsonValue $owner "id")
  if ($userID -le 0) { throw "Core owner creation omitted a valid id" }
  $instance = Invoke-SafeJson "POST" "$core/api/v1/instances" @{
    user_id = $userID; name = "UI17 disposable NewAPI"; kind = "newapi"
  } $adminHeaders
  $instanceID = [string](Get-JsonValue $instance "id")
  $install = Get-JsonValue $instance "connector_install"
  if ($null -eq $install) { $install = Invoke-SafeJson "POST" "$core/api/v1/instances/$instanceID/connector-install" $null $adminHeaders }
  $enrollment = Get-JsonValue $install "enrollment"
  $connectorID = [string](Get-JsonValue $enrollment "connector_id")
  $enrollmentToken = [string](Get-JsonValue $install "token")
  if ($instanceID -eq "" -or $connectorID -eq "" -or $enrollmentToken -eq "") { throw "Core connector install response was incomplete" }
  [IO.File]::WriteAllText((Join-Path $RuntimeDir "connector/enrollment.token"), $enrollmentToken + "`n", [Text.UTF8Encoding]::new($false))
  [Environment]::SetEnvironmentVariable("E2M_UI17_CONNECTOR_ID", $connectorID, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_INSTANCE_ID", $instanceID, "Process")
  Invoke-Compose up --build --detach --force-recreate --no-deps connector-newapi

  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $localToken = ""
  $connectorToken = ""
  while ((Get-Date) -lt $deadline) {
    try {
      $localToken = Invoke-ComposeCapture exec --no-TTY connector-newapi cat /var/lib/e2m-agent/local-ui.token
      $connectorToken = Invoke-ComposeCapture exec --no-TTY connector-newapi cat /var/lib/e2m-agent/connector.token
      if ($localToken -ne "" -and $connectorToken -ne "") { break }
    } catch {}
    Start-Sleep -Seconds 1
  }
  if ($localToken -eq "" -or $connectorToken -eq "") { throw "Connector did not persist its local and runtime tokens" }
  [IO.File]::WriteAllText((Join-Path $RuntimeDir "connector/enrollment.token"), "", [Text.UTF8Encoding]::new($false))
  $localBase = "http://127.0.0.1:$ConnectorPort"
  $localHeaders = @{ "X-E2M-Local-Token" = $localToken; Origin = $localBase }
  $config = @{
    gateway_kind = "newapi"; gateway_url = "http://newapi:3000"; auth = "newapi"
    credentials = @{ newapi_user_id = [string]$NewAPI.UID; newapi_token = [string]$NewAPI.Token }
  }
  [void](Invoke-SafeJson "POST" "$localBase/api/local/connector/test" $config $localHeaders)
  $saved = Invoke-SafeJson "POST" "$localBase/api/local/connector/config" $config $localHeaders
  if (-not [bool](Get-JsonValue (Get-JsonValue $saved "config") "gateway_configured")) { throw "Connector did not report a saved gateway configuration" }
  $lastStatus = ""
  while ((Get-Date) -lt $deadline) {
    # Invoke-RestMethod preserves a top-level one-element JSON array as a
    # non-enumerated Object[]. Force one pipeline enumeration before @(...),
    # otherwise the collection becomes a nested array and identity matching
    # can never observe the already-online Connector.
    $connectors = @(Invoke-SafeJson "GET" "$core/api/v1/connectors?user_id=$userID&instance_id=$instanceID" $null $adminHeaders | ForEach-Object { $_ })
    $match = @($connectors | Where-Object { [string](Get-JsonValue $_ "connector_id") -eq $connectorID })
    if ($match.Count -eq 1) {
      $lastStatus = [string](Get-JsonValue $match[0] "status")
      if ($lastStatus -eq "online") { break }
    }
    Start-Sleep -Seconds 1
  }
  if ($lastStatus -ne "online") { throw "Connector did not become online" }
  return [pscustomobject]@{
    CoreBase = $core; AdminHeaders = $adminHeaders; UserID = $userID; InstanceID = $instanceID
    ConnectorID = $connectorID; ConnectorToken = $connectorToken; PlanID = "plan-ui17-newapi"
  }
}
function Initialize-ProtocolV3Observers {
  param($Channels)
  Write-Step "Installing fixture-only protocol-v3 observers"
  foreach ($role in @("from", "to", "unrelated")) {
    if ($null -eq $Channels[$role] -or [string]$Channels[$role].ID -notmatch '^[1-9][0-9]*$') {
      throw "protocol-v3 observer channel identity was invalid"
    }
  }
  $coreSql = @"
BEGIN;
CREATE TABLE ui17_connector_task_status_observations (
  observation_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  task_id TEXT NOT NULL,
  status TEXT NOT NULL,
  observed_at TIMESTAMPTZ NOT NULL
);
CREATE OR REPLACE FUNCTION ui17_observe_connector_task_status()
RETURNS trigger LANGUAGE plpgsql AS `$`$
BEGIN
  IF TG_OP = 'INSERT' OR NEW.status IS DISTINCT FROM OLD.status THEN
    INSERT INTO ui17_connector_task_status_observations(task_id,status,observed_at)
    VALUES (NEW.id,NEW.status,clock_timestamp());
  END IF;
  RETURN NEW;
END;
`$`$;
CREATE TRIGGER trg_ui17_observe_connector_task_status
AFTER INSERT OR UPDATE OF status ON connector_tasks
FOR EACH ROW EXECUTE FUNCTION ui17_observe_connector_task_status();
COMMIT;
"@
  [void](Invoke-Postgres $coreSql)

  $newAPISql = @"
BEGIN;
CREATE TABLE ui17_channel_weight_observations (
  observation_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  channel_id BIGINT NOT NULL,
  old_weight BIGINT NOT NULL,
  new_weight BIGINT NOT NULL,
  observed_at TIMESTAMPTZ NOT NULL
);
CREATE OR REPLACE FUNCTION ui17_observe_channel_weight()
RETURNS trigger LANGUAGE plpgsql AS `$`$
BEGIN
  IF NEW.weight IS DISTINCT FROM OLD.weight THEN
    PERFORM pg_advisory_xact_lock(913747201);
    INSERT INTO ui17_channel_weight_observations(channel_id,old_weight,new_weight,observed_at)
    VALUES (NEW.id,OLD.weight,NEW.weight,clock_timestamp());
  END IF;
  RETURN NEW;
END;
`$`$;
CREATE TRIGGER trg_ui17_observe_channel_weight
AFTER UPDATE OF weight ON channels
FOR EACH ROW EXECUTE FUNCTION ui17_observe_channel_weight();
COMMIT;
"@
  [void](Invoke-NewAPIPostgres $newAPISql)
  $coreCount = Invoke-Postgres "SELECT count(*) FROM ui17_connector_task_status_observations;"
  $weightCount = Invoke-NewAPIPostgresClock "SELECT count(*) FROM ui17_channel_weight_observations;"
  Assert-Equal ([int]$coreCount) 0 "initial protocol-v3 task observer row count"
  Assert-Equal ([int]$weightCount) 0 "initial protocol-v3 weight observer row count"
}
function Get-ProtocolV3HeartbeatEvidence {
  param($Context)
  Write-Step "Capturing protocol-v3 Connector heartbeat"
  $connector = ConvertTo-SqlLiteral ([string]$Context.ConnectorID)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $parts = @()
  while ((Get-Date) -lt $deadline) {
    $row = Invoke-Postgres "SELECT protocol_version||'|'||COALESCE((gateway_state->>'protocol_version'),'')||'|'||COALESCE(last_seen_at::text,'') FROM connectors WHERE connector_id=$connector;"
    $parts = @($row -split '\|', -1)
    if ($parts.Count -eq 3 -and $parts[0] -eq "3" -and $parts[1] -eq "3" -and $parts[2] -ne "") { break }
    Start-Sleep -Seconds 1
  }
  if ($parts.Count -ne 3) { throw "protocol-v3 heartbeat row had an invalid shape" }
  Assert-Equal ([int]$parts[0]) 3 "Connector protocol version"
  Assert-Equal ([int]$parts[1]) 3 "Connector gateway-state protocol version"
  $lastSeen = ConvertFrom-UtcTimestamp $parts[2] "Connector last_seen_at"
  return [ordered]@{
    protocol_version = 3; gateway_state_protocol_version = 3
    last_seen_at = $lastSeen.ToString("o"); database_source = "disposable_core_postgresql"
  }
}
function New-ProtocolV3FixtureTask {
  param($Context, [string]$TaskID, [long]$Generation, [long]$Sequence, [string]$AccountID, [int]$Weight)
  if ($TaskID -notmatch '^ctask-ui17-[a-z0-9-]{1,48}$' -or $Generation -le 0 -or $Sequence -le 0 -or
      $AccountID -notmatch '^[1-9][0-9]*$' -or $Weight -lt 0 -or $Weight -gt 100) {
    throw "protocol-v3 fixture task input was invalid"
  }
  $task = ConvertTo-SqlLiteral $TaskID
  $owner = [long]$Context.UserID
  $instance = ConvertTo-SqlLiteral ([string]$Context.InstanceID)
  $connector = ConvertTo-SqlLiteral ([string]$Context.ConnectorID)
  $plan = ConvertTo-SqlLiteral ([string]$Context.PlanID)
  $account = ConvertTo-SqlLiteral $AccountID
  $idempotency = ConvertTo-SqlLiteral "ui17-protocol-v3-$TaskID"
  $sql = @"
INSERT INTO connector_tasks(
 id,user_id,instance_id,connector_id,plan_id,scheduling_generation,type,schema_version,risk_level,status,input,result,error,
 idempotency_key,lease_owner,lease_nonce,lease_until,attempts,max_attempts,available_at,expires_at,created_at,updated_at)
VALUES (
 $task,$owner,$instance,$connector,$plan,$Generation,'gateway.account.traffic_share.set',1,'L1','pending',
 jsonb_build_object('account_id',$account,'weight',$Weight,'fence',jsonb_build_object('scope','auto-switch/plan/'||$plan,'version',$Generation,'sequence',$Sequence)),
 'null'::jsonb,'{}'::jsonb,$idempotency,'','',NULL,0,3,statement_timestamp(),statement_timestamp()+interval '10 minutes',statement_timestamp(),statement_timestamp()
);
"@
  [void](Invoke-Postgres $sql)
  $stored = Invoke-Postgres "SELECT id||'|'||status||'|'||plan_id||'|'||scheduling_generation FROM connector_tasks WHERE id=$task;"
  Assert-Equal $stored "$TaskID|pending|$($Context.PlanID)|$Generation" "protocol-v3 fixture task identity"
  return $TaskID
}
function Lease-ProtocolV3FixtureTask {
  param($Context, [string]$ExpectedTaskID)
  $headers = @{ Authorization = "Bearer $($Context.ConnectorToken)" }
  $response = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/connectors/tasks/lease" @{
    connector_id = [string]$Context.ConnectorID; max_tasks = 1; lease_seconds = 120
    version = "0.1.0-ui17.0"; protocol_version = 3
    runtime_state = @{ protocol_version = 3; gateway_configured = $true; gateway_kind = "newapi"; gateway_status = "configured" }
  } $headers
  Assert-Equal ([int](Get-JsonValue $response "protocol_version")) 3 "protocol-v3 lease response version"
  $tasks = @(Get-JsonArrayItems $response "tasks")
  Assert-Equal $tasks.Count 1 "protocol-v3 fixture lease task count"
  $task = $tasks[0]
  Assert-Equal ([string](Get-JsonValue $task "id")) $ExpectedTaskID "protocol-v3 fixture lease identity"
  Assert-Equal ([string](Get-JsonValue $task "status")) "leased" "protocol-v3 fixture lease status"
  $nonce = [string](Get-JsonValue $task "lease_nonce")
  Assert-True (-not [string]::IsNullOrWhiteSpace($nonce)) "protocol-v3 fixture lease omitted nonce"
  return [pscustomobject]@{ TaskID = $ExpectedTaskID; LeaseNonce = $nonce }
}
function Assert-Postgres23514 {
  param([string]$Sql, [string]$Label)
  $caught = $false
  $detail = ""
  try { [void](Invoke-Postgres $Sql) }
  catch {
    $detail = [string]$_
    $caught = $detail -match '23514'
  }
  Assert-True $caught "$Label was not rejected by PostgreSQL 23514"
  return [ordered]@{ rejected = $true; sqlstate = "23514"; error_sha256 = Get-Sha256Hex $detail }
}
function Invoke-ProtocolV3ConflictAndResolutionDrills {
  param($Context, $Channels)
  Write-Step "Running protocol-v3 conflict, generation guard and operator resolution drills"
  $plan = ConvertTo-SqlLiteral ([string]$Context.PlanID)
  $connector = ConvertTo-SqlLiteral ([string]$Context.ConnectorID)
  $connectorHeaders = @{ Authorization = "Bearer $($Context.ConnectorToken)" }
  $paused = $false
  try {
    Invoke-Compose pause connector-newapi
    $paused = $true
    $script:ConnectorPausedByRunner = $true
    $generation = [long](Invoke-Postgres "SELECT scheduling_generation FROM route_plans WHERE id=$plan;")
    if ($generation -le 0) { throw "protocol-v3 drill route-plan generation was invalid" }

    $conflictID = "ctask-ui17-execute-conflict"
    [void](New-ProtocolV3FixtureTask $Context $conflictID $generation 900001 ([string]$Channels.unrelated.ID) 11)
    $conflictLease = Lease-ProtocolV3FixtureTask $Context $conflictID
    $conflictWeightBefore = [int](Invoke-NewAPIPostgresClock "SELECT count(*) FROM ui17_channel_weight_observations;")
    $conflictExecutingBefore = [int](Invoke-Postgres "SELECT count(*) FROM ui17_connector_task_status_observations WHERE task_id=$(ConvertTo-SqlLiteral $conflictID) AND status='executing';")
    $conflictAuditBefore = [int](Invoke-Postgres "SELECT count(*) FROM operation_audits WHERE target_id=$(ConvertTo-SqlLiteral $conflictID) AND action LIKE 'connector_task.complete%';")
    $generationAfterBump = [long](Invoke-Postgres "UPDATE route_plans SET scheduling_generation=scheduling_generation+1,updated_at=statement_timestamp() WHERE id=$plan RETURNING scheduling_generation;")
    Assert-Equal $generationAfterBump ($generation + 1) "protocol-v3 conflict drill generation bump"
    $conflict = Invoke-JsonStatus "POST" "$($Context.CoreBase)/api/v1/connectors/tasks/$conflictID/execute" @{
      connector_id = [string]$Context.ConnectorID; lease_nonce = [string]$conflictLease.LeaseNonce
    } $connectorHeaders 409
    Assert-Equal ([string](Get-JsonValue $conflict.Value "code")) "task_execution_conflict" "protocol-v3 execute conflict code"
    $conflictWeightAfter = [int](Invoke-NewAPIPostgresClock "SELECT count(*) FROM ui17_channel_weight_observations;")
    $conflictExecutingAfter = [int](Invoke-Postgres "SELECT count(*) FROM ui17_connector_task_status_observations WHERE task_id=$(ConvertTo-SqlLiteral $conflictID) AND status='executing';")
    $conflictAuditAfter = [int](Invoke-Postgres "SELECT count(*) FROM operation_audits WHERE target_id=$(ConvertTo-SqlLiteral $conflictID) AND action LIKE 'connector_task.complete%';")
    Assert-Equal $conflictWeightAfter $conflictWeightBefore "execute conflict remote-weight observer count"
    Assert-Equal $conflictExecutingAfter $conflictExecutingBefore "execute conflict executing observer count"
    Assert-Equal $conflictAuditAfter $conflictAuditBefore "execute conflict completion audit count"
    $postConflictLease = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/connectors/tasks/lease" @{
      connector_id = [string]$Context.ConnectorID; max_tasks = 1; lease_seconds = 120; version = "0.1.0-ui17.0"; protocol_version = 3
      runtime_state = @{ protocol_version = 3; gateway_configured = $true; gateway_kind = "newapi"; gateway_status = "configured" }
    } $connectorHeaders
    Assert-Equal ([int](Get-JsonValue $postConflictLease "protocol_version")) 3 "post-conflict lease protocol version"
    $postConflictTasks = @(Get-JsonArrayItems $postConflictLease "tasks")
    Assert-Equal $postConflictTasks.Count 0 "post-conflict lease task count"
    $conflictTerminal = Invoke-Postgres "SELECT status||'|'||COALESCE(error->>'code','')||'|'||COALESCE(result::text,'')||'|'||lease_owner||'|'||lease_nonce FROM connector_tasks WHERE id=$(ConvertTo-SqlLiteral $conflictID);"
    Assert-Equal $conflictTerminal "failed|scheduling_fence_stale|null||" "stale conflict task terminal state"

    $guardID = "ctask-ui17-execution-guard"
    [void](New-ProtocolV3FixtureTask $Context $guardID $generationAfterBump 900002 ([string]$Channels.unrelated.ID) 12)
    $guardLease = Lease-ProtocolV3FixtureTask $Context $guardID
    $executing = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/connectors/tasks/$guardID/execute" @{
      connector_id = [string]$Context.ConnectorID; lease_nonce = [string]$guardLease.LeaseNonce
    } $connectorHeaders
    Assert-Equal ([string](Get-JsonValue $executing "status")) "executing" "protocol-v3 guard task executing status"
    Assert-Equal ([string](Get-JsonValue $executing "id")) $guardID "protocol-v3 guard task executing identity"
    $generationBeforeGuards = [long](Invoke-Postgres "SELECT scheduling_generation FROM route_plans WHERE id=$plan;")
    $updateGuard = Assert-Postgres23514 "UPDATE route_plans SET scheduling_generation=scheduling_generation+1 WHERE id=$plan;" "executing generation update guard"
    $deleteGuard = Assert-Postgres23514 "DELETE FROM route_plans WHERE id=$plan;" "executing route-plan delete guard"
    Assert-Equal ([long](Invoke-Postgres "SELECT scheduling_generation FROM route_plans WHERE id=$plan;")) $generationBeforeGuards "route-plan generation after rejected guards"
    $rawNonce = [string]$guardLease.LeaseNonce
    $nonceHash = "sha256:" + (Get-Sha256Hex $rawNonce)
    $resolved = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/connector-tasks/$guardID/resolve-execution" @{
      lease_nonce = $rawNonce; resolution = "confirmed_not_applied"; evidence_note = "UI17 disposable observer verified no remote weight mutation"
    } $Context.AdminHeaders
    Assert-Equal ([string](Get-JsonValue $resolved "id")) $guardID "manual resolution response identity"
    Assert-Equal ([string](Get-JsonValue $resolved "status")) "failed" "manual resolution response status"
    Assert-Equal ([string](Get-JsonValue (Get-JsonValue $resolved "error") "code")) "execution_abandoned" "manual resolution response error"
    foreach ($forbiddenProperty in @("input", "result", "lease_nonce", "lease_owner")) {
      Assert-True ($null -eq $resolved.PSObject.Properties[$forbiddenProperty]) "manual resolution response exposed $forbiddenProperty"
    }
    $resolvedJSON = $resolved | ConvertTo-Json -Depth 20 -Compress
    Assert-True ($resolvedJSON.IndexOf($rawNonce, [StringComparison]::Ordinal) -lt 0) "manual resolution response exposed the raw nonce"
    $atomic = Invoke-Postgres "SELECT task.status||'|'||COALESCE(task.error->>'code','')||'|'||task.lease_owner||'|'||task.lease_nonce||'|'||audit.risk_level||'|'||audit.event_level||'|'||audit.action||'|'||audit.result||'|'||COALESCE(audit.details->>'lease_nonce_sha256','')||'|'||COALESCE(audit.details->>'lease_nonce','') FROM connector_tasks task JOIN operation_audits audit ON audit.target_id=task.id AND audit.action='connector_task.resolve_execution' WHERE task.id=$(ConvertTo-SqlLiteral $guardID);"
    $atomicParts = @($atomic -split '\|', -1)
    if ($atomicParts.Count -ne 10) { throw "manual execution resolution atomic evidence had an invalid shape" }
    Assert-Equal ($atomicParts[0..7] -join '|') "failed|execution_abandoned|||L3|L3|connector_task.resolve_execution|confirmed_not_applied" "manual resolution atomic state and audit"
    Assert-True ($atomicParts[8] -match '^sha256:[0-9a-f]{64}$' -and $atomicParts[8] -eq $nonceHash) "manual resolution audit nonce hash was invalid"
    Assert-Equal $atomicParts[9] "" "manual resolution audit raw nonce field"
    $auditJSON = Invoke-Postgres "SELECT details::text FROM operation_audits WHERE target_id=$(ConvertTo-SqlLiteral $guardID) AND action='connector_task.resolve_execution';"
    Assert-True ($auditJSON.IndexOf($rawNonce, [StringComparison]::Ordinal) -lt 0) "manual resolution audit exposed the raw nonce"
    Assert-Equal ([int](Invoke-NewAPIPostgresClock "SELECT count(*) FROM ui17_channel_weight_observations WHERE channel_id=$([string]$Channels.unrelated.ID) AND new_weight IN (11,12);")) 0 "protocol-v3 drills remote weight mutation count"
    $generationAfterResolution = [long](Invoke-Postgres "UPDATE route_plans SET scheduling_generation=scheduling_generation+1,updated_at=statement_timestamp() WHERE id=$plan RETURNING scheduling_generation;")
    Assert-Equal $generationAfterResolution ($generationBeforeGuards + 1) "generation advance after manual resolution"

    return [ordered]@{
      execute_conflict = [ordered]@{
        http_status = [int]$conflict.Status; code = "task_execution_conflict"
        task_id_sha256 = Get-Sha256Hex $conflictID; generation_before = $generation; generation_after = $generationAfterBump
        remote_weight_writes = 0; executing_transitions = 0; completion_audits = 0
        terminal_status = "failed"; terminal_error_code = "scheduling_fence_stale"
      }
      executing_generation_guard = [ordered]@{
        task_id_sha256 = Get-Sha256Hex $guardID; status = "executing"
        update = $updateGuard; delete = $deleteGuard; generation_unchanged = $true
      }
      manual_resolution = [ordered]@{
        authenticated_role = "platform_admin"; resolution = "confirmed_not_applied"
        terminal_status = "failed"; terminal_error_code = "execution_abandoned"; atomic_transition_and_audit = $true
        risk_level = "L3"; event_level = "L3"; action = "connector_task.resolve_execution"
        nonce_reference = $nonceHash; raw_nonce_absent = $true; sensitive_response_fields_absent = $true
        generation_advanced_after_resolution = $true
      }
    }
  } finally {
    if ($paused) {
      Invoke-Compose unpause connector-newapi
      $script:ConnectorPausedByRunner = $false
    }
  }
}
function Invoke-UncertainGatewayUnitProof {
  Write-Step "Running directed uncertain-gateway no-bypass proof"
  $output = @()
  $exitCode = -1
  Push-Location (Join-Path $RepoRoot "app/e2m-agent")
  try {
    $output = & go test ./internal/connector -run '^TestExecutionPermitUncertainGatewayOutcomeIsNotCompleted$' -count=1 -v 2>&1
    $exitCode = $LASTEXITCODE
  } finally {
    Pop-Location
  }
  $raw = (($output | ForEach-Object { [string]$_ }) -join "`n").Trim()
  if ($exitCode -ne 0) { throw "uncertain-gateway directed unit proof failed with exit code $exitCode" }
  Assert-True ($raw -match '(?m)^--- PASS: TestExecutionPermitUncertainGatewayOutcomeIsNotCompleted') "uncertain-gateway directed unit proof omitted PASS"
  return [ordered]@{
    proof_kind = "unit_no_bypass"; real_external_fault_injection = $false
    claim_scope = "directed_unit_test_only"; command = "go test ./internal/connector -run '^TestExecutionPermitUncertainGatewayOutcomeIsNotCompleted$' -count=1 -v"
    working_directory = "app/e2m-agent"; exit_code = $exitCode; output_sha256 = Get-Sha256Hex $raw
    uncertain_outcome_not_completed = $true
  }
}
function Initialize-FixtureOnlyRecommendationFacts {
  param($Context, $Channels)
  Write-Step "Installing recommendation-only facts in disposable Core PostgreSQL"
  foreach ($name in @("from", "to", "unrelated")) {
    if ($null -eq $Channels[$name] -or [string]$Channels[$name].ID -notmatch '^[1-9][0-9]*$') {
      throw "fixture channel identity is invalid"
    }
  }
  $owner = [long]$Context.UserID
  $instance = ConvertTo-SqlLiteral ([string]$Context.InstanceID)
  $connector = ConvertTo-SqlLiteral ([string]$Context.ConnectorID)
  $plan = ConvertTo-SqlLiteral ([string]$Context.PlanID)
  $fromRemote = ConvertTo-SqlLiteral ([string]$Channels.from.ID)
  $toRemote = ConvertTo-SqlLiteral ([string]$Channels.to.ID)
  $unrelatedRemote = ConvertTo-SqlLiteral ([string]$Channels.unrelated.ID)
  $hashA = "a" * 64
  $hashB = "b" * 64
  $costKeyA = "c" * 64
  $costKeyB = "d" * 64
  $sql = @"
BEGIN;
DO `$`$ BEGIN
  IF (SELECT COUNT(*) FROM upstream_pools) <> 0 OR (SELECT COUNT(*) FROM upstream_channels) <> 0 OR
     (SELECT COUNT(*) FROM route_plans) <> 0 OR (SELECT COUNT(*) FROM upstream_intelligence_sources) <> 0 THEN
    RAISE EXCEPTION 'disposable recommendation fixture database was not empty';
  END IF;
END `$`$;
INSERT INTO upstream_pools(id,name,provider,models,status,description,labels,safety_stock_threshold)
VALUES ('pool-ui17-newapi','UI17 disposable pool','newapi',jsonb_build_array('gpt-test'),'active','fixture-only',jsonb_build_object(),0);
INSERT INTO upstream_channels(id,pool_id,source_id,account_ownership,display_name,provider,models,groups,weight,status,inventory_state,labels)
VALUES
 ('channel-ui17-from','pool-ui17-newapi','allocation-ui17-from','owner_provided','UI17 from','newapi',jsonb_build_array('gpt-test'),jsonb_build_array('paid'),80,'active','ready',jsonb_build_object()),
 ('channel-ui17-to','pool-ui17-newapi','allocation-ui17-to','owner_provided','UI17 to','newapi',jsonb_build_array('gpt-test'),jsonb_build_array('paid'),10,'active','ready',jsonb_build_object()),
 ('channel-ui17-unrelated','pool-ui17-newapi','allocation-ui17-unrelated','owner_provided','UI17 unrelated','newapi',jsonb_build_array('gpt-test'),jsonb_build_array('paid'),10,'active','ready',jsonb_build_object());
INSERT INTO route_plans(id,user_id,instance_id,pool_id,status,max_channels,scheduling_generation,labels)
VALUES ($plan,$owner,$instance,'pool-ui17-newapi','published',3,1,jsonb_build_object());
INSERT INTO pool_rollout_targets(id,pool_id,scope,user_id,instance_id,enabled,rollout,rollout_batch_size,rollout_canary_count,note)
VALUES ('rollout-target-ui17-newapi','pool-ui17-newapi','instance',$owner,$instance,true,'immediate',0,0,'UI17 disposable authorization');
INSERT INTO upstream_channel_allocations(channel_id,source_id,user_id,first_plan_id)
VALUES ('channel-ui17-from','allocation-ui17-from',$owner,$plan),('channel-ui17-to','allocation-ui17-to',$owner,$plan),
       ('channel-ui17-unrelated','allocation-ui17-unrelated',$owner,$plan);
INSERT INTO published_bindings(id,plan_id,instance_id,channel_id,remote_id,account_ownership,state,scheduling_generation,
 verification_status,verification_source,verified_at,verification_error_code)
VALUES
 ('binding-ui17-from',$plan,$instance,'channel-ui17-from',$fromRemote,'owner_provided','active',1,'awaiting_first_request','publish',NULL,''),
 ('binding-ui17-to',$plan,$instance,'channel-ui17-to',$toRemote,'owner_provided','active',1,'awaiting_first_request','publish',NULL,''),
 ('binding-ui17-unrelated',$plan,$instance,'channel-ui17-unrelated',$unrelatedRemote,'owner_provided','active',1,'awaiting_first_request','publish',NULL,'');
INSERT INTO upstream_intelligence_sources(id,user_id,connector_id,instance_id,local_ref,mode,provider,display_name,currency,status,
 capability_balance,capability_groups,capability_rates,capability_prices,last_run_at,last_success_at,last_coverage)
VALUES
 ('uisrc-ui17-from',$owner,$connector,$instance,'fixture-source-from','external','sub2api','UI17 expensive source','USD','active',true,true,true,true,statement_timestamp(),statement_timestamp(),'complete'),
 ('uisrc-ui17-to',$owner,$connector,$instance,'fixture-source-to','external','sub2api','UI17 cheap source','USD','active',true,true,true,true,statement_timestamp(),statement_timestamp(),'complete');
INSERT INTO upstream_collection_runs(id,user_id,source_id,connector_id,trigger,status,coverage,started_at,observed_at,completed_at,
 snapshot_hash,manifest_hash,batch_count,fact_count,page_count,finalized_fact_version)
VALUES
 ('uirun-ui17-from',$owner,'uisrc-ui17-from',$connector,'manual','succeeded','complete',statement_timestamp()-interval '2 minutes',statement_timestamp()-interval '90 seconds',statement_timestamp()-interval '80 seconds','$hashA','$hashA',1,2,1,1),
 ('uirun-ui17-to',$owner,'uisrc-ui17-to',$connector,'manual','succeeded','complete',statement_timestamp()-interval '2 minutes',statement_timestamp()-interval '90 seconds',statement_timestamp()-interval '80 seconds','$hashB','$hashB',1,2,1,2);
INSERT INTO upstream_wallet_observations(run_id,id,user_id,source_id,balance_amount,unit_kind,currency,observed_at,fresh_until,accuracy,coverage,missing_fields,reason_code)
VALUES
 ('uirun-ui17-from','wallet-ui17-from',$owner,'uisrc-ui17-from',100,'fiat','USD',statement_timestamp()-interval '90 seconds',statement_timestamp()+interval '2 hours','exact','complete',jsonb_build_array(),''),
 ('uirun-ui17-to','wallet-ui17-to',$owner,'uisrc-ui17-to',100,'fiat','USD',statement_timestamp()-interval '90 seconds',statement_timestamp()+interval '2 hours','exact','complete',jsonb_build_array(),'');
INSERT INTO upstream_offer_observations(run_id,id,user_id,source_id,group_key,model_key,price_dimension,settlement_currency,
 published_unit_price,per_tokens,effective_unit_cost,formula_version,accuracy,coverage,observed_at,effective_at,fresh_until,
 missing_fields,reason_code,adapter_schema_version,source_revision)
VALUES
 ('uirun-ui17-from','offer-ui17-from',$owner,'uisrc-ui17-from','paid','gpt-test','input','USD',10,1000000,10,'effective-cost/v1','exact','complete',statement_timestamp()-interval '90 seconds',statement_timestamp()-interval '90 seconds',statement_timestamp()+interval '2 hours',jsonb_build_array(),'',1,'fixture-v1'),
 ('uirun-ui17-to','offer-ui17-to',$owner,'uisrc-ui17-to','paid','gpt-test','input','USD',6,1000000,6,'effective-cost/v1','exact','complete',statement_timestamp()-interval '90 seconds',statement_timestamp()-interval '90 seconds',statement_timestamp()+interval '2 hours',jsonb_build_array(),'',1,'fixture-v1');
INSERT INTO upstream_intelligence_links(id,user_id,intelligence_source_id,link_scope,upstream_source_identity,channel_id,price_dimension,status,verified_at)
VALUES
 ('uilink-ui17-from',$owner,'uisrc-ui17-from','channel','','channel-ui17-from','input','active',statement_timestamp()),
 ('uilink-ui17-to',$owner,'uisrc-ui17-to','channel','','channel-ui17-to','input','active',statement_timestamp());
SELECT out_fact_version FROM record_upstream_intelligence_fact_mutation(
  $owner,'collection','uirun-ui17-from'
);
SELECT out_fact_version FROM record_upstream_intelligence_fact_mutation(
  $owner,'collection','uirun-ui17-to'
);
INSERT INTO upstream_cost_fact_versions(user_id,fact_version,updated_at) VALUES ($owner,1,statement_timestamp())
ON CONFLICT(user_id) DO UPDATE SET fact_version=1,updated_at=statement_timestamp();
INSERT INTO upstream_cost_facts(id,idempotency_key,user_id,fact_version,usage_observation_id,channel_id,instance_id,intelligence_source_id,
 model_key,group_key,price_dimension,quantity,per_tokens,price_observation_id,price_effective_at,unit_cost,amount,currency,
 attribution,price_status,calculation_version,missing_fields,occurred_at)
VALUES
 ('ucost-ui17-from','$costKeyA',$owner,1,'usage-ui17-from','channel-ui17-from',$instance,'uisrc-ui17-from','gpt-test','paid','input',1000000,1000000,'offer-ui17-from',(SELECT effective_at FROM upstream_offer_observations WHERE id='offer-ui17-from'),10,10,'USD','exact','valid','upstream-cost-v1',jsonb_build_array(),statement_timestamp()-interval '30 seconds'),
 ('ucost-ui17-to','$costKeyB',$owner,1,'usage-ui17-to','channel-ui17-to',$instance,'uisrc-ui17-to','gpt-test','paid','input',1000000,1000000,'offer-ui17-to',(SELECT effective_at FROM upstream_offer_observations WHERE id='offer-ui17-to'),6,6,'USD','exact','valid','upstream-cost-v1',jsonb_build_array(),statement_timestamp()-interval '30 seconds');
COMMIT;
"@
  [void](Invoke-Postgres $sql)
  $counts = Invoke-Postgres "SELECT (SELECT count(*) FROM upstream_channels)||','||(SELECT count(*) FROM published_bindings)||','||(SELECT count(*) FROM upstream_intelligence_sources)||','||(SELECT count(*) FROM upstream_cost_facts);"
  Assert-Equal $counts "3,3,2,2" "fixture row counts"
  $lineage = Invoke-Postgres "SELECT version.fact_version||'|'||watermark.fact_version||'|'||string_agg(mutation.fact_version||':'||mutation.mutation_kind||':'||mutation.evidence_id,',' ORDER BY mutation.fact_version) FROM upstream_intelligence_fact_versions version JOIN upstream_intelligence_fact_lineage_watermarks watermark USING(user_id) JOIN upstream_intelligence_fact_mutations mutation USING(user_id) WHERE version.user_id=$owner GROUP BY version.fact_version,watermark.fact_version;"
  Assert-Equal $lineage "2|0|1:collection:uirun-ui17-from,2:collection:uirun-ui17-to" "fixture typed intelligence lineage"
}
function Submit-SyntheticQualityObservations {
  param($Context, [string[]]$ChannelIDs, [bool]$Healthy, [string]$Prefix = "ui17")
  if ($ChannelIDs.Count -ne 2) { throw "synthetic quality requires exactly two managed channels" }
  $observations = @()
  $now = [DateTimeOffset]::UtcNow
  foreach ($channelID in $ChannelIDs) {
    for ($index = 0; $index -lt 6; $index++) {
      $observations += @{
        observation_id = "$Prefix-$($channelID.Replace('channel-ui17-',''))-$index-$([guid]::NewGuid().ToString('N').Substring(0,8))"
        channel_id = $channelID; model = "gpt-test"; success = $Healthy
        status_code = $(if ($Healthy) { 200 } else { 503 })
        error_type = $(if ($Healthy) { "" } else { "server_error" })
        first_token_ms = $(if ($Healthy) { 100 } else { 8000 })
        total_ms = $(if ($Healthy) { 300 } else { 30000 })
        source = "passive"; observed_at = $now.ToString("o")
      }
    }
  }
  $response = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/connectors/observations" @{
    connector_id = $Context.ConnectorID; observations = $observations
  } @{ Authorization = "Bearer $($Context.ConnectorToken)" }
  Assert-Equal ([int](Get-JsonValue $response "accepted")) $observations.Count "typed quality observation count"
  return $now
}
function Wait-FreshQualitySnapshot {
  param($Context, [string[]]$ChannelIDs, [DateTimeOffset]$After, [bool]$Healthy)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    $instance = ([string]$Context.InstanceID).Replace("'", "''")
    $ids = @($ChannelIDs | ForEach-Object { "'" + $_.Replace("'", "''") + "'" }) -join ','
    # Snapshot history is minute-bucketed. Once a second bucket is written,
    # counting every post-fence row makes a healthy two-channel result grow
    # from two rows to four and can never satisfy the exact-count gate again.
    # Evaluate only the newest post-fence 5m snapshot for each channel.
    $rows = Invoke-Postgres "SELECT id||'|'||channel_id||'|'||health_state||'|'||quality_sample_count||'|'||created_at FROM (SELECT DISTINCT ON (channel_id) id,channel_id,health_state,quality_sample_count,created_at,bucket_start FROM channel_health_snapshots WHERE instance_id='$instance' AND model='gpt-test' AND `"window`"='5m' AND channel_id IN ($ids) AND created_at > '$($After.UtcDateTime.ToString("o"))' ORDER BY channel_id,bucket_start DESC,created_at DESC) latest ORDER BY channel_id;"
    $lines = @($rows -split '[\r\n]+' | Where-Object { $_ -ne "" })
    $expectedState = if ($Healthy) { "healthy" } else { "unhealthy" }
    $parsed = @($lines | ForEach-Object {
      $parts = $_ -split '\|'
      if ($parts.Count -ne 5) { throw "quality snapshot row had an invalid shape" }
      [pscustomobject]@{ id = $parts[0]; channel_id = $parts[1]; state = $parts[2]; sample_count = [int]$parts[3]; created_at = $parts[4] }
    })
    if ($parsed.Count -eq $ChannelIDs.Count -and
        @($parsed | Select-Object -ExpandProperty id -Unique).Count -eq $ChannelIDs.Count -and
        @($parsed | Select-Object -ExpandProperty channel_id -Unique).Count -eq $ChannelIDs.Count -and
        @($parsed | Where-Object { $_.state -eq $expectedState -and $_.sample_count -ge 5 }).Count -eq $ChannelIDs.Count) {
      foreach ($item in $parsed) { Write-Output $item }
      return
    }
    Start-Sleep -Seconds 1
  }
  $wanted = if ($Healthy) { "healthy" } else { "unhealthy" }
  throw "timed out waiting for fresh $wanted quality snapshots"
}
function Get-MockOpenAIRequests {
  $raw = Invoke-ComposeCapture exec --no-TTY mock-openai wget -qO- http://127.0.0.1:8093/debug/requests
  if ([string]::IsNullOrWhiteSpace($raw)) { throw "mock OpenAI request read-back was empty" }
  $value = $raw | ConvertFrom-Json
  $items = @(Get-JsonArrayItems $value "items")
  Assert-Equal ([int](Get-JsonValue $value "count")) $items.Count "mock OpenAI request count"
  foreach ($item in $items) {
    $hash = [string](Get-JsonValue $item "correlation_sha256")
    [void](ConvertFrom-UtcTimestamp (Get-JsonValue $item "observed_at") "mock OpenAI observed_at")
    if ($hash -notmatch '^[0-9a-f]{64}$') { throw "mock OpenAI request correlation hash was invalid" }
  }
  foreach ($item in $items) { Write-Output $item }
}
function Wait-NewAPILogTimestampAfter {
  param([DateTimeOffset]$After)
  # NewAPI exposes passive log created_at as whole Unix seconds. Wait until
  # the first whole-second boundary strictly after the rollout observation
  # fence so a genuinely later request cannot be rounded back before it.
  $target = [DateTimeOffset]::FromUnixTimeSeconds($After.ToUnixTimeSeconds() + 1)
  while ([DateTimeOffset]::UtcNow -lt $target) { Start-Sleep -Milliseconds 100 }
  return $target
}
function Invoke-RealNewAPITraffic {
  param($Context, $Channels, [DateTimeOffset]$After, [string]$Prefix)
  Write-Step "Sending real OpenAI-compatible traffic through both managed NewAPI channels"
  $trafficStartedAt = Wait-NewAPILogTimestampAfter $After
  $before = @(Get-MockOpenAIRequests)
  $requests = @()
  foreach ($role in @("from", "to")) {
    $remoteID = [string]$Channels[$role].ID
    if ($remoteID -notmatch '^[1-9][0-9]*$') { throw "real traffic target channel was invalid" }
    for ($index = 0; $index -lt 6; $index++) {
      $correlation = "$Project|$Prefix|$role|$index|$([guid]::NewGuid().ToString('N'))"
      $correlationHash = Get-Sha256Hex $correlation
      $specificKey = "sk-$($script:newAPI.RelayToken.Key)-$remoteID"
      $response = Invoke-SafeJson "POST" "$($script:newAPI.BaseUrl)/v1/chat/completions" @{
        model = "gpt-test"; messages = @(@{ role = "user"; content = "ui17" }); stream = $false
      } @{ Authorization = "Bearer $specificKey"; "X-E2M-Correlation" = $correlation }
      $responseID = [string](Get-JsonValue $response "id")
      if ($responseID -notmatch '^chatcmpl-[0-9a-f]{16}$') { throw "real NewAPI relay response identity was invalid" }
      $requests += [ordered]@{
        role = $role; channel_identity_sha256 = Get-Sha256Hex "$Project|$role|$remoteID"
        correlation_sha256 = $correlationHash; response_id_sha256 = Get-Sha256Hex $responseID
        requested_at = [DateTimeOffset]::UtcNow.ToString("o")
      }
    }
  }
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $afterRequests = @()
  while ((Get-Date) -lt $deadline) {
    $afterRequests = @(Get-MockOpenAIRequests)
    if ($afterRequests.Count -ge $before.Count + $requests.Count) { break }
    Start-Sleep -Seconds 1
  }
  Assert-Equal ($afterRequests.Count - $before.Count) $requests.Count "mock OpenAI newly observed request count"
  $newHashes = @($afterRequests | Select-Object -Skip $before.Count | ForEach-Object { [string](Get-JsonValue $_ "correlation_sha256") })
  foreach ($request in $requests) {
    Assert-True ($newHashes -contains [string]$request.correlation_sha256) "mock OpenAI did not observe a correlated relay request"
  }

  $plan = ([string]$Context.PlanID).Replace("'", "''")
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $bindingRows = @()
  while ((Get-Date) -lt $deadline) {
    $rawRows = Invoke-Postgres "SELECT channel_id||'|'||verification_status||'|'||verification_source||'|'||COALESCE(verified_at::text,'') FROM published_bindings WHERE plan_id='$plan' AND channel_id IN ('channel-ui17-from','channel-ui17-to') ORDER BY channel_id;"
    $bindingRows = @($rawRows -split '[\r\n]+' | Where-Object { $_ -ne "" } | ForEach-Object {
      $parts = $_ -split '\|'
      if ($parts.Count -ne 4) { throw "binding verification row had an invalid shape" }
      [pscustomobject]@{ channel_id = $parts[0]; status = $parts[1]; source = $parts[2]; verified_at = $parts[3] }
    })
    if ($bindingRows.Count -eq 2 -and @($bindingRows | Where-Object {
      $_.status -eq "passive_verified" -and $_.source -eq "passive" -and
      (ConvertFrom-UtcTimestamp $_.verified_at "binding verified_at") -ge $After
    }).Count -eq 2) { break }
    Start-Sleep -Seconds 1
  }
  Assert-Equal @($bindingRows | Where-Object {
    $_.status -eq "passive_verified" -and $_.source -eq "passive" -and
    (ConvertFrom-UtcTimestamp $_.verified_at "binding verified_at") -ge $After
  }).Count 2 "real passive binding verification count"
  $snapshots = @(Wait-FreshQualitySnapshot $Context @("channel-ui17-from", "channel-ui17-to") $After $true)
  Assert-Equal $snapshots.Count 2 "real passive quality snapshot count"
  return [pscustomobject]@{
    EvidenceKind = "real_openai_compatible_traffic"; RequestCount = $requests.Count
    PerChannelRequestCount = 6; Requests = $requests; Bindings = $bindingRows; Snapshots = $snapshots
    MockBeforeCount = $before.Count; MockAfterCount = $afterRequests.Count
    EvidenceLowerBound = $After.ToUniversalTime().ToString("o")
    TrafficStartedAt = $trafficStartedAt.ToUniversalTime().ToString("o")
  }
}
function Get-NewAPIWeightSet {
  param($NewAPI, $Channels)
  $actual = @(Get-NewAPIChannelsPaginated $NewAPI)
  if ($actual.Count -ne 3) { throw "NewAPI read-back did not contain exactly three channels" }
  $result = [ordered]@{}
  foreach ($role in @("from", "to", "unrelated")) {
    $id = [string]$Channels[$role].ID
    $match = @($actual | Where-Object { [string](Get-JsonValue $_ "id") -eq $id })
    if ($match.Count -ne 1) { throw "NewAPI read-back omitted or duplicated the $role channel" }
    $weight = Get-JsonValue $match[0] "weight"
    if ($null -eq $weight) { throw "NewAPI read-back returned an unknown $role weight" }
    $result[$role] = [int]$weight
  }
  return $result
}
function Assert-NewAPIWeightSet {
  param($Actual, [int]$From, [int]$To, [int]$Unrelated, [string]$Label)
  Assert-Equal ([int]$Actual.from) $From "$Label from weight"
  Assert-Equal ([int]$Actual.to) $To "$Label to weight"
  Assert-Equal ([int]$Actual.unrelated) $Unrelated "$Label unrelated weight"
  Assert-Equal ([int]$Actual.from + [int]$Actual.to + [int]$Actual.unrelated) 100 "$Label total weight"
  return Get-Sha256Hex "from=$From`nto=$To`nunrelated=$Unrelated`n"
}
function Invoke-RecommendationWorkflow {
  param(
    $Context, $Channels, [string]$ScenarioName, [bool]$CreateExecutionPolicy,
    [string]$WorkerInterval = "1s", [bool]$RestartAfterStart = $true
  )
  Write-Step "Running recommendation, shadow, dry-run and rollout start for scenario $ScenarioName"
  if ($ScenarioName -notin @("A", "B", "C") -or $WorkerInterval -notin @("1s", "1m")) {
    throw "recommendation workflow scenario or worker interval was invalid"
  }
  if (-not $RestartAfterStart -and $WorkerInterval -ne "1m") {
    throw "startup-claim workflow requires the one-minute worker interval"
  }
  $managedChannelIDs = @("channel-ui17-from", "channel-ui17-to")
  $healthyAfter = [DateTimeOffset]::UtcNow
  $realBaseline = Invoke-RealNewAPITraffic $Context $Channels $healthyAfter "baseline"
  $healthySnapshots = @($realBaseline.Snapshots)
  Assert-Equal $healthySnapshots.Count $managedChannelIDs.Count "baseline quality snapshot count"
  # Freeze aggregation after the baseline is durable. Re-aggregating the same
  # scope bumps its fact version, and start must reject a refresh between
  # recommendation/dry-run and the initial rollout generation.
  $freezeReason = "$ScenarioName-freeze-before-generation"
  Restart-DisposableCoreWithIntervals $Context "30m" "30m" "1m" $freezeReason

  $ownerQuery = "user_id=$($Context.UserID)"
  $generation = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/upstream-intelligence/recommendations/generate?$ownerQuery" @{} $Context.AdminHeaders
  $recommendationsProperty = $generation.PSObject.Properties["recommendations"]
  if ($null -eq $recommendationsProperty) { throw "recommendation generation omitted recommendations" }
  $recommendations = @($recommendationsProperty.Value)
  if ($recommendations.Count -ne 1) {
    $blocked = @($generation.PSObject.Properties["blocked"].Value)
    $summary = @($blocked | ForEach-Object { "$([string](Get-JsonValue $_ 'reason')):$([int](Get-JsonValue $_ 'count'))" }) -join ","
    throw "trusted fixture recommendation count=$($recommendations.Count) blocked=$summary"
  }
  $recommendation = $recommendations[0]
  $recommendationID = [string](Get-JsonValue $recommendation "id")
  $recommendationFingerprint = [string](Get-JsonValue $recommendation "fingerprint")
  if ($recommendationID -notmatch '^rec-[0-9a-f]{32}$' -or $recommendationFingerprint -notmatch '^[0-9a-f]{64}$') {
    throw "recommendation identity or fingerprint was invalid"
  }
  Assert-Equal ([string](Get-JsonValue $recommendation "status")) "open" "generated recommendation status"
  Assert-Equal ([string](Get-JsonValue $recommendation "from_channel_id")) "channel-ui17-from" "recommendation source channel"
  Assert-Equal ([string](Get-JsonValue $recommendation "to_channel_id")) "channel-ui17-to" "recommendation destination channel"
  $affectedPlans = @(Get-JsonArrayItems $recommendation "affected_plan_ids")
  if ($affectedPlans.Count -ne 1 -or [string]$affectedPlans[0] -ne [string]$Context.PlanID) {
    throw "recommendation did not bind the exact disposable plan"
  }

  $encodedRecommendationID = [uri]::EscapeDataString($recommendationID)
  $shadowResponse = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/upstream-intelligence/recommendations/$encodedRecommendationID/shadow?$ownerQuery" @{} $Context.AdminHeaders
  $shadowRecommendation = Get-JsonValue $shadowResponse "recommendation"
  $shadow = Get-JsonValue $shadowResponse "experiment"
  Assert-Equal ([string](Get-JsonValue $shadowRecommendation "status")) "ready_for_dry_run" "shadow recommendation status"
  Assert-Equal ([string](Get-JsonValue $shadow "recommendation_id")) $recommendationID "shadow recommendation identity"
  Assert-Equal ([string](Get-JsonValue $shadow "recommendation_fingerprint")) $recommendationFingerprint "shadow fingerprint"
  $winner = Get-JsonValue $shadow "winner"
  Assert-Equal ([string](Get-JsonValue $winner "channel_id")) "channel-ui17-to" "shadow winner"

  $dryRunResponse = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/upstream-intelligence/recommendations/$encodedRecommendationID/dry-run?$ownerQuery" @{} $Context.AdminHeaders
  $dryRecommendation = Get-JsonValue $dryRunResponse "recommendation"
  $dryRun = Get-JsonValue $dryRunResponse "experiment"
  Assert-Equal ([string](Get-JsonValue $dryRecommendation "status")) "dry_run_passed" "dry-run recommendation status"
  Assert-Equal ([string](Get-JsonValue $dryRun "recommendation_id")) $recommendationID "dry-run recommendation identity"
  Assert-Equal ([string](Get-JsonValue $dryRun "recommendation_fingerprint")) $recommendationFingerprint "dry-run fingerprint"
  Assert-Equal ([string](Get-JsonValue $dryRun "plan_id")) ([string]$Context.PlanID) "dry-run plan"
  $dryRunIntent = @(Get-JsonArrayItems $dryRun "desired_scheduling")
  if ($dryRunIntent.Count -ne 2) { throw "dry-run omitted the exact two-channel desired intent" }
  $fromIntent = @($dryRunIntent | Where-Object { [string](Get-JsonValue $_ "channel_id") -eq "channel-ui17-from" })
  $toIntent = @($dryRunIntent | Where-Object { [string](Get-JsonValue $_ "channel_id") -eq "channel-ui17-to" })
  if ($fromIntent.Count -ne 1 -or [bool](Get-JsonValue $fromIntent[0] "enabled") -or
      $toIntent.Count -ne 1 -or -not [bool](Get-JsonValue $toIntent[0] "enabled")) {
    throw "dry-run desired scheduling did not encode the exact recommendation"
  }
  $dryRunActions = @(Get-JsonArrayItems $dryRun "actions")
  # The destination is already enabled in the 80/10/10 baseline. A real
  # planner therefore returns the minimal one-action diff while preserving the
  # full two-channel desired intent above.
  if ($dryRunActions.Count -ne 1 -or [string](Get-JsonValue $dryRunActions[0] "type") -ne "disable" -or
      [string](Get-JsonValue $dryRunActions[0] "channel_id") -ne "channel-ui17-from") {
    throw "dry-run did not return the exact minimal scheduling diff"
  }

  if ($CreateExecutionPolicy) {
    $policy = Invoke-SafeJson "PUT" "$($Context.CoreBase)/api/v1/upstream-intelligence/execution-policies" @{
      user_id = [long]$Context.UserID; scope = "plan"; plan_id = [string]$Context.PlanID
      enabled = $true; kill_switch = $false; daily_execution_cap = 4
      cooldown_seconds = 0; minimum_savings = "0.1"; expected_version = 0
    } $Context.AdminHeaders
    $script:ExecutionPolicyID = [string](Get-JsonValue $policy "id")
  } else {
    $policies = @(Invoke-SafeJson "GET" "$($Context.CoreBase)/api/v1/upstream-intelligence/execution-policies?user_id=$($Context.UserID)" $null $Context.AdminHeaders | ForEach-Object { $_ })
    $matches = @($policies | Where-Object {
      [string](Get-JsonValue $_ "id") -eq $script:ExecutionPolicyID -and
      [string](Get-JsonValue $_ "scope") -eq "plan" -and
      [string](Get-JsonValue $_ "plan_id") -eq [string]$Context.PlanID
    })
    if ($matches.Count -ne 1) { throw "scenario $ScenarioName did not resolve the reusable execution policy" }
    $policy = $matches[0]
  }
  Assert-Equal ([string](Get-JsonValue $policy "scope")) "plan" "execution policy scope"
  Assert-Equal ([string](Get-JsonValue $policy "plan_id")) ([string]$Context.PlanID) "execution policy plan"
  Assert-Equal ([string](Get-JsonValue $policy "id")) $script:ExecutionPolicyID "execution policy identity"
  if (-not [bool](Get-JsonValue $policy "enabled") -or [bool](Get-JsonValue $policy "kill_switch")) {
    throw "execution policy did not authorize forward rollout"
  }

  $startupClaimRestartReason = ""
  $startupClaimGate = $null
  if (-not $RestartAfterStart) {
    # A disposable PostgreSQL trigger rejects only pending->running forward
    # claims while the rollout is created. This removes the timer race without
    # fabricating a claim. The gate is dropped after Core stops; Scenario B then
    # starts the same image and proves its real startup RunOnce acquired lease.
    $startupClaimRestartReason = "scenario-b-arm-startup-claim-window"
    Restart-DisposableCoreWithIntervals $Context "30m" "30m" $WorkerInterval $startupClaimRestartReason
    $startupClaimGate = Install-StartupClaimGate
  }
  $started = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/upstream-intelligence/recommendations/$encodedRecommendationID/rollout?$ownerQuery" @{} $Context.AdminHeaders
  $rolloutID = [string](Get-JsonValue $started "id")
  if ($rolloutID -notmatch '^rec-rollout-[0-9a-f]{32}$') { throw "rollout start omitted a valid opaque identity" }
  $startupClaimArm = $null
  if (-not $RestartAfterStart) {
    $startupClaimArm = Stop-DisposableCoreForStartupClaim $rolloutID $startupClaimGate
    $startupClaimArm.gate_removal = Remove-StartupClaimGate
  }
  Assert-Equal ([string](Get-JsonValue $started "recommendation_id")) $recommendationID "rollout recommendation identity"
  Assert-Equal ([string](Get-JsonValue $started "recommendation_fingerprint")) $recommendationFingerprint "rollout recommendation fingerprint"
  Assert-Equal ([string](Get-JsonValue $started "plan_id")) ([string]$Context.PlanID) "rollout plan"
  Assert-Equal ([int](Get-JsonValue $started "stage")) 0 "rollout initial stage"
  Assert-Equal ([int](Get-JsonValue $started "pending_stage")) 10 "rollout first pending stage"
  if (-not [bool](Get-JsonValue $started "baseline_verified") -or [int](Get-JsonValue $started "account_count") -ne 3) {
    throw "rollout start did not capture and verify the complete three-account baseline"
  }
  $baselineFingerprint = [string](Get-JsonValue $started "baseline_fingerprint")
  if ($baselineFingerprint -notmatch '^[0-9a-f]{64}$') { throw "rollout baseline fingerprint was invalid" }
  # The rollout now owns an immutable generation. Explicit typed observations
  # may produce fresh per-stage evidence while mapping/cost fences stay fixed.
  if ($RestartAfterStart) {
    Restart-DisposableCoreWithIntervals $Context "1s" "30m" $WorkerInterval "$ScenarioName-enable-health-after-start"
  }

  return [pscustomobject]@{
    RecommendationID = $recommendationID; RecommendationFingerprint = $recommendationFingerprint
    ShadowID = [string](Get-JsonValue $shadow "id"); DryRunID = [string](Get-JsonValue $dryRun "id")
    ScenarioName = $ScenarioName; PolicyID = [string](Get-JsonValue $policy "id"); RolloutID = $rolloutID
    BaselineFingerprint = $baselineFingerprint; Started = $started
    BaselinePublicWeightProof = New-PublicRoleWeightProof ([int]$Channels.from.Weight) ([int]$Channels.to.Weight) ([int]$Channels.unrelated.Weight)
    HealthySnapshots = $healthySnapshots; ManagedChannelIDs = $managedChannelIDs; RealBaseline = $realBaseline
    FreezeRestartReason = $freezeReason; StartupClaimRestartReason = $startupClaimRestartReason
    StartupClaimArm = $startupClaimArm
  }
}
function Wait-RolloutState {
  param(
    $Context,
    [string]$RolloutID,
    [string[]]$Statuses,
    [int]$Stage = -1,
    [int]$PendingStage = -1,
    [bool]$RequireSucceededOperation = $false,
    [bool]$RequireRollbackVerified = $false
  )
  if ($RolloutID -notmatch '^rec-rollout-[0-9a-f]{32}$' -or $Statuses.Count -eq 0) {
    throw "rollout wait target is invalid"
  }
  $allowedStatuses = @("ready", "applying", "observing", "rollback_required", "completed", "rolled_back", "blocked")
  foreach ($status in $Statuses) {
    if ($allowedStatuses -notcontains $status) { throw "rollout wait status is invalid" }
  }
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $encodedRolloutID = [uri]::EscapeDataString($RolloutID)
  $lastSummary = "unread"
  while ((Get-Date) -lt $deadline) {
    $value = Invoke-SafeJson "GET" "$($Context.CoreBase)/api/v1/upstream-intelligence/rollouts/${encodedRolloutID}?user_id=$($Context.UserID)" $null $Context.AdminHeaders
    Assert-Equal ([string](Get-JsonValue $value "id")) $RolloutID "rollout read-back identity"
    $status = [string](Get-JsonValue $value "status")
    $actualStage = [int](Get-JsonValue $value "stage")
    $actualPending = [int](Get-JsonValue $value "pending_stage")
    $operation = Get-JsonValue $value "latest_operation"
    $operationStatus = if ($null -eq $operation) { "none" } else { [string](Get-JsonValue $operation "status") }
    $lastSummary = "status=$status stage=$actualStage pending=$actualPending operation=$operationStatus"
    $matches = $Statuses -contains $status
    if ($Stage -ge 0) { $matches = $matches -and $actualStage -eq $Stage }
    if ($PendingStage -ge 0) { $matches = $matches -and $actualPending -eq $PendingStage }
    if ($RequireSucceededOperation) { $matches = $matches -and $operationStatus -eq "succeeded" }
    if ($RequireRollbackVerified) { $matches = $matches -and [bool](Get-JsonValue $value "rollback_verified") }
    if ($matches) { return $value }

    if ($operationStatus -eq "failed") {
      $errorCode = [string](Get-JsonValue $operation "error_code")
      throw "rollout operation failed closed: status=$status stage=$actualStage error_code=$errorCode"
    }
    if ($status -eq "blocked" -and $Statuses -notcontains "blocked") {
      throw "rollout entered an unexpected blocked terminal state"
    }
    if ($status -eq "completed" -and $Statuses -notcontains "completed") {
      throw "rollout completed before the requested acceptance state"
    }
    if ($status -eq "rolled_back" -and $Statuses -notcontains "rolled_back") {
      throw "rollout rolled back before the requested acceptance state"
    }
    Start-Sleep -Seconds 1
  }
  throw "timed out waiting for rollout state ($lastSummary)"
}
function Wait-ObservationWindow {
  param($Rollout, [string]$Label)
  $observeUntil = ConvertFrom-UtcTimestamp (Get-JsonValue $Rollout "observe_until") "$Label observe_until"
  while ([DateTimeOffset]::UtcNow -lt $observeUntil) { Start-Sleep -Milliseconds 200 }
  return $observeUntil
}
function Get-ObservationWindowEvidence {
  param([string]$RolloutID, [int]$ExpectedStage, [string]$Label)
  $rollout = ConvertTo-SqlLiteral $RolloutID
  $row = Invoke-Postgres "SELECT observation_seconds||'|'||stage||'|'||stage_started_at::text||'|'||observe_until::text||'|'||EXTRACT(EPOCH FROM (observe_until-stage_started_at))::text FROM recommendation_rollouts WHERE id=$rollout;"
  $parts = $row -split '\|', -1
  if ($parts.Count -ne 5) { throw "$Label observation window evidence had an invalid shape" }
  $seconds = [int]$parts[0]
  $stage = [int]$parts[1]
  $startedAt = ConvertFrom-UtcTimestamp $parts[2] "$Label stage_started_at"
  $observeUntil = ConvertFrom-UtcTimestamp $parts[3] "$Label observe_until"
  $duration = [decimal]::Parse($parts[4], [Globalization.CultureInfo]::InvariantCulture)
  $expectedSeconds = if ($ReleaseEligible) { 300 } else { 5 }
  Assert-Equal $seconds $expectedSeconds "$Label persisted observation seconds"
  Assert-Equal $stage $ExpectedStage "$Label persisted observation stage"
  Assert-True ($duration -ge [decimal]$expectedSeconds) "$Label persisted observation duration was too short"
  Assert-True ($observeUntil -ge $startedAt.AddSeconds($expectedSeconds)) "$Label observe_until was before its persisted minimum duration"
  return [ordered]@{
    observation_seconds = $seconds; stage = $stage
    stage_started_at = $startedAt.ToString("o"); observe_until = $observeUntil.ToString("o")
    duration_seconds = [string]$duration; minimum_satisfied = $true
  }
}
function Get-ProtocolV3StageTaskEvidence {
  param($Context, [long]$Generation, [int]$Stage, [long]$PreviousObservationID)
  if ($Generation -le 0 -or $Stage -notin @(10, 25, 50, 100) -or $PreviousObservationID -lt 0) {
    throw "protocol-v3 stage task evidence input was invalid"
  }
  $plan = ConvertTo-SqlLiteral ([string]$Context.PlanID)
  $connector = ConvertTo-SqlLiteral ([string]$Context.ConnectorID)
  $rows = Invoke-Postgres "SELECT task.id||'|'||task.plan_id||'|'||task.scheduling_generation||'|'||(task.input->>'account_id')||'|'||(task.input->>'weight')||'|'||string_agg(observation.status,',' ORDER BY observation.observation_id)||'|'||string_agg(observation.observed_at::text,',' ORDER BY observation.observation_id) FROM connector_tasks task JOIN ui17_connector_task_status_observations observation ON observation.task_id=task.id WHERE observation.observation_id>$PreviousObservationID AND task.connector_id=$connector AND task.plan_id=$plan AND task.scheduling_generation=$Generation AND task.type='gateway.account.traffic_share.set' GROUP BY task.id,task.plan_id,task.scheduling_generation,task.input ORDER BY task.id;"
  $lines = @($rows -split '[\r\n]+' | Where-Object { $_ -ne "" })
  if ($lines.Count -lt 1 -or $lines.Count -gt 2) { throw "stage $Stage protocol-v3 evidence expected one or two real weight tasks" }
  $taskEvidence = @()
  $taskExpectations = @()
  $taskIDs = @()
  $terminalUpperBound = [DateTimeOffset]::MinValue
  $executingLowerBound = [DateTimeOffset]::MaxValue
  foreach ($line in $lines) {
    $parts = @($line -split '\|', -1)
    if ($parts.Count -ne 7 -or $parts[0] -notmatch '^ctask-[0-9a-f]{16}$' -or
        $parts[3] -notmatch '^[1-9][0-9]*$' -or $parts[4] -notmatch '^(0|[1-9][0-9]{0,2})$') { throw "stage $Stage protocol-v3 task row had an invalid shape" }
    Assert-Equal $parts[1] ([string]$Context.PlanID) "stage $Stage task plan identity"
    Assert-Equal ([long]$parts[2]) $Generation "stage $Stage task generation"
    $statuses = @($parts[5] -split ',')
    Assert-True ($statuses.Count -eq 4 -and ($statuses -join ',') -eq 'pending,leased,executing,succeeded') "stage $Stage task lifecycle was not pending,leased,executing,succeeded"
    $timestamps = @($parts[6] -split ',')
    Assert-Equal $timestamps.Count 4 "stage $Stage lifecycle timestamp count"
    $parsedTimes = @()
    for ($index = 0; $index -lt $timestamps.Count; $index++) {
      $parsedTimes += ConvertFrom-UtcTimestamp $timestamps[$index] "stage $Stage lifecycle timestamp"
      if ($index -gt 0) { Assert-True ($parsedTimes[$index] -ge $parsedTimes[$index - 1]) "stage $Stage lifecycle timestamps were not monotonic" }
    }
    if ($parsedTimes[2] -lt $executingLowerBound) { $executingLowerBound = $parsedTimes[2] }
    if ($parsedTimes[3] -gt $terminalUpperBound) { $terminalUpperBound = $parsedTimes[3] }
    $taskIDs += $parts[0]
    $taskExpectations += [pscustomobject]@{
      TaskID = $parts[0]; AccountID = $parts[3]; Weight = [int]$parts[4]
      ExecutingAt = $parsedTimes[2]; TerminalAt = $parsedTimes[3]
    }
    $taskEvidence += [ordered]@{
      task_id_sha256 = Get-Sha256Hex $parts[0]
      plan_id = $parts[1]; scheduling_generation = [long]$parts[2]
      target_channel_sha256 = Get-Sha256Hex "$Project|newapi-channel|$($parts[3])"; target_weight = [int]$parts[4]
      lifecycle = @($statuses); observed_at = @($parsedTimes | ForEach-Object { $_.ToString("o") })
    }
  }
  $taskList = @($taskIDs | ForEach-Object { ConvertTo-SqlLiteral $_ }) -join ','
  $observerRange = Invoke-Postgres "SELECT min(observation_id)||'|'||max(observation_id) FROM ui17_connector_task_status_observations WHERE task_id IN ($taskList);"
  $rangeParts = @($observerRange -split '\|', -1)
  if ($rangeParts.Count -ne 2) { throw "stage $Stage observer range had an invalid shape" }
  $executingLiteral = ConvertTo-SqlLiteral $executingLowerBound.UtcDateTime.ToString("o")
  $terminalLiteral = ConvertTo-SqlLiteral $terminalUpperBound.UtcDateTime.ToString("o")
  $weightRows = Invoke-NewAPIPostgresClock "SELECT channel_id||'|'||old_weight||'|'||new_weight||'|'||observed_at::text FROM ui17_channel_weight_observations WHERE observed_at>=$executingLiteral AND observed_at<=$terminalLiteral ORDER BY observation_id;"
  $weightLines = @($weightRows -split '[\r\n]+' | Where-Object { $_ -ne "" })
  Assert-True ($weightLines.Count -ge $lines.Count) "stage $Stage observed too few NewAPI weight writes"
  $weightEvidence = @()
  $parsedWeightRows = @()
  foreach ($line in $weightLines) {
    $parts = @($line -split '\|', -1)
    if ($parts.Count -ne 4 -or $parts[0] -notmatch '^[1-9][0-9]*$') { throw "stage $Stage weight observer row had an invalid shape" }
    $observedAt = ConvertFrom-UtcTimestamp $parts[3] "stage $Stage NewAPI weight observed_at"
    $parsedWeightRows += [pscustomobject]@{ AccountID = $parts[0]; OldWeight = [int]$parts[1]; NewWeight = [int]$parts[2]; ObservedAt = $observedAt }
  }
  $unmatchedWeights = [Collections.Generic.List[object]]::new()
  foreach ($weight in $parsedWeightRows) { $unmatchedWeights.Add($weight) }
  foreach ($expected in @($taskExpectations | Sort-Object ExecutingAt, TaskID)) {
    $matches = @($unmatchedWeights | Where-Object {
      $_.AccountID -eq $expected.AccountID -and $_.NewWeight -eq $expected.Weight -and
      $_.ObservedAt -ge $expected.ExecutingAt -and $_.ObservedAt -le $expected.TerminalAt
    } | Sort-Object ObservedAt)
    Assert-True ($matches.Count -ge 1) "stage $Stage task lacked a NewAPI weight write inside its executing-to-terminal interval"
    $matched = $matches[0]
    [void]$unmatchedWeights.Remove($matched)
    $weightEvidence += [ordered]@{
      task_id_sha256 = Get-Sha256Hex $expected.TaskID
      channel_identity_sha256 = Get-Sha256Hex "$Project|newapi-channel|$($expected.AccountID)"
      old_weight = [int]$matched.OldWeight; new_weight = [int]$matched.NewWeight; observed_at = $matched.ObservedAt.ToString("o")
    }
  }
  return [ordered]@{
    stage = $Stage; plan_id = [string]$Context.PlanID; scheduling_generation = $Generation
    strict_lifecycle = @("pending", "leased", "executing", "succeeded")
    task_count = $taskEvidence.Count; tasks = @($taskEvidence)
    remote_weight_write_count = $weightEvidence.Count; remote_weight_writes = @($weightEvidence)
    executing_preceded_remote_write = $true; remote_write_preceded_terminal = $true
    observer_max_id = [long]$rangeParts[1]
  }
}
function Invoke-OperatorRollback {
  param($Context, $Workflow, $Channels, [string]$Label)
  $encodedRolloutID = [uri]::EscapeDataString([string]$Workflow.RolloutID)
  $response = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/upstream-intelligence/rollouts/$encodedRolloutID/rollback?user_id=$($Context.UserID)" @{} $Context.AdminHeaders
  Assert-Equal ([string](Get-JsonValue $response "id")) ([string]$Workflow.RolloutID) "$Label operator rollback identity"
  $rolledBack = Wait-RolloutState $Context $Workflow.RolloutID @("rolled_back") 0 0 $true $true
  $operation = Get-JsonValue $rolledBack "latest_operation"
  Assert-Equal ([string](Get-JsonValue $operation "action")) "rollback" "$Label rollback action"
  Assert-Equal ([int](Get-JsonValue $operation "target_stage")) 0 "$Label rollback target"
  Assert-Equal ([string](Get-JsonValue $operation "status")) "succeeded" "$Label rollback status"
  $weights = Get-NewAPIWeightSet $script:newAPI $Channels
  [void](Assert-NewAPIWeightSet $weights 80 10 10 "$Label restored baseline")
  return [pscustomobject]@{
    Requested = $response; RolledBack = $rolledBack
    PublicWeightProof = New-PublicRoleWeightProof ([int]$weights.from) ([int]$weights.to) ([int]$weights.unrelated)
  }
}
function Prepare-ScenarioBaseline {
  param($Context, $Channels, [string]$ScenarioName)
  if ($ScenarioName -notin @("A", "B", "C")) { throw "scenario baseline name was invalid" }
  Write-Step "Preparing exact 80/10/10 baseline for scenario $ScenarioName"
  $weights = Get-NewAPIWeightSet $script:newAPI $Channels
  $fingerprint = Assert-NewAPIWeightSet $weights 80 10 10 "$ScenarioName baseline"
  $active = Invoke-Postgres "SELECT count(*) FROM recommendation_rollout_operations WHERE status IN ('pending','running');"
  Assert-Equal ([int]$active) 0 "$ScenarioName active rollout operation count"
  $nonterminal = Invoke-Postgres "SELECT count(*) FROM recommendation_rollouts WHERE status NOT IN ('completed','rolled_back');"
  Assert-Equal ([int]$nonterminal) 0 "$ScenarioName nonterminal rollout count"
  $existingRollouts = Invoke-Postgres "SELECT count(*) FROM recommendation_rollouts;"
  if ($ScenarioName -eq "A") { Assert-Equal ([int]$existingRollouts) 0 "scenario A prior rollout count" }
  if ($ScenarioName -eq "B") { Assert-Equal ([int]$existingRollouts) 1 "scenario B prior rollout count" }
  if ($ScenarioName -eq "C") { Assert-Equal ([int]$existingRollouts) 2 "scenario C prior rollout count" }
  $plan = ConvertTo-SqlLiteral ([string]$Context.PlanID)
  $generation = Invoke-Postgres "SELECT scheduling_generation FROM route_plans WHERE id=$plan;"
  if ([string]$generation -notmatch '^[1-9][0-9]*$') { throw "$ScenarioName plan generation was invalid" }
  $updated = Invoke-Postgres "UPDATE published_bindings SET scheduling_generation=$generation,updated_at=statement_timestamp() WHERE plan_id=$plan RETURNING channel_id;"
  $rows = @($updated -split '[\r\n]+' | Where-Object { $_ -ne "" })
  Assert-Equal $rows.Count 3 "$ScenarioName synchronized binding count"
  $mismatch = Invoke-Postgres "SELECT count(*) FROM published_bindings binding JOIN route_plans plan ON plan.id=binding.plan_id WHERE binding.plan_id=$plan AND binding.scheduling_generation<>plan.scheduling_generation;"
  Assert-Equal ([int]$mismatch) 0 "$ScenarioName binding generation mismatch count"
  return [pscustomobject]@{
    SchedulingGeneration = [long]$generation; BaselineFingerprint = $fingerprint
    BaselinePublicWeightProof = New-PublicRoleWeightProof 80 10 10
  }
}
function Restart-DisposableCoreWithIntervals {
  param($Context, [string]$HealthInterval, [string]$RolloutInterval, [string]$WorkerInterval, [string]$Reason)
  foreach ($value in @($HealthInterval, $RolloutInterval, $WorkerInterval)) {
    if ($value -notmatch '^[1-9][0-9]*(ms|s|m|h)$') { throw "Core restart interval was invalid" }
  }
  if ([string]::IsNullOrWhiteSpace($Reason)) { throw "Core restart reason was empty" }
  Write-Step "Restarting disposable Core for $Reason"
  [Environment]::SetEnvironmentVariable("E2M_UI17_HEALTH_METRICS_INTERVAL", $HealthInterval, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_ROLLOUT_RUNNER_INTERVAL", $RolloutInterval, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_ROLLOUT_WORKER_INTERVAL", $WorkerInterval, "Process")
  Invoke-Compose up --detach --force-recreate --no-deps e2m-core
  Wait-Http "$($Context.CoreBase)/healthz" "restarted E2M Core"
  Record-DisposableCoreRestartEvidence $HealthInterval $RolloutInterval $WorkerInterval $Reason
}
function Record-DisposableCoreRestartEvidence {
  param([string]$HealthInterval, [string]$RolloutInterval, [string]$WorkerInterval, [string]$Reason)
  foreach ($value in @($HealthInterval, $RolloutInterval, $WorkerInterval)) {
    if ($value -notmatch '^[1-9][0-9]*(ms|s|m|h)$') { throw "Core restart evidence interval was invalid" }
  }
  if ([string]::IsNullOrWhiteSpace($Reason)) { throw "Core restart evidence reason was empty" }
  Assert-ComposeProjectLabels $ExpectedServices
  $script:ComposeSHA256AfterRestart = Get-ComposeSHA256
  Assert-True ($script:ComposeSHA256AfterRestart -eq $ComposeSHA256Before) "Compose changed during the Core runner restart"
  $rows = @(Get-DisposableImageEvidence -ExpectedServiceNames $ExpectedServices -InspectionReason "core-restart:$Reason")
  $core = @($rows | Where-Object { $_.service -eq "e2m-core" })
  $before = @($ImageEvidence | Where-Object { $_.service -eq "e2m-core" })
  Assert-True ($core.Count -eq 1 -and $before.Count -eq 1) "Core restart image evidence was incomplete"
  Assert-True ($core[0].image_id -eq $before[0].image_id -and $core[0].config_image -eq $before[0].config_image) "Core restart changed image identity"
  $script:RestartImageEvidence += [ordered]@{
    reason = $Reason; health_interval = $HealthInterval; rollout_interval = $RolloutInterval; worker_interval = $WorkerInterval
    service = $core[0].service; config_image = $core[0].config_image; image_id = $core[0].image_id
  }
  $currentBuildInput = Get-BuildInputManifest
  Assert-BuildInputManifestEqual $currentBuildInput $BuildInputBefore "Core restart"
  if ($null -eq $script:BuildInputAfterRestart) {
    $script:BuildInputAfterRestart = $currentBuildInput
  } else {
    Assert-BuildInputManifestEqual $currentBuildInput $script:BuildInputAfterRestart "subsequent Core restart"
  }
  $script:ImagesVerified = $true
}
function Install-StartupClaimGate {
  $sql = @'
BEGIN;
CREATE OR REPLACE FUNCTION public.e2m_ui17_block_forward_claim()
RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
  IF OLD.status='pending' AND NEW.status='running' AND OLD.action='apply_stage' THEN
    RAISE EXCEPTION USING ERRCODE='P0001', MESSAGE='ui17 startup-claim gate';
  END IF;
  RETURN NEW;
END
$function$;
DROP TRIGGER IF EXISTS e2m_ui17_block_forward_claim ON recommendation_rollout_operations;
CREATE TRIGGER e2m_ui17_block_forward_claim
BEFORE UPDATE ON recommendation_rollout_operations
FOR EACH ROW EXECUTE FUNCTION public.e2m_ui17_block_forward_claim();
COMMIT;
'@
  [void](Invoke-Postgres $sql)
  $script:StartupClaimGateInstalled = $true
  $installed = Invoke-Postgres "SELECT (SELECT count(*) FROM pg_trigger WHERE tgname='e2m_ui17_block_forward_claim' AND tgrelid='recommendation_rollout_operations'::regclass AND NOT tgisinternal)||'|'||(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='e2m_ui17_block_forward_claim');"
  $parts = $installed -split '\|'
  if ($parts.Count -ne 2 -or [int]$parts[0] -ne 1 -or [int]$parts[1] -ne 1) {
    throw "scenario B startup-claim gate installation was incomplete"
  }
  return [ordered]@{
    installed = $true; trigger_count = [int]$parts[0]; function_count = [int]$parts[1]
    transition = "pending_to_running_apply_stage"; behavior = "transaction_rejected"
  }
}
function Remove-StartupClaimGate {
  $sql = @'
DROP TRIGGER IF EXISTS e2m_ui17_block_forward_claim ON recommendation_rollout_operations;
DROP FUNCTION IF EXISTS public.e2m_ui17_block_forward_claim();
'@
  [void](Invoke-Postgres $sql)
  $remaining = Invoke-Postgres "SELECT (SELECT count(*) FROM pg_trigger WHERE tgname='e2m_ui17_block_forward_claim' AND NOT tgisinternal)+(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='e2m_ui17_block_forward_claim');"
  Assert-Equal ([int]$remaining) 0 "scenario B startup-claim gate removal count"
  $script:StartupClaimGateInstalled = $false
  return [ordered]@{ removed = $true; remaining_objects = [int]$remaining }
}
function Stop-DisposableCoreForStartupClaim {
  param([string]$RolloutID, $ClaimGate)
  if ($RolloutID -notmatch '^rec-rollout-[0-9a-f]{32}$') { throw "startup-claim rollout identity was invalid" }
  Assert-True ($null -ne $ClaimGate -and [bool]$ClaimGate.installed -and
    [string]$ClaimGate.transition -eq "pending_to_running_apply_stage" -and
    [string]$ClaimGate.behavior -eq "transaction_rejected") "startup-claim gate evidence was invalid"
  Write-Step "Stopping disposable Core to preserve scenario B pending operation for startup claim"
  Invoke-Compose stop e2m-core
  $containerID = (Invoke-ComposeCapture ps --all --quiet e2m-core).Trim()
  Assert-True ($containerID -match '^[0-9a-f]{12,64}$') "stopped disposable Core container identity was invalid"
  $raw = & docker inspect $containerID 2>$null
  if ($LASTEXITCODE -ne 0) { throw "stopped disposable Core was not inspectable" }
  $item = @(($raw -join "`n") | ConvertFrom-Json)[0]
  Assert-Equal ([string]$item.Config.Labels.'com.docker.compose.project') $Project "stopped disposable Core project label"
  Assert-Equal ([string]$item.Config.Labels.'com.docker.compose.service') "e2m-core" "stopped disposable Core service label"
  Assert-True (-not [bool]$item.State.Running -and [string]$item.State.Status -eq "exited") "disposable Core did not stop before startup claim"
  $rollout = ConvertTo-SqlLiteral $RolloutID
  $pending = Invoke-Postgres "SELECT status||'|'||attempts||'|'||version FROM recommendation_rollout_operations WHERE rollout_id=$rollout AND action='apply_stage' ORDER BY created_at DESC LIMIT 1;"
  $pendingParts = $pending -split '\|'
  if ($pendingParts.Count -ne 3 -or $pendingParts[0] -ne "pending" -or [int]$pendingParts[1] -ne 0 -or [long]$pendingParts[2] -ne 1) {
    throw "scenario B rollout did not remain a fresh pending operation after Core stop"
  }
  return [ordered]@{
    core_stopped = $true; container_status = [string]$item.State.Status
    container_id_sha256 = Get-Sha256Hex ([string]$item.Id)
    operation_status = [string]$pendingParts[0]; operation_attempts = [int]$pendingParts[1]
    operation_version = [long]$pendingParts[2]; claim_gate = $ClaimGate
  }
}
function Enable-FastAutomaticRolloutRunner {
  param($Context)
  Write-Step "Enabling the disposable automatic rollback runner"
  Restart-DisposableCoreWithIntervals $Context "1s" "1s" "1s" "enable-automatic-rollback"
}
function Invoke-CompletionAndRestoreScenario {
  param($Context, $Workflow, $Channels)
  Write-Step "Scenario A: applying 10%, 25%, 50% and 100%, completing, then restoring baseline"
  $stageSpecs = @(
    [pscustomobject]@{ Stage = 10; From = 72; To = 18 },
    [pscustomobject]@{ Stage = 25; From = 60; To = 30 },
    [pscustomobject]@{ Stage = 50; From = 40; To = 50 },
    [pscustomobject]@{ Stage = 100; From = 0; To = 90 }
  )
  $evidence = @()
  [long]$lastProtocolObservationID = 0
  for ($index = 0; $index -lt $stageSpecs.Count; $index++) {
    $spec = $stageSpecs[$index]
    $observing = Wait-RolloutState $Context $Workflow.RolloutID @("observing") $spec.Stage 0 $true $false
    if (-not [bool](Get-JsonValue $observing "last_after_verified") -and $spec.Stage -ne 10) {
      # The latest after-evidence belongs to the preceding observed stage while
      # the new stage is still inside its own observation window.
      $previousStage = [int]$stageSpecs[$index - 1].Stage
      $lastEvidence = Invoke-Postgres "SELECT COALESCE((last_after_evidence->>'stage')::int,0) FROM recommendation_rollouts WHERE id=$(ConvertTo-SqlLiteral $Workflow.RolloutID);"
      Assert-Equal ([int]$lastEvidence) $previousStage "previous-stage after evidence"
    }
    $weights = Get-NewAPIWeightSet $script:newAPI $Channels
    $weightFingerprint = Assert-NewAPIWeightSet $weights $spec.From $spec.To 10 "stage $($spec.Stage)% read-back"
    $operation = Get-JsonValue $observing "latest_operation"
    Assert-Equal ([string](Get-JsonValue $operation "action")) "apply_stage" "stage $($spec.Stage)% operation action"
    Assert-Equal ([int](Get-JsonValue $operation "target_stage")) $spec.Stage "stage $($spec.Stage)% operation target"
    $windowEvidence = Get-ObservationWindowEvidence $Workflow.RolloutID $spec.Stage "scenario A stage $($spec.Stage)%"
    $generation = Invoke-Postgres "SELECT scheduling_generation FROM recommendation_rollouts WHERE id=$(ConvertTo-SqlLiteral ([string]$Workflow.RolloutID));"
    if ([string]$generation -notmatch '^[1-9][0-9]*$') { throw "scenario A stage $($spec.Stage)% generation was invalid" }
    $protocolV3 = Get-ProtocolV3StageTaskEvidence $Context ([long]$generation) $spec.Stage $lastProtocolObservationID
    $lastProtocolObservationID = [long]$protocolV3.observer_max_id
    $evidence += [pscustomobject]@{
      stage = $spec.Stage; status = [string](Get-JsonValue $observing "status")
      readback_fingerprint = $weightFingerprint; observed_until = [string](Get-JsonValue $observing "observe_until")
      operation_status = [string](Get-JsonValue $operation "status")
      from_weight = [int]$weights.from; to_weight = [int]$weights.to; unrelated_weight = [int]$weights.unrelated
      observation_window = $windowEvidence; protocol_v3 = $protocolV3
    }
    [void](Wait-ObservationWindow $observing "scenario A stage $($spec.Stage)%")
    $qualityAfter = [DateTimeOffset]::UtcNow
    $traffic = Invoke-RealNewAPITraffic $Context $Channels $qualityAfter "scenario-a-stage-$($spec.Stage)"
    $evidence[$evidence.Count - 1] | Add-Member -NotePropertyName real_traffic -NotePropertyValue $traffic
    $advanced = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/upstream-intelligence/rollouts/$([uri]::EscapeDataString($Workflow.RolloutID))/advance?user_id=$($Context.UserID)" @{} $Context.AdminHeaders
    Assert-Equal ([string](Get-JsonValue $advanced "id")) ([string]$Workflow.RolloutID) "rollout advance identity"
    if ($spec.Stage -eq 100) {
      Assert-Equal ([string](Get-JsonValue $advanced "status")) "completed" "scenario A completed status"
      Assert-Equal ([int](Get-JsonValue $advanced "stage")) 100 "scenario A completed stage"
    } else {
      Assert-Equal ([int](Get-JsonValue $advanced "pending_stage")) ([int]$stageSpecs[$index + 1].Stage) "rollout next pending stage"
    }
  }
  $completed = Wait-RolloutState $Context $Workflow.RolloutID @("completed") 100 0 $false $false
  Assert-True ([bool](Get-JsonValue $completed "last_after_verified")) "scenario A completed state lacked verified 100% after evidence"
  $restored = Invoke-OperatorRollback $Context $Workflow $Channels "scenario A post-completion"
  $auditQuery = "SELECT count(*) FROM operation_audits WHERE user_id=$($Context.UserID) AND target_id=$(ConvertTo-SqlLiteral ([string]$Workflow.RolloutID)) AND action='upstream_recommendation.rollout.rollback' AND result='accepted';"
  $auditDeadline = (Get-Date).AddSeconds(30)
  do {
    $auditCount = Invoke-Postgres $auditQuery
    if ([int]$auditCount -eq 1) { break }
    Start-Sleep -Milliseconds 100
  } while ((Get-Date) -lt $auditDeadline)
  Assert-Equal ([int]$auditCount) 1 "scenario A accepted operator rollback audit count"
  return [pscustomobject]@{
    Name = "completion"; Workflow = $Workflow; RolloutID = $Workflow.RolloutID
    Stages = $evidence; Completed = $completed; Restoration = $restored; AcceptedOperatorRollbackAuditCount = [int]$auditCount
  }
}
function Wait-RunningForwardLease {
  param([string]$RolloutID)
  $rollout = ConvertTo-SqlLiteral $RolloutID
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $last = ""
  while ((Get-Date) -lt $deadline) {
    $last = Invoke-Postgres "SELECT id||'|'||version||'|'||lease_owner||'|'||attempts||'|'||CASE WHEN lease_until>statement_timestamp() THEN 'live' ELSE 'expired' END FROM recommendation_rollout_operations WHERE rollout_id=$rollout AND action='apply_stage' AND status='running' ORDER BY created_at DESC LIMIT 1;"
    if ($last -ne "") {
      $parts = $last -split '\|'
      if ($parts.Count -eq 5 -and $parts[0] -match '^rec-rollout-op-[0-9a-f]{16}$' -and
          [long]$parts[1] -gt 0 -and $parts[2] -ne "" -and [int]$parts[3] -eq 1 -and $parts[4] -eq "live") {
        return [pscustomobject]@{
          OperationID = $parts[0]; OperationVersion = [long]$parts[1]
          LeaseOwner = $parts[2]; Attempts = [int]$parts[3]; LeaseLive = $true
        }
      }
    }
    Start-Sleep -Milliseconds 200
  }
  throw "timed out waiting for a live running forward lease ($last)"
}
function Invoke-LiveStaleCASProof {
  param($Takeover)
  Write-Step "Proving stale worker renew and complete CAS conflicts against live PostgreSQL"
  $previous = @{}
  $names = @(
    "E2M_UI17_STALE_CAS_PROOF", "E2M_TEST_POSTGRES_DSN", "E2M_UI17_STALE_ROLLOUT_ID",
    "E2M_UI17_STALE_OPERATION_ID", "E2M_UI17_STALE_LEASE_OWNER", "E2M_UI17_STALE_OPERATION_VERSION",
    "E2M_UI17_STALE_ROLLOUT_VERSION"
  )
  foreach ($name in $names) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, "Process") }
  try {
    $dsn = "postgres://e2m_ui17:e2m_ui17@127.0.0.1:$PostgresPort/e2m_ui17?sslmode=disable"
    [Environment]::SetEnvironmentVariable("E2M_UI17_STALE_CAS_PROOF", "1", "Process")
    [Environment]::SetEnvironmentVariable("E2M_TEST_POSTGRES_DSN", $dsn, "Process")
    [Environment]::SetEnvironmentVariable("E2M_UI17_STALE_ROLLOUT_ID", [string]$Takeover.RolloutID, "Process")
    [Environment]::SetEnvironmentVariable("E2M_UI17_STALE_OPERATION_ID", [string]$Takeover.OperationID, "Process")
    [Environment]::SetEnvironmentVariable("E2M_UI17_STALE_LEASE_OWNER", [string]$Takeover.LeaseOwner, "Process")
    [Environment]::SetEnvironmentVariable("E2M_UI17_STALE_OPERATION_VERSION", [string]$Takeover.OperationVersion, "Process")
    [Environment]::SetEnvironmentVariable("E2M_UI17_STALE_ROLLOUT_VERSION", [string]$Takeover.RolloutVersion, "Process")
    $operationHash = Get-Sha256Hex ([string]$Takeover.OperationID)
    $output = & go test ./internal/store -run '^TestUI17LiveStaleRolloutCAS$' -count=1 -v 2>&1 |
      ForEach-Object { [string]$_ }
    if ($LASTEXITCODE -ne 0) { throw "live stale CAS proof failed" }
    $joined = ($output -join "`n")
    Assert-True ($joined -match 'UI17_STALE_CAS_PASS operation_sha256=[0-9a-f]{64} renew=conflict complete=conflict') "live stale CAS proof omitted its PASS marker"
    Assert-True ($joined -match "UI17_STALE_CAS_PASS operation_sha256=$operationHash renew=conflict complete=conflict") "live stale CAS proof did not bind the stale operation hash"
    Assert-True ($joined.IndexOf([string]$Takeover.LeaseOwner, [StringComparison]::Ordinal) -lt 0) "live stale CAS output exposed its lease owner"
    Assert-True ($joined.IndexOf([string]$Takeover.OperationID, [StringComparison]::Ordinal) -lt 0) "live stale CAS output exposed its operation identity"
    return [ordered]@{
      test = "TestUI17LiveStaleRolloutCAS"
      output_sha256 = Get-Sha256Hex $joined
      stale_operation_sha256 = $operationHash
      pass_marker = "UI17_STALE_CAS_PASS"
      renew = "conflict"; complete = "conflict"; state_unchanged = $true
    }
  } finally {
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name, $previous[$name], "Process") }
  }
}
function Invoke-OperatorTakeoverScenario {
  param($Context, $Workflow, $Channels)
  Write-Step "Scenario B: atomically preempting a real running forward lease"
  $rollout = ConvertTo-SqlLiteral ([string]$Workflow.RolloutID)
  $connectorPaused = $false
  try {
    $preClaim = Invoke-Postgres "SELECT operation.id||'|'||operation.status||'|'||operation.attempts||'|'||operation.version||'|'||rollout.version||'|'||rollout.scheduling_generation FROM recommendation_rollout_operations operation JOIN recommendation_rollouts rollout ON rollout.id=operation.rollout_id WHERE operation.rollout_id=$rollout AND operation.action='apply_stage' ORDER BY operation.created_at DESC LIMIT 1;"
    $preParts = $preClaim -split '\|'
    if ($preParts.Count -ne 6 -or $preParts[0] -notmatch '^rec-rollout-op-[0-9a-f]{16}$' -or
        $preParts[1] -ne "pending" -or [int]$preParts[2] -ne 0 -or [long]$preParts[3] -ne 1 -or
        [long]$preParts[4] -le 0 -or [long]$preParts[5] -le 0) {
      throw "scenario B forward operation was not pending before the takeover drill"
    }
    $freezeRestart = @($RestartImageEvidence | Where-Object { [string]$_.reason -eq [string]$Workflow.FreezeRestartReason })
    Assert-Equal $freezeRestart.Count 1 "scenario B pre-start freeze restart evidence count"
    Assert-Equal ([string]$freezeRestart[0].worker_interval) "1m" "scenario B pre-start worker interval"
    Assert-True ($null -ne $Workflow.StartupClaimArm -and [bool]$Workflow.StartupClaimArm.core_stopped -and
      [string]$Workflow.StartupClaimArm.container_status -eq "exited" -and
      [string]$Workflow.StartupClaimArm.container_id_sha256 -match '^[0-9a-f]{64}$' -and
      [string]$Workflow.StartupClaimArm.operation_status -eq "pending" -and
      [int]$Workflow.StartupClaimArm.operation_attempts -eq 0 -and
      [long]$Workflow.StartupClaimArm.operation_version -eq 1 -and
      [bool]$Workflow.StartupClaimArm.claim_gate.installed -and
      [int]$Workflow.StartupClaimArm.claim_gate.trigger_count -eq 1 -and
      [int]$Workflow.StartupClaimArm.claim_gate.function_count -eq 1 -and
      [bool]$Workflow.StartupClaimArm.gate_removal.removed -and
      [int]$Workflow.StartupClaimArm.gate_removal.remaining_objects -eq 0) "scenario B did not stop Core around the gated fresh pending operation"
    $startupClaimRestart = @($RestartImageEvidence | Where-Object { [string]$_.reason -eq [string]$Workflow.StartupClaimRestartReason })
    Assert-Equal $startupClaimRestart.Count 1 "scenario B startup-claim arm restart evidence count"
    Assert-Equal ([string]$startupClaimRestart[0].worker_interval) "1m" "scenario B startup-claim arm worker interval"
    # Core is stopped and the exact operation is still pending here. Pause the
    # Connector before starting Core so startup RunOnce acquires a real live
    # lease but cannot finish the gateway interaction before preemption.
    Invoke-Compose pause connector-newapi
    $connectorPaused = $true
    $script:ConnectorPausedByRunner = $true
    [Environment]::SetEnvironmentVariable("E2M_UI17_HEALTH_METRICS_INTERVAL", "30m", "Process")
    [Environment]::SetEnvironmentVariable("E2M_UI17_ROLLOUT_RUNNER_INTERVAL", "30m", "Process")
    [Environment]::SetEnvironmentVariable("E2M_UI17_ROLLOUT_WORKER_INTERVAL", "1m", "Process")
    Invoke-Compose up --detach --force-recreate --no-deps e2m-core
    $lease = Wait-RunningForwardLease $Workflow.RolloutID
    Wait-Http "$($Context.CoreBase)/healthz" "scenario B restarted E2M Core"
    Record-DisposableCoreRestartEvidence "30m" "30m" "1m" "scenario-b-claim-blocked-forward"
    Assert-Equal ([string]$lease.OperationID) ([string]$preParts[0]) "scenario B claimed operation identity"
    $before = Invoke-Postgres "SELECT version||'|'||scheduling_generation FROM recommendation_rollouts WHERE id=$rollout;"
    $beforeParts = $before -split '\|'
    if ($beforeParts.Count -ne 2) { throw "scenario B rollout pre-takeover snapshot was invalid" }
    $takeover = [pscustomobject]@{
      RolloutID = [string]$Workflow.RolloutID; OperationID = [string]$lease.OperationID
      OperationVersion = [long]$lease.OperationVersion; LeaseOwner = [string]$lease.LeaseOwner
      RolloutVersion = [long]$beforeParts[0]; SchedulingGeneration = [long]$beforeParts[1]
    }
    $requested = Invoke-SafeJson "POST" "$($Context.CoreBase)/api/v1/upstream-intelligence/rollouts/$([uri]::EscapeDataString($Workflow.RolloutID))/rollback?user_id=$($Context.UserID)" @{} $Context.AdminHeaders
    Assert-Equal ([string](Get-JsonValue $requested "id")) ([string]$Workflow.RolloutID) "scenario B rollback request identity"

    $oldOperation = ConvertTo-SqlLiteral ([string]$takeover.OperationID)
    $post = Invoke-Postgres "SELECT operation.status||'|'||operation.version||'|'||operation.lease_owner||'|'||COALESCE(operation.lease_until::text,'')||'|'||rollout.version||'|'||rollout.scheduling_generation||'|'||plan.scheduling_generation FROM recommendation_rollout_operations operation JOIN recommendation_rollouts rollout ON rollout.id=operation.rollout_id JOIN route_plans plan ON plan.id=rollout.plan_id WHERE operation.id=$oldOperation;"
    $postParts = $post -split '\|', -1
    if ($postParts.Count -ne 7) { throw "scenario B takeover post-state was invalid" }
    Assert-Equal $postParts[0] "superseded" "scenario B stale operation status"
    Assert-Equal ([long]$postParts[1]) ($takeover.OperationVersion + 1) "scenario B stale operation version"
    Assert-Equal $postParts[2] "" "scenario B stale operation lease owner"
    Assert-Equal $postParts[3] "" "scenario B stale operation lease until"
    Assert-Equal ([long]$postParts[4]) ($takeover.RolloutVersion + 1) "scenario B rollout takeover version"
    Assert-Equal ([long]$postParts[5]) ($takeover.SchedulingGeneration + 1) "scenario B rollout takeover generation"
    Assert-Equal ([long]$postParts[6]) ([long]$postParts[5]) "scenario B plan/rollout generation"
    Assert-Equal ([int](Get-JsonValue $requested "scheduling_generation")) ([long]$postParts[5]) "scenario B HTTP takeover generation"
    Assert-Equal ([string](Get-JsonValue $requested "status")) "rollback_required" "scenario B HTTP takeover status"
    $rollbackOperation = Invoke-Postgres "SELECT id||'|'||status||'|'||target_stage FROM recommendation_rollout_operations WHERE rollout_id=$rollout AND action='rollback' ORDER BY created_at DESC LIMIT 1;"
    $rollbackParts = $rollbackOperation -split '\|'
    if ($rollbackParts.Count -ne 3 -or $rollbackParts[1] -ne "pending" -or [int]$rollbackParts[2] -ne 0) {
      throw "scenario B did not create exactly one pending baseline rollback"
    }
    $requestedOperation = Get-JsonValue $requested "latest_operation"
    Assert-True ($null -ne $requestedOperation) "scenario B mutation response omitted its pending rollback operation"
    Assert-Equal ([string](Get-JsonValue $requestedOperation "action")) "rollback" "scenario B HTTP rollback operation action"
    Assert-Equal ([string](Get-JsonValue $requestedOperation "status")) "pending" "scenario B HTTP rollback operation status"
    Assert-Equal ([int](Get-JsonValue $requestedOperation "target_stage")) 0 "scenario B HTTP rollback operation target"
    Assert-Equal ([string](Get-JsonValue $requestedOperation "id")) ([string]$rollbackParts[0]) "scenario B HTTP/SQL rollback operation identity"
    $activeOperationCount = Invoke-Postgres "SELECT count(*) FROM recommendation_rollout_operations WHERE rollout_id=$rollout AND status IN ('pending','running');"
    Assert-Equal ([int]$activeOperationCount) 1 "scenario B unique active rollback operation count"
    $reasonCount = Invoke-Postgres "SELECT count(*) FROM recommendation_rollouts rollout WHERE rollout.id=$rollout AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(rollout.rollback_reasons) AS reason(value) WHERE reason.value='operator_requested');"
    Assert-Equal ([int]$reasonCount) 1 "scenario B operator-requested reason count"
    $reasonCardinality = Invoke-Postgres "SELECT jsonb_array_length(rollback_reasons) FROM recommendation_rollouts WHERE id=$rollout;"
    Assert-Equal ([int]$reasonCardinality) 1 "scenario B rollback reason cardinality"
    $auditQuery = "SELECT count(*) FROM operation_audits WHERE user_id=$($Context.UserID) AND target_id=$rollout AND action='upstream_recommendation.rollout.rollback' AND result='accepted';"
    $auditDeadline = (Get-Date).AddSeconds(30)
    do {
      $auditCount = Invoke-Postgres $auditQuery
      if ([int]$auditCount -eq 1) { break }
      Start-Sleep -Milliseconds 100
    } while ((Get-Date) -lt $auditDeadline)
    Assert-Equal ([int]$auditCount) 1 "scenario B accepted operator rollback audit count"
  } finally {
    if ($connectorPaused) {
      Invoke-Compose unpause connector-newapi
      $script:ConnectorPausedByRunner = $false
    }
  }

  $rolledBack = Wait-RolloutState $Context $Workflow.RolloutID @("rolled_back") 0 0 $true $true
  $weights = Get-NewAPIWeightSet $script:newAPI $Channels
  [void](Assert-NewAPIWeightSet $weights 80 10 10 "scenario B restored baseline")
  $staleOperation = ConvertTo-SqlLiteral ([string]$takeover.OperationID)
  $stalePostRollback = Invoke-Postgres "SELECT status||'|'||version||'|'||lease_owner||'|'||COALESCE(lease_until::text,'') FROM recommendation_rollout_operations WHERE id=$staleOperation;"
  $stalePostParts = $stalePostRollback -split '\|', -1
  if ($stalePostParts.Count -ne 4) { throw "scenario B stale post-rollback snapshot was invalid" }
  Assert-Equal $stalePostParts[0] "superseded" "scenario B stale operation terminal status"
  Assert-Equal ([long]$stalePostParts[1]) ($takeover.OperationVersion + 1) "scenario B stale operation stable version"
  Assert-Equal $stalePostParts[2] "" "scenario B stale operation final lease owner"
  Assert-Equal $stalePostParts[3] "" "scenario B stale operation final lease until"
  Push-Location (Join-Path $RepoRoot "app/e2m-core")
  try { $casProof = Invoke-LiveStaleCASProof $takeover } finally { Pop-Location }
  return [pscustomobject]@{
    Name = "operator_takeover"; Workflow = $Workflow; RolloutID = $Workflow.RolloutID
    Takeover = [ordered]@{
      core_stopped_before_startup_claim = [bool]$Workflow.StartupClaimArm.core_stopped
      stopped_core_status = [string]$Workflow.StartupClaimArm.container_status
      stopped_core_id_sha256 = [string]$Workflow.StartupClaimArm.container_id_sha256
      claim_gate_installed = [bool]$Workflow.StartupClaimArm.claim_gate.installed
      claim_gate_transition = [string]$Workflow.StartupClaimArm.claim_gate.transition
      claim_gate_removed_before_startup = [bool]$Workflow.StartupClaimArm.gate_removal.removed
      claim_gate_remaining_objects = [int]$Workflow.StartupClaimArm.gate_removal.remaining_objects
      startup_run_once_claimed_forward = ([int]$lease.Attempts -eq 1 -and [bool]$lease.LeaseLive)
      preclaim_status = [string]$preParts[1]; preclaim_attempts = [int]$preParts[2]
      preclaim_operation_version = [long]$preParts[3]
      claimed_attempts = [int]$lease.Attempts; live_lease_before_takeover = [bool]$lease.LeaseLive
      startup_claim_restart_reason = [string]$Workflow.StartupClaimRestartReason
      stale_operation_status = "superseded"; stale_operation_version_increment = 1
      stale_lease_cleared = $true; rollout_version_increment = 1; generation_increment = 1
      rollback_operation_status_at_takeover = "pending"; rollback_target_stage = 0
      operator_reason = "operator_requested"; accepted_operator_audit_count = 1
    }
    RolledBack = $rolledBack; StaleCAS = $casProof
    RollbackPublicWeightProof = New-PublicRoleWeightProof 80 10 10
  }
}
function Invoke-AutomaticRollbackScenario {
  param($Context, $Workflow, $Channels)
  Write-Step "Scenario C: applying only 10%, injecting regression and proving automatic rollback"
  $stage10 = Wait-RolloutState $Context $Workflow.RolloutID @("observing") 10 0 $true $false
  $weights10 = Get-NewAPIWeightSet $script:newAPI $Channels
  $stage10Fingerprint = Assert-NewAPIWeightSet $weights10 72 18 10 "scenario C stage 10% read-back"
  $stage10Window = Get-ObservationWindowEvidence $Workflow.RolloutID 10 "scenario C stage 10%"
  [void](Wait-ObservationWindow $stage10 "scenario C stage 10%")
  $healthyAfter = [DateTimeOffset]::UtcNow
  $stage10Traffic = Invoke-RealNewAPITraffic $Context $Channels $healthyAfter "scenario-c-stage-10"

  # This is intentionally typed Connector observation input, but synthetic
  # fixture quality. It proves the rollback control path, not model callability.
  $unhealthyAfter = Submit-SyntheticQualityObservations $Context $Workflow.ManagedChannelIDs $false "ui17-scenario-c-regression"
  $unhealthySnapshots = @(Wait-FreshQualitySnapshot $Context $Workflow.ManagedChannelIDs $unhealthyAfter $false)
  Assert-Equal $unhealthySnapshots.Count $Workflow.ManagedChannelIDs.Count "synthetic unhealthy snapshot count"
  $unhealthyEvidence = @($unhealthySnapshots | ForEach-Object {
    [ordered]@{
      id = [string]$_.id
      channel_id = [string]$_.channel_id
      window = "5m"
      state = [string]$_.state
      generated_at = [string]$_.created_at
    }
  })
  Assert-Equal $unhealthyEvidence.Count $Workflow.ManagedChannelIDs.Count "synthetic unhealthy evidence count"
  # Keep Runner out of every manual stage boundary, then restart only this
  # disposable Core with a fast cadence after the bad evidence is durable.
  # Runner calls Controller.Advance; the script never calls /rollback.
  Enable-FastAutomaticRolloutRunner $Context
  $rollbackRequired = Wait-RolloutState $Context $Workflow.RolloutID @("rollback_required", "rolled_back") -1 0 $false $false
  $rollbackReasons = @(Get-JsonArrayItems $rollbackRequired "rollback_reasons")
  Assert-True ($rollbackReasons.Count -eq 1 -and $rollbackReasons[0] -is [string] -and
    [string]$rollbackReasons[0] -eq "quality_failed") "automatic regression did not record exactly quality_failed"

  $rolledBack = Wait-RolloutState $Context $Workflow.RolloutID @("rolled_back") 0 0 $true $true
  $finalRollbackReasons = @(Get-JsonArrayItems $rolledBack "rollback_reasons")
  Assert-True ($finalRollbackReasons.Count -eq 1 -and $finalRollbackReasons[0] -is [string] -and
    [string]$finalRollbackReasons[0] -eq "quality_failed") "automatic rollback did not preserve exactly quality_failed"
  Assert-Equal ([string](Get-JsonValue $rolledBack "recommendation_id")) ([string]$Workflow.RecommendationID) "rolled-back recommendation identity"
  Assert-Equal ([string](Get-JsonValue $rolledBack "baseline_fingerprint")) ([string]$Workflow.BaselineFingerprint) "rolled-back baseline fingerprint"
  if ([bool](Get-JsonValue $rolledBack "last_after_verified")) {
    throw "rollback scheduling proof was incorrectly presented as forward health evidence"
  }
  $rollbackOperation = Get-JsonValue $rolledBack "latest_operation"
  Assert-Equal ([string](Get-JsonValue $rollbackOperation "action")) "rollback" "automatic rollback operation action"
  Assert-Equal ([int](Get-JsonValue $rollbackOperation "target_stage")) 0 "automatic rollback operation target"
  Assert-Equal ([string](Get-JsonValue $rollbackOperation "status")) "succeeded" "automatic rollback operation status"
  $baselineWeights = Get-NewAPIWeightSet $script:newAPI $Channels
  [void](Assert-NewAPIWeightSet $baselineWeights 80 10 10 "automatic rollback baseline read-back")

  $proofID = "weight-set-sha256:$($Workflow.BaselineFingerprint)"
  $storedProof = Invoke-Postgres "SELECT COALESCE(last_after_evidence->'evidence_ids'->>0,'') FROM recommendation_rollouts WHERE id=$(ConvertTo-SqlLiteral $Workflow.RolloutID);"
  Assert-Equal $storedProof $proofID "automatic rollback proof"
  $operatorRollbackAuditQuery = "SELECT count(*) FROM operation_audits WHERE user_id=$($Context.UserID) AND target_id=$(ConvertTo-SqlLiteral $Workflow.RolloutID) AND action='upstream_recommendation.rollout.rollback' AND result='accepted';"
  $operatorRollbackAudits = Invoke-Postgres $operatorRollbackAuditQuery
  Assert-Equal ([int]$operatorRollbackAudits) 0 "operator rollback audit count"
  $runnerRollbackOperation = Invoke-Postgres "SELECT jsonb_build_object('id',id,'action',action,'status',status,'target_stage',target_stage,'created_at',created_at,'updated_at',updated_at)::text FROM recommendation_rollout_operations WHERE rollout_id=$(ConvertTo-SqlLiteral $Workflow.RolloutID) AND action='rollback' ORDER BY created_at DESC LIMIT 1;"
  if ([string]::IsNullOrWhiteSpace($runnerRollbackOperation)) { throw "automatic rollback operation evidence was missing" }
  $runnerRollbackEvidence = $runnerRollbackOperation | ConvertFrom-Json
  Assert-Equal ([string](Get-JsonValue $runnerRollbackEvidence "action")) "rollback" "runner rollback evidence action"
  Assert-Equal ([string](Get-JsonValue $runnerRollbackEvidence "status")) "succeeded" "runner rollback evidence status"
  Assert-Equal ([int](Get-JsonValue $runnerRollbackEvidence "target_stage")) 0 "runner rollback evidence target"

  return [pscustomobject]@{
    Name = "automatic_regression"; Workflow = $Workflow; RolloutID = $Workflow.RolloutID
    Stage10 = $stage10; Stage10Fingerprint = $stage10Fingerprint; Stage10Traffic = $stage10Traffic; Stage10ObservationWindow = $stage10Window
    RegressionKind = "synthetic_quality"; UnhealthySnapshots = $unhealthySnapshots; UnhealthyEvidence = $unhealthyEvidence
    RollbackRequired = $rollbackRequired; RolledBack = $rolledBack; RollbackProof = $proofID
    RunnerRollbackEvidence = $runnerRollbackEvidence
    RollbackPublicWeightProof = New-PublicRoleWeightProof ([int]$baselineWeights.from) ([int]$baselineWeights.to) ([int]$baselineWeights.unrelated)
    OperatorRollbackAudit = [ordered]@{
      query_text = $operatorRollbackAuditQuery
      query_sha256 = Get-Sha256Hex $operatorRollbackAuditQuery
      table = "operation_audits"
      target_id = [string]$Workflow.RolloutID
      action = "upstream_recommendation.rollout.rollback"
      expected_count = 0
      actual_count = [int]$operatorRollbackAudits
      assertion = "actual_count_equals_expected_count"
      passed = ([int]$operatorRollbackAudits -eq 0)
    }
  }
}
function Write-RedactedEvidence {
  param([bool]$ReleasePass, [string]$FailureCode)
  Write-Step "Writing redacted disposable NewAPI evidence"
  $scenarioCReasons = @()
  if ($null -ne $ScenarioC -and $null -ne $ScenarioC.RolledBack) {
    $scenarioCReasons = @(Get-JsonArrayItems $ScenarioC.RolledBack "rollback_reasons")
  }
  if ($ReleasePass) {
    Assert-True ($BusinessPassed -and $DisposableRemoved -and $RuntimeRemoved -and $EnvironmentRestored) "PASS evidence lacked business or cleanup proof"
    Assert-True ($ProtectedUnchanged -and $ComposeUnchanged -and $RunnerUnchanged -and $BuildInputUnchanged -and $ImagesVerified -and $BuiltImagesProvenanceBound) "PASS evidence lacked protected-stack, source, Compose, or image proof"
    Assert-True ($null -eq $PrimaryFailure -and $null -eq $CleanupFailure) "PASS evidence was attempted after a failure"
    Assert-True (-not $StartupClaimGateInstalled) "PASS evidence retained the temporary scenario B claim gate"
    Assert-True ($null -ne $Context -and $null -ne $ScenarioA -and $null -ne $ScenarioB -and $null -ne $ScenarioC) "PASS evidence lacked all three scenario results"
    $rolloutIDs = @([string]$ScenarioA.RolloutID, [string]$ScenarioB.RolloutID, [string]$ScenarioC.RolloutID)
    Assert-Equal @($rolloutIDs | Sort-Object -Unique).Count 3 "independent scenario rollout identity count"
    Assert-Equal ([string](Get-JsonValue $ScenarioA.Completed "status")) "completed" "scenario A completion evidence"
    Assert-Equal ([string](Get-JsonValue $ScenarioA.Restoration.RolledBack "status")) "rolled_back" "scenario A restoration evidence"
    Assert-Equal ([int]$ScenarioA.AcceptedOperatorRollbackAuditCount) 1 "scenario A accepted operator rollback audit evidence"
    Assert-Equal ([string](Get-JsonValue $ScenarioB.RolledBack "status")) "rolled_back" "scenario B takeover evidence"
    Assert-Equal ([int]$ScenarioB.Takeover.accepted_operator_audit_count) 1 "scenario B accepted operator rollback audit evidence"
    Assert-True ([string]$ScenarioB.StaleCAS.pass_marker -eq "UI17_STALE_CAS_PASS" -and [bool]$ScenarioB.StaleCAS.state_unchanged) "scenario B stale CAS evidence was incomplete"
    Assert-True ([string]$ScenarioB.StaleCAS.output_sha256 -match '^[0-9a-f]{64}$' -and
      [string]$ScenarioB.StaleCAS.stale_operation_sha256 -match '^[0-9a-f]{64}$') "scenario B stale CAS hashes were invalid"
    Assert-True ([bool]$ScenarioB.Takeover.core_stopped_before_startup_claim -and
      [string]$ScenarioB.Takeover.stopped_core_status -eq "exited" -and
      [string]$ScenarioB.Takeover.stopped_core_id_sha256 -match '^[0-9a-f]{64}$' -and
      [bool]$ScenarioB.Takeover.claim_gate_installed -and
      [string]$ScenarioB.Takeover.claim_gate_transition -eq "pending_to_running_apply_stage" -and
      [bool]$ScenarioB.Takeover.claim_gate_removed_before_startup -and
      [int]$ScenarioB.Takeover.claim_gate_remaining_objects -eq 0 -and
      [bool]$ScenarioB.Takeover.startup_run_once_claimed_forward -and
      [string]$ScenarioB.Takeover.preclaim_status -eq "pending" -and
      [int]$ScenarioB.Takeover.preclaim_attempts -eq 0 -and [long]$ScenarioB.Takeover.preclaim_operation_version -eq 1 -and
      [int]$ScenarioB.Takeover.claimed_attempts -eq 1 -and
      [bool]$ScenarioB.Takeover.live_lease_before_takeover) "scenario B startup-claim evidence was incomplete"
    Assert-Equal @($ScenarioC.UnhealthySnapshots).Count 2 "scenario C unhealthy snapshot count"
    Assert-True ([string]$ScenarioC.Workflow.RealBaseline.EvidenceKind -eq "real_openai_compatible_traffic" -and
      [int]$ScenarioC.Workflow.RealBaseline.RequestCount -eq 12 -and [int]$ScenarioC.Workflow.RealBaseline.PerChannelRequestCount -eq 6) "scenario C lacked the real baseline callability drill"
    Assert-True ([string]$ScenarioC.Workflow.PreWorkflowHealthRestart.reason -eq "scenario-c-enable-health-before-workflow" -and
      [string]$ScenarioC.Workflow.PreWorkflowHealthRestart.health_interval -eq "1s" -and
      [string]$ScenarioC.Workflow.PreWorkflowHealthRestart.rollout_interval -eq "30m" -and
      [string]$ScenarioC.Workflow.PreWorkflowHealthRestart.worker_interval -eq "1m" -and
      [string]$ScenarioC.Workflow.PreWorkflowHealthRestart.image_id -ne "") "scenario C lacked its pre-workflow health restart proof"
    Assert-True ([string]$ScenarioC.Stage10Traffic.EvidenceKind -eq "real_openai_compatible_traffic" -and
      [int]$ScenarioC.Stage10Traffic.RequestCount -eq 12) "scenario C lacked fresh real 10% traffic"
    Assert-True ([bool]$ScenarioC.OperatorRollbackAudit.passed -and [int]$ScenarioC.OperatorRollbackAudit.actual_count -eq 0) "scenario C lacked the operator rollback audit absence assertion"
    Assert-Equal ([string]$ScenarioC.OperatorRollbackAudit.query_sha256) (Get-Sha256Hex ([string]$ScenarioC.OperatorRollbackAudit.query_text)) "scenario C operator rollback audit query hash"
    Assert-Equal ([string]$ScenarioC.OperatorRollbackAudit.target_id) ([string]$ScenarioC.RolloutID) "scenario C operator rollback audit target"
    Assert-Equal ([string]$ScenarioC.OperatorRollbackAudit.action) "upstream_recommendation.rollout.rollback" "scenario C operator rollback audit action"
    Assert-True ($null -ne $CleanupEvidence -and [bool]$CleanupEvidence.verified_empty -and [bool]$CleanupEvidence.inspection.verified -and
      [int]$CleanupEvidence.inspection.total_resource_query_count -eq 3) "PASS evidence lacked exact cleanup inspection counts"
    Assert-Equal @($ImageEvidence).Count $ExpectedServices.Count "initial image evidence row count"
    Assert-Equal @($ImageInspectionRuns).Count (@($RestartImageEvidence).Count + 1) "dynamic image inspection run count"
    Assert-True (@($ImageInspectionRuns | Where-Object {
      -not $_.verified -or [int]$_.expected_service_count -ne $ExpectedServices.Count -or
      [int]$_.unique_service_count -ne $ExpectedServices.Count -or
      [int]$_.container_inspect_count -ne $ExpectedServices.Count -or
      [int]$_.image_metadata_inspect_count -ne $ExpectedServices.Count
    }).Count -eq 0) "PASS evidence contained incomplete image inspection counts"
    foreach ($stage in @($ScenarioA.Stages)) {
      Assert-True ([bool]$stage.observation_window.minimum_satisfied -and
        [int]$stage.observation_window.observation_seconds -eq $(if ($ReleaseEligible) { 300 } else { 5 })) "scenario A evidence omitted its persisted observation window"
      Assert-True ($null -ne $stage.protocol_v3 -and [int]$stage.protocol_v3.stage -eq [int]$stage.stage -and
        [string]$stage.protocol_v3.plan_id -eq [string]$Context.PlanID -and [long]$stage.protocol_v3.scheduling_generation -gt 0 -and
        [int]$stage.protocol_v3.task_count -ge 1 -and [int]$stage.protocol_v3.task_count -le 2 -and
        [int]$stage.protocol_v3.remote_weight_write_count -eq [int]$stage.protocol_v3.task_count -and
        [bool]$stage.protocol_v3.executing_preceded_remote_write -and [bool]$stage.protocol_v3.remote_write_preceded_terminal -and
        (@($stage.protocol_v3.strict_lifecycle) -join ',') -eq 'pending,leased,executing,succeeded') "scenario A stage protocol-v3 evidence was incomplete"
      foreach ($task in @($stage.protocol_v3.tasks)) {
        Assert-True ([string]$task.task_id_sha256 -match '^[0-9a-f]{64}$' -and
          (@($task.lifecycle) -join ',') -eq 'pending,leased,executing,succeeded' -and @($task.observed_at).Count -eq 4) "scenario A stage protocol-v3 task evidence was invalid"
      }
    }
    Assert-True ([bool]$ScenarioC.Stage10ObservationWindow.minimum_satisfied -and
      [int]$ScenarioC.Stage10ObservationWindow.observation_seconds -eq $(if ($ReleaseEligible) { 300 } else { 5 })) "scenario C evidence omitted its persisted observation window"
    Assert-True ($scenarioCReasons.Count -eq 1 -and $scenarioCReasons[0] -is [string] -and
      [string]$scenarioCReasons[0] -eq "quality_failed") "PASS evidence did not preserve exactly quality_failed"
    foreach ($scenario in @($ScenarioA, $ScenarioB, $ScenarioC)) {
      Assert-True ([string]$scenario.Workflow.BaselinePublicWeightProof.canonical_sha256 -eq (Get-Sha256Hex "from=80`nto=10`nunrelated=10`n") -and
        [string]$scenario.Workflow.BaselinePublicWeightProof.canonical_payload -eq "from=80`nto=10`nunrelated=10`n" -and
        @($scenario.Workflow.BaselinePublicWeightProof.rows).Count -eq 3 -and
        [string]$scenario.Workflow.BaselinePublicWeightProof.identity_scope -eq "public_roles_only") "PASS evidence lacked a valid scenario baseline weight proof"
    }
    Assert-True ($null -ne $ProtocolV3Heartbeat -and [int]$ProtocolV3Heartbeat.protocol_version -eq 3 -and
      [int]$ProtocolV3Heartbeat.gateway_state_protocol_version -eq 3 -and [string]$ProtocolV3Heartbeat.last_seen_at -ne "") "PASS evidence lacked protocol-v3 heartbeat proof"
    Assert-True ($null -ne $ProtocolV3Drills -and [int]$ProtocolV3Drills.execute_conflict.http_status -eq 409 -and
      [string]$ProtocolV3Drills.execute_conflict.code -eq "task_execution_conflict" -and
      [int]$ProtocolV3Drills.execute_conflict.remote_weight_writes -eq 0 -and
      [int]$ProtocolV3Drills.execute_conflict.executing_transitions -eq 0 -and
      [int]$ProtocolV3Drills.execute_conflict.completion_audits -eq 0 -and
      [string]$ProtocolV3Drills.execute_conflict.terminal_error_code -eq "scheduling_fence_stale") "PASS evidence lacked the real execute-conflict zero-effect drill"
    Assert-True ([bool]$ProtocolV3Drills.executing_generation_guard.update.rejected -and
      [string]$ProtocolV3Drills.executing_generation_guard.update.sqlstate -eq "23514" -and
      [bool]$ProtocolV3Drills.executing_generation_guard.delete.rejected -and
      [string]$ProtocolV3Drills.executing_generation_guard.delete.sqlstate -eq "23514" -and
      [bool]$ProtocolV3Drills.executing_generation_guard.generation_unchanged) "PASS evidence lacked executing generation guards"
    Assert-True ([string]$ProtocolV3Drills.manual_resolution.authenticated_role -eq "platform_admin" -and
      [string]$ProtocolV3Drills.manual_resolution.resolution -eq "confirmed_not_applied" -and
      [string]$ProtocolV3Drills.manual_resolution.risk_level -eq "L3" -and
      [string]$ProtocolV3Drills.manual_resolution.event_level -eq "L3" -and
      [string]$ProtocolV3Drills.manual_resolution.action -eq "connector_task.resolve_execution" -and
      [string]$ProtocolV3Drills.manual_resolution.nonce_reference -match '^sha256:[0-9a-f]{64}$' -and
      [bool]$ProtocolV3Drills.manual_resolution.raw_nonce_absent -and
      [bool]$ProtocolV3Drills.manual_resolution.sensitive_response_fields_absent -and
      [bool]$ProtocolV3Drills.manual_resolution.atomic_transition_and_audit) "PASS evidence lacked safe atomic manual resolution"
    Assert-True ($null -ne $UncertainGatewayProof -and [string]$UncertainGatewayProof.proof_kind -eq "unit_no_bypass" -and
      -not [bool]$UncertainGatewayProof.real_external_fault_injection -and
      [string]$UncertainGatewayProof.claim_scope -eq "directed_unit_test_only" -and
      [int]$UncertainGatewayProof.exit_code -eq 0 -and [string]$UncertainGatewayProof.output_sha256 -match '^[0-9a-f]{64}$' -and
      [bool]$UncertainGatewayProof.uncertain_outcome_not_completed) "PASS evidence misrepresented or omitted the uncertain-gateway unit proof"
  }
  $evidence = [ordered]@{
    schema = 6
    acceptance = "ui17-disposable-newapi"
    generated_at = [DateTimeOffset]::UtcNow.ToString("o")
    observation_profile = $ObservationProfile
    source_frozen_acknowledged = $SourceFrozenAcknowledged
    release_eligible = [bool]$ReleaseEligible
    release_pass = ($ReleaseEligible -and $ReleasePass)
    test_pass = $ReleasePass
    failure_code = $FailureCode
    provenance = [ordered]@{
      runner_sha256 = $RunnerSHA256Before
      compose_sha256 = $ComposeSHA256Before
      runner = [ordered]@{
        sha256_before = $RunnerSHA256Before; sha256_after_cleanup = $RunnerSHA256AfterCleanup
        unchanged = $RunnerUnchanged; path_kind = "repository_relative"; path = "scripts/test-ui17-disposable-newapi.ps1"
      }
      compose = [ordered]@{
        sha256 = $ComposeSHA256Before; path_kind = "repository_relative"
        path = "deployments/templates/compose/e2m-ui17-disposable-newapi.compose.yml"
      }
      build_input = [ordered]@{
        before = $BuildInputBefore; after_start = $BuildInputAfterStart
        after_restart = $BuildInputAfterRestart; after_cleanup = $BuildInputAfterCleanup
        unchanged = $BuildInputUnchanged; go_proxy_policy = "proxy_with_direct_fallback"; go_proxy = $FixedGoProxy
      }
      built_images_bound = $BuiltImagesProvenanceBound
      built_images = @($BuiltImageProvenance)
    }
    isolation = [ordered]@{
      compose_project = $Project; disposable = $true; exact_channel_count = 3
      protected_stack_unchanged = $ProtectedUnchanged
      disposable_cleanup = $CleanupEvidence
      runtime_directory_removed = $RuntimeRemoved
      environment_restored = $EnvironmentRestored
    }
    compose = [ordered]@{
      sha256_before = $ComposeSHA256Before; sha256_after_start = $ComposeSHA256AfterStart
      sha256_after_restart = $ComposeSHA256AfterRestart; sha256_after_cleanup = $ComposeSHA256AfterCleanup
      unchanged = $ComposeUnchanged
    }
    protected_stack_before = @($ProtectedBefore)
    protected_stack_after = @($ProtectedAfter)
    images_verified = $ImagesVerified
    images = @($ImageEvidence)
    image_inspection = [ordered]@{
      run_count = @($ImageInspectionRuns).Count
      initial_evidence_row_count = @($ImageEvidence).Count
      core_restart_evidence_row_count = @($RestartImageEvidence).Count
      unique_initial_service_count = @($ImageEvidence.service | Sort-Object -Unique).Count
      container_inspect_count = Get-ImageInspectionCountTotal @($ImageInspectionRuns) "container_inspect_count"
      image_metadata_inspect_count = Get-ImageInspectionCountTotal @($ImageInspectionRuns) "image_metadata_inspect_count"
      runs = @($ImageInspectionRuns)
      all_verified = (@($ImageInspectionRuns | Where-Object { -not $_.verified }).Count -eq 0)
    }
    core_restart_image = $RestartImageEvidence
  }
  if ($null -ne $ScenarioA -and $null -ne $ScenarioB -and $null -ne $ScenarioC) {
    $completionStages = @($ScenarioA.Stages | ForEach-Object {
      [ordered]@{
        stage = [int]$_.stage; status = [string]$_.status; operation_status = [string]$_.operation_status
        readback_fingerprint = [string]$_.readback_fingerprint
        readback_weights = [ordered]@{ from = [int]$_.from_weight; to = [int]$_.to_weight; unrelated = [int]$_.unrelated_weight }
        observe_until = [string]$_.observed_until; observation_window = $_.observation_window
        protocol_v3 = $_.protocol_v3; real_traffic = $_.real_traffic
      }
    })
    $scenarioCFinal = $ScenarioC.RolledBack
    $scenarioCOperation = Get-JsonValue $scenarioCFinal "latest_operation"
    $evidence.execution_policy_id = [string]$ExecutionPolicyID
    $evidence.protocol_v3 = [ordered]@{
      protocol_version = 3; heartbeat = $ProtocolV3Heartbeat
      execute_and_resolution_drills = $ProtocolV3Drills
      uncertain_gateway = $UncertainGatewayProof
    }
    $evidence.scenarios = [ordered]@{
      completion = [ordered]@{
        recommendation_id = [string]$ScenarioA.Workflow.RecommendationID
        rollout_id = [string]$ScenarioA.RolloutID; stages = $completionStages
        completed = [ordered]@{
          status = [string](Get-JsonValue $ScenarioA.Completed "status")
          stage = [int](Get-JsonValue $ScenarioA.Completed "stage")
          last_after_verified = [bool](Get-JsonValue $ScenarioA.Completed "last_after_verified")
        }
        restored = [ordered]@{
          status = [string](Get-JsonValue $ScenarioA.Restoration.RolledBack "status")
          rollback_verified = [bool](Get-JsonValue $ScenarioA.Restoration.RolledBack "rollback_verified")
          readback_weights = [ordered]@{ from = 80; to = 10; unrelated = 10 }
          public_weight_proof = $ScenarioA.Restoration.PublicWeightProof
          accepted_operator_rollback_audit_count = [int]$ScenarioA.AcceptedOperatorRollbackAuditCount
        }
        baseline_public_weight_proof = $ScenarioA.Workflow.BaselinePublicWeightProof
      }
      operator_takeover = [ordered]@{
        recommendation_id = [string]$ScenarioB.Workflow.RecommendationID
        rollout_id = [string]$ScenarioB.RolloutID; takeover = $ScenarioB.Takeover
        final_status = [string](Get-JsonValue $ScenarioB.RolledBack "status")
        rollback_verified = [bool](Get-JsonValue $ScenarioB.RolledBack "rollback_verified")
        readback_weights = [ordered]@{ from = 80; to = 10; unrelated = 10 }
        stale_cas = $ScenarioB.StaleCAS
        baseline_public_weight_proof = $ScenarioB.Workflow.BaselinePublicWeightProof
      }
      automatic_regression = [ordered]@{
        recommendation_id = [string]$ScenarioC.Workflow.RecommendationID
        rollout_id = [string]$ScenarioC.RolloutID
        stage = 10; stage_readback_fingerprint = [string]$ScenarioC.Stage10Fingerprint
        stage_real_traffic = $ScenarioC.Stage10Traffic
        regression_kind = [string]$ScenarioC.RegressionKind
        typed_connector_observations = $true
        unhealthy_snapshot_count = @($ScenarioC.UnhealthySnapshots).Count
        unhealthy_snapshots = @($ScenarioC.UnhealthyEvidence)
        final_status = [string](Get-JsonValue $scenarioCFinal "status")
        rollback_verified = [bool](Get-JsonValue $scenarioCFinal "rollback_verified")
        rollback_reasons = @(Get-JsonArrayItems $scenarioCFinal "rollback_reasons")
        observation_window = $ScenarioC.Stage10ObservationWindow
        rollback_operation = [ordered]@{
          action = [string](Get-JsonValue $scenarioCOperation "action")
          status = [string](Get-JsonValue $scenarioCOperation "status")
          target_stage = [int](Get-JsonValue $scenarioCOperation "target_stage")
        }
        automatic_runner_operation = $ScenarioC.RunnerRollbackEvidence
        operator_rollback_invoked = $false; operator_rollback_audit = $ScenarioC.OperatorRollbackAudit
        baseline_real_traffic = $ScenarioC.Workflow.RealBaseline
        pre_workflow_health_restart = $ScenarioC.Workflow.PreWorkflowHealthRestart
        baseline_public_weight_proof = $ScenarioC.Workflow.BaselinePublicWeightProof
      }
    }
    $evidence.claims = [ordered]@{
      independent_rollouts = $true; real_newapi_weight_writes = $true; complete_paginated_readback = $true
      real_newapi_model_traffic = $true; passive_binding_verification = $true; unrelated_weight_preserved = $true
      completion_then_restoration = $true; running_forward_lease_preempted = $true
      stale_worker_cas_fenced = ([string]$ScenarioB.StaleCAS.pass_marker -eq "UI17_STALE_CAS_PASS")
      automatic_quality_rollback = ($scenarioCReasons.Count -eq 1 -and $scenarioCReasons[0] -is [string] -and
        [string]$scenarioCReasons[0] -eq "quality_failed")
      automatic_operator_rollback_audit_absent = [bool]$ScenarioC.OperatorRollbackAudit.passed
      protocol_v3_execution_permit_ordering = $true
      protocol_v3_execute_conflict_zero_remote_effect = $true
      protocol_v3_executing_generation_guard = $true
      protocol_v3_manual_resolution_nonce_hash_only = $true
      uncertain_gateway_unit_no_bypass = $true
      release_pass = ($ReleaseEligible -and $ReleasePass)
    }
  }
  $json = $evidence | ConvertTo-Json -Depth 30
  $forbiddenValues = @()
  if ($null -ne $Context) { $forbiddenValues += @([string]$Context.ConnectorToken, [string]$Context.AdminHeaders["Authorization"]) }
  if ($null -ne $ScenarioB -and $null -ne $ScenarioB.StaleCAS) {
    # Raw worker/operation identities never enter scenario evidence; only their
    # SHA-256 bindings survive. Keep this explicit invariant close to redaction.
    Assert-True ($json -notmatch '"lease_owner"|"operation_id"') "redacted evidence exposed stale worker identity fields"
  }
  if ($null -ne $script:newAPI) {
    $forbiddenValues += [string](Get-JsonValue $script:newAPI "Token")
    $relayToken = Get-JsonValue $script:newAPI "RelayToken"
    if ($null -ne $relayToken) { $forbiddenValues += [string](Get-JsonValue $relayToken "Key") }
  }
  foreach ($forbidden in $forbiddenValues) {
    if ($forbidden -ne "" -and $json.IndexOf($forbidden, [StringComparison]::Ordinal) -ge 0) {
      throw "redacted evidence contained a runtime credential"
    }
  }
  if ($json -match '(?i)bearer\s+[a-z0-9._~-]+|sk-ui17-disposable|password|api[_-]?key') {
    throw "redacted evidence contained a sensitive marker"
  }
  $persistentDirectory = [System.IO.Path]::GetDirectoryName($PersistentEvidencePath)
  New-Item -ItemType Directory -Force -Path $persistentDirectory | Out-Null
  [IO.File]::WriteAllText($PersistentEvidencePath, $json + "`n", [Text.UTF8Encoding]::new($false))
  Write-Host "    preserved redacted evidence: $PersistentEvidencePath"
  return $evidence
}

try {
  Assert-ImageInspectionCountAggregation
  Assert-CorePostgresSessionContract
  Assert-NewAPIPostgresClockContract
  Assert-Schema6ProtocolEvidenceContract
  if ($Project -notmatch $ProjectPattern -or $Project -match 'real-gateways') { throw "generated project name failed the UI-17 allowlist" }
  if ($null -eq (Get-Command docker -ErrorAction SilentlyContinue)) { throw "docker is required" }
  $BuildInputBefore = Get-BuildInputManifest
  $ComposeSHA256Before = Get-ComposeSHA256
  Assert-True ($ComposeSHA256Before -match '^[0-9a-f]{64}$') "initial Compose SHA256 was invalid"
  Assert-ComposeImagePins
  $ProtectedBefore = @(Get-ProtectedStackSnapshot)
  $ProtectedBeforeCaptured = $true
  New-Item -ItemType Directory -Force -Path (Join-Path $RuntimeDir "connector") | Out-Null
  [IO.File]::WriteAllText((Join-Path $RuntimeDir "connector/enrollment.token"), "", [Text.UTF8Encoding]::new($false))
  @{ schema = 1; project = $Project; compose_file = $ComposeFile; runtime_dir = $RuntimeDir } |
    ConvertTo-Json -Compress | Set-Content -NoNewline -Encoding utf8 -LiteralPath $MarkerFile

  [Environment]::SetEnvironmentVariable("E2M_UI17_RUNTIME_DIR", $RuntimeDir, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_CORE_PORT", [string]$CorePort, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_NEWAPI_PORT", [string]$NewAPIPort, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_CONNECTOR_PORT", [string]$ConnectorPort, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_POSTGRES_PORT", [string]$PostgresPort, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_CORE_IMAGE", $CoreImage, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_CONNECTOR_IMAGE", $ConnectorImage, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_MOCK_OPENAI_IMAGE", $MockOpenAIImage, "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_HEALTH_METRICS_INTERVAL", "10s", "Process")
  # Compose interpolates the entire model even when only the pre-enrollment
  # services are selected. These non-secret placeholders are never used to
  # start the Connector and are replaced with Core-issued identities below.
  [Environment]::SetEnvironmentVariable("E2M_UI17_CONNECTOR_ID", "pending-$Project", "Process")
  [Environment]::SetEnvironmentVariable("E2M_UI17_INSTANCE_ID", "pending-$Project", "Process")
  if ($ObservationProfile -eq "test-only") {
    Write-Warning "test-only short observation mode cannot produce a UI-17 release PASS"
    [Environment]::SetEnvironmentVariable("E2M_UI17_ROLLOUT_OBSERVATION", "5s", "Process")
    [Environment]::SetEnvironmentVariable("E2M_UI17_ROLLOUT_RUNNER_INTERVAL", "30m", "Process")
  } else {
    [Environment]::SetEnvironmentVariable("E2M_UI17_ROLLOUT_OBSERVATION", "5m", "Process")
    [Environment]::SetEnvironmentVariable("E2M_UI17_ROLLOUT_RUNNER_INTERVAL", "30m", "Process")
  }
  [Environment]::SetEnvironmentVariable("E2M_UI17_ROLLOUT_WORKER_INTERVAL", "1s", "Process")

  Assert-ProjectBoundary
  Write-Step "Starting isolated disposable stack $Project"
  $StackMayExist = $true
  Invoke-Compose up --build --detach core-postgres newapi-postgres newapi-redis mock-openai newapi e2m-core
  Assert-ComposeProjectLabels @("core-postgres", "newapi-postgres", "newapi-redis", "mock-openai", "newapi", "e2m-core")
  Wait-Http "http://127.0.0.1:$CorePort/healthz" "E2M Core"
  Wait-Http "http://127.0.0.1:$NewAPIPort/api/status" "NewAPI"

  $script:newAPI = Initialize-NewAPI
  $channels = New-ThreeNewAPIChannels $script:newAPI
  $Context = Initialize-E2MConnector $script:newAPI
  Assert-ComposeProjectLabels $ExpectedServices
  $ComposeSHA256AfterStart = Get-ComposeSHA256
  Assert-True ($ComposeSHA256AfterStart -eq $ComposeSHA256Before) "Compose changed between validation and full startup"
  $BuildInputAfterStart = Get-BuildInputManifest
  Assert-BuildInputManifestEqual $BuildInputAfterStart $BuildInputBefore "post-start"
  $ImageEvidence = @(Get-DisposableImageEvidence -ExpectedServiceNames $ExpectedServices -InspectionReason "initial-complete-stack")
  $BuiltImageProvenance = @(Get-BuiltImageProvenance $ImageEvidence)
  $BuiltImagesProvenanceBound = $true
  Initialize-FixtureOnlyRecommendationFacts $Context $channels
  Initialize-ProtocolV3Observers $channels
  $ProtocolV3Heartbeat = Get-ProtocolV3HeartbeatEvidence $Context
  [void](Prepare-ScenarioBaseline $Context $channels "A")
  $workflowA = Invoke-RecommendationWorkflow $Context $channels "A" $true
  $ScenarioA = Invoke-CompletionAndRestoreScenario $Context $workflowA $channels

  [void](Prepare-ScenarioBaseline $Context $channels "B")
  # The workflow installs a disposable pending-claim gate, creates the rollout,
  # stops Core, verifies the untouched operation, and removes the gate. Scenario
  # B pauses Connector and force-recreates Core for one real startup RunOnce.
  $workflowB = Invoke-RecommendationWorkflow $Context $channels "B" $false "1m" $false
  $ScenarioB = Invoke-OperatorTakeoverScenario $Context $workflowB $channels

  [void](Prepare-ScenarioBaseline $Context $channels "C")
  # Scenario B deliberately leaves health aggregation at 30m while proving a
  # startup lease takeover. Real traffic only appends observations; aggregation
  # is ticker-driven, so restore a fast health cadence before scenario C asks
  # for a post-traffic healthy snapshot. Keep rollout/worker slow so no rollout
  # work can race the C baseline fixture.
  $scenarioCPreWorkflowRestartReason = "scenario-c-enable-health-before-workflow"
  Restart-DisposableCoreWithIntervals $Context "1s" "30m" "1m" $scenarioCPreWorkflowRestartReason
  $scenarioCPreWorkflowRestart = @($RestartImageEvidence | Where-Object { [string]$_.reason -eq $scenarioCPreWorkflowRestartReason })
  Assert-Equal $scenarioCPreWorkflowRestart.Count 1 "scenario C pre-workflow restart evidence count"
  Assert-True ([string]$scenarioCPreWorkflowRestart[0].health_interval -eq "1s" -and
    [string]$scenarioCPreWorkflowRestart[0].rollout_interval -eq "30m" -and
    [string]$scenarioCPreWorkflowRestart[0].worker_interval -eq "1m") "scenario C pre-workflow restart intervals were invalid"
  $workflowC = Invoke-RecommendationWorkflow $Context $channels "C" $false
  $workflowC | Add-Member -NotePropertyName PreWorkflowHealthRestart -NotePropertyValue ([ordered]@{
    reason = $scenarioCPreWorkflowRestartReason
    health_interval = [string]$scenarioCPreWorkflowRestart[0].health_interval
    rollout_interval = [string]$scenarioCPreWorkflowRestart[0].rollout_interval
    worker_interval = [string]$scenarioCPreWorkflowRestart[0].worker_interval
    image_id = [string]$scenarioCPreWorkflowRestart[0].image_id
  })
  $ScenarioC = Invoke-AutomaticRollbackScenario $Context $workflowC $channels
  $ProtocolV3Drills = Invoke-ProtocolV3ConflictAndResolutionDrills $Context $channels
  $UncertainGatewayProof = Invoke-UncertainGatewayUnitProof
  $BusinessPassed = $true
} catch {
  $PrimaryFailure = $_
} finally {
  if ($StackMayExist -and $ConnectorPausedByRunner) {
    try {
      Invoke-Compose unpause connector-newapi
      $ConnectorPausedByRunner = $false
    } catch {
      if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
    }
  }
  if ($StackMayExist -and $StartupClaimGateInstalled) {
    try {
      [void](Remove-StartupClaimGate)
    } catch {
      if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
    }
  }
  if ($StackMayExist -and $KeepOnFailure -and $null -ne $PrimaryFailure) {
    Write-Warning "failure stack retained by -KeepOnFailure: project=$Project runtime=$RuntimeDir"
    $CleanupFailure = [Management.Automation.RuntimeException]::new("final cleanup was intentionally skipped by KeepOnFailure")
  } elseif (Test-Path -LiteralPath $MarkerFile -PathType Leaf) {
    try {
      Assert-ProjectBoundary
      if ($StackMayExist) {
        Remove-DisposableStack
        $CleanupEvidence = Assert-DisposableProjectRemoved
      } else {
        $CleanupEvidence = [ordered]@{
          containers = 0; volumes = 0; networks = 0; verified_empty = $true
          inspection = [ordered]@{
            compose_container_query_count = 0; volume_project_label_query_count = 0; network_project_label_query_count = 0
            total_resource_query_count = 0; exact_project_scope = $true; verified = $true; reason = "stack_not_started"
          }
        }
      }
      $DisposableRemoved = $true
    } catch {
      $CleanupFailure = $_
    }
    if ($DisposableRemoved) {
      try {
        Assert-ProjectBoundary
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
    }
  } catch {
    if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
  }
  try {
    $ComposeSHA256AfterCleanup = Get-ComposeSHA256
    Assert-True ($ComposeSHA256Before -match '^[0-9a-f]{64}$' -and $ComposeSHA256AfterStart -match '^[0-9a-f]{64}$' -and
      $ComposeSHA256AfterRestart -match '^[0-9a-f]{64}$') "Compose SHA256 evidence was incomplete"
    Assert-True ($ComposeSHA256AfterStart -eq $ComposeSHA256Before -and $ComposeSHA256AfterRestart -eq $ComposeSHA256Before -and
      $ComposeSHA256AfterCleanup -eq $ComposeSHA256Before) "Compose changed during disposable acceptance"
    $ComposeUnchanged = $true
  } catch {
    if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
  }
  try {
    $RunnerSHA256AfterCleanup = (Get-FileHash -Algorithm SHA256 -LiteralPath $RunnerFile).Hash.ToLowerInvariant()
    Assert-True ($RunnerSHA256Before -match '^[0-9a-f]{64}$' -and $RunnerSHA256AfterCleanup -match '^[0-9a-f]{64}$') "runner SHA256 evidence was incomplete"
    Assert-Equal $RunnerSHA256AfterCleanup $RunnerSHA256Before "runner changed during disposable acceptance"
    $RunnerUnchanged = $true
  } catch {
    if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
  }
  try {
    Assert-True ($null -ne $BuildInputAfterRestart) "no Core restart build input manifest was captured"
    Assert-BuildInputManifestEqual $BuildInputAfterRestart $BuildInputBefore "post-restart"
    $BuildInputAfterCleanup = Get-BuildInputManifest
    Assert-BuildInputManifestEqual $BuildInputAfterCleanup $BuildInputBefore "post-cleanup"
    $BuildInputUnchanged = $true
  } catch {
    if ($null -eq $CleanupFailure) { $CleanupFailure = $_ }
  }
  try {
    $FailureCode = ""
    if ($null -ne $PrimaryFailure) { $FailureCode = "acceptance_failed" }
    elseif ($null -ne $CleanupFailure) { $FailureCode = "finalization_failed" }
    $Succeeded = $BusinessPassed -and $DisposableRemoved -and $RuntimeRemoved -and $EnvironmentRestored -and
      $ProtectedUnchanged -and $ComposeUnchanged -and $RunnerUnchanged -and $BuildInputUnchanged -and $ImagesVerified -and $BuiltImagesProvenanceBound -and
      $null -eq $PrimaryFailure -and $null -eq $CleanupFailure
    $FinalEvidence = Write-RedactedEvidence $Succeeded $FailureCode
  } catch {
    $FinalizationFailure = $_
    $Succeeded = $false
  }
}

if ($null -ne $PrimaryFailure) { throw $PrimaryFailure }
if ($null -ne $CleanupFailure) { throw $CleanupFailure }
if ($null -ne $FinalizationFailure) { throw $FinalizationFailure }
if (-not $Succeeded) { throw "UI-17 disposable NewAPI acceptance did not complete" }
Write-Step "UI-17 disposable NewAPI acceptance passed ($ObservationProfile)"
Write-Host "    evidence: $PersistentEvidencePath"
