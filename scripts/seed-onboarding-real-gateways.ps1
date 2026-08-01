<#
.SYNOPSIS
Seeds and verifies automatic onboarding against the three local real gateways.

.DESCRIPTION
Creates one shared platform-managed pool with three distinct source Keys. When
no Key is supplied, a random local-test Key is generated in memory, stored in
the Core Vault, and never printed. A completed rerun reuses existing deliveries.
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
  [string]$E2MOwnerPassword = "admin123456",
  [string]$Sub2APIInstanceID = "inst-9cef27a502cd7c82", # gitleaks:allow -- public fixture ID
  [string]$NewAPIInstanceID = "inst-958940169e2b72ed", # gitleaks:allow -- public fixture ID
  [string]$CPAInstanceID = "inst-3eb2841327f0abcb",
  [securestring]$SourceAKey,
  [securestring]$SourceBKey,
  [securestring]$SourceCKey,
  [int]$TimeoutSeconds = 600,
  [int]$PollIntervalSeconds = 5,
  [switch]$LibraryOnly
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$script:SeedName = "real-onboarding-v1"
$script:PoolSeedKey = "$($script:SeedName)-shared-pool"

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
      if ($null -ne $Object[$key]) {
        $out[[string]$key] = [string]$Object[$key]
      }
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
    [hashtable]$Headers = @{},
    [switch]$Sensitive
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
    if ($Sensitive) {
      throw "$Method $Uri failed while storing a sensitive value; response detail withheld"
    }
    $detail = $_.Exception.Message
    if ($_.ErrorDetails -and $_.ErrorDetails.Message) {
      $detail = $_.ErrorDetails.Message
    }
    # Request bodies are deliberately excluded: delivery bodies may contain a Key.
    throw "$Method $Uri failed: $detail"
  }
}

function Wait-Http {
  param(
    [Parameter(Mandatory = $true)][string]$Uri,
    [Parameter(Mandatory = $true)][string]$Name
  )
  $deadline = (Get-Date).AddSeconds([Math]::Min($TimeoutSeconds, 120))
  $lastError = "not attempted"
  while ((Get-Date) -lt $deadline) {
    try {
      $response = Invoke-WebRequest -Uri $Uri -UseBasicParsing -TimeoutSec 5
      if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 300) {
        Write-Host ("    {0}: ready" -f $Name)
        return
      }
      $lastError = "HTTP $($response.StatusCode)"
    } catch {
      $lastError = $_.Exception.Message
    }
    Start-Sleep -Seconds 2
  }
  throw "Timed out waiting for $Name at $Uri. Last error: $lastError"
}

function Login-E2M {
  param(
    [Parameter(Mandatory = $true)][string]$Email,
    [Parameter(Mandatory = $true)][string]$Password
  )
  $login = Invoke-Json "POST" "$E2MBaseUrl/api/v1/auth/login" @{
    email = $Email
    password = $Password
  }
  $token = [string](Get-JsonValue $login "token")
  if ($token -eq "") {
    throw "E2M login did not return a token"
  }
  return $token
}

function Get-SeedSourceDefinitions {
  @(
    [pscustomobject]@{
      Slot = "A"
      SourceID = "real-e2e-source-a"
      DisplayName = "Real E2E Source A"
      CredentialBindingID = "real-e2e-source-a-key"
      SeedKey = "$($script:SeedName)-source-a"
      Priority = 10
      Weight = 100
      EnvironmentVariable = "E2M_REAL_E2E_SOURCE_A_KEY"
    },
    [pscustomobject]@{
      Slot = "B"
      SourceID = "real-e2e-source-b"
      DisplayName = "Real E2E Source B"
      CredentialBindingID = "real-e2e-source-b-key"
      SeedKey = "$($script:SeedName)-source-b"
      Priority = 20
      Weight = 90
      EnvironmentVariable = "E2M_REAL_E2E_SOURCE_B_KEY"
    },
    [pscustomobject]@{
      Slot = "C"
      SourceID = "real-e2e-source-c"
      DisplayName = "Real E2E Source C"
      CredentialBindingID = "real-e2e-source-c-key"
      SeedKey = "$($script:SeedName)-source-c"
      Priority = 30
      Weight = 80
      EnvironmentVariable = "E2M_REAL_E2E_SOURCE_C_KEY"
    }
  )
}

function Assert-SeedConfiguration {
  param([Parameter(Mandatory = $true)][array]$Definitions)
  if ($Definitions.Count -ne 3) {
    throw "The real onboarding seed must contain exactly three upstream sources"
  }
  foreach ($propertyName in @("SourceID", "DisplayName", "CredentialBindingID", "SeedKey", "EnvironmentVariable")) {
    $values = @($Definitions | ForEach-Object { [string](Get-JsonValue $_ $propertyName) })
    if (@($values | Where-Object { $_ -eq "" }).Count -gt 0) {
      throw "Seed source $propertyName values must not be empty"
    }
    if (@($values | Select-Object -Unique).Count -ne $Definitions.Count) {
      throw "Seed source $propertyName values must be unique"
    }
  }
}

function New-EphemeralDeliveryKey {
  $bytes = New-Object byte[] 32
  $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
  try {
    $rng.GetBytes($bytes)
  } finally {
    $rng.Dispose()
  }
  $encoded = [Convert]::ToBase64String($bytes).TrimEnd("=").Replace("+", "-").Replace("/", "_")
  [Array]::Clear($bytes, 0, $bytes.Length)
  return "sk-e2m-e2e-$encoded"
}

function ConvertFrom-PrivateSecureString {
  param([securestring]$Value)
  if ($null -eq $Value) {
    return ""
  }
  return ([System.Net.NetworkCredential]::new("", $Value)).Password
}

function Resolve-DeliveryKey {
  param(
    [securestring]$SecureValue,
    [Parameter(Mandatory = $true)][string]$EnvironmentVariable,
    [Parameter(Mandatory = $true)][string]$SourceSlot
  )
  $value = ConvertFrom-PrivateSecureString $SecureValue
  if ($value -eq "") {
    $value = [string][Environment]::GetEnvironmentVariable($EnvironmentVariable, "Process")
  }
  if ([string]::IsNullOrWhiteSpace($value)) {
    return New-EphemeralDeliveryKey
  }
  $value = $value.Trim()
  if ($value -eq "") {
    throw "Upstream source $SourceSlot Key must not be empty"
  }
  return $value
}

function Get-SecretFingerprint {
  param([Parameter(Mandatory = $true)][string]$Value)
  $sha = [System.Security.Cryptography.SHA256]::Create()
  try {
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($Value)
    try {
      return [Convert]::ToBase64String($sha.ComputeHash($bytes))
    } finally {
      [Array]::Clear($bytes, 0, $bytes.Length)
    }
  } finally {
    $sha.Dispose()
  }
}

function Resolve-Instance {
  param(
    [Parameter(Mandatory = $true)][array]$Instances,
    [string]$PreferredID,
    [Parameter(Mandatory = $true)][string]$Kind,
    [Parameter(Mandatory = $true)][string]$NameNeedle
  )
  foreach ($instance in $Instances) {
    if ([string](Get-JsonValue $instance "id") -eq $PreferredID -and [string](Get-JsonValue $instance "kind") -eq $Kind) {
      return $instance
    }
  }
  $matches = @($Instances | Where-Object {
    [string](Get-JsonValue $_ "kind") -eq $Kind -and
      ([string](Get-JsonValue $_ "name")).ToLowerInvariant().Contains($NameNeedle.ToLowerInvariant())
  })
  if ($matches.Count -eq 1) {
    return $matches[0]
  }
  $matches = @($Instances | Where-Object { [string](Get-JsonValue $_ "kind") -eq $Kind })
  if ($matches.Count -eq 1) {
    return $matches[0]
  }
  throw "Could not uniquely resolve the $Kind real gateway instance; run bootstrap-real-gateways.ps1 first or pass its instance ID"
}

function Ensure-UpstreamPool {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][AllowEmptyCollection()][array]$Pools
  )
  $existing = $null
  foreach ($pool in $Pools) {
    $labels = ConvertTo-Hashtable (Get-JsonValue $pool "labels")
    if ($labels["seed_key"] -eq $script:PoolSeedKey) {
      if ($null -ne $existing) {
        throw "More than one pool has seed_key '$($script:PoolSeedKey)'"
      }
      $existing = $pool
    }
  }
  $poolName = "Real E2E Shared Upstream Pool"
  if ($null -eq $existing) {
    foreach ($pool in $Pools) {
      if ([string](Get-JsonValue $pool "name") -eq $poolName) {
        throw "Pool name '$poolName' already exists without the expected seed_key; refusing to adopt it"
      }
    }
  }
  $existingLabels = $null
  if ($null -ne $existing) {
    $existingLabels = Get-JsonValue $existing "labels"
  }
  $body = @{
    name = $poolName
    provider = "openai"
    models = @("gpt-4o-mini")
    region = "local"
    # A new pool remains closed until all delivery Keys are safely in Vault.
    status = if ($null -ne $existing -and [string](Get-JsonValue $existing "status") -eq "active") { "active" } else { "maintenance" }
    description = "Shared platform-managed catalog used by the three real gateway onboarding paths."
    labels = Merge-Labels $existingLabels @{
      source = "seed-onboarding-real-gateways"
      seed_key = $script:PoolSeedKey
      local_real_gateway = "true"
      access = "explicit_instances"
    }
  }
  if ($null -ne $existing) {
    return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-pools/$([string](Get-JsonValue $existing "id"))" $body $Headers
  }
  return Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-pools" $body $Headers
}

function Enable-UpstreamPool {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)]$Pool
  )
  if ([string](Get-JsonValue $Pool "status") -eq "active") {
    return $Pool
  }
  $body = @{
    name = [string](Get-JsonValue $Pool "name")
    provider = [string](Get-JsonValue $Pool "provider")
    models = @(Get-JsonValue $Pool "models")
    region = [string](Get-JsonValue $Pool "region")
    status = "active"
    description = [string](Get-JsonValue $Pool "description")
    labels = ConvertTo-Hashtable (Get-JsonValue $Pool "labels")
  }
  return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-pools/$([string](Get-JsonValue $Pool "id"))" $body $Headers
}

function Ensure-UpstreamChannel {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][AllowEmptyCollection()][array]$Channels,
    [Parameter(Mandatory = $true)][string]$PoolID,
    [Parameter(Mandatory = $true)]$Definition
  )
  $existing = $null
  foreach ($channel in $Channels) {
    $labels = ConvertTo-Hashtable (Get-JsonValue $channel "labels")
    if ($labels["seed_key"] -eq [string]$Definition.SeedKey) {
      if ($null -ne $existing) {
        throw "More than one channel has seed_key '$($Definition.SeedKey)'"
      }
      $existing = $channel
    }
  }
  if ($null -eq $existing) {
    foreach ($channel in $Channels) {
      if ([string](Get-JsonValue $channel "pool_id") -eq $PoolID -and
          [string](Get-JsonValue $channel "source_id") -eq [string]$Definition.SourceID) {
        throw "Source '$($Definition.SourceID)' already exists in the pool without the expected seed_key; refusing to adopt it"
      }
    }
  }
  $existingLabels = $null
  if ($null -ne $existing) {
    if ([string](Get-JsonValue $existing "account_ownership") -ne "platform_managed") {
      throw "Seeded channel '$($Definition.SeedKey)' is not platform_managed and cannot receive a delivery Key"
    }
    if ([string](Get-JsonValue $existing "pool_id") -ne $PoolID) {
      throw "Seeded channel '$($Definition.SeedKey)' belongs to a different pool"
    }
    $existingLabels = Get-JsonValue $existing "labels"
  }
  $body = @{
    pool_id = $PoolID
    account_ownership = "platform_managed"
    source_id = [string]$Definition.SourceID
    display_name = [string]$Definition.DisplayName
    provider = "openai"
    models = @("gpt-4o-mini")
    groups = @()
    credential_binding_id = [string]$Definition.CredentialBindingID
    proxy_binding_id = ""
    priority = [int]$Definition.Priority
    weight = [int]$Definition.Weight
    cost_hint = 0
    status = "active"
    labels = Merge-Labels $existingLabels @{
      source = "seed-onboarding-real-gateways"
      seed_key = [string]$Definition.SeedKey
      local_real_gateway = "true"
      type = "apikey"
    }
  }
  if ($null -ne $existing) {
    $channel = Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-channels/$([string](Get-JsonValue $existing "id"))" $body $Headers
  } else {
    # The product API deliberately forces new inventory to maintenance/draft.
    # This fixture must explicitly admit it before opening the pool.
    $channel = Invoke-Json "POST" "$E2MBaseUrl/api/v1/upstream-channels" $body $Headers
  }
  $channelID = [string](Get-JsonValue $channel "id")
  if ([string](Get-JsonValue $channel "inventory_state") -ne "ready") {
    [void](Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-channels/$channelID/inventory-state" @{ state = "ready" } $Headers)
    # Refresh the full channel projection after the state-only endpoint.
    $channel = Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-channels/$channelID" $body $Headers
  } elseif ([string](Get-JsonValue $channel "status") -ne "active") {
    $channel = Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-channels/$channelID" $body $Headers
  }
  if ([string](Get-JsonValue $channel "status") -ne "active" -or
      [string](Get-JsonValue $channel "inventory_state") -ne "ready") {
    throw "Seeded channel '$channelID' was not admitted as active/ready inventory"
  }
  return $channel
}

function Ensure-InstancePoolRollout {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][string]$PoolID,
    [Parameter(Mandatory = $true)][long]$OwnerID,
    [Parameter(Mandatory = $true)][string[]]$InstanceIDs
  )
  foreach ($instanceID in $InstanceIDs) {
    [void](Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-pools/$PoolID/rollout-targets" @{
      scope = "instance"; user_id = $OwnerID; instance_id = $instanceID;
      enabled = $true; rollout = "immediate"; note = "real onboarding E2E fixture"
    } $Headers)
  }
  $preview = Invoke-Json "GET" "$E2MBaseUrl/api/v1/upstream-pools/$PoolID/rollout-targets" $null $Headers
  $resolutions = ConvertTo-Array (Get-JsonValue $preview "instances")
  foreach ($instanceID in $InstanceIDs) {
    $resolution = @($resolutions | Where-Object { [string](Get-JsonValue $_ "instance_id") -eq $instanceID })
    if ($resolution.Count -ne 1 -or -not [bool](Get-JsonValue $resolution[0] "enabled") -or
        [string](Get-JsonValue $resolution[0] "source") -ne "instance") {
      throw "Pool '$PoolID' does not have an explicit enabled instance target for '$instanceID'"
    }
  }
  $unexpected = @($resolutions | Where-Object {
    [bool](Get-JsonValue $_ "enabled") -and $InstanceIDs -notcontains [string](Get-JsonValue $_ "instance_id")
  })
  if ($unexpected.Count -gt 0) {
    throw "Seed pool rollout is not isolated to the three real gateway instances"
  }
  return $preview
}

function Set-DeliveryKey {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][string]$ChannelID,
    [Parameter(Mandatory = $true)][string]$Value
  )
  # Invoke-Json never includes Body in diagnostics. The response is masked.
  return Invoke-Json "PUT" "$E2MBaseUrl/api/v1/upstream-channels/$ChannelID/delivery-key" @{ value = $Value } $Headers -Sensitive
}

function Get-ChannelsMissingDelivery {
  param(
    [Parameter(Mandatory = $true)][AllowEmptyCollection()][array]$Deliveries,
    [Parameter(Mandatory = $true)][string[]]$ChannelIDs
  )
  $delivered = @{}
  foreach ($delivery in $Deliveries) {
    $channelID = [string](Get-JsonValue $delivery "channel_id")
    if ($channelID -ne "") {
      $delivered[$channelID] = $true
    }
  }
  return @($ChannelIDs | Where-Object { -not $delivered.ContainsKey($_) })
}

function Get-MatchingOnboardingWorkflows {
  param(
    [Parameter(Mandatory = $true)]$Operations,
    [Parameter(Mandatory = $true)][string]$PoolID,
    [Parameter(Mandatory = $true)][string[]]$InstanceIDs
  )
  # ConvertTo-Array deliberately emits one array object so an empty API result
  # does not collapse to $null. Capture it before piping so PowerShell expands
  # the array into individual workflow objects for filtering.
  $allWorkflows = ConvertTo-Array (Get-JsonValue $Operations "onboarding")
  return @($allWorkflows | Where-Object {
    [string](Get-JsonValue $_ "pool_id") -eq $PoolID -and
      $InstanceIDs -contains [string](Get-JsonValue $_ "instance_id")
  })
}

function Test-AllValuesUnique {
  param([Parameter(Mandatory = $true)][array]$Values)
  return @($Values | Select-Object -Unique).Count -eq $Values.Count
}

function Wait-OnboardingActive {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][string]$PoolID,
    [Parameter(Mandatory = $true)][string[]]$InstanceIDs
  )
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $lastSummary = ""
  while ((Get-Date) -lt $deadline) {
    $operations = Invoke-Json "GET" "$E2MBaseUrl/api/v1/operations-center" $null $Headers
    $workflows = @(Get-MatchingOnboardingWorkflows $operations $PoolID $InstanceIDs)
    $parts = @()
    foreach ($instanceID in $InstanceIDs) {
      $workflow = @($workflows | Where-Object { [string](Get-JsonValue $_ "instance_id") -eq $instanceID })
      if ($workflow.Count -eq 0) {
        $parts += "$instanceID=undiscovered"
      } elseif ($workflow.Count -gt 1) {
        throw "More than one onboarding workflow exists for pool '$PoolID' and instance '$instanceID'"
      } else {
        $parts += ("{0}={1}/{2}" -f $instanceID, [string](Get-JsonValue $workflow[0] "stage"), [string](Get-JsonValue $workflow[0] "status"))
      }
    }
    $summary = $parts -join ", "
    if ($summary -ne $lastSummary) {
      Write-Host "    $summary"
      $lastSummary = $summary
    }
    $active = @($workflows | Where-Object {
      [string](Get-JsonValue $_ "stage") -eq "active" -and
        [string](Get-JsonValue $_ "status") -eq "active" -and
        [int](Get-JsonValue $_ "delivered_keys") -eq 3
    })
    if ($workflows.Count -eq $InstanceIDs.Count -and $active.Count -eq $InstanceIDs.Count) {
      return $workflows
    }
    Start-Sleep -Seconds $PollIntervalSeconds
  }
  throw "Timed out waiting for automatic onboarding. Last state: $lastSummary"
}

function Assert-RemoteBindings {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][array]$Instances,
    [Parameter(Mandatory = $true)][array]$Workflows,
    [Parameter(Mandatory = $true)][string[]]$ChannelIDs,
    [Parameter(Mandatory = $true)][long]$OwnerID
  )
  $sets = @()
  $verifiedBindingCount = 0
  foreach ($instance in $Instances) {
    $instanceID = [string](Get-JsonValue $instance "id")
    $workflow = @($Workflows | Where-Object { [string](Get-JsonValue $_ "instance_id") -eq $instanceID })
    if ($workflow.Count -ne 1) {
      throw "Expected one active workflow for instance '$instanceID'"
    }
    $planID = [string](Get-JsonValue $workflow[0] "plan_id")
    if ($planID -eq "") {
      throw "Active workflow for '$instanceID' has no route plan"
    }
    $plans = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/route-plans?user_id=$OwnerID" $null $Headers)
    $plan = @($plans | Where-Object { [string](Get-JsonValue $_ "id") -eq $planID })
    if ($plan.Count -ne 1 -or [string](Get-JsonValue $plan[0] "status") -ne "published") {
      throw "Onboarding plan '$planID' is not published"
    }
    $encodedPlanID = [System.Uri]::EscapeDataString($planID)
    $bindings = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/published-bindings?plan_id=$encodedPlanID" $null $Headers)
    $selected = @($bindings | Where-Object { $ChannelIDs -contains [string](Get-JsonValue $_ "channel_id") })
    if ($selected.Count -ne $ChannelIDs.Count) {
      throw "Plan '$planID' does not contain all three seeded source bindings"
    }
    $accounts = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/instances/$instanceID/accounts" $null $Headers)
    $boundIDs = @()
    foreach ($binding in $selected) {
      $channelID = [string](Get-JsonValue $binding "channel_id")
      $remoteID = [string](Get-JsonValue $binding "remote_id")
      if ([string](Get-JsonValue $binding "state") -ne "active" -or $remoteID -eq "") {
        throw "Binding for channel '$channelID' on '$instanceID' is not active"
      }
      $remote = @($accounts | Where-Object { [string](Get-JsonValue $_ "id") -eq $remoteID })
      if ($remote.Count -ne 1 -or -not [bool](Get-JsonValue $remote[0] "schedulable")) {
        throw "Gateway '$instanceID' did not acknowledge schedulable remote account '$remoteID'"
      }
      $boundIDs += $channelID
      $verifiedBindingCount++
    }
    $sets += ,(@($boundIDs | Sort-Object))
    Write-Host ("    {0}: plan {1}, 3 active remote bindings" -f [string](Get-JsonValue $instance "kind"), $planID)
  }
  $expected = (@($ChannelIDs | Sort-Object) -join ",")
  foreach ($set in $sets) {
    if (($set -join ",") -ne $expected) {
      throw "The owner did not reuse the same three source Keys across all gateway instances"
    }
  }
  $expectedCount = $Instances.Count * $ChannelIDs.Count
  if ($verifiedBindingCount -ne $expectedCount) {
    throw "Expected $expectedCount verified remote bindings, got $verifiedBindingCount"
  }
  return $verifiedBindingCount
}

function Assert-NoForeignReuse {
  param(
    [Parameter(Mandatory = $true)][hashtable]$Headers,
    [Parameter(Mandatory = $true)][long]$OwnerID,
    [Parameter(Mandatory = $true)][string[]]$ChannelIDs
  )
  $plans = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/route-plans" $null $Headers)
  foreach ($plan in $plans) {
    if ([long](Get-JsonValue $plan "user_id") -eq $OwnerID) {
      continue
    }
    $planID = [string](Get-JsonValue $plan "id")
    $encodedPlanID = [System.Uri]::EscapeDataString($planID)
    $bindings = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/published-bindings?plan_id=$encodedPlanID" $null $Headers)
    foreach ($binding in $bindings) {
      if ($ChannelIDs -contains [string](Get-JsonValue $binding "channel_id")) {
        throw "A seeded permanent Key is also bound to a different owner plan '$planID'"
      }
    }
  }
}

function Assert-AssignedKeys {
  param(
    [Parameter(Mandatory = $true)][hashtable]$OwnerHeaders,
    [Parameter(Mandatory = $true)][string[]]$DisplayNames
  )
  $assigned = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/owner/assigned-keys" $null $OwnerHeaders)
  $seeded = @($assigned | Where-Object { $DisplayNames -contains [string](Get-JsonValue $_ "display_name") })
  if ($seeded.Count -ne 3) {
    throw "The owner-safe assigned Key view does not contain exactly three seeded Keys"
  }
  $deliveryIDs = @()
  foreach ($key in $seeded) {
    $deliveryIDs += [string](Get-JsonValue $key "id")
    $masked = [string](Get-JsonValue $key "masked_value")
    if (-not $masked.StartsWith("********") -or [int64](Get-JsonValue $key "key_version") -le 0) {
      throw "An assigned Key is not masked or versioned"
    }
    if ([string](Get-JsonValue $key "proof_status") -ne "verified") {
      throw "An assigned Key has no verified Connector proof"
    }
  }
  if (-not (Test-AllValuesUnique $deliveryIDs)) {
    throw "Assigned delivery IDs are not unique"
  }
}

function Invoke-RealOnboardingSeed {
  $definitions = @(Get-SeedSourceDefinitions)
  Assert-SeedConfiguration $definitions
  if ($TimeoutSeconds -lt 30) {
    throw "TimeoutSeconds must be at least 30"
  }
  if ($PollIntervalSeconds -lt 1 -or $PollIntervalSeconds -gt 30) {
    throw "PollIntervalSeconds must be between 1 and 30"
  }

  Write-Step "Checking Core and all three real gateways"
  Wait-Http "$E2MBaseUrl/healthz" "E2M Core"
  Wait-Http "$Sub2APIBaseUrl/health" "Sub2API"
  Wait-Http "$NewAPIBaseUrl/api/status" "NewAPI"
  Wait-Http "$CPABaseUrl/healthz" "CPA"

  Write-Step "Resolving the shared owner and real gateway instances"
  $adminToken = Login-E2M $E2MAdminEmail $E2MAdminPassword
  $adminHeaders = @{ Authorization = "Bearer $adminToken" }
  $users = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/users" $null $adminHeaders)
  $owners = @($users | Where-Object { [string](Get-JsonValue $_ "email") -eq $E2MOwnerEmail })
  if ($owners.Count -ne 1) {
    throw "Expected one owner '$E2MOwnerEmail'; run bootstrap-real-gateways.ps1 first"
  }
  $ownerID = [long](Get-JsonValue $owners[0] "id")
  $instances = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/instances?user_id=$ownerID" $null $adminHeaders)
  $targetInstances = @(
    Resolve-Instance $instances $Sub2APIInstanceID "sub2api" "sub2api"
    Resolve-Instance $instances $NewAPIInstanceID "newapi" "new"
    Resolve-Instance $instances $CPAInstanceID "cpa" "cpa"
  )
  $instanceIDs = @($targetInstances | ForEach-Object { [string](Get-JsonValue $_ "id") })
  if (-not (Test-AllValuesUnique $instanceIDs)) {
    throw "Resolved real gateway instance IDs are not unique"
  }
  foreach ($instance in $targetInstances) {
    if ([long](Get-JsonValue $instance "user_id") -ne $ownerID) {
      throw "Every real gateway must belong to the same owner"
    }
    $instanceID = [string](Get-JsonValue $instance "id")
    [void](Invoke-Json "GET" "$E2MBaseUrl/api/v1/instances/$instanceID/accounts" $null $adminHeaders)
  }
  Write-Host ("    owner {0}, instances: {1}" -f $ownerID, ($instanceIDs -join ", "))

  Write-Step "Ensuring one all-user shared pool and three source Keys"
  $pools = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/upstream-pools" $null $adminHeaders)
  $pool = Ensure-UpstreamPool $adminHeaders $pools
  $poolID = [string](Get-JsonValue $pool "id")
  $catalog = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/upstream-channels" $null $adminHeaders)
  $channels = @()
  foreach ($definition in $definitions) {
    $channels += Ensure-UpstreamChannel $adminHeaders $catalog $poolID $definition
  }
  $channelIDs = @($channels | ForEach-Object { [string](Get-JsonValue $_ "id") })
  if (-not (Test-AllValuesUnique $channelIDs)) {
    throw "Seeded channel IDs are not unique"
  }

  $deliveries = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/upstream-key-deliveries" $null $adminHeaders)
  $missingDelivery = @(Get-ChannelsMissingDelivery $deliveries $channelIDs)
  $seededDeliveries = @($deliveries | Where-Object { $channelIDs -contains [string](Get-JsonValue $_ "channel_id") })
  if ($missingDelivery.Count -gt 0) {
    if ([string](Get-JsonValue $pool "status") -ne "maintenance") {
      throw "The active seed pool has an incomplete delivery set; refusing to rotate live Keys"
    }
    # The pool is still closed. Replace the full set so a prior partial Vault
    # write can be recovered without guessing which plaintext was persisted.
    $secureInputs = @($SourceAKey, $SourceBKey, $SourceCKey)
    $deliveryValues = @()
    $fingerprints = @()
    try {
      for ($index = 0; $index -lt $definitions.Count; $index++) {
        $value = Resolve-DeliveryKey $secureInputs[$index] ([string]$definitions[$index].EnvironmentVariable) ([string]$definitions[$index].Slot)
        $deliveryValues += $value
        $fingerprints += Get-SecretFingerprint $value
      }
      if (-not (Test-AllValuesUnique $fingerprints)) {
        throw "The three upstream source Keys must be different"
      }
      $seededDeliveries = @()
      for ($index = 0; $index -lt $channels.Count; $index++) {
        $seededDeliveries += Set-DeliveryKey $adminHeaders $channelIDs[$index] $deliveryValues[$index]
      }
    } finally {
      $deliveryValues = @()
      $fingerprints = @()
      $secureInputs = @()
      $SourceAKey = $null
      $SourceBKey = $null
      $SourceCKey = $null
    }
  }
  if ($seededDeliveries.Count -ne 3) {
    throw "Expected exactly three delivery records after seeding"
  }
  [void](Ensure-InstancePoolRollout $adminHeaders $poolID $ownerID $instanceIDs)
  $pool = Enable-UpstreamPool $adminHeaders $pool
  Write-Host ("    pool {0}, channels: {1}; delivery values withheld" -f $poolID, ($channelIDs -join ", "))

  Write-Step "Waiting for durable automatic onboarding"
  $workflows = @(Wait-OnboardingActive $adminHeaders $poolID $instanceIDs)

  Write-Step "Verifying plans, gateway receipts, owner view, and permanent isolation"
  $activeBindingCount = Assert-RemoteBindings $adminHeaders $targetInstances $workflows $channelIDs $ownerID
  if ($activeBindingCount -ne 9) {
    throw "The three real gateways did not produce exactly nine active bindings"
  }
  Assert-NoForeignReuse $adminHeaders $ownerID $channelIDs
  $ownerToken = Login-E2M $E2MOwnerEmail $E2MOwnerPassword
  $ownerHeaders = @{ Authorization = "Bearer $ownerToken" }
  Assert-AssignedKeys $ownerHeaders @($definitions | ForEach-Object { [string]$_.DisplayName })

  $latestDeliveries = ConvertTo-Array (Invoke-Json "GET" "$E2MBaseUrl/api/v1/upstream-key-deliveries" $null $adminHeaders)
  foreach ($channelID in $channelIDs) {
    $delivery = @($latestDeliveries | Where-Object { [string](Get-JsonValue $_ "channel_id") -eq $channelID })
    if ($delivery.Count -ne 1 -or [string](Get-JsonValue $delivery[0] "proof_status") -ne "verified") {
      throw "Channel '$channelID' has no verified delivery record"
    }
  }

  Write-Step "Real onboarding E2E passed"
  [ordered]@{
    owner_user_id = $ownerID
    pool_id = $poolID
    channel_ids = $channelIDs
    instances = @($targetInstances | ForEach-Object {
      $instanceID = [string](Get-JsonValue $_ "id")
      $workflow = @($workflows | Where-Object { [string](Get-JsonValue $_ "instance_id") -eq $instanceID })[0]
      [ordered]@{
        kind = [string](Get-JsonValue $_ "kind")
        instance_id = $instanceID
        plan_id = [string](Get-JsonValue $workflow "plan_id")
        onboarding = "active"
      }
    })
    checks = [ordered]@{
      distinct_sources = 3
      delivery_plaintext_exposed = $false
      same_owner_key_reused_across_instances = $true
      foreign_owner_reuse = $false
      active_gateway_bindings = $activeBindingCount
    }
  } | ConvertTo-Json -Depth 8
}

if ($LibraryOnly) {
  return
}

Invoke-RealOnboardingSeed
