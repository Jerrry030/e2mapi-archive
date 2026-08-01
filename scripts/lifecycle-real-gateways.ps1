<#
.SYNOPSIS
Validates the real Connector account lifecycle against Sub2API, NewAPI, and CPA.

.DESCRIPTION
This is an isolated, non-destructive lifecycle acceptance test. It creates a
fresh, uniquely labelled temporary pool and one platform-managed channel, then
drives the production path through create, update, maintenance/disable, and
retired/deferred-delete. A second temporary pool verifies that Core rejects an
owner-provided channel whose remote account does not already exist before a
Connector create task is queued.

Credential-blind owner-provided updates are optional. Pass one or more
gateway-specific OwnerRemoteID parameters to use independently created,
disposable native accounts. Each supplied account must already exist before
this run creates its platform-managed fixtures. The script updates and then
drains those accounts, but never reads their credentials or deletes them.

The script never adopts an existing pool or account. Existing task IDs are
captured before the run and only newly created task summaries are evaluated.
Delivery Keys are kept in memory and are never printed or serialized in the
result. By default the final delete is only scheduled; use -WaitForDelete to
wait through the real 30 minute safety delay and verify remote removal.
#>
[CmdletBinding()]
param(
  [string]$E2MBaseUrl = "http://localhost:18080",
  [string]$Sub2APIBaseUrl = "http://localhost:18090",
  [string]$NewAPIBaseUrl = "http://localhost:13000",
  [string]$CPABaseUrl = "http://localhost:18317",
  [string]$E2MAdminEmail = "admin@local.dev",
  [string]$E2MAdminPassword = "admin123456",
  [string]$E2MOwnerEmail = "owner-local@local.dev",
  [string]$Sub2APIInstanceID = "",
  [string]$NewAPIInstanceID = "",
  [string]$CPAInstanceID = "",
  [string]$Sub2APIOwnerRemoteID = "",
  [string]$NewAPIOwnerRemoteID = "",
  [string]$CPAOwnerRemoteID = "",
  [int]$TimeoutSeconds = 900,
  [int]$PollIntervalSeconds = 5,
  [switch]$WaitForDelete,
  [int]$DeleteWaitTimeoutSeconds = 2100,
  [switch]$LibraryOnly
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$script:RunID = "lifecycle-e2e-" + ([guid]::NewGuid().ToString("N").Substring(0, 16))
$script:PlatformPoolName = "E2M Lifecycle E2E $($script:RunID)"
$script:OwnerPoolName = "E2M Lifecycle Owner E2E $($script:RunID)"

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
  if ($null -eq $Value) { return @() }
  if ($Value -is [System.Array]) { return [object[]]$Value }
  return @($Value)
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
  if ($null -ne $property) { return $property.Value }
  return $null
}

function Get-InstanceRemoteKey {
  param(
    [Parameter(Mandatory = $true)][string]$InstanceID,
    [Parameter(Mandatory = $true)][string]$RemoteID
  )
  if ($InstanceID -eq "" -or $RemoteID -eq "") { throw "instance and remote ids are required" }
  return $InstanceID + [char]0 + $RemoteID
}

function ConvertTo-Hashtable {
  param($Object)
  $out = @{}
  if ($null -eq $Object) { return $out }
  if ($Object -is [System.Collections.IDictionary]) {
    foreach ($key in $Object.Keys) {
      if ($null -ne $Object[$key]) { $out[[string]$key] = [string]$Object[$key] }
    }
    return $out
  }
  foreach ($property in $Object.PSObject.Properties) {
    if ($null -ne $property.Value) { $out[$property.Name] = [string]$property.Value }
  }
  return $out
}

function New-TestKey {
  $bytes = New-Object byte[] 32
  $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
  try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
  try {
    $encoded = [Convert]::ToBase64String($bytes).TrimEnd("=").Replace("+", "-").Replace("/", "_")
    return "sk-e2m-e2e-$encoded"
  } finally {
    [Array]::Clear($bytes, 0, $bytes.Length)
  }
}

function Get-SecretFingerprint {
  param([Parameter(Mandatory = $true)][string]$Value)
  $sha = [System.Security.Cryptography.SHA256]::Create()
  try {
    $bytes = [Text.Encoding]::UTF8.GetBytes($Value)
    try { return ([BitConverter]::ToString($sha.ComputeHash($bytes))).Replace("-", "").ToLowerInvariant() }
    finally { [Array]::Clear($bytes, 0, $bytes.Length) }
  } finally { $sha.Dispose() }
}

function Invoke-Json {
  param(
    [Parameter(Mandatory = $true)][string]$Method,
    [Parameter(Mandatory = $true)][string]$Uri,
    $Body = $null,
    [hashtable]$Headers = @{},
    [switch]$Sensitive
  )
  $params = @{ Method = $Method; Uri = $Uri; Headers = $Headers; TimeoutSec = 30 }
  if ($null -ne $Body) {
    $params.Body = ConvertTo-JsonBody $Body
    $params.ContentType = "application/json; charset=utf-8"
  }
  try {
    return Invoke-RestMethod @params
  } catch {
    if ($Sensitive) { throw "$Method $Uri failed while storing a sensitive value; response detail withheld" }
    $detail = $_.Exception.Message
    if ($_.ErrorDetails -and $_.ErrorDetails.Message) { $detail = $_.ErrorDetails.Message }
    throw "$Method $Uri failed: $detail"
  }
}

# Returns status and parsed JSON without throwing for an expected 4xx/5xx. It
# intentionally never writes the response body to the console.
function Invoke-JsonResponse {
  param(
    [Parameter(Mandatory = $true)][string]$Method,
    [Parameter(Mandatory = $true)][string]$Uri,
    $Body = $null,
    [hashtable]$Headers = @{}
  )
  $params = @{ Method = $Method; Uri = $Uri; Headers = $Headers; TimeoutSec = 30; UseBasicParsing = $true }
  if ($null -ne $Body) {
    $params.Body = ConvertTo-JsonBody $Body
    $params.ContentType = "application/json; charset=utf-8"
  }
  try {
    $response = Invoke-WebRequest @params
    $raw = [string]$response.Content
    $json = $null
    if ($raw.Trim() -ne "") { try { $json = $raw | ConvertFrom-Json } catch {} }
    return [pscustomobject]@{ Status = [int]$response.StatusCode; Json = $json }
  } catch {
    $response = $_.Exception.Response
    if ($null -eq $response) { throw "$Method $Uri failed: $($_.Exception.Message)" }
    $raw = ""
    # Windows PowerShell 5.1 commonly consumes the response stream while
    # constructing its WebCmdlet error and preserves the body only in
    # ErrorDetails.Message. Prefer that safe in-memory copy, then fall back to
    # the response stream for hosts that leave it readable. Neither path emits
    # the raw body.
    if ($_.ErrorDetails -and $_.ErrorDetails.Message) {
      $raw = [string]$_.ErrorDetails.Message
    }
    if ($raw.Trim() -eq "") {
      try {
        $reader = New-Object IO.StreamReader($response.GetResponseStream())
        try { $raw = $reader.ReadToEnd() } finally { $reader.Dispose() }
      } catch {}
    }
    $json = $null
    if ($raw.Trim() -ne "") { try { $json = $raw | ConvertFrom-Json } catch {} }
    return [pscustomobject]@{ Status = [int]$response.StatusCode; Json = $json }
  }
}

function Wait-Http {
  param([Parameter(Mandatory = $true)][string]$Uri, [Parameter(Mandatory = $true)][string]$Name)
  $deadline = (Get-Date).AddSeconds([Math]::Min($TimeoutSeconds, 180))
  while ((Get-Date) -lt $deadline) {
    try {
      $response = Invoke-WebRequest -Uri $Uri -UseBasicParsing -TimeoutSec 5
      if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 300) { return }
    } catch {}
    Start-Sleep -Seconds 2
  }
  throw "Timed out waiting for $Name"
}

function Login-E2M {
  param([Parameter(Mandatory = $true)][string]$Email, [Parameter(Mandatory = $true)][string]$Password)
  $result = Invoke-Json "POST" "$E2MBaseUrl/api/v1/auth/login" @{ email = $Email; password = $Password }
  $token = [string](Get-JsonValue $result "token")
  if ($token -eq "") { throw "E2M login did not return a token" }
  return $token
}

function Resolve-Instance {
  param(
    [Parameter(Mandatory = $true)][array]$Instances,
    [string]$PreferredID,
    [Parameter(Mandatory = $true)][string]$Kind,
    [Parameter(Mandatory = $true)][string]$Needle
  )
  if ($PreferredID -ne "") {
    $exact = @($Instances | Where-Object { [string](Get-JsonValue $_ "id") -eq $PreferredID -and [string](Get-JsonValue $_ "kind") -eq $Kind })
    if ($exact.Count -eq 1) { return $exact[0] }
  }
  $matches = @($Instances | Where-Object {
    [string](Get-JsonValue $_ "kind") -eq $Kind -and
      ([string](Get-JsonValue $_ "name")).ToLowerInvariant().Contains($Needle.ToLowerInvariant())
  })
  if ($matches.Count -eq 1) { return $matches[0] }
  $matches = @($Instances | Where-Object { [string](Get-JsonValue $_ "kind") -eq $Kind })
  if ($matches.Count -eq 1) { return $matches[0] }
  throw "Could not uniquely resolve the $Kind instance"
}

function Assert-Unique {
  param([Parameter(Mandatory = $true)][array]$Values, [Parameter(Mandatory = $true)][string]$Message)
  if (@($Values | Select-Object -Unique).Count -ne $Values.Count) { throw $Message }
}

function Set-InstancePoolRollout {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][string]$PoolID,
    [Parameter(Mandatory = $true)][long]$OwnerID,
    [Parameter(Mandatory = $true)][string[]]$TargetInstanceIDs
  )
  foreach ($instanceID in $TargetInstanceIDs) {
    [void](Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-pools/$PoolID/rollout-targets" @{
      scope = "instance"; user_id = $OwnerID; instance_id = $instanceID;
      enabled = $true; rollout = "immediate"; note = "temporary lifecycle E2E fixture"
    } $Headers)
  }
  $preview = Invoke-Json "GET" "$E2MBaseUrl/api/v1/upstream-pools/$PoolID/rollout-targets" $null $Headers
  $resolutions = ConvertTo-Array (Get-JsonValue $preview "instances")
  foreach ($instanceID in $TargetInstanceIDs) {
    $resolution = @($resolutions | Where-Object { [string](Get-JsonValue $_ "instance_id") -eq $instanceID })
    if ($resolution.Count -ne 1 -or -not [bool](Get-JsonValue $resolution[0] "enabled") -or
        [string](Get-JsonValue $resolution[0] "source") -ne "instance") {
      throw "Lifecycle pool is not explicitly enabled for target instance '$instanceID'"
    }
  }
  $unexpected = @($resolutions | Where-Object {
    [bool](Get-JsonValue $_ "enabled") -and $TargetInstanceIDs -notcontains [string](Get-JsonValue $_ "instance_id")
  })
  if ($unexpected.Count -gt 0) {
    throw "Lifecycle pool rollout reaches an instance outside this run's precise target scope"
  }
  return $preview
}

function Disable-InstancePoolRollout {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][string]$PoolID,
    [Parameter(Mandatory = $true)][long]$OwnerID,
    [Parameter(Mandatory = $true)][string[]]$TargetInstanceIDs
  )
  foreach ($instanceID in $TargetInstanceIDs) {
    [void](Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-pools/$PoolID/rollout-targets" @{
      scope = "instance"; user_id = $OwnerID; instance_id = $instanceID;
      enabled = $false; rollout = "immediate"; note = "lifecycle E2E cleanup"
    } $Headers)
  }
}

function Set-InventoryReady {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)][string]$ChannelID)
  return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-channels/$ChannelID/inventory-state" @{ state = "ready" } $Headers
}

function Complete-PoolRetirement {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][string]$PoolID
  )
  $jobs = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/pool-retirement-jobs?pool_id=$PoolID" $null $Headers)
  $job = @($jobs | Where-Object { [string](Get-JsonValue $_ "status") -ne "completed" } | Select-Object -First 1)
  if ($job.Count -eq 0) {
    $completed = @($jobs | Where-Object { [string](Get-JsonValue $_ "status") -eq "completed" } | Select-Object -First 1)
    if ($completed.Count -eq 1) { return $completed[0] }
    $job = @(Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-pools/$PoolID/retirement-jobs" $null $Headers)
  }
  $jobID = [string](Get-JsonValue $job[0] "id")
  if ($jobID -eq "") { throw "Pool retirement for '$PoolID' did not return a job id" }

  $deadline = (Get-Date).AddSeconds([Math]::Min($TimeoutSeconds, 300))
  $lastStatus = [string](Get-JsonValue $job[0] "status")
  while ((Get-Date) -lt $deadline) {
    if ($lastStatus -eq "completed") { return $job[0] }
    if ($lastStatus -in @("pending", "partial", "finalizing", "cleanup")) {
      try {
        $job = @(Invoke-Json "POST" "$E2MBaseUrl/api/v1/pool-retirement-jobs/$jobID/run" $null $Headers)
        $lastStatus = [string](Get-JsonValue $job[0] "status")
        continue
      } catch {
        # A background retirement worker may have claimed the same durable job.
        # Re-read its state before deciding whether the operation really failed.
      }
    }
    Start-Sleep -Seconds ([Math]::Max(1, [Math]::Min($PollIntervalSeconds, 5)))
    $jobs = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/pool-retirement-jobs?pool_id=$PoolID" $null $Headers)
    $current = @($jobs | Where-Object { [string](Get-JsonValue $_ "id") -eq $jobID })
    if ($current.Count -ne 1) { throw "Pool retirement job '$jobID' disappeared" }
    $job = @($current[0])
    $lastStatus = [string](Get-JsonValue $job[0] "status")
  }
  throw "Timed out completing durable pool retirement '$jobID' (status=$lastStatus)"
}

function New-PoolBody {
  param([Parameter(Mandatory = $true)][string]$Name, [Parameter(Mandatory = $true)][string]$Marker, [string]$Status = "active")
  return @{
    name = $Name; provider = "openai"; models = @("gpt-4o-mini"); region = "local";
    status = $Status; description = "Temporary isolated lifecycle acceptance resource";
    labels = @{ source = "lifecycle-real-gateways"; lifecycle_run = $Marker; access = "explicit_instances" }
  }
}

function New-ChannelBody {
  param(
    [Parameter(Mandatory = $true)][string]$PoolID,
    [Parameter(Mandatory = $true)][string]$SourceID,
    [Parameter(Mandatory = $true)][string]$DisplayName,
    [Parameter(Mandatory = $true)][string]$BindingID,
    [Parameter(Mandatory = $true)][string]$Marker,
    [Parameter(Mandatory = $true)][string]$Ownership,
    [string]$Status = "active"
  )
  return @{
    pool_id = $PoolID; account_ownership = $Ownership; source_id = $SourceID;
    display_name = $DisplayName; provider = "openai"; models = @("gpt-4o-mini"); groups = @();
    credential_binding_id = $BindingID; proxy_binding_id = ""; priority = 10; weight = 100;
    cost_hint = 0; status = $Status;
    labels = @{ source = "lifecycle-real-gateways"; lifecycle_run = $Marker; type = "apikey" }
  }
}

function Update-PoolStatus {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)]$Pool, [Parameter(Mandatory = $true)][string]$Status)
  $body = New-PoolBody ([string](Get-JsonValue $Pool "name")) ([string](Get-JsonValue (Get-JsonValue $Pool "labels") "lifecycle_run")) $Status
  $body.labels = ConvertTo-Hashtable (Get-JsonValue $Pool "labels")
  $body.status = $Status
  return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-pools/$([string](Get-JsonValue $Pool "id"))" $body $Headers
}

function Update-Channel {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)]$Channel, [Parameter(Mandatory = $true)][string]$Status, [string]$DisplayName = "")
  if ($DisplayName -eq "") { $DisplayName = [string](Get-JsonValue $Channel "display_name") }
  $body = @{
    pool_id = [string](Get-JsonValue $Channel "pool_id"); account_ownership = [string](Get-JsonValue $Channel "account_ownership");
    source_id = [string](Get-JsonValue $Channel "source_id"); display_name = $DisplayName;
    provider = [string](Get-JsonValue $Channel "provider"); models = @(Get-JsonValue $Channel "models");
    groups = @(Get-JsonValue $Channel "groups"); credential_binding_id = [string](Get-JsonValue $Channel "credential_binding_id");
    proxy_binding_id = [string](Get-JsonValue $Channel "proxy_binding_id"); priority = [int](Get-JsonValue $Channel "priority");
    weight = [int](Get-JsonValue $Channel "weight"); cost_hint = [double](Get-JsonValue $Channel "cost_hint"); status = $Status;
    labels = ConvertTo-Hashtable (Get-JsonValue $Channel "labels")
  }
  return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-channels/$([string](Get-JsonValue $Channel "id"))" $body $Headers
}

function Set-DeliveryKey {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)][string]$ChannelID, [Parameter(Mandatory = $true)][string]$Value)
  return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-channels/$ChannelID/delivery-key" @{ value = $Value } $Headers -Sensitive
}

function Get-Tasks {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)][string]$InstanceID)
  return @(ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/connector-tasks?instance_id=$InstanceID&limit=500" $null $Headers))
}

function Select-NewCorrelatedTasks {
  param(
    [Parameter(Mandatory = $true)][AllowEmptyCollection()][array]$Tasks,
    [Parameter(Mandatory = $true)][AllowEmptyCollection()][string[]]$BaselineIDs,
    [Parameter(Mandatory = $true)][string]$Type,
    [string]$TargetChannelID = "",
    [string]$TargetAccountID = ""
  )
  if ($TargetChannelID -eq "" -and $TargetAccountID -eq "") {
    throw "Lifecycle task correlation requires a target channel or account id"
  }
  return @($Tasks | Where-Object {
    $taskID = [string](Get-JsonValue $_ "id")
    $channelMatches = $TargetChannelID -eq "" -or [string](Get-JsonValue $_ "target_channel_id") -eq $TargetChannelID
    $accountMatches = $TargetAccountID -eq "" -or [string](Get-JsonValue $_ "target_account_id") -eq $TargetAccountID
    $BaselineIDs -notcontains $taskID -and [string](Get-JsonValue $_ "type") -eq $Type -and $channelMatches -and $accountMatches
  })
}

function Assert-NewSuccessfulTask {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][string]$InstanceID,
    [Parameter(Mandatory = $true)][AllowEmptyCollection()][string[]]$BaselineIDs,
    [Parameter(Mandatory = $true)][string]$Type,
    [Parameter(Mandatory = $true)][string]$Name,
    [string]$TargetChannelID = "",
    [string]$TargetAccountID = ""
  )
  $tasks = Get-Tasks $Headers $InstanceID
  $selected = @(Select-NewCorrelatedTasks $tasks $BaselineIDs $Type $TargetChannelID $TargetAccountID)
  if ($selected.Count -lt 1) { throw "No new correlated $Name task was observed on instance $InstanceID" }
  if (@($selected | Where-Object { [string](Get-JsonValue $_ "status") -in @("failed", "expired") }).Count -gt 0) {
    throw "A new $Name task failed on instance $InstanceID"
  }
  $succeeded = @($selected | Where-Object { [string](Get-JsonValue $_ "status") -eq "succeeded" })
  if ($succeeded.Count -lt 1) { throw "New correlated $Name task on instance $InstanceID did not complete" }
  return $succeeded[0]
}

function Wait-Onboarding {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)][string]$PoolID, [Parameter(Mandatory = $true)][string[]]$InstanceIDs)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $last = ""
  while ((Get-Date) -lt $deadline) {
    $ops = Invoke-Json "GET" "$E2MBaseUrl/api/v1/operations-center" $null $Headers
    $allFlows = ConvertTo-Array (Get-JsonValue $ops "onboarding")
    $poolFlows = @($allFlows | Where-Object { [string](Get-JsonValue $_ "pool_id") -eq $PoolID })
    $unexpected = @($poolFlows | Where-Object { $InstanceIDs -notcontains [string](Get-JsonValue $_ "instance_id") })
    if ($unexpected.Count -gt 0) {
      throw "Lifecycle pool produced onboarding workflow outside the preflight target scope"
    }
    $flows = @($poolFlows | Where-Object { $InstanceIDs -contains [string](Get-JsonValue $_ "instance_id") })
    # Operations Center exposes the durable execution stage and generation
    # receipt separately. A currently-serving workflow briefly becomes
    # running/checking_gateway during its periodic verification, so stage/status
    # alone would make the acceptance count flap. The current generation is the
    # stable business-ready receipt; generation zero still prevents a first
    # activation from being accepted before it has ever completed.
    $ready = @($flows | Where-Object {
      $desired = [long](Get-JsonValue $_ "desired_generation")
      $lastReady = [long](Get-JsonValue $_ "last_ready_generation")
      $desired -gt 0 -and $lastReady -eq $desired
    })
    $summary = "{0}/{1} ready" -f $ready.Count, $InstanceIDs.Count
    if ($summary -ne $last) { Write-Host "    $summary"; $last = $summary }
    if ($flows.Count -eq $InstanceIDs.Count -and $ready.Count -eq $InstanceIDs.Count) { return $flows }
    Start-Sleep -Seconds $PollIntervalSeconds
  }
  throw "Timed out waiting for lifecycle onboarding"
}

function Reconcile {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)][string]$PlanID)
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/route-plans/$PlanID/reconcile?dry_run=false" $null $Headers
}

function Wait-TasksSucceeded {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)][string]$InstanceID, [Parameter(Mandatory = $true)][string[]]$TaskIDs, [Parameter(Mandatory = $true)][string]$Type)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    $tasks = Get-Tasks $Headers $InstanceID
    $selected = @($tasks | Where-Object { $TaskIDs -contains [string](Get-JsonValue $_ "id") -and [string](Get-JsonValue $_ "type") -eq $Type })
    if ($selected.Count -eq $TaskIDs.Count -and @($selected | Where-Object { [string](Get-JsonValue $_ "status") -eq "succeeded" }).Count -eq $TaskIDs.Count) { return $selected }
    if (@($selected | Where-Object { [string](Get-JsonValue $_ "status") -in @("failed", "expired") }).Count -gt 0) { throw "A $Type lifecycle task failed" }
    Start-Sleep -Seconds $PollIntervalSeconds
  }
  throw "Timed out waiting for $Type tasks"
}

function Get-PlanBindings {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)][string]$PlanID)
  $encodedPlanID = [System.Uri]::EscapeDataString($PlanID)
  return @(ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/published-bindings?plan_id=$encodedPlanID" $null $Headers))
}

function Get-OwnerBindingClaims {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][long]$OwnerID
  )
  $plans = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/route-plans?user_id=$OwnerID" $null $Headers)
  $claims = @()
  foreach ($plan in $plans) {
    $planID = [string](Get-JsonValue $plan "id")
    if ($planID -eq "") { throw "Owner route plan inventory contains an empty plan id" }
    $claims += @(Get-PlanBindings $Headers $planID)
  }
  return @($claims)
}

function Get-RoutePlan {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][long]$OwnerID,
    [Parameter(Mandatory = $true)][string]$PlanID
  )
  $plans = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/route-plans?user_id=$OwnerID" $null $Headers)
  $matches = @($plans | Where-Object { [string](Get-JsonValue $_ "id") -eq $PlanID })
  if ($matches.Count -ne 1) { throw "Expected one route plan '$PlanID'" }
  return $matches[0]
}

function Assert-DeferredDeleteTask {
  param(
    [Parameter(Mandatory = $true)]$Task,
    [Parameter(Mandatory = $true)][string]$PlanID,
    [Parameter(Mandatory = $true)][long]$SchedulingGeneration
  )
  if ([string](Get-JsonValue $Task "status") -notin @("pending", "leased")) {
    throw "Deferred delete task for plan $PlanID is not queued"
  }
  if ([int](Get-JsonValue $Task "max_attempts") -ne 12) {
    throw "Deferred delete task for plan $PlanID does not retain 12 attempts"
  }
  $createdAt = [datetime]::Parse([string](Get-JsonValue $Task "created_at")).ToUniversalTime()
  $availableAt = [datetime]::Parse([string](Get-JsonValue $Task "available_at")).ToUniversalTime()
  $delaySeconds = ($availableAt - $createdAt).TotalSeconds
  if ($delaySeconds -lt 1790 -or $delaySeconds -gt 1810) {
    throw "Deferred delete task for plan $PlanID does not retain the 30 minute delay"
  }
  $fence = Get-JsonValue $Task "scheduling_fence"
  if ($null -eq $fence -or [string](Get-JsonValue $fence "scope") -ne "auto-switch/plan/$PlanID" -or
      [long](Get-JsonValue $fence "version") -ne $SchedulingGeneration -or
      [long](Get-JsonValue $fence "sequence") -le 0) {
    throw "Deferred delete task for plan $PlanID does not match the retirement generation fence"
  }
}

function Get-RemoteIDForChannel {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][string]$PlanID,
    [Parameter(Mandatory = $true)][string]$ChannelID
  )
  $bindings = Get-PlanBindings $Headers $PlanID
  $binding = @($bindings | Where-Object { [string](Get-JsonValue $_ "channel_id") -eq $ChannelID })
  if ($binding.Count -ne 1) { throw "Expected one lifecycle binding on plan $PlanID" }
  $remoteID = [string](Get-JsonValue $binding[0] "remote_id")
  if ($remoteID -eq "") { throw "Lifecycle binding on plan $PlanID has no remote id" }
  return $remoteID
}

function Get-Accounts {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)][string]$InstanceID)
  return @(ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/instances/$InstanceID/accounts" $null $Headers))
}

function Assert-RemoteState {
  param([Parameter(Mandatory = $true)][hashtable]$Headers, [Parameter(Mandatory = $true)]$Instances, [Parameter(Mandatory = $true)][hashtable]$PlanByInstance, [Parameter(Mandatory = $true)][string]$ChannelID, [Parameter(Mandatory = $true)][bool]$Schedulable)
  foreach ($instance in $Instances) {
    $instanceID = [string](Get-JsonValue $instance "id")
    $bindings = Get-PlanBindings $Headers ([string]$PlanByInstance[$instanceID])
    $binding = @($bindings | Where-Object { [string](Get-JsonValue $_ "channel_id") -eq $ChannelID })
    if ($binding.Count -ne 1) { throw "Expected one lifecycle binding on instance $instanceID" }
    $remoteID = [string](Get-JsonValue $binding[0] "remote_id")
    if ($remoteID -eq "") { throw "Lifecycle binding on instance $instanceID has no remote id" }
    $accounts = Get-Accounts $Headers $instanceID
    $account = @($accounts | Where-Object { [string](Get-JsonValue $_ "id") -eq $remoteID })
    if ($account.Count -ne 1) { throw "Expected one lifecycle account on instance $instanceID" }
    if ([bool](Get-JsonValue $account[0] "schedulable") -ne $Schedulable) { throw "Unexpected scheduling state on instance $instanceID" }
  }
}

function Assert-OwnerOnlyRejection {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)]$Instance,
    [Parameter(Mandatory = $true)][string]$PoolID,
    [Parameter(Mandatory = $true)][string]$Marker,
    [Parameter(Mandatory = $true)][hashtable]$CleanupState
  )
  $channelBody = New-ChannelBody $PoolID ("owner-source-" + $Marker) ("Owner-only " + $Marker) ("owner-binding-" + $Marker) $Marker "owner_provided"
  $channel = Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-channels" $channelBody $Headers
  $channel = Update-Channel $Headers $channel "active"
  $CleanupState.OwnerChannel = $channel
  $channelID = [string](Get-JsonValue $channel "id")
  $plan = Invoke-Json "POST" "$E2MBaseUrl/api/v1/route-plans" @{
    user_id = [long](Get-JsonValue $Instance "user_id"); instance_id = [string](Get-JsonValue $Instance "id");
    pool_id = $PoolID; tier = "stability"; status = "draft"; max_channels = 0; rollout = "immediate"; labels = @{ lifecycle_run = $Marker }
  } $Headers
  $CleanupState.OwnerPlan = $plan
  $planID = [string](Get-JsonValue $plan "id")
  $response = Invoke-JsonResponse "POST" "$E2MBaseUrl/api/v1/route-plans/$planID/reconcile?dry_run=false" $null $Headers
  if ($response.Status -ne 422 -or $null -eq $response.Json -or [string](Get-JsonValue $response.Json "code") -ne "unsupported_lifecycle") {
    throw "owner-provided lifecycle was not rejected before create (status=$($response.Status))"
  }
  return [ordered]@{ pool_id = $PoolID; channel_id = $channelID; plan_id = $planID; rejection = "unsupported_lifecycle" }
}

function Assert-OwnerUpdateOnly {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)]$Instance,
    [Parameter(Mandatory = $true)][string]$RemoteID,
    [Parameter(Mandatory = $true)][string]$Marker,
    [Parameter(Mandatory = $true)][hashtable]$CleanupState
  )
  $instanceID = [string](Get-JsonValue $Instance "id")
  $kind = [string](Get-JsonValue $Instance "kind")
  $displayName = "Owner update $kind $Marker"
  # This pool intentionally remains in maintenance. A manual plan reconcile can
  # update the explicitly scoped existing account, while the background
  # all-users onboarding runner never discovers this fixture.
  $pool = Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-pools" (New-PoolBody ("E2M Owner Update " + $kind + " " + $Marker) ($Marker + "-" + $kind) "maintenance") $Headers
  $CleanupState.OwnerUpdatePools += $pool
  $labels = @{
    source = "lifecycle-real-gateways"; lifecycle_run = $Marker; type = "apikey";
    remote_id = $RemoteID; instance_id = $instanceID
  }
  $channelBody = @{
    pool_id = [string](Get-JsonValue $pool "id"); account_ownership = "owner_provided";
    source_id = "owner-update-source-$kind-$Marker"; display_name = $displayName;
    provider = "openai"; models = @(); groups = @(); credential_binding_id = "";
    proxy_binding_id = ""; priority = 11; weight = 0; cost_hint = 0; status = "active";
    labels = $labels
  }
  $channel = Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-channels" $channelBody $Headers
  $channel = Update-Channel $Headers $channel "active" $displayName
  $CleanupState.OwnerUpdateChannels += $channel
  $channelID = [string](Get-JsonValue $channel "id")
  $plan = Invoke-Json "POST" "$E2MBaseUrl/api/v1/route-plans" @{
    user_id = [long](Get-JsonValue $Instance "user_id"); instance_id = $instanceID;
    pool_id = [string](Get-JsonValue $pool "id"); tier = "stability"; status = "draft";
    max_channels = 0; rollout = "immediate"; labels = @{ lifecycle_run = $Marker; owner_update = $kind }
  } $Headers
  $CleanupState.OwnerUpdatePlans += $plan
  $baseline = @((Get-Tasks $Headers $instanceID) | ForEach-Object { [string](Get-JsonValue $_ "id") })
  Reconcile $Headers ([string](Get-JsonValue $plan "id")) | Out-Null
  Assert-NewSuccessfulTask $Headers $instanceID $baseline "gateway.account.update" "owner-provided update" $channelID $RemoteID | Out-Null

  $bindings = Get-PlanBindings $Headers ([string](Get-JsonValue $plan "id"))
  $binding = @($bindings | Where-Object { [string](Get-JsonValue $_ "channel_id") -eq $channelID })
  if ($binding.Count -ne 1 -or [string](Get-JsonValue $binding[0] "remote_id") -ne $RemoteID -or
      [string](Get-JsonValue $binding[0] "account_ownership") -ne "owner_provided") {
    throw "Owner-provided update did not retain the scoped existing remote account on $kind"
  }
  $accounts = Get-Accounts $Headers $instanceID
  $account = @($accounts | Where-Object { [string](Get-JsonValue $_ "id") -eq $RemoteID })
  if ($account.Count -ne 1) { throw "Disposable owner-provided account disappeared after update on $kind" }
  if ($kind -ne "cpa" -and [string](Get-JsonValue $account[0] "display_name") -ne $displayName) {
    throw "Owner-provided metadata update was not visible on $kind"
  }
  return [ordered]@{
    kind = $kind; pool_id = [string](Get-JsonValue $pool "id"); channel_id = $channelID;
    plan_id = [string](Get-JsonValue $plan "id"); remote_id = $RemoteID; update = "succeeded"
  }
}

# Failure cleanup is deliberately scoped by the IDs created during this run.
# It never selects resources by a human name and never mutates a baseline pool,
# channel, plan, or account. Errors are returned as step codes so a cleanup
# failure cannot replace the primary acceptance-test error or leak API bodies.
function Invoke-LifecycleCleanup {
  param([Parameter(Mandatory = $true)][hashtable]$State)

  $errors = New-Object System.Collections.Generic.List[string]
  $headers = $State.Headers
  if ($null -eq $headers) { return @() }

  # Retire this run's channels first, then capture their remote bindings before
  # any withdrawal. The durable retirement job is the sole owner of both the
  # reversible drain and final-generation deprovision cleanup. Cleanup must not
  # call normal reconcile after the job completes: doing so would advance the
  # plan generation and make the job-owned delayed delete stale.
  $retiredChannels = @{}
  foreach ($entry in @(
    [pscustomobject]@{ Name = "platform"; Value = $State.PlatformChannel },
    [pscustomobject]@{ Name = "owner"; Value = $State.OwnerChannel }
  )) {
    if ($null -eq $entry.Value) { continue }
    $channelID = [string](Get-JsonValue $entry.Value "id")
    if ($channelID -eq "") { continue }
    try {
      $updated = Update-Channel $headers $entry.Value "retired"
      $entry.Value = $updated
      $retiredChannels[$channelID] = $true
    } catch {
      $errors.Add("$($entry.Name)_channel_retire")
    }
  }
  foreach ($channel in @($State.OwnerUpdateChannels)) {
    if ($null -eq $channel) { continue }
    $channelID = [string](Get-JsonValue $channel "id")
    if ($channelID -eq "") { continue }
    try {
      Update-Channel $headers $channel "retired" | Out-Null
      $retiredChannels[$channelID] = $true
    } catch {
      $errors.Add("owner_update_channel_retire:$channelID")
    }
  }
  $poolIDs = @()
  foreach ($pool in @($State.PlatformPool, $State.OwnerPool) + @($State.OwnerUpdatePools)) {
    if ($null -ne $pool) {
      $poolID = [string](Get-JsonValue $pool "id")
      if ($poolID -ne "") { $poolIDs += $poolID }
    }
  }
  $instanceIDs = @($State.InstanceIDs)
  $plans = @()
  if ($State.OwnerID -gt 0 -and $poolIDs.Count -gt 0) {
    try {
      $allPlans = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/route-plans?user_id=$($State.OwnerID)" $null $headers)
      $plans += @($allPlans | Where-Object {
        $poolIDs -contains [string](Get-JsonValue $_ "pool_id") -and
          $instanceIDs -contains [string](Get-JsonValue $_ "instance_id")
      })
    } catch {
      $errors.Add("temporary_plan_discovery")
    }
  }
  foreach ($plan in @($State.KnownPlans.Values)) {
    if ($null -ne $plan) { $plans += $plan }
  }
  if ($null -ne $State.OwnerPlan) { $plans += $State.OwnerPlan }
  $plans += @($State.OwnerUpdatePlans)

  $seenPlans = @{}
  $withdrawals = @()
  $deleteTasksBefore = @{}
  $finalDeletePlans = @{}
  $finalDeleteTasks = @{}
  if ($State.ContainsKey("FinalDeletePlans") -and $null -ne $State.FinalDeletePlans) {
    $finalDeletePlans = $State.FinalDeletePlans
  }
  if ($State.ContainsKey("FinalDeleteTasks") -and $null -ne $State.FinalDeleteTasks) {
    $finalDeleteTasks = $State.FinalDeleteTasks
  }
  foreach ($plan in $plans) {
    $planID = [string](Get-JsonValue $plan "id")
    $poolID = [string](Get-JsonValue $plan "pool_id")
    if ($planID -eq "" -or $seenPlans.ContainsKey($planID) -or $poolIDs -notcontains $poolID) { continue }
    $seenPlans[$planID] = $true

    $channelID = ""
    if ($null -ne $State.PlatformPool -and $poolID -eq [string](Get-JsonValue $State.PlatformPool "id") -and $null -ne $State.PlatformChannel) {
      $channelID = [string](Get-JsonValue $State.PlatformChannel "id")
    } elseif ($null -ne $State.OwnerPool -and $poolID -eq [string](Get-JsonValue $State.OwnerPool "id") -and $null -ne $State.OwnerChannel) {
      $channelID = [string](Get-JsonValue $State.OwnerChannel "id")
    } else {
      $ownerUpdateChannel = @($State.OwnerUpdateChannels | Where-Object {
        [string](Get-JsonValue $_ "pool_id") -eq $poolID
      })
      if ($ownerUpdateChannel.Count -eq 1) {
        $channelID = [string](Get-JsonValue $ownerUpdateChannel[0] "id")
      }
    }
    if ($channelID -eq "" -or -not $retiredChannels.ContainsKey($channelID)) { continue }

    try {
      $bindings = Get-PlanBindings $headers $planID
      $planWithdrawals = @($bindings | Where-Object {
        [string](Get-JsonValue $_ "channel_id") -eq $channelID -and
          [string](Get-JsonValue $_ "remote_id") -ne ""
      })
      if ($planWithdrawals.Count -eq 0) { continue }
      foreach ($binding in $planWithdrawals) {
        $ownership = [string](Get-JsonValue $binding "account_ownership")
        if ($ownership -eq "") {
          if ($channelID -eq [string](Get-JsonValue $State.PlatformChannel "id")) { $ownership = "platform_managed" }
          else { $ownership = "owner_provided" }
        }
        if ($ownership -ne "platform_managed") { continue }
        $instanceID = [string](Get-JsonValue $binding "instance_id")
        if ($instanceID -eq "") { $instanceID = [string](Get-JsonValue $plan "instance_id") }
        $remoteID = [string](Get-JsonValue $binding "remote_id")
        if ($instanceID -ne "" -and $remoteID -ne "") {
          $deleteTaskKey = Get-InstanceRemoteKey $instanceID $remoteID
          try {
            $deleteTasksBefore[$deleteTaskKey] = @(Get-Tasks $headers $instanceID)
          } catch {
            $deleteTasksBefore[$deleteTaskKey] = @()
          }
        }
      }
      $withdrawals += [pscustomobject]@{
        Plan = $plan; PlanID = $planID; PoolID = $poolID; ChannelID = $channelID;
        Bindings = @($planWithdrawals)
      }
    } catch {
      $errors.Add("temporary_plan_capture:$planID")
    }
  }

  # Finish durable pool retirement. Completion means every plan was drained,
  # the pool was retired, and the job's final normal reconcile reliably queued
  # each required current-generation delayed delete.
  $retiredPools = @{}
  foreach ($entry in @(
    [pscustomobject]@{ Name = "platform"; Value = $State.PlatformPool },
    [pscustomobject]@{ Name = "owner"; Value = $State.OwnerPool }
  )) {
    if ($null -eq $entry.Value) { continue }
    $poolID = [string](Get-JsonValue $entry.Value "id")
    if ($poolID -eq "") { continue }
    try {
      Complete-PoolRetirement $headers $poolID | Out-Null
      $retiredPools[$poolID] = $true
    } catch {
      $errors.Add("$($entry.Name)_pool_retire")
    }
  }
  foreach ($pool in @($State.OwnerUpdatePools)) {
    if ($null -eq $pool) { continue }
    $poolID = [string](Get-JsonValue $pool "id")
    if ($poolID -eq "") { continue }
    try {
      Complete-PoolRetirement $headers $poolID | Out-Null
      $retiredPools[$poolID] = $true
    } catch {
      $errors.Add("owner_update_pool_retire:$poolID")
    }
  }

  # Disable this run's exact instance rules only after retirement suspended the
  # plans. In that state EnsurePoolRolloutOperations does not create another
  # drain operation, so this cannot overtake the job-owned final delete.
  foreach ($poolID in @($State.RolloutPoolIDs)) {
    foreach ($instanceID in @($State.InstanceIDs)) {
      try {
        Disable-InstancePoolRollout $headers $poolID $State.OwnerID @($instanceID)
      } catch {
        $errors.Add("rollout_disable:${poolID}:${instanceID}")
      }
    }
  }

  # Verify the job-owned cleanup receipt without mutating the plan. A new
  # correlated delete must exist even when an older generation has a pending or
  # failed delete for the same remote account.
  foreach ($withdrawal in $withdrawals) {
    $plan = $withdrawal.Plan
    $planID = [string]$withdrawal.PlanID
    $poolID = [string]$withdrawal.PoolID
    $channelID = [string]$withdrawal.ChannelID
    if (-not $retiredPools.ContainsKey($poolID)) { continue }
    try {
      $hasFinalDelete = $finalDeletePlans.ContainsKey($planID) -and [bool]$finalDeletePlans[$planID]
      $withdrawn = $true
      $afterBindings = Get-PlanBindings $headers $planID
      foreach ($binding in @($withdrawal.Bindings)) {
        $remoteID = [string](Get-JsonValue $binding "remote_id")
        $after = @($afterBindings | Where-Object {
          [string](Get-JsonValue $_ "channel_id") -eq $channelID -and
            [string](Get-JsonValue $_ "remote_id") -eq $remoteID
        })
        if ($after.Count -ne 1 -or [string](Get-JsonValue $after[0] "state") -ne "revoked") {
          $withdrawn = $false
          continue
        }
        $ownership = [string](Get-JsonValue $binding "account_ownership")
        if ($ownership -eq "") {
          if ($channelID -eq [string](Get-JsonValue $State.PlatformChannel "id")) { $ownership = "platform_managed" }
          else { $ownership = "owner_provided" }
        }
        if ($ownership -ne "platform_managed") { continue }
        $instanceID = [string](Get-JsonValue $binding "instance_id")
        if ($instanceID -eq "") { $instanceID = [string](Get-JsonValue $plan "instance_id") }
        if ($instanceID -eq "") {
          $withdrawn = $false
          continue
        }
        $tasks = Get-Tasks $headers $instanceID
        $deleteTaskKey = Get-InstanceRemoteKey $instanceID $remoteID
        $beforeTasks = @($deleteTasksBefore[$deleteTaskKey])
        if ($hasFinalDelete) {
          $expectedTaskID = [string]$finalDeleteTasks[$deleteTaskKey]
          $deletes = @(Select-NewCorrelatedTasks $tasks @() "gateway.account.delete" "" $remoteID | Where-Object {
            [string](Get-JsonValue $_ "id") -eq $expectedTaskID
          })
        } else {
          $baselineIDs = @($beforeTasks | ForEach-Object { [string](Get-JsonValue $_ "id") })
          $deletes = @(Select-NewCorrelatedTasks $tasks $baselineIDs "gateway.account.delete" "" $remoteID)
        }
        $acceptable = @($deletes | Where-Object { [string](Get-JsonValue $_ "status") -in @("pending", "leased", "succeeded") })
        if ($acceptable.Count -eq 0) {
          $withdrawn = $false
        }
      }
      if (-not $withdrawn) {
        $errors.Add("temporary_plan_deprovision:$planID")
      }
    } catch {
      $errors.Add("temporary_plan_deprovision:$planID")
    }
  }
  return @($errors)
}

function Invoke-LifecycleE2E {
  if ($TimeoutSeconds -lt 60) { throw "TimeoutSeconds must be at least 60" }
  if ($PollIntervalSeconds -lt 1 -or $PollIntervalSeconds -gt 30) { throw "PollIntervalSeconds must be between 1 and 30" }
  if ($WaitForDelete -and $DeleteWaitTimeoutSeconds -lt 1800) { throw "DeleteWaitTimeoutSeconds must be at least 1800 when -WaitForDelete is used" }

  $cleanupState = @{
    Headers = $null; OwnerID = 0; InstanceIDs = @();
    PlatformPool = $null; PlatformChannel = $null;
    OwnerPool = $null; OwnerChannel = $null; OwnerPlan = $null;
    OwnerUpdatePools = @(); OwnerUpdateChannels = @(); OwnerUpdatePlans = @();
    KnownPlans = @{}; RolloutPoolIDs = @(); FinalDeletePlans = @{}; FinalDeleteTasks = @{}
  }
  $primaryError = $null
  $cleanupErrors = @()
  $resultJSON = ""
  $key = ""
  try {
  Write-Step "Checking Core and all three real gateways"
  Wait-Http "$E2MBaseUrl/healthz" "E2M Core"
  Wait-Http "$Sub2APIBaseUrl/health" "Sub2API"
  Wait-Http "$NewAPIBaseUrl/api/status" "NewAPI"
  Wait-Http "$CPABaseUrl/healthz" "CPA"

  $adminToken = Login-E2M $E2MAdminEmail $E2MAdminPassword
  $headers = @{ Authorization = "Bearer $adminToken" }
  $cleanupState.Headers = $headers
  $users = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/users" $null $headers)
  $owner = @($users | Where-Object { [string](Get-JsonValue $_ "email") -eq $E2MOwnerEmail })
  if ($owner.Count -ne 1) { throw "Expected one owner '$E2MOwnerEmail'" }
  $ownerID = [long](Get-JsonValue $owner[0] "id")
  $cleanupState.OwnerID = $ownerID
  $instances = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/instances?user_id=$ownerID" $null $headers)
  $targets = @(
    Resolve-Instance $instances $Sub2APIInstanceID "sub2api" "sub2api"
    Resolve-Instance $instances $NewAPIInstanceID "newapi" "new"
    Resolve-Instance $instances $CPAInstanceID "cpa" "cpa"
  )
  $instanceIDs = @($targets | ForEach-Object { [string](Get-JsonValue $_ "id") })
  Assert-Unique $instanceIDs "Real gateway instance IDs are not unique"
  $cleanupState.InstanceIDs = $instanceIDs

  # Owner-provided update fixtures must predate every platform account created
  # by this run. This keeps the two ownership paths isolated and makes reuse of
  # a platform-managed remote account impossible by construction.
  $ownerRemoteByKind = @{
    sub2api = $Sub2APIOwnerRemoteID.Trim()
    newapi = $NewAPIOwnerRemoteID.Trim()
    cpa = $CPAOwnerRemoteID.Trim()
  }
  $ownerRemoteByInstance = @{}
  $ownerUpdateTargets = @()
  # The published-binding API is intentionally plan scoped. Enumerate only
  # this owner's plans, then inspect each plan separately; never perform a
  # cross-owner, unscoped binding read.
  $existingBindings = @(Get-OwnerBindingClaims $headers $ownerID)
  foreach ($instance in $targets) {
    $instanceID = [string](Get-JsonValue $instance "id")
    $kind = [string](Get-JsonValue $instance "kind")
    $remoteID = [string]$ownerRemoteByKind[$kind]
    if ($remoteID -eq "") { continue }
    $accounts = Get-Accounts $headers $instanceID
    $account = @($accounts | Where-Object { [string](Get-JsonValue $_ "id") -eq $remoteID })
    if ($account.Count -ne 1) {
      throw "Independent owner-provided account '$remoteID' does not exist on $kind before platform fixture creation"
    }
    $existingClaim = @($existingBindings | Where-Object {
      [string](Get-JsonValue $_ "instance_id") -eq $instanceID -and
        [string](Get-JsonValue $_ "remote_id") -eq $remoteID
    })
    if ($existingClaim.Count -gt 0) {
      throw "Owner-provided account '$remoteID' on $kind is already claimed by an E2M route plan"
    }
    $ownerRemoteByInstance[$instanceID] = $remoteID
    $ownerUpdateTargets += $instance
  }

  $baselineTasks = @{}
  foreach ($instanceID in $instanceIDs) {
    $baselineTasks[$instanceID] = @((Get-Tasks $headers $instanceID) | ForEach-Object { [string](Get-JsonValue $_ "id") })
  }

  $key = New-TestKey
  $sourceID = "lifecycle-source-$($script:RunID.Substring($script:RunID.Length - 12))"
  $bindingID = "lifecycle-binding-$($script:RunID.Substring($script:RunID.Length - 12))"
  $displayName = "Lifecycle temporary $($script:RunID)"
  $updatedName = "$displayName updated"

  Write-Step "Creating an isolated platform-managed temporary pool and channel"
  $pool = Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-pools" (New-PoolBody $script:PlatformPoolName $script:RunID "maintenance") $headers
  $cleanupState.PlatformPool = $pool
  $poolID = [string](Get-JsonValue $pool "id")
  $channelBody = New-ChannelBody $poolID $sourceID $displayName $bindingID $script:RunID "platform_managed"
  $channel = Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-channels" $channelBody $headers
  $cleanupState.PlatformChannel = $channel
  $channelID = [string](Get-JsonValue $channel "id")
  Set-DeliveryKey $headers $channelID $key | Out-Null
  [void](Set-InventoryReady $headers $channelID)
  $channel = Update-Channel $headers $channel "active" $displayName
  $cleanupState.PlatformChannel = $channel
  [void](Set-InstancePoolRollout $headers $poolID $ownerID $instanceIDs)
  $cleanupState.RolloutPoolIDs += $poolID
  $pool = Update-PoolStatus $headers $pool "active"
  $cleanupState.PlatformPool = $pool
  $key = ""

  Write-Step "Waiting for automatic create and deployment on all three gateways"
  $flows = Wait-Onboarding $headers $poolID $instanceIDs
  $planByInstance = @{}
  foreach ($flow in $flows) {
    $instanceID = [string](Get-JsonValue $flow "instance_id")
    $planID = [string](Get-JsonValue $flow "plan_id")
    $planByInstance[$instanceID] = $planID
    $cleanupState.KnownPlans[$instanceID] = [pscustomobject]@{ id = $planID; instance_id = $instanceID; pool_id = $poolID }
  }
  foreach ($instanceID in $instanceIDs) {
    Assert-NewSuccessfulTask $headers $instanceID $baselineTasks[$instanceID] "gateway.account.create" "managed create" $channelID | Out-Null
  }
  Assert-RemoteState $headers $targets $planByInstance $channelID $true

  Write-Step "Verifying managed update"
  $beforeUpdates = @{}
  $remoteByInstance = @{}
  foreach ($instanceID in $instanceIDs) {
    $beforeUpdates[$instanceID] = @((Get-Tasks $headers $instanceID) | ForEach-Object { [string](Get-JsonValue $_ "id") })
    $remoteByInstance[$instanceID] = Get-RemoteIDForChannel $headers $planByInstance[$instanceID] $channelID
  }
  $channel = Update-Channel $headers $channel "active" $updatedName
  $cleanupState.PlatformChannel = $channel
  foreach ($instanceID in $instanceIDs) {
    Reconcile $headers $planByInstance[$instanceID] | Out-Null
    Assert-NewSuccessfulTask $headers $instanceID $beforeUpdates[$instanceID] "gateway.account.update" "managed update" $channelID $remoteByInstance[$instanceID] | Out-Null
  }
  Assert-RemoteState $headers $targets $planByInstance $channelID $true

  $ownerUpdates = @()
  if ($ownerUpdateTargets.Count -gt 0) {
    Write-Step "Verifying credential-blind owner-provided updates on independent disposable accounts"
    foreach ($instance in $ownerUpdateTargets) {
      $instanceID = [string](Get-JsonValue $instance "id")
      if ([string]$ownerRemoteByInstance[$instanceID] -eq [string]$remoteByInstance[$instanceID]) {
        throw "Owner-provided update account must not reuse this run's platform-managed account"
      }
      $ownerUpdates += Assert-OwnerUpdateOnly $headers $instance $ownerRemoteByInstance[$instanceID] $script:RunID $cleanupState
    }

    Write-Step "Withdrawing owner-provided fixtures without deleting their remote accounts"
    for ($i = 0; $i -lt $cleanupState.OwnerUpdateChannels.Count; $i++) {
      $cleanupState.OwnerUpdateChannels[$i] = Update-Channel $headers $cleanupState.OwnerUpdateChannels[$i] "retired"
    }
    for ($i = 0; $i -lt $cleanupState.OwnerUpdatePlans.Count; $i++) {
      $ownerUpdate = $ownerUpdates[$i]
      $ownerPlanID = [string](Get-JsonValue $cleanupState.OwnerUpdatePlans[$i] "id")
      Reconcile $headers $ownerPlanID | Out-Null
      $ownerBindings = Get-PlanBindings $headers $ownerPlanID
      $ownerBinding = @($ownerBindings | Where-Object {
        [string](Get-JsonValue $_ "channel_id") -eq [string]$ownerUpdate.channel_id -and
          [string](Get-JsonValue $_ "remote_id") -eq [string]$ownerUpdate.remote_id
      })
      if ($ownerBinding.Count -ne 1 -or [string](Get-JsonValue $ownerBinding[0] "state") -ne "revoked") {
        throw "Owner-provided fixture was not withdrawn without deletion on $([string]$ownerUpdate.kind)"
      }
    }
    for ($i = 0; $i -lt $cleanupState.OwnerUpdatePools.Count; $i++) {
      Complete-PoolRetirement $headers ([string](Get-JsonValue $cleanupState.OwnerUpdatePools[$i] "id")) | Out-Null
    }
  } else {
    Write-Step "Skipping optional owner-provided updates; no independent remote account IDs were supplied"
  }

  Write-Step "Verifying immediate maintenance drain"
  $channel = Update-Channel $headers $channel "maintenance" $updatedName
  $cleanupState.PlatformChannel = $channel
  foreach ($instanceID in $instanceIDs) { Reconcile $headers $planByInstance[$instanceID] | Out-Null }
  Assert-RemoteState $headers $targets $planByInstance $channelID $false

  Write-Step "Verifying retirement-fenced 30 minute deferred delete"
  $beforeDeletes = @{}
  foreach ($instanceID in $instanceIDs) {
    $beforeDeletes[$instanceID] = @((Get-Tasks $headers $instanceID) | ForEach-Object { [string](Get-JsonValue $_ "id") })
    $remoteByInstance[$instanceID] = Get-RemoteIDForChannel $headers $planByInstance[$instanceID] $channelID
  }
  $channel = Update-Channel $headers $channel "retired" $updatedName
  $cleanupState.PlatformChannel = $channel

  # The durable job is the sole generation owner: it suspends each plan, retires
  # the pool, then runs final normal cleanup before it may report completed.
  Complete-PoolRetirement $headers $poolID | Out-Null
  Disable-InstancePoolRollout $headers $poolID $ownerID $instanceIDs
  $deleteObserved = @{}
  foreach ($instanceID in $instanceIDs) {
    $tasks = Get-Tasks $headers $instanceID
    $newDeletes = @(Select-NewCorrelatedTasks $tasks $beforeDeletes[$instanceID] "gateway.account.delete" "" $remoteByInstance[$instanceID])
    if ($newDeletes.Count -ne 1) { throw "Expected one deferred delete task for instance $instanceID" }
    $task = $newDeletes[0]
    $finalPlanID = [string]$planByInstance[$instanceID]
    $finalPlan = Get-RoutePlan $headers $ownerID $finalPlanID
    Assert-DeferredDeleteTask $task $finalPlanID ([long](Get-JsonValue $finalPlan "scheduling_generation"))
    $deleteTaskID = [string](Get-JsonValue $task "id")
    $deleteObserved[$instanceID] = $deleteTaskID
    $cleanupState.FinalDeletePlans[$finalPlanID] = $true
    $cleanupState.FinalDeleteTasks[(Get-InstanceRemoteKey $instanceID $remoteByInstance[$instanceID])] = $deleteTaskID
  }
  Assert-RemoteState $headers $targets $planByInstance $channelID $false

  # Keep the owner-provided fixture closed so the background onboarding runner
  # cannot race this explicit capability-rejection assertion. The reconcile
  # preflight still evaluates the latent create and must reject it.
  $ownerBaseline = @((Get-Tasks $headers $instanceIDs[0]) | ForEach-Object { [string](Get-JsonValue $_ "id") })
  $ownerPool = Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-pools" (New-PoolBody $script:OwnerPoolName ($script:RunID + "-owner") "maintenance") $headers
  $cleanupState.OwnerPool = $ownerPool
  $ownerResult = Assert-OwnerOnlyRejection $headers $targets[0] ([string](Get-JsonValue $ownerPool "id")) ($script:RunID + "-owner") $cleanupState
  $ownerTasks = Get-Tasks $headers $instanceIDs[0]
  $ownerCreates = @(Select-NewCorrelatedTasks $ownerTasks $ownerBaseline "gateway.account.create" ([string]$ownerResult.channel_id))
  if ($ownerCreates.Count -ne 0) { throw "owner-provided rejection queued a create task" }
  Complete-PoolRetirement $headers ([string](Get-JsonValue $ownerPool "id")) | Out-Null

  $deleteCompleted = $false
  if ($WaitForDelete) {
    $deadline = (Get-Date).AddSeconds($DeleteWaitTimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
      $allGone = $true
      foreach ($instance in $targets) {
        $instanceID = [string](Get-JsonValue $instance "id")
        $accounts = Get-Accounts $headers $instanceID
        $remoteID = [string]$remoteByInstance[$instanceID]
        if ($remoteID -eq "" -or @($accounts | Where-Object { [string](Get-JsonValue $_ "id") -eq $remoteID }).Count -gt 0) {
          $allGone = $false
        }
      }
      if ($allGone) { $deleteCompleted = $true; break }
      Start-Sleep -Seconds $PollIntervalSeconds
    }
    if (-not $deleteCompleted) { throw "Timed out waiting for deferred deletes" }
    foreach ($instanceID in $instanceIDs) {
      $tasks = Get-Tasks $headers $instanceID
      $deleteID = [string]$deleteObserved[$instanceID]
      $task = @($tasks | Where-Object { [string](Get-JsonValue $_ "id") -eq $deleteID })
      if ($task.Count -ne 1 -or [string](Get-JsonValue $task[0] "status") -ne "succeeded") {
        throw "Deferred delete task did not complete successfully on instance $instanceID"
      }
    }
  }
  $resultJSON = [ordered]@{
    run_id = $script:RunID; owner_user_id = $ownerID; platform_pool_id = $poolID; platform_channel_id = $channelID;
    instances = $targets | ForEach-Object { [ordered]@{ kind = [string](Get-JsonValue $_ "kind"); instance_id = [string](Get-JsonValue $_ "id"); plan_id = $planByInstance[[string](Get-JsonValue $_ "id")] } };
    checks = [ordered]@{
      platform_managed_create = $true; managed_update = $true; maintenance_immediate_disable = $true;
      deferred_delete_queued = $true; deferred_delete_completed = $deleteCompleted; deferred_delete_delay_minutes = 30;
      owner_provided_create_rejected = $true; owner_rejection_code = "unsupported_lifecycle";
      owner_provided_existing_updated = ($ownerUpdates.Count -gt 0); key_plaintext_exposed = $false;
      key_configured = $true
    }; owner_provided = $ownerResult; owner_updates = $ownerUpdates
  } | ConvertTo-Json -Depth 10
  } catch {
    $primaryError = $_
  } finally {
    $key = ""
    try {
      $cleanupErrors = @(Invoke-LifecycleCleanup $cleanupState)
    } catch {
      # The cleanup implementation itself must not replace the acceptance-test
      # failure. Surface only a stable, non-sensitive step code below.
      $cleanupErrors = @("cleanup_runtime")
    }
  }

  if ($null -ne $primaryError) {
    if ($cleanupErrors.Count -gt 0) {
      Write-Warning ("Lifecycle cleanup incomplete: " + ($cleanupErrors -join ", "))
    }
    throw $primaryError
  }
  if ($cleanupErrors.Count -gt 0) {
    throw "Lifecycle E2E passed but cleanup was incomplete: $($cleanupErrors -join ', ')"
  }
  return $resultJSON
}

if ($LibraryOnly) { return }
Invoke-LifecycleE2E
