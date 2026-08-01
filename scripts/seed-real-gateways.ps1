param(
  [string]$ComposeFile = "",
  [string]$E2MBaseUrl = "http://localhost:18080",
  [string]$Sub2APIBaseUrl = "http://localhost:18090",
  [string]$NewAPIBaseUrl = "http://localhost:13000",
  [string]$CPABaseUrl = "http://localhost:18317",
  [string]$E2MAdminEmail = "admin@local.dev",
  [string]$E2MAdminPassword = "admin123456",
  [string]$E2MSupplierEmail = "supplier-local@local.dev",
  [string]$E2MSupplierPassword = "admin123456",
  [string]$Sub2APIInstanceID = "inst-9cef27a502cd7c82", # gitleaks:allow -- public fixture ID
  [string]$NewAPIInstanceID = "inst-958940169e2b72ed", # gitleaks:allow -- public fixture ID
  [string]$CPAInstanceID = "inst-3eb2841327f0abcb",
  [int]$TimeoutSeconds = 90,
  [switch]$SkipReconcile,
  [switch]$SkipHealthSeed,
  [switch]$SkipAutoSwitchEvaluate
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RepoRoot = Split-Path -Parent $PSScriptRoot
if ($ComposeFile -eq "") {
  $ComposeFile = Join-Path $RepoRoot "deployments/templates/compose/e2m-core-real-gateways.compose.yml"
}
$ComposeFile = [System.IO.Path]::GetFullPath($ComposeFile)

function Write-Step {
  param([Parameter(Mandatory = $true)][string]$Message)
  Write-Host "==> $Message"
}

function ConvertTo-JsonBody {
  param([Parameter(Mandatory = $true)]$Body)
  $json = $Body | ConvertTo-Json -Depth 30 -Compress
  return ,[System.Text.Encoding]::UTF8.GetBytes($json)
}

function ConvertTo-Array {
  param($Value)
  if ($null -eq $Value) {
    return ,([object[]]@())
  }
  if ($Value -is [System.Array]) {
    return ,([object[]]$Value)
  }
  return ,([object[]]@($Value))
}

function Get-JsonValue {
  param(
    [Parameter(Mandatory = $true)]$Object,
    [Parameter(Mandatory = $true)][string]$Name
  )
  if ($Object -is [System.Collections.IDictionary] -and $Object.Contains($Name)) {
    return $Object[$Name]
  }
  $property = $Object.PSObject.Properties[$Name]
  if ($null -ne $property) {
    return $property.Value
  }
  return $null
}

function ConvertTo-Hashtable {
  param($Object)
  $out = @{}
  if ($null -eq $Object) {
    return $out
  }
  if ($Object -is [System.Collections.IDictionary]) {
    foreach ($key in $Object.Keys) {
      $out[[string]$key] = [string]$Object[$key]
    }
    return $out
  }
  foreach ($property in $Object.PSObject.Properties) {
    if ($null -ne $property.Value) {
      $out[$property.Name] = [string]$property.Value
    }
  }
  return $out
}

function Merge-Labels {
  param(
    $Existing,
    [hashtable]$Add
  )
  $labels = ConvertTo-Hashtable $Existing
  foreach ($key in $Add.Keys) {
    $labels[$key] = [string]$Add[$key]
  }
  return $labels
}

function Invoke-Json {
  param(
    [Parameter(Mandatory = $true)][string]$Method,
    [Parameter(Mandatory = $true)][string]$Uri,
    $Body = $null,
    [hashtable]$Headers = @{}
  )
  $params = @{
    Method = $Method
    Uri = $Uri
    Headers = $Headers
    TimeoutSec = 30
  }
  if ($null -ne $Body) {
    $params.Body = ConvertTo-JsonBody $Body
    $params.ContentType = "application/json; charset=utf-8"
  }
  try {
    return Invoke-RestMethod @params
  } catch {
    $detail = $_.Exception.Message
    if ($_.ErrorDetails -and $_.ErrorDetails.Message) {
      $detail = $_.ErrorDetails.Message
    }
    throw "$Method $Uri failed: $detail"
  }
}

function Wait-Http {
  param(
    [Parameter(Mandatory = $true)][string]$Uri,
    [Parameter(Mandatory = $true)][string]$Name
  )
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $lastError = $null
  while ((Get-Date) -lt $deadline) {
    try {
      $response = Invoke-WebRequest -Uri $Uri -UseBasicParsing -TimeoutSec 5
      if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 300) {
        Write-Host ("    {0}: OK ({1})" -f $Name, $Uri)
        return
      }
      $lastError = "HTTP $($response.StatusCode)"
    } catch {
      $lastError = $_.Exception.Message
    }
    Start-Sleep -Seconds 2
  }
  throw "Timed out waiting for $Name at $Uri. Last error: $lastError. Start or repair the stack with .\scripts\bootstrap-real-gateways.ps1."
}

function Login-E2M {
  $login = Invoke-Json "POST" "$E2MBaseUrl/api/v1/auth/login" @{
    email = $E2MAdminEmail
    password = $E2MAdminPassword
  }
  $token = [string](Get-JsonValue $login "token")
  if ($token -eq "") {
    throw "E2M login did not return a token"
  }
  Write-Host ("    logged in as {0}" -f $E2MAdminEmail)
  return $token
}

function Resolve-Instance {
  param(
    [array]$Instances,
    [string]$PreferredID,
    [string]$Kind,
    [string]$NameNeedle
  )
  foreach ($instance in $Instances) {
    if ([string](Get-JsonValue $instance "id") -eq $PreferredID) {
      return $instance
    }
  }
  foreach ($instance in $Instances) {
    $kindValue = [string](Get-JsonValue $instance "kind")
    $nameValue = ([string](Get-JsonValue $instance "name")).ToLowerInvariant()
    if ($kindValue -eq $Kind -and $nameValue.Contains($NameNeedle.ToLowerInvariant())) {
      return $instance
    }
  }
  foreach ($instance in $Instances) {
    if ([string](Get-JsonValue $instance "kind") -eq $Kind) {
      return $instance
    }
  }
  throw "Could not resolve $Kind instance. Run .\scripts\bootstrap-real-gateways.ps1 first."
}

function Assert-RemoteAccount {
  param(
    [array]$Accounts,
    [string]$RemoteID,
    [string]$Label
  )
  foreach ($account in $Accounts) {
    if ([string](Get-JsonValue $account "id") -eq $RemoteID) {
      return
    }
  }
  throw "Expected remote account '$RemoteID' for $Label was not returned by E2M. Re-run .\scripts\bootstrap-real-gateways.ps1 -SkipComposeUp to refresh the Connector-local gateway credential."
}

function Ensure-User {
  param(
    [hashtable]$Headers,
    [string]$DisplayName,
    [string]$Email,
    [string]$Password,
    [string[]]$Roles
  )
  $users = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/users" $null $Headers)
  foreach ($user in $users) {
    if ([string](Get-JsonValue $user "email") -eq $Email) {
      return $user
    }
  }
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/users" @{
    email = $Email
    password = $Password
    display_name = $DisplayName
    roles = $Roles
  } $Headers
}

function Ensure-UpstreamPool {
  param(
    [hashtable]$Headers,
    [string]$Name,
    [string]$Provider,
    [string[]]$Models,
    [string]$Region,
    [string]$Description,
    [string]$SeedKey
  )
  $pools = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/upstream-pools" $null $Headers)
  $existing = $null
  foreach ($pool in $pools) {
    $labels = ConvertTo-Hashtable (Get-JsonValue $pool "labels")
    if ($labels["seed_key"] -eq $SeedKey -or [string](Get-JsonValue $pool "name") -eq $Name) {
      $existing = $pool
      break
    }
  }
  $existingLabels = $null
  if ($existing) {
    $existingLabels = Get-JsonValue $existing "labels"
  }
  $labels = Merge-Labels $existingLabels @{
    source = "seed-real-gateways"
    seed_key = $SeedKey
    local_real_gateway = "true"
  }
  $body = @{
    name = $Name
    provider = $Provider
    models = $Models
    region = $Region
    status = "active"
    description = $Description
    labels = $labels
  }
  if ($existing) {
    return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-pools/$([string](Get-JsonValue $existing "id"))" $body $Headers
  }
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-pools" $body $Headers
}

function Ensure-UpstreamChannel {
  param(
    [hashtable]$Headers,
    [string]$PoolID,
    [string]$SourceID,
    [string]$DisplayName,
    [string]$Provider,
    [string[]]$Models,
    [string[]]$Groups,
    [string]$CredentialBindingID,
    [string]$ProxyBindingID,
    [int]$Priority,
    [int]$Weight,
    [double]$CostHint,
    [string]$Status,
    [string]$RemoteID,
    [string]$GatewayKind,
    [string]$InstanceID,
    [string]$SeedKey
  )
  $channels = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/upstream-channels" $null $Headers)
  $existing = $null
  foreach ($channel in $channels) {
    $labels = ConvertTo-Hashtable (Get-JsonValue $channel "labels")
    if ($labels["seed_key"] -eq $SeedKey -or ([string](Get-JsonValue $channel "pool_id") -eq $PoolID -and $labels["remote_id"] -eq $RemoteID)) {
      $existing = $channel
      break
    }
  }
  $existingLabels = $null
  if ($existing) {
    $existingLabels = Get-JsonValue $existing "labels"
  }
  $labels = Merge-Labels $existingLabels @{
    source = "seed-real-gateways"
    seed_key = $SeedKey
    local_real_gateway = "true"
    remote_id = $RemoteID
    gateway_kind = $GatewayKind
    instance_id = $InstanceID
  }
  $body = @{
    pool_id = $PoolID
    source_id = $SourceID
    display_name = $DisplayName
    provider = $Provider
    models = $Models
    groups = $Groups
    credential_binding_id = $CredentialBindingID
    proxy_binding_id = $ProxyBindingID
    priority = $Priority
    weight = $Weight
    cost_hint = $CostHint
    status = $Status
    labels = $labels
  }
  if ($existing) {
    return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-channels/$([string](Get-JsonValue $existing "id"))" $body $Headers
  }
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-channels" $body $Headers
}

function Ensure-RoutePlan {
  param(
    [hashtable]$Headers,
    [string]$InstanceID,
    [string]$PoolID,
    [string]$Tier,
    [int]$MaxChannels,
    [string]$Rollout,
    [int]$RolloutCanaryCount,
    [int]$RolloutBatchSize,
    [string]$SeedKey
  )
  $plans = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/route-plans" $null $Headers)
  $existing = $null
  foreach ($plan in $plans) {
    $labels = ConvertTo-Hashtable (Get-JsonValue $plan "labels")
    if ($labels["seed_key"] -eq $SeedKey -or ([string](Get-JsonValue $plan "instance_id") -eq $InstanceID -and [string](Get-JsonValue $plan "pool_id") -eq $PoolID)) {
      $existing = $plan
      break
    }
  }
  $existingLabels = $null
  if ($existing) {
    $existingLabels = Get-JsonValue $existing "labels"
  }
  $labels = Merge-Labels $existingLabels @{
    source = "seed-real-gateways"
    seed_key = $SeedKey
    local_real_gateway = "true"
  }
  $body = @{
    instance_id = $InstanceID
    pool_id = $PoolID
    tier = $Tier
    status = "published"
    max_channels = $MaxChannels
    rollout = $Rollout
    rollout_canary_count = $RolloutCanaryCount
    rollout_batch_size = $RolloutBatchSize
    labels = $labels
  }
  if ($existing) {
    return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/route-plans/$([string](Get-JsonValue $existing "id"))" $body $Headers
  }
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/route-plans" $body $Headers
}

function Upsert-PlanStrategy {
  param(
    [hashtable]$Headers,
    $Plan,
    [string]$Name,
    [string]$Type,
    [bool]$AutoApply
  )
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/route-strategies" @{
    name = $Name
    type = $Type
    scope = "plan"
    plan_id = [string](Get-JsonValue $Plan "id")
    thresholds = @{
      min_samples = 5
      target_success_rate = 0.95
      floor_success_rate = 0.85
      max_ttft_p95_ms = 4000
      max_duration_p95_ms = 20000
      consecutive_failure_limit = 3
    }
    weights = @{}
    auto_apply = $AutoApply
    approval_required = $false
    cooldown_seconds = 60
    recovery_observation_seconds = 120
    max_auto_switches_per_hour = 3
  } $Headers
}

function Ensure-SupplyOffer {
  param(
    [hashtable]$Headers,
    [long]$SupplierID,
    [string]$Kind,
    [string]$Provider,
    [string]$CredentialRef,
    [string]$ProxyRef,
    [long]$Quota,
    [string]$UnitPrice,
    [string]$RemoteID,
    [string]$SeedKey
  )
  $offers = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/supply-offers?supplier_user_id=$SupplierID" $null $Headers)
  foreach ($offer in $offers) {
    $labels = ConvertTo-Hashtable (Get-JsonValue $offer "labels")
    if ($labels["seed_key"] -eq $SeedKey -or [string](Get-JsonValue $offer "credential_ref") -eq $CredentialRef) {
      return $offer
    }
  }
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/supply-offers" @{
    supplier_user_id = $SupplierID
    kind = $Kind
    provider = $Provider
    credential_ref = $CredentialRef
    proxy_ref = $ProxyRef
    quota = $Quota
    unit_price = $UnitPrice
    labels = @{
      source = "seed-real-gateways"
      seed_key = $SeedKey
      local_real_gateway = "true"
      remote_id = $RemoteID
    }
  } $Headers
}

function Ensure-LedgerAllocation {
  param(
    [hashtable]$Headers,
    $Offer,
    [string]$InstanceID,
    [string]$Note
  )
  $offerID = [string](Get-JsonValue $Offer "id")
  $ledger = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/supply-ledger" $null $Headers)
  foreach ($entry in $ledger) {
    if ([string](Get-JsonValue $entry "offer_id") -eq $offerID -and [string](Get-JsonValue $entry "instance_id") -eq $InstanceID -and [string](Get-JsonValue $entry "status") -eq "allocated") {
      return $entry
    }
  }
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/supply-offers/$offerID/allocate" @{
    instance_id = $InstanceID
    note = $Note
  } $Headers
}

function Ensure-Approval {
  param(
    [hashtable]$Headers,
    [string]$InstanceID,
    [string]$AccountID
  )
  $reason = "local-real-seed: demo L2 approval for batch schedulable change"
  $approvals = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/approvals" $null $Headers)
  foreach ($approval in $approvals) {
    if ([string](Get-JsonValue $approval "reason") -eq $reason) {
      return $approval
    }
  }
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/approvals" @{
    instance_id = $InstanceID
    action = "batch_set_schedulable"
    account_ids = @($AccountID)
    schedulable = $false
    reason = $reason
    requested_by = "seed-real-gateways"
  } $Headers
}

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

function Invoke-DockerCompose {
  param([Parameter(Mandatory = $true)][string[]]$Arguments)
  $composeArgs = @("compose", "-f", $ComposeFile) + $Arguments
  Invoke-External docker @composeArgs
}

function Sql-Lit {
  param([string]$Value)
  if ($null -eq $Value) {
    return "''"
  }
  return "'" + $Value.Replace("'", "''") + "'"
}

function Sql-Num {
  param([double]$Value)
  return $Value.ToString([System.Globalization.CultureInfo]::InvariantCulture)
}

function Upsert-HealthSnapshot {
  param(
    [string]$SeedKey,
    [string]$ChannelID,
    [string]$PoolID,
    [string]$InstanceID,
    [string]$Window,
    [int]$SampleCount,
    [double]$SuccessRate,
    [double]$TTFTP50,
    [double]$TTFTP95,
    [double]$DurationP50,
    [double]$DurationP95,
    [double]$ErrorRate,
    [double]$TimeoutRate,
    [double]$RateLimitRate,
    [double]$CostPer1K,
    [double]$HealthScore,
    [double]$QualityScore,
    [double]$SuccessScore,
    [double]$TTFTScore,
    [double]$DurationScore,
    [double]$StabilityScore,
    [double]$CostScore,
    [double]$RiskScore,
    [string]$HealthState
  )
  $id = ("chsnap-{0}-{1}" -f $SeedKey, $Window).Replace("_", "-").Replace(":", "-")
  $windowColumn = '\"window\"'
  $columns = "id, channel_id, pool_id, instance_id, $windowColumn, sample_count, success_rate, ttft_p50, ttft_p95, duration_p50, duration_p95, error_rate, timeout_rate, rate_limit_rate, estimated_cost_per_1k_tokens, health_score, quality_score, success_score, ttft_score, duration_score, stability_score, cost_score, risk_score, health_state, created_at"
  $values = @(
    (Sql-Lit $id),
    (Sql-Lit $ChannelID),
    (Sql-Lit $PoolID),
    (Sql-Lit $InstanceID),
    (Sql-Lit $Window),
    [string]$SampleCount,
    (Sql-Num $SuccessRate),
    (Sql-Num $TTFTP50),
    (Sql-Num $TTFTP95),
    (Sql-Num $DurationP50),
    (Sql-Num $DurationP95),
    (Sql-Num $ErrorRate),
    (Sql-Num $TimeoutRate),
    (Sql-Num $RateLimitRate),
    (Sql-Num $CostPer1K),
    (Sql-Num $HealthScore),
    (Sql-Num $QualityScore),
    (Sql-Num $SuccessScore),
    (Sql-Num $TTFTScore),
    (Sql-Num $DurationScore),
    (Sql-Num $StabilityScore),
    (Sql-Num $CostScore),
    (Sql-Num $RiskScore),
    (Sql-Lit $HealthState),
    "now()"
  ) -join ", "
  $sql = "INSERT INTO channel_health_snapshots ($columns) VALUES ($values) ON CONFLICT (channel_id, $windowColumn) DO UPDATE SET pool_id=EXCLUDED.pool_id, instance_id=EXCLUDED.instance_id, sample_count=EXCLUDED.sample_count, success_rate=EXCLUDED.success_rate, ttft_p50=EXCLUDED.ttft_p50, ttft_p95=EXCLUDED.ttft_p95, duration_p50=EXCLUDED.duration_p50, duration_p95=EXCLUDED.duration_p95, error_rate=EXCLUDED.error_rate, timeout_rate=EXCLUDED.timeout_rate, rate_limit_rate=EXCLUDED.rate_limit_rate, estimated_cost_per_1k_tokens=EXCLUDED.estimated_cost_per_1k_tokens, health_score=EXCLUDED.health_score, quality_score=EXCLUDED.quality_score, success_score=EXCLUDED.success_score, ttft_score=EXCLUDED.ttft_score, duration_score=EXCLUDED.duration_score, stability_score=EXCLUDED.stability_score, cost_score=EXCLUDED.cost_score, risk_score=EXCLUDED.risk_score, health_state=EXCLUDED.health_state, created_at=EXCLUDED.created_at;"
  Invoke-DockerCompose @("exec", "-T", "postgres", "psql", "-U", "e2m", "-d", "e2m", "-v", "ON_ERROR_STOP=1", "-c", $sql)
}

function Upsert-HealthObservation {
  param(
    [string]$SeedKey,
    [int]$Index,
    [string]$ChannelID,
    [string]$PoolID,
    [string]$InstanceID,
    [string]$Model,
    [bool]$Success,
    [int]$StatusCode,
    [string]$ErrorType,
    [double]$FirstTokenMS,
    [double]$TotalMS,
    [long]$InputTokens,
    [long]$OutputTokens,
    [double]$EstimatedCost,
    [string]$Source,
    [int]$SecondsAgo
  )
  $id = ("chobs-{0}-{1}" -f $SeedKey, $Index).Replace("_", "-").Replace(":", "-")
  $successSQL = if ($Success) { "true" } else { "false" }
  $sourceValue = if ($Source -eq "") { "passive" } else { $Source }
  $sql = "INSERT INTO channel_observations (id, channel_id, instance_id, pool_id, model, success, status_code, error_type, first_token_ms, total_ms, input_tokens, output_tokens, estimated_cost, source, observed_at) VALUES (" +
    ((@(
      (Sql-Lit $id),
      (Sql-Lit $ChannelID),
      (Sql-Lit $InstanceID),
      (Sql-Lit $PoolID),
      (Sql-Lit $Model),
      $successSQL,
      [string]$StatusCode,
      (Sql-Lit $ErrorType),
      (Sql-Num $FirstTokenMS),
      (Sql-Num $TotalMS),
      [string]$InputTokens,
      [string]$OutputTokens,
      (Sql-Num $EstimatedCost),
      (Sql-Lit $sourceValue),
      ("now() - interval '{0} seconds'" -f $SecondsAgo)
    ) -join ", ")) +
    ") ON CONFLICT (id) DO UPDATE SET channel_id=EXCLUDED.channel_id, instance_id=EXCLUDED.instance_id, pool_id=EXCLUDED.pool_id, model=EXCLUDED.model, success=EXCLUDED.success, status_code=EXCLUDED.status_code, error_type=EXCLUDED.error_type, first_token_ms=EXCLUDED.first_token_ms, total_ms=EXCLUDED.total_ms, input_tokens=EXCLUDED.input_tokens, output_tokens=EXCLUDED.output_tokens, estimated_cost=EXCLUDED.estimated_cost, source=EXCLUDED.source, observed_at=EXCLUDED.observed_at;"
  Invoke-DockerCompose @("exec", "-T", "postgres", "psql", "-U", "e2m", "-d", "e2m", "-v", "ON_ERROR_STOP=1", "-c", $sql)
}

function Seed-ObservationSeries {
  param(
    [string]$SeedKey,
    $Channel,
    $Plan,
    [bool]$Healthy
  )
  $channelID = [string](Get-JsonValue $Channel "id")
  $poolID = [string](Get-JsonValue $Channel "pool_id")
  $instanceID = [string](Get-JsonValue $Plan "instance_id")
  $model = "gpt-4o-mini"
  $provider = [string](Get-JsonValue $Channel "provider")
  if ($provider -eq "oauth" -or $provider -eq "api_key" -or $provider -eq "anthropic") {
    $model = "claude-3-5-sonnet"
  }
  for ($i = 1; $i -le 8; $i++) {
    if ($Healthy) {
      Upsert-HealthObservation `
        -SeedKey $SeedKey `
        -Index $i `
        -ChannelID $channelID `
        -PoolID $poolID `
        -InstanceID $instanceID `
        -Model $model `
        -Success $true `
        -StatusCode 200 `
        -ErrorType "" `
        -FirstTokenMS (420 + ($i * 12)) `
        -TotalMS (1800 + ($i * 80)) `
        -InputTokens 900 `
        -OutputTokens 300 `
        -EstimatedCost 0.0024 `
        -Source "passive" `
        -SecondsAgo (4 + ($i * 5))
    } else {
      $ok = $i -le 2
      Upsert-HealthObservation `
        -SeedKey $SeedKey `
        -Index $i `
        -ChannelID $channelID `
        -PoolID $poolID `
        -InstanceID $instanceID `
        -Model $model `
        -Success $ok `
        -StatusCode $(if ($ok) { 200 } else { 504 }) `
        -ErrorType $(if ($ok) { "" } else { "timeout" }) `
        -FirstTokenMS $(if ($ok) { 1250 + ($i * 50) } else { 0 }) `
        -TotalMS $(if ($ok) { 7200 + ($i * 200) } else { 32000 + ($i * 300) }) `
        -InputTokens $(if ($ok) { 900 } else { 0 }) `
        -OutputTokens $(if ($ok) { 220 } else { 0 }) `
        -EstimatedCost $(if ($ok) { 0.0018 } else { 0 }) `
        -Source "passive" `
        -SecondsAgo (4 + ($i * 5))
    }
  }
}

function Seed-ChannelHealth {
  param(
    $NewAPIChannel,
    $Sub2APIChannel,
    $CPAPrimaryChannel,
    $CPASpareChannel,
    $NewAPIPlan,
    $Sub2APIPlan,
    $CPAPlan
  )
  Seed-ObservationSeries -SeedKey "local-real-newapi-primary" -Channel $NewAPIChannel -Plan $NewAPIPlan -Healthy $true
  Seed-ObservationSeries -SeedKey "local-real-sub2api-primary" -Channel $Sub2APIChannel -Plan $Sub2APIPlan -Healthy $true
  Seed-ObservationSeries -SeedKey "local-real-cpa-primary" -Channel $CPAPrimaryChannel -Plan $CPAPlan -Healthy $false
  Seed-ObservationSeries -SeedKey "local-real-cpa-spare" -Channel $CPASpareChannel -Plan $CPAPlan -Healthy $true

  $healthyRows = @(
    @{ Seed = "local-real-newapi-primary"; Channel = $NewAPIChannel; Plan = $NewAPIPlan; Sample = 14; Success = 0.986; TTFTP50 = 510; TTFTP95 = 930; DurationP50 = 2100; DurationP95 = 4400; Cost = 1.80; Health = 94; Quality = 93; Stability = 96; CostScore = 91 },
    @{ Seed = "local-real-sub2api-primary"; Channel = $Sub2APIChannel; Plan = $Sub2APIPlan; Sample = 11; Success = 0.982; TTFTP50 = 620; TTFTP95 = 1150; DurationP50 = 2600; DurationP95 = 5100; Cost = 1.60; Health = 92; Quality = 91; Stability = 94; CostScore = 92 },
    @{ Seed = "local-real-cpa-spare"; Channel = $CPASpareChannel; Plan = $CPAPlan; Sample = 12; Success = 0.995; TTFTP50 = 420; TTFTP95 = 780; DurationP50 = 1800; DurationP95 = 3600; Cost = 2.10; Health = 96; Quality = 94; Stability = 97; CostScore = 89 }
  )
  foreach ($row in $healthyRows) {
    foreach ($window in @("1m", "5m")) {
      Upsert-HealthSnapshot `
        -SeedKey "$($row.Seed)-$window" `
        -ChannelID ([string](Get-JsonValue $row.Channel "id")) `
        -PoolID ([string](Get-JsonValue $row.Channel "pool_id")) `
        -InstanceID ([string](Get-JsonValue $row.Plan "instance_id")) `
        -Window $window `
        -SampleCount ([int]$row.Sample) `
        -SuccessRate ([double]$row.Success) `
        -TTFTP50 ([double]$row.TTFTP50) `
        -TTFTP95 ([double]$row.TTFTP95) `
        -DurationP50 ([double]$row.DurationP50) `
        -DurationP95 ([double]$row.DurationP95) `
        -ErrorRate (1.0 - [double]$row.Success) `
        -TimeoutRate 0.0 `
        -RateLimitRate 0.0 `
        -CostPer1K ([double]$row.Cost) `
        -HealthScore ([double]$row.Health) `
        -QualityScore ([double]$row.Quality) `
        -SuccessScore ([double]$row.Success * 100.0) `
        -TTFTScore 96.0 `
        -DurationScore 90.0 `
        -StabilityScore ([double]$row.Stability) `
        -CostScore ([double]$row.CostScore) `
        -RiskScore 1.0 `
        -HealthState "healthy"
    }
  }
  foreach ($window in @("1m", "5m")) {
    Upsert-HealthSnapshot `
      -SeedKey "local-real-cpa-primary-$window" `
      -ChannelID ([string](Get-JsonValue $CPAPrimaryChannel "id")) `
      -PoolID ([string](Get-JsonValue $CPAPrimaryChannel "pool_id")) `
      -InstanceID ([string](Get-JsonValue $CPAPlan "instance_id")) `
      -Window $window `
      -SampleCount 8 `
      -SuccessRate 0.25 `
      -TTFTP50 1800 `
      -TTFTP95 6500 `
      -DurationP50 9000 `
      -DurationP95 32000 `
      -ErrorRate 0.75 `
      -TimeoutRate 0.50 `
      -RateLimitRate 0.125 `
      -CostPer1K 0.90 `
      -HealthScore 20 `
      -QualityScore 45 `
      -SuccessScore 25 `
      -TTFTScore 0 `
      -DurationScore 0 `
      -StabilityScore 30 `
      -CostScore 95 `
      -RiskScore 70 `
      -HealthState "unhealthy"
  }
}

function Invoke-ReconcileApply {
  param(
    [hashtable]$Headers,
    $Plan,
    [string]$Label,
    $ExpectedChannel,
    [string]$ExpectedRemoteID
  )
  $planID = [string](Get-JsonValue $Plan "id")
  $channelID = [string](Get-JsonValue $ExpectedChannel "id")
  $bindings = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/published-bindings?plan_id=$planID" $null $Headers)
  foreach ($binding in $bindings) {
    if ([string](Get-JsonValue $binding "channel_id") -eq $channelID -and [string](Get-JsonValue $binding "remote_id") -eq $ExpectedRemoteID -and [string](Get-JsonValue $binding "state") -eq "active") {
      Write-Host ("    {0}: binding already active, skipping apply" -f $Label)
      return $null
    }
  }
  try {
    $result = Invoke-Json "POST" "$E2MBaseUrl/api/v1/route-plans/$planID/reconcile?dry_run=false" $null $Headers
    Write-Host ("    {0}: apply OK, actions={1}" -f $Label, (ConvertTo-Array (Get-JsonValue $result "actions")).Count)
    return $result
  } catch {
    Write-Warning ("    {0}: reconcile apply failed: {1}" -f $Label, $_.Exception.Message)
    return $null
  }
}

function Wait-InstanceHealthSnapshots {
  param([hashtable]$Headers)
  $deadline = (Get-Date).AddSeconds(45)
  while ((Get-Date) -lt $deadline) {
    $snapshots = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/health-snapshots" $null $Headers)
    if ($snapshots.Count -gt 0) {
      Write-Host ("    instance health snapshots: {0}" -f $snapshots.Count)
      return $snapshots
    }
    Start-Sleep -Seconds 3
  }
  Write-Warning "    instance health snapshots are still empty; the background checker may need another cycle or a gateway account read may be failing."
  return @()
}

Write-Step "Verifying local real-gateway services"
Wait-Http "$E2MBaseUrl/healthz" "E2M Core"
Wait-Http "$Sub2APIBaseUrl/health" "sub2api"
Wait-Http "$NewAPIBaseUrl/api/status" "new-api"
Wait-Http "$CPABaseUrl/healthz" "CPA"

Write-Step "Logging in to E2M"
$token = Login-E2M
$headers = @{ Authorization = "Bearer $token" }

Write-Step "Reading real E2M instances and gateway accounts"
$instances = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/instances" $null $headers)
if ($instances.Count -lt 3) {
  throw "Expected at least 3 E2M instances, got $($instances.Count). Run .\scripts\bootstrap-real-gateways.ps1 first."
}
$sub2api = Resolve-Instance $instances $Sub2APIInstanceID "sub2api" "sub2api"
$newapi = Resolve-Instance $instances $NewAPIInstanceID "newapi" "new"
$cpa = Resolve-Instance $instances $CPAInstanceID "cpa" "cpa"

$sub2apiID = [string](Get-JsonValue $sub2api "id")
$newapiID = [string](Get-JsonValue $newapi "id")
$cpaID = [string](Get-JsonValue $cpa "id")

$sub2apiAccounts = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/instances/$sub2apiID/accounts" $null $headers)
$newapiAccounts = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/instances/$newapiID/accounts" $null $headers)
$cpaAccounts = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/instances/$cpaID/accounts" $null $headers)
Write-Host ("    sub2api {0}: {1} accounts" -f $sub2apiID, $sub2apiAccounts.Count)
Write-Host ("    new-api {0}: {1} channels" -f $newapiID, $newapiAccounts.Count)
Write-Host ("    CPA {0}: {1} auth files" -f $cpaID, $cpaAccounts.Count)

Assert-RemoteAccount $sub2apiAccounts "2" "sub2api seed account"
Assert-RemoteAccount $newapiAccounts "1" "new-api seed channel"
Assert-RemoteAccount $cpaAccounts "e2m-seed-cpa-primary.json" "CPA primary auth file"
Assert-RemoteAccount $cpaAccounts "e2m-seed-cpa-spare.json" "CPA spare auth file"

Write-Step "Ensuring supplier user"
$supplier = Ensure-User $headers "Local Supplier Pool" $E2MSupplierEmail $E2MSupplierPassword @("supplier")
$supplierID = [long](Get-JsonValue $supplier "id")
Write-Host ("    supplier: {0}" -f $supplierID)

Write-Step "Ensuring upstream pools"
$newapiPool = Ensure-UpstreamPool $headers "Local OpenAI Stable Pool" "openai" @("gpt-4o-mini", "gpt-5-mini") "local" "Local real new-api channel bound to channel id 1." "local-openai-stable-pool"
$sub2apiPool = Ensure-UpstreamPool $headers "Local sub2api Stable Pool" "openai" @("gpt-4o-mini") "local" "Local real sub2api account bound to account id 2." "local-sub2api-stable-pool"
$cpaPool = Ensure-UpstreamPool $headers "Local Anthropic OAuth Pool" "anthropic" @("claude-3-5-sonnet", "claude-3-opus") "local" "Local real CPA auth files for OAuth subscription demos." "local-anthropic-oauth-pool"

Write-Step "Ensuring upstream channels"
$newapiChannel = Ensure-UpstreamChannel `
  -Headers $headers `
  -PoolID ([string](Get-JsonValue $newapiPool "id")) `
  -SourceID "local-newapi-source" `
  -DisplayName "e2m-seed-newapi-primary" `
  -Provider "openai" `
  -Models @("gpt-4o-mini", "gpt-5-mini") `
  -Groups @() `
  -CredentialBindingID "local-newapi-channel-1" `
  -ProxyBindingID "" `
  -Priority 10 `
  -Weight 100 `
  -CostHint 1.80 `
  -Status "active" `
  -RemoteID "1" `
  -GatewayKind "newapi" `
  -InstanceID $newapiID `
  -SeedKey "local-newapi-channel-1"

$sub2apiChannel = Ensure-UpstreamChannel `
  -Headers $headers `
  -PoolID ([string](Get-JsonValue $sub2apiPool "id")) `
  -SourceID "local-sub2api-source" `
  -DisplayName "e2m-seed-sub2api-primary" `
  -Provider "openai" `
  -Models @("gpt-4o-mini") `
  -Groups @() `
  -CredentialBindingID "local-sub2api-account-2" `
  -ProxyBindingID "" `
  -Priority 10 `
  -Weight 100 `
  -CostHint 1.60 `
  -Status "active" `
  -RemoteID "2" `
  -GatewayKind "sub2api" `
  -InstanceID $sub2apiID `
  -SeedKey "local-sub2api-account-2"

$cpaPrimaryChannel = Ensure-UpstreamChannel `
  -Headers $headers `
  -PoolID ([string](Get-JsonValue $cpaPool "id")) `
  -SourceID "local-cpa-primary-source" `
  -DisplayName "seed-primary@local.dev" `
  -Provider "oauth" `
  -Models @("claude-3-5-sonnet") `
  -Groups @() `
  -CredentialBindingID "local-cpa-primary-auth-file" `
  -ProxyBindingID "local-cpa-primary" `
  -Priority 10 `
  -Weight 100 `
  -CostHint 2.90 `
  -Status "active" `
  -RemoteID "e2m-seed-cpa-primary.json" `
  -GatewayKind "cpa" `
  -InstanceID $cpaID `
  -SeedKey "local-cpa-primary-auth-file"

$cpaSpareChannel = Ensure-UpstreamChannel `
  -Headers $headers `
  -PoolID ([string](Get-JsonValue $cpaPool "id")) `
  -SourceID "local-cpa-spare-source" `
  -DisplayName "seed-spare@local.dev" `
  -Provider "api_key" `
  -Models @("claude-3-5-sonnet") `
  -Groups @() `
  -CredentialBindingID "local-cpa-spare-auth-file" `
  -ProxyBindingID "local-cpa-spare" `
  -Priority 20 `
  -Weight 80 `
  -CostHint 2.10 `
  -Status "active" `
  -RemoteID "e2m-seed-cpa-spare.json" `
  -GatewayKind "cpa" `
  -InstanceID $cpaID `
  -SeedKey "local-cpa-spare-auth-file"

Write-Step "Ensuring route plans"
$newapiPlan = Ensure-RoutePlan $headers $newapiID ([string](Get-JsonValue $newapiPool "id")) "stability" 1 "canary" 1 1 "local-newapi-plan"
$sub2apiPlan = Ensure-RoutePlan $headers $sub2apiID ([string](Get-JsonValue $sub2apiPool "id")) "stability" 1 "canary" 1 1 "local-sub2api-plan"
$cpaPlan = Ensure-RoutePlan $headers $cpaID ([string](Get-JsonValue $cpaPool "id")) "stability" 1 "canary" 1 1 "local-cpa-plan"

Write-Step "Ensuring route strategies"
[void](Upsert-PlanStrategy $headers $newapiPlan "Local new-api stability strategy" "stability_first" $true)
[void](Upsert-PlanStrategy $headers $sub2apiPlan "Local sub2api stability strategy" "stability_first" $true)
[void](Upsert-PlanStrategy $headers $cpaPlan "Local CPA demo advisory strategy" "stability_first" $false)

if (-not $SkipReconcile) {
  Write-Step "Reconciling route plans to record published bindings"
  [void](Invoke-ReconcileApply $headers $newapiPlan "new-api plan" $newapiChannel "1")
  [void](Invoke-ReconcileApply $headers $sub2apiPlan "sub2api plan" $sub2apiChannel "2")
  [void](Invoke-ReconcileApply $headers $cpaPlan "CPA plan" $cpaPrimaryChannel "e2m-seed-cpa-primary.json")
}

Write-Step "Ensuring supply offers and ledger allocations"
$newapiCredentialRef = "credential_ref:user/$supplierID/upstream/local-newapi-channel-1"
$sub2apiCredentialRef = "credential_ref:user/$supplierID/upstream/local-sub2api-account-2"
$cpaCredentialRef = "credential_ref:user/$supplierID/upstream/local-cpa-primary-auth-file"
$cpaProxyRef = "credential_ref:user/$supplierID/proxy/local-cpa-primary"
$newapiOffer = Ensure-SupplyOffer $headers $supplierID "api_key" "openai" $newapiCredentialRef "" 1000000 "0.0008 CNY / 1K tokens" "1" "local-newapi-offer"
$sub2apiOffer = Ensure-SupplyOffer $headers $supplierID "api_key" "openai" $sub2apiCredentialRef "" 600000 "0.0007 CNY / 1K tokens" "2" "local-sub2api-offer"
$cpaOffer = Ensure-SupplyOffer $headers $supplierID "oauth_subscription" "anthropic" $cpaCredentialRef $cpaProxyRef 500000 "39 CNY / day" "e2m-seed-cpa-primary.json" "local-cpa-offer"
[void](Ensure-LedgerAllocation $headers $newapiOffer $newapiID "local-real-seed allocation: new-api channel 1")
[void](Ensure-LedgerAllocation $headers $sub2apiOffer $sub2apiID "local-real-seed allocation: sub2api account 2")
[void](Ensure-LedgerAllocation $headers $cpaOffer $cpaID "local-real-seed allocation: CPA primary auth file")

Write-Step "Ensuring a demo approval request"
$approval = Ensure-Approval $headers $cpaID "e2m-seed-cpa-spare.json"
Write-Host ("    approval: {0} ({1})" -f ([string](Get-JsonValue $approval "id")), ([string](Get-JsonValue $approval "status")))

if (-not $SkipHealthSeed) {
  Write-Step "Seeding deterministic channel health snapshots"
  Seed-ChannelHealth `
    -NewAPIChannel $newapiChannel `
    -Sub2APIChannel $sub2apiChannel `
    -CPAPrimaryChannel $cpaPrimaryChannel `
    -CPASpareChannel $cpaSpareChannel `
    -NewAPIPlan $newapiPlan `
    -Sub2APIPlan $sub2apiPlan `
    -CPAPlan $cpaPlan
}

if (-not $SkipAutoSwitchEvaluate) {
  Write-Step "Creating or reusing an auto-switch decision for the CPA plan"
  try {
    $decision = Invoke-Json "POST" "$E2MBaseUrl/api/v1/route-plans/$([string](Get-JsonValue $cpaPlan "id"))/auto-switch/evaluate" $null $headers
    if ($null -eq $decision) {
      Write-Warning "    auto-switch evaluation returned 204; summary will still show channel health, but no decision was needed."
    } else {
      Write-Host ("    decision: {0} ({1})" -f ([string](Get-JsonValue $decision "id")), ([string](Get-JsonValue $decision "status")))
    }
  } catch {
    Write-Warning ("    auto-switch evaluation failed: {0}" -f $_.Exception.Message)
  }
}

Write-Step "Waiting briefly for instance health snapshots"
[void](Wait-InstanceHealthSnapshots $headers)

Write-Step "Seed complete"
$resources = [ordered]@{
  supplier_user = $supplierID
  pools = @(
    @{ name = [string](Get-JsonValue $newapiPool "name"); id = [string](Get-JsonValue $newapiPool "id") },
    @{ name = [string](Get-JsonValue $sub2apiPool "name"); id = [string](Get-JsonValue $sub2apiPool "id") },
    @{ name = [string](Get-JsonValue $cpaPool "name"); id = [string](Get-JsonValue $cpaPool "id") }
  )
  channels = @(
    @{ name = [string](Get-JsonValue $newapiChannel "display_name"); id = [string](Get-JsonValue $newapiChannel "id"); remote_id = "1" },
    @{ name = [string](Get-JsonValue $sub2apiChannel "display_name"); id = [string](Get-JsonValue $sub2apiChannel "id"); remote_id = "2" },
    @{ name = [string](Get-JsonValue $cpaPrimaryChannel "display_name"); id = [string](Get-JsonValue $cpaPrimaryChannel "id"); remote_id = "e2m-seed-cpa-primary.json" },
    @{ name = [string](Get-JsonValue $cpaSpareChannel "display_name"); id = [string](Get-JsonValue $cpaSpareChannel "id"); remote_id = "e2m-seed-cpa-spare.json" }
  )
  route_plans = @(
    @{ name = "new-api"; id = [string](Get-JsonValue $newapiPlan "id") },
    @{ name = "sub2api"; id = [string](Get-JsonValue $sub2apiPlan "id") },
    @{ name = "CPA"; id = [string](Get-JsonValue $cpaPlan "id") }
  )
}
$resources | ConvertTo-Json -Depth 8

Write-Host ""
Write-Host "Next validation commands:"
Write-Host "  `$body = @{ email='admin@local.dev'; password='admin123456' } | ConvertTo-Json -Compress"
Write-Host "  `$login = Invoke-RestMethod -Method Post -Uri '$E2MBaseUrl/api/v1/auth/login' -Body `$body -ContentType 'application/json'"
Write-Host "  `$h = @{ Authorization = `"Bearer `$(`$login.token)`" }"
Write-Host "  Invoke-RestMethod -Method Get -Uri '$E2MBaseUrl/api/v1/upstream-pools' -Headers `$h | ConvertTo-Json -Depth 8"
Write-Host "  Invoke-RestMethod -Method Get -Uri '$E2MBaseUrl/api/v1/route-plans/$([string](Get-JsonValue $cpaPlan "id"))/auto-switch-summary' -Headers `$h | ConvertTo-Json -Depth 8"
