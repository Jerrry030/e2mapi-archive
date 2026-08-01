[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$scriptPath = Join-Path $PSScriptRoot "lifecycle-real-gateways.ps1"
$tokens = $null
$parseErrors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) {
  $messages = @($parseErrors | ForEach-Object { $_.Message }) -join "; "
  throw "PowerShell parser errors: $messages"
}

. $scriptPath -LibraryOnly

function Assert-True {
  param(
    [Parameter(Mandatory = $true)][bool]$Condition,
    [Parameter(Mandatory = $true)][string]$Message
  )
  if (-not $Condition) { throw $Message }
}

$keyA = New-TestKey
$keyB = New-TestKey
try {
  Assert-True ($keyA.StartsWith("sk-e2m-e2e-")) "test key prefix is invalid"
  Assert-True ($keyA -ne $keyB) "test keys must be unique"
  Assert-True ((Get-SecretFingerprint $keyA) -ne (Get-SecretFingerprint $keyB)) "test key fingerprints must differ"
} finally {
  $keyA = ""
  $keyB = ""
}

$pool = New-PoolBody "temporary" "marker" "maintenance"
Assert-True ($pool.status -eq "maintenance") "temporary pool must start closed"
Assert-True ($pool.labels.lifecycle_run -eq "marker") "pool marker missing"
$channel = New-ChannelBody "pool-id" "source-id" "display" "binding-id" "marker" "platform_managed"
Assert-True ($channel.account_ownership -eq "platform_managed") "managed channel ownership missing"
$ownerChannel = New-ChannelBody "pool-id" "source-id" "display" "binding-id" "marker" "owner_provided"
Assert-True ($ownerChannel.account_ownership -eq "owner_provided") "owner channel ownership missing"

$scopeTargets = @("inst-sub2api", "inst-newapi", "inst-cpa")
$script:rolloutCalls = New-Object System.Collections.Generic.List[string]
function Invoke-Json {
  param($Method, $Uri, $Body = $null, $Headers = @{}, [switch]$Sensitive)
  if ($Method -eq "PUT" -and $Uri -like "*/rollout-targets") {
    $script:rolloutCalls.Add("$($Body.scope):$($Body.instance_id):$($Body.enabled)")
    return [pscustomobject]@{ id = "target-$($Body.instance_id)" }
  }
  if ($Method -eq "GET" -and $Uri -like "*/rollout-targets") {
    return [pscustomobject]@{ instances = @(
      [pscustomobject]@{ instance_id = "inst-sub2api"; enabled = $true; source = "instance" },
      [pscustomobject]@{ instance_id = "inst-newapi"; enabled = $true; source = "instance" },
      [pscustomobject]@{ instance_id = "inst-cpa"; enabled = $true; source = "instance" },
      [pscustomobject]@{ instance_id = "inst-other"; enabled = $false; source = "default" }
    ) }
  }
  throw "unexpected rollout fixture request"
}
[void](Set-InstancePoolRollout @{} "pool-temp" 101 $scopeTargets)
Assert-True ($script:rolloutCalls.Count -eq 3) "lifecycle fixture did not create three precise instance targets"

$correlationTasks = @(
  [pscustomobject]@{ id = "baseline-target"; type = "gateway.account.update"; status = "succeeded"; target_channel_id = "channel-target"; target_account_id = "account-target" },
  [pscustomobject]@{ id = "concurrent-decoy"; type = "gateway.account.update"; status = "succeeded"; target_channel_id = "channel-other"; target_account_id = "account-other" },
  [pscustomobject]@{ id = "current-target"; type = "gateway.account.update"; status = "succeeded"; target_channel_id = "channel-target"; target_account_id = "account-target" }
)
$correlated = @(Select-NewCorrelatedTasks $correlationTasks @("baseline-target") "gateway.account.update" "channel-target" "account-target")
Assert-True ($correlated.Count -eq 1 -and $correlated[0].id -eq "current-target") "task correlation accepted a baseline or concurrent decoy"
$decoyOnly = @(Select-NewCorrelatedTasks @($correlationTasks[1]) @() "gateway.account.update" "channel-target" "account-target")
Assert-True ($decoyOnly.Count -eq 0) "task correlation accepted a successful task for another lifecycle target"
$deleteTasks = @(
  [pscustomobject]@{ id = "delete-decoy"; type = "gateway.account.delete"; status = "pending"; target_account_id = "account-other" },
  [pscustomobject]@{ id = "delete-target"; type = "gateway.account.delete"; status = "pending"; target_account_id = "account-target" }
)
$correlatedDelete = @(Select-NewCorrelatedTasks $deleteTasks @() "gateway.account.delete" "" "account-target")
Assert-True ($correlatedDelete.Count -eq 1 -and $correlatedDelete[0].id -eq "delete-target") "delete correlation accepted another remote account"
$deleteCreatedAt = (Get-Date).ToUniversalTime()
$validDeleteSummary = [pscustomobject]@{
  status = "pending"; max_attempts = 12; created_at = $deleteCreatedAt.ToString("o");
  available_at = $deleteCreatedAt.AddMinutes(30).ToString("o");
  scheduling_fence = [pscustomobject]@{ scope = "auto-switch/plan/plan-delete"; version = 9; sequence = 3 }
}
Assert-DeferredDeleteTask $validDeleteSummary "plan-delete" 9
$badDeleteSummary = $validDeleteSummary.PSObject.Copy()
$badDeleteSummary.max_attempts = 3
$badDeleteRejected = $false
try { Assert-DeferredDeleteTask $badDeleteSummary "plan-delete" 9 } catch { $badDeleteRejected = $true }
Assert-True $badDeleteRejected "deferred delete validation accepted the wrong retry policy"
$badDeleteSummary = $validDeleteSummary.PSObject.Copy()
$badDeleteSummary.scheduling_fence = [pscustomobject]@{ scope = "auto-switch/plan/plan-delete"; version = 8; sequence = 3 }
$badDeleteRejected = $false
try { Assert-DeferredDeleteTask $badDeleteSummary "plan-delete" 9 } catch { $badDeleteRejected = $true }
Assert-True $badDeleteRejected "deferred delete validation accepted a stale generation fence"
$sharedRemoteBaselines = @{}
$sharedRemoteBaselines[(Get-InstanceRemoteKey "inst-a" "remote-shared")] = @("task-a")
$sharedRemoteBaselines[(Get-InstanceRemoteKey "inst-b" "remote-shared")] = @("task-b")
Assert-True ($sharedRemoteBaselines.Count -eq 2) "same remote id on two instances collapsed into one delete baseline"
Assert-True ($sharedRemoteBaselines[(Get-InstanceRemoteKey "inst-a" "remote-shared")][0] -eq "task-a") "instance A read instance B's delete baseline"
Assert-True ($sharedRemoteBaselines[(Get-InstanceRemoteKey "inst-b" "remote-shared")][0] -eq "task-b") "instance B read instance A's delete baseline"
$missingTargetRejected = $false
try {
  Select-NewCorrelatedTasks $correlationTasks @() "gateway.account.update" | Out-Null
} catch {
  $missingTargetRejected = $_.Exception.Message -like "*requires a target*"
}
Assert-True $missingTargetRejected "task correlation permits an unscoped type-only match"

$script:onboardingPolls = 0
function Invoke-Json {
  param($Method, $Uri, $Body = $null, $Headers = @{}, [switch]$Sensitive)
  if ($Method -ne "GET" -or $Uri -notlike "*/operations-center") {
    throw "unexpected onboarding fixture request"
  }
  $script:onboardingPolls++
  $lastCPAReadyGeneration = if ($script:onboardingPolls -eq 1) { 0 } else { 1 }
  return [pscustomobject]@{ onboarding = @(
    # A current-generation periodic check is still serving even though its
    # execution state is temporarily running/checking_gateway.
    [pscustomobject]@{ pool_id = "pool-ready"; instance_id = "inst-sub2api"; stage = "checking_gateway"; status = "running"; desired_generation = 1; last_ready_generation = 1; plan_id = "plan-sub2api" },
    [pscustomobject]@{ pool_id = "pool-ready"; instance_id = "inst-newapi"; stage = "active"; status = "active"; desired_generation = 1; last_ready_generation = 1; plan_id = "plan-newapi" },
    # The first response has never completed generation one and must not pass.
    [pscustomobject]@{ pool_id = "pool-ready"; instance_id = "inst-cpa"; stage = "checking_gateway"; status = "running"; desired_generation = 1; last_ready_generation = $lastCPAReadyGeneration; plan_id = "plan-cpa" }
  ) }
}
$originalTimeoutSeconds = $TimeoutSeconds
$originalPollIntervalSeconds = $PollIntervalSeconds
try {
  $TimeoutSeconds = 5
  $PollIntervalSeconds = 1
  $readyFlows = @(Wait-Onboarding @{} "pool-ready" $scopeTargets)
  Assert-True ($readyFlows.Count -eq 3) "generation-ready onboarding did not return all target workflows"
  Assert-True ($script:onboardingPolls -eq 2) "first activation was accepted before its desired generation completed"
} finally {
  $TimeoutSeconds = $originalTimeoutSeconds
  $PollIntervalSeconds = $originalPollIntervalSeconds
}

$script:claimURIs = New-Object System.Collections.Generic.List[string]
function Invoke-Json {
  param($Method, $Uri, $Body = $null, $Headers = @{}, [switch]$Sensitive)
  if ($Uri -like "*/route-plans?user_id=101") {
    return @([pscustomobject]@{ id = "plan/one" }, [pscustomobject]@{ id = "plan two" })
  }
  if ($Uri -like "*/published-bindings?plan_id=*") {
    $script:claimURIs.Add($Uri)
    return @([pscustomobject]@{ plan_id = ($Uri -split "=")[-1] })
  }
  throw "unexpected owner claim fixture request"
}
$claimFixture = @(Get-OwnerBindingClaims @{} 101)
Assert-True ($claimFixture.Count -eq 2) "owner claim preflight did not aggregate per-plan bindings"
Assert-True (@($script:claimURIs | Where-Object { $_ -like "*plan%2Fone" }).Count -eq 1) "slash plan id was not encoded"
Assert-True (@($script:claimURIs | Where-Object { $_ -like "*plan%20two" }).Count -eq 1) "space plan id was not encoded"

$source = Get-Content -LiteralPath $scriptPath -Raw
foreach ($forbidden in @(
  'Write-Host\s+.*\$key',
  'Write-Output\s+.*\$key',
  'ConvertTo-Json\s+.*\$key',
  'ConvertTo-Json\s+.*keyFingerprint',
  'Invoke-Json\s+"PUT"\s+"\$E2MBaseUrl/api/v1/upstream-channels/\$ChannelID/delivery-key"(?![^\r\n]*-Sensitive)',
  'down\s+-v',
  'docker\s+volume\s+(rm|prune)',
  'Remove-Item\s+.*(pool|channel|volume)',
  'seed-onboarding-real-gateways',
  'scope\s*=\s*"user"'
)) {
  Assert-True (-not [regex]::IsMatch($source, $forbidden, [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)) "lifecycle E2E violates safety rule: $forbidden"
}

Assert-True ([regex]::IsMatch($source, 'gateway\.account\.delete')) "lifecycle E2E does not inspect deferred delete tasks"
Assert-True ([regex]::IsMatch($source, 'gateway\.account\.create')) "lifecycle E2E does not inspect create receipts"
Assert-True ([regex]::IsMatch($source, 'gateway\.account\.update')) "lifecycle E2E does not inspect update receipts"
Assert-True ([regex]::IsMatch($source, 'unsupported_lifecycle')) "lifecycle E2E does not verify owner-only rejection"
Assert-True ([regex]::IsMatch($source, '\$availableAt\s*-\s*\$createdAt')) "lifecycle E2E does not enforce the persisted 30 minute delay"
Assert-True ([regex]::IsMatch($source, 'finally\s*\{[\s\S]*Invoke-LifecycleCleanup')) "lifecycle E2E has no finally cleanup"
Assert-True ([regex]::IsMatch($source, 'throw\s+\$primaryError')) "lifecycle E2E cleanup may replace the primary error"
Assert-True ([regex]::IsMatch($source, 'poolFlows[\s\S]*unexpected[\s\S]*outside the preflight target scope')) "onboarding polling ignores non-target workflows"
Assert-True ([regex]::IsMatch($source, 'desired_generation[\s\S]*last_ready_generation')) "onboarding polling does not use the stable generation receipt"
Assert-True ([regex]::IsMatch($source, '\$desired\s+-gt\s+0\s+-and\s+\$lastReady\s+-eq\s+\$desired')) "onboarding polling can accept an uncompleted first generation"
Assert-True ([regex]::IsMatch($source, 'Assert-NewSuccessfulTask[^\r\n]*gateway\.account\.create[^\r\n]*\$channelID')) "create receipt is not correlated to the lifecycle channel"
Assert-True ([regex]::IsMatch($source, 'Assert-NewSuccessfulTask[^\r\n]*gateway\.account\.update[^\r\n]*\$channelID[^\r\n]*\$remoteByInstance')) "update receipt is not correlated to the lifecycle channel and account"
Assert-True ([regex]::IsMatch($source, 'Select-NewCorrelatedTasks[^\r\n]*gateway\.account\.delete[^\r\n]*\$remoteByInstance')) "delete receipt is not correlated to the lifecycle account"
Assert-True ([regex]::IsMatch($source, '\$deleteTasksBefore\[\$deleteTaskKey\]')) "cleanup delete baselines are not scoped by the instance/remote composite key"
$waitForDeleteBlock = [regex]::Match($source, 'if \(\$WaitForDelete\)\s*\{[\s\S]*?Deferred delete task did not complete successfully[\s\S]*?\r?\n\s*\}').Value
Assert-True ($waitForDeleteBlock -ne "") "WaitForDelete verification block is missing"
Assert-True ([regex]::IsMatch($waitForDeleteBlock, '\$remoteByInstance\[\$instanceID\]')) "WaitForDelete does not use the remote id captured before retirement"
Assert-True (-not [regex]::IsMatch($waitForDeleteBlock, 'Get-PlanBindings')) "WaitForDelete still depends on a retired binding retaining remote_id"
Assert-True ([regex]::IsMatch($waitForDeleteBlock, 'status[^\r\n]*-ne\s+"succeeded"')) "WaitForDelete no longer verifies successful delete task completion"

$ownerUpdateFunction = [regex]::Match($source, 'function Assert-OwnerUpdateOnly\s*\{[\s\S]*?\r?\n\}').Value
Assert-True ($ownerUpdateFunction -ne "") "owner-provided update helper is missing"
Assert-True ([regex]::IsMatch($ownerUpdateFunction, '"maintenance"')) "owner-provided update pool does not start in maintenance"
Assert-True (-not [regex]::IsMatch($ownerUpdateFunction, 'Update-PoolStatus[^\r\n]*"active"')) "owner-provided update opens an all-users pool"
Assert-True (-not [regex]::IsMatch($ownerUpdateFunction, 'Wait-Onboarding')) "owner-provided update relies on global onboarding"
Assert-True ([regex]::IsMatch($ownerUpdateFunction, 'Reconcile\s+\$Headers[^\r\n]*\$plan')) "owner-provided update does not manually reconcile its scoped plan"

$ownerPreflightIndex = $source.IndexOf('$ownerRemoteByKind = @{')
$platformCreateIndex = $source.IndexOf('Write-Step "Creating an isolated platform-managed temporary pool and channel"')
$ownerWithdrawIndex = $source.IndexOf('Write-Step "Withdrawing owner-provided fixtures without deleting their remote accounts"')
$maintenanceDrainIndex = $source.IndexOf('Write-Step "Verifying immediate maintenance drain"')
Assert-True ($ownerPreflightIndex -ge 0 -and $ownerPreflightIndex -lt $platformCreateIndex) "independent owner account preflight must run before platform fixture creation"
Assert-True ([regex]::IsMatch($source, 'function Get-OwnerBindingClaims\s*\{[\s\S]*Get-PlanBindings')) "owner claim helper does not aggregate plan-scoped bindings"
Assert-True ([regex]::IsMatch($source, 'existingClaim[\s\S]*instance_id[\s\S]*remote_id')) "owner account preflight does not scope binding claims by instance and remote account"
Assert-True (-not [regex]::IsMatch($source, 'published-bindings"\s+\$null')) "lifecycle E2E performs an unscoped published-binding read"
Assert-True ([regex]::IsMatch($source, 'Get-OwnerBindingClaims\s+\$headers\s+\$ownerID')) "owner account preflight does not enumerate owner-scoped plans"
Assert-True ([regex]::IsMatch($source, 'EscapeDataString\(\$PlanID\)')) "published-binding plan_id is not URL encoded"
Assert-True ($ownerWithdrawIndex -ge 0 -and $maintenanceDrainIndex -gt $ownerWithdrawIndex) "owner fixture withdrawal must finish before the platform maintenance test"
Assert-True ([regex]::IsMatch($source, 'Assert-OwnerUpdateOnly[^\r\n]*\$ownerRemoteByInstance\[\$instanceID\]')) "owner update does not use an independently supplied remote account"
Assert-True (-not [regex]::IsMatch($source, 'Assert-OwnerUpdateOnly[^\r\n]*\$remoteByInstance\[\$instanceID\]')) "owner update reuses this run's platform-managed account"
Assert-True (-not [regex]::IsMatch($source, 'Restoring disposable accounts through their original platform plans')) "owner update still depends on restoring a reused platform account"
Assert-True (-not [regex]::IsMatch($source, 'Update-PoolStatus[^\r\n]*"retired"')) "pool retirement bypasses the durable job API"
Assert-True ([regex]::IsMatch($source, 'upstream-pools/\$PoolID/retirement-jobs')) "lifecycle E2E does not create durable retirement jobs"
Assert-True ([regex]::IsMatch($source, '"pending",\s*"partial",\s*"finalizing",\s*"cleanup"')) "pool retirement helper does not actively resume the durable cleanup phase"
$createIndex = $source.IndexOf('Write-Step "Creating an isolated platform-managed temporary pool and channel"')
$activateIndex = $source.IndexOf('$pool = Update-PoolStatus $headers $pool "active"')
$rolloutIndex = $source.IndexOf('[void](Set-InstancePoolRollout $headers $poolID $ownerID $instanceIDs)')
$inventoryIndex = $source.IndexOf('[void](Set-InventoryReady $headers $channelID)')
Assert-True ($createIndex -ge 0 -and $inventoryIndex -gt $createIndex -and $inventoryIndex -lt $activateIndex) "inventory is not admitted before pool activation"
Assert-True ($rolloutIndex -gt $createIndex -and $rolloutIndex -lt $activateIndex) "precise rollout is not installed before pool activation"
$deleteStepIndex = $source.IndexOf('Write-Step "Verifying retirement-fenced 30 minute deferred delete"')
$finalPlatformRetireIndex = $source.IndexOf('Complete-PoolRetirement $headers $poolID | Out-Null', $deleteStepIndex)
$finalRolloutDisableIndex = $source.IndexOf('Disable-InstancePoolRollout $headers $poolID $ownerID $instanceIDs', $deleteStepIndex)
$deleteReceiptIndex = $source.IndexOf('$tasks = Get-Tasks $headers $instanceID', $finalRolloutDisableIndex)
Assert-True ($deleteStepIndex -ge 0 -and $finalPlatformRetireIndex -gt $deleteStepIndex -and $finalRolloutDisableIndex -gt $finalPlatformRetireIndex -and $deleteReceiptIndex -gt $finalRolloutDisableIndex) "platform delete receipt must be checked only after durable retirement and rollout disable"
$afterFinalRetirement = $source.Substring($finalPlatformRetireIndex, $deleteReceiptIndex - $finalPlatformRetireIndex)
Assert-True (-not [regex]::IsMatch($afterFinalRetirement, 'Reconcile\s+\$headers\s+\$planByInstance')) "script advanced a plan generation after the retirement job owned final cleanup"

# Windows PowerShell 5.1 may consume the HTTP response stream before catch and
# preserve an expected API error body only in ErrorDetails.Message. The helper
# must recover the structured rejection without printing or requiring the
# response stream.
$script:errorDetailBody = '{"code":"unsupported_lifecycle","message":"fixture rejection"}'
function Invoke-WebRequest {
  $exception = New-Object System.Net.WebException("fixture 422")
  $record = New-Object System.Management.Automation.ErrorRecord(
    $exception,
    "fixture-422",
    [System.Management.Automation.ErrorCategory]::InvalidOperation,
    $null
  )
  $record.ErrorDetails = New-Object System.Management.Automation.ErrorDetails($script:errorDetailBody)
  $response = New-Object psobject -Property @{ StatusCode = 422 }
  Add-Member -InputObject $response -MemberType ScriptMethod -Name GetResponseStream -Value { throw "response stream was already consumed" }
  $exception | Add-Member -MemberType NoteProperty -Name Response -Value $response -Force
  throw $record
}
$capturedErrorOutput = @(& {
  $script:parsed422 = Invoke-JsonResponse "POST" "http://fixture.local/reject" @{} @{}
} 6>&1 | Out-String)
Assert-True ($script:parsed422.Status -eq 422) "expected error status was not preserved"
Assert-True ([string](Get-JsonValue $script:parsed422.Json "code") -eq "unsupported_lifecycle") "ErrorDetails JSON was not parsed"
Assert-True (-not $capturedErrorOutput.Contains($script:errorDetailBody)) "expected error response body was written to output"
Remove-Item Function:\Invoke-WebRequest

$script:cleanupCalls = New-Object System.Collections.Generic.List[string]
$script:cleanupBindingState = "active"
$script:cleanupTasks = @([pscustomobject]@{
  id = "delete-stale-generation"; type = "gateway.account.delete"; status = "pending";
  target_account_id = "remote-temp"; max_attempts = 12;
  created_at = (Get-Date).ToUniversalTime().ToString("o");
  available_at = (Get-Date).ToUniversalTime().AddMinutes(30).ToString("o");
  scheduling_fence = [pscustomobject]@{ scope = "auto-switch/plan/plan-temp"; version = 1; sequence = 2 }
})
function Update-PoolStatus {
  param($Headers, $Pool, $Status)
  $script:cleanupCalls.Add("pool:$([string](Get-JsonValue $Pool 'id')):$Status")
  return $Pool
}
function Update-Channel {
  param($Headers, $Channel, $Status, $DisplayName = "")
  $script:cleanupCalls.Add("channel:$([string](Get-JsonValue $Channel 'id')):$Status")
  return $Channel
}
function Invoke-Json {
  param($Method, $Uri, $Body = $null, $Headers = @{}, [switch]$Sensitive)
  if ($Uri -like "*/rollout-targets" -and $Method -eq "PUT") {
    $script:cleanupCalls.Add("rollout:$($Body.instance_id):$($Body.enabled)")
    return [pscustomobject]@{}
  }
  if ($Uri -like "*/pool-retirement-jobs?pool_id=*" -and $Method -eq "GET") {
    return @()
  }
  if ($Uri -like "*/upstream-pools/*/retirement-jobs" -and $Method -eq "POST") {
    $poolID = ($Uri -split "/")[-2]
    return [pscustomobject]@{ id = "retire-$poolID"; pool_id = $poolID; status = "finalizing" }
  }
  if ($Uri -like "*/pool-retirement-jobs/*/run" -and $Method -eq "POST") {
    $jobID = ($Uri -split "/")[-2]
    $poolID = $jobID.Substring("retire-".Length)
    $script:cleanupCalls.Add("pool:${poolID}:retired")
    if ($poolID -eq "pool-temp") {
      $script:cleanupBindingState = "revoked"
      $script:cleanupTasks += [pscustomobject]@{
        id = "delete-temp"; type = "gateway.account.delete"; status = "pending";
        target_account_id = "remote-temp"; max_attempts = 12;
        created_at = (Get-Date).ToUniversalTime().ToString("o");
        available_at = (Get-Date).ToUniversalTime().AddMinutes(30).ToString("o");
        scheduling_fence = [pscustomobject]@{ scope = "auto-switch/plan/plan-temp"; version = 2; sequence = 2 }
      }
    }
    return [pscustomobject]@{ id = $jobID; pool_id = $poolID; status = "completed" }
  }
  if ($Uri -like "*/route-plans?*") {
    return @(
      [pscustomobject]@{ id = "plan-temp"; pool_id = "pool-temp"; instance_id = "inst-temp" },
      [pscustomobject]@{ id = "plan-baseline"; pool_id = "pool-baseline"; instance_id = "inst-temp" }
    )
  }
  if ($Uri -like "*/connector-tasks?instance_id=inst-temp*") {
    return @($script:cleanupTasks)
  }
  throw "unexpected cleanup fixture request"
}
function Get-PlanBindings {
  param($Headers, $PlanID)
  if ($PlanID -eq "plan-baseline") { throw "cleanup read a baseline plan" }
  if ($PlanID -eq "plan-owner") { return @() }
  return @([pscustomobject]@{
    channel_id = "channel-temp"; instance_id = "inst-temp"; remote_id = "remote-temp";
    account_ownership = "platform_managed"; state = $script:cleanupBindingState
  })
}
function Reconcile {
  param($Headers, $PlanID)
  $script:cleanupCalls.Add("reconcile:$PlanID")
  return [pscustomobject]@{}
}

$cleanupState = @{
  Headers = @{ Authorization = "Bearer fixture" }; OwnerID = 101; InstanceIDs = @("inst-temp");
  PlatformPool = [pscustomobject]@{ id = "pool-temp"; name = "temp"; labels = [pscustomobject]@{ lifecycle_run = "fixture" } };
  PlatformChannel = [pscustomobject]@{ id = "channel-temp"; pool_id = "pool-temp"; display_name = "temp"; account_ownership = "platform_managed"; source_id = "source"; provider = "openai"; models = @(); groups = @(); credential_binding_id = "binding"; proxy_binding_id = ""; priority = 1; weight = 1; cost_hint = 0; labels = [pscustomobject]@{} };
  OwnerPool = [pscustomobject]@{ id = "pool-owner"; name = "owner"; labels = [pscustomobject]@{ lifecycle_run = "fixture-owner" } };
  OwnerChannel = [pscustomobject]@{ id = "channel-owner"; pool_id = "pool-owner"; display_name = "owner"; account_ownership = "owner_provided"; source_id = "owner-source"; provider = "openai"; models = @(); groups = @(); credential_binding_id = "owner-binding"; proxy_binding_id = ""; priority = 1; weight = 1; cost_hint = 0; labels = [pscustomobject]@{} };
  OwnerUpdatePools = @(); OwnerUpdateChannels = @(); OwnerUpdatePlans = @();
  KnownPlans = @{ "inst-temp" = [pscustomobject]@{ id = "plan-temp"; pool_id = "pool-temp"; instance_id = "inst-temp" } };
  RolloutPoolIDs = @("pool-temp");
  FinalDeletePlans = @{}; FinalDeleteTasks = @{};
  OwnerPlan = [pscustomobject]@{ id = "plan-owner"; pool_id = "pool-owner"; instance_id = "inst-temp" }
}
$cleanupErrors = @(Invoke-LifecycleCleanup $cleanupState)
Assert-True ($cleanupErrors.Count -eq 0) "cleanup fixture unexpectedly failed"
foreach ($expected in @("pool:pool-temp:retired", "pool:pool-owner:retired", "channel:channel-temp:retired", "channel:channel-owner:retired")) {
  Assert-True ($script:cleanupCalls.Contains($expected)) "cleanup did not perform $expected"
}
Assert-True ($script:cleanupCalls.Contains("rollout:inst-temp:False")) "cleanup did not disable this run's rollout target"
$channelRetireIndex = $script:cleanupCalls.IndexOf("channel:channel-temp:retired")
$poolRetireIndex = $script:cleanupCalls.IndexOf("pool:pool-temp:retired")
$rolloutDisableIndex = $script:cleanupCalls.IndexOf("rollout:inst-temp:False")
Assert-True ($channelRetireIndex -lt $poolRetireIndex) "cleanup must retire channels before pools"
Assert-True ($channelRetireIndex -lt $poolRetireIndex -and $poolRetireIndex -lt $rolloutDisableIndex) "cleanup must disable rollout only after the durable retirement job owns final cleanup"
Assert-True (-not $script:cleanupCalls.Contains("reconcile:plan-temp")) "cleanup advanced the plan after retirement completed"
Assert-True (@($script:cleanupTasks | Where-Object {
  $_.id -eq "delete-temp" -and $_.type -eq "gateway.account.delete" -and
    $_.target_account_id -eq "remote-temp" -and $_.status -eq "pending"
}).Count -eq 1) "cleanup did not observe the retirement job's replacement deferred delete after the stale pending generation"
Assert-True (@($script:cleanupTasks | Where-Object { $_.id -eq "delete-stale-generation" }).Count -eq 1) "cleanup fixture did not preserve the stale pending delete needed for the replacement test"
Assert-True (-not $script:cleanupCalls.Contains("reconcile:plan-baseline")) "cleanup touched a baseline plan"

# A fully verified final-generation delete is an explicit cleanup receipt. The
# finally path must re-check that exact task without claiming a newer plan
# generation and invalidating the task it is about to wait for.
$script:cleanupCalls.Clear()
$script:cleanupBindingState = "revoked"
$script:cleanupTasks = @([pscustomobject]@{
  id = "delete-final"; type = "gateway.account.delete"; status = "pending";
  target_account_id = "remote-temp"; max_attempts = 12;
  created_at = (Get-Date).ToUniversalTime().ToString("o");
  available_at = (Get-Date).ToUniversalTime().AddMinutes(30).ToString("o");
  scheduling_fence = [pscustomobject]@{ scope = "auto-switch/plan/plan-temp"; version = 2; sequence = 2 }
})
$cleanupState.FinalDeletePlans["plan-temp"] = $true
$cleanupState.FinalDeleteTasks[(Get-InstanceRemoteKey "inst-temp" "remote-temp")] = "delete-final"
$idempotentCleanupErrors = @(Invoke-LifecycleCleanup $cleanupState)
Assert-True ($idempotentCleanupErrors.Count -eq 0) "verified final delete cleanup fixture unexpectedly failed"
Assert-True (-not $script:cleanupCalls.Contains("reconcile:plan-temp")) "finally cleanup advanced the generation after a verified final delete"
$cleanupState.FinalDeletePlans.Clear()
$cleanupState.FinalDeleteTasks.Clear()

# A durable retirement cleanup failure must be surfaced by the retirement job;
# the script itself must never attempt a compensating reconcile afterward.
$script:cleanupCalls.Clear()
$script:cleanupBindingState = "active"
$script:cleanupTasks = @()
function Invoke-Json {
  param($Method, $Uri, $Body = $null, $Headers = @{}, [switch]$Sensitive)
  if ($Uri -like "*/rollout-targets" -and $Method -eq "PUT") { return [pscustomobject]@{} }
  if ($Uri -like "*/pool-retirement-jobs?pool_id=pool-temp*" -and $Method -eq "GET") { return @() }
  if ($Uri -like "*/upstream-pools/pool-temp/retirement-jobs" -and $Method -eq "POST") {
    return [pscustomobject]@{ id = "retire-pool-temp"; pool_id = "pool-temp"; status = "cleanup" }
  }
  if ($Uri -like "*/pool-retirement-jobs/retire-pool-temp/run" -and $Method -eq "POST") {
    throw "fixture durable cleanup failure"
  }
  if ($Uri -like "*/pool-retirement-jobs?pool_id=pool-owner*" -and $Method -eq "GET") {
    return @([pscustomobject]@{ id = "retire-pool-owner"; pool_id = "pool-owner"; status = "completed" })
  }
  if ($Uri -like "*/route-plans?*") { return @([pscustomobject]@{ id = "plan-temp"; pool_id = "pool-temp"; instance_id = "inst-temp" }) }
  if ($Uri -like "*/connector-tasks?instance_id=inst-temp*") { return @() }
  throw "unexpected failed-cleanup fixture request"
}
$cleanupFailureErrors = @(Invoke-LifecycleCleanup $cleanupState)
Assert-True ($cleanupFailureErrors -contains "platform_pool_retire") "durable retirement cleanup failure was not reported"
Assert-True (-not $script:cleanupCalls.Contains("reconcile:plan-temp")) "failed retirement cleanup triggered a second generation owner"

Write-Host "lifecycle-real-gateways.ps1 static checks passed"
