[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$scriptPath = Join-Path $PSScriptRoot "seed-onboarding-real-gateways.ps1"
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
  if (-not $Condition) {
    throw $Message
  }
}

$definitions = @(Get-SeedSourceDefinitions)
Assert-SeedConfiguration $definitions
Assert-True ($definitions.Count -eq 3) "expected three source definitions"
Assert-True (Test-AllValuesUnique @($definitions | ForEach-Object { $_.SourceID })) "source IDs must be unique"
Assert-True (Test-AllValuesUnique @($definitions | ForEach-Object { $_.CredentialBindingID })) "binding IDs must be unique"

$merged = Merge-Labels @{ keep = "yes"; replace = "old" } @{ replace = "new"; added = "yes" }
Assert-True ($merged["keep"] -eq "yes") "Merge-Labels dropped an existing label"
Assert-True ($merged["replace"] -eq "new") "Merge-Labels did not replace a label"
Assert-True ($merged["added"] -eq "yes") "Merge-Labels did not add a label"

$deliveries = @(
  [pscustomobject]@{ channel_id = "channel-a" },
  [pscustomobject]@{ channel_id = "channel-c" }
)
$missing = @(Get-ChannelsMissingDelivery $deliveries @("channel-a", "channel-b", "channel-c"))
Assert-True ($missing.Count -eq 1 -and $missing[0] -eq "channel-b") "delivery idempotency helper returned the wrong missing channel"
$allMissing = @(Get-ChannelsMissingDelivery @() @("channel-a", "channel-b"))
Assert-True ($allMissing.Count -eq 2) "empty delivery catalog did not report every channel missing"

$workflowFixture = [pscustomobject]@{
  onboarding = @(
    [pscustomobject]@{ id = "workflow-a"; pool_id = "pool-seed"; instance_id = "instance-a" },
    [pscustomobject]@{ id = "workflow-b"; pool_id = "pool-seed"; instance_id = "instance-b" },
    [pscustomobject]@{ id = "workflow-c"; pool_id = "pool-seed"; instance_id = "instance-c" },
    [pscustomobject]@{ id = "workflow-other-pool"; pool_id = "pool-other"; instance_id = "instance-a" },
    [pscustomobject]@{ id = "workflow-other-instance"; pool_id = "pool-seed"; instance_id = "instance-other" }
  )
}
$matchingWorkflows = @(Get-MatchingOnboardingWorkflows $workflowFixture "pool-seed" @("instance-a", "instance-b", "instance-c"))
Assert-True ($matchingWorkflows.Count -eq 3) "workflow polling did not expand the operations-center onboarding array"
Assert-True ((@($matchingWorkflows | ForEach-Object { $_.id } | Sort-Object) -join ",") -eq "workflow-a,workflow-b,workflow-c") "workflow polling selected the wrong pool or instances"
$emptyWorkflows = @(Get-MatchingOnboardingWorkflows ([pscustomobject]@{ onboarding = @() }) "pool-seed" @("instance-a"))
Assert-True ($emptyWorkflows.Count -eq 0) "empty onboarding state did not remain empty"

$fallbackInstance = Resolve-Instance @(
  [pscustomobject]@{ id = "inst-current-sub2api"; kind = "sub2api"; name = "Local sub2api" }
) "inst-not-present" "sub2api" "sub2api"
Assert-True ($fallbackInstance.id -eq "inst-current-sub2api") "stale default instance ID did not fall back to the unique real gateway"

$script:rolloutCalls = New-Object System.Collections.Generic.List[string]
function Invoke-Json {
  param($Method, $Uri, $Body = $null, $Headers = @{}, [switch]$Sensitive)
  if ($Method -eq "PUT" -and $Uri -like "*/rollout-targets") {
    $script:rolloutCalls.Add("$($Body.scope):$($Body.user_id):$($Body.instance_id):$($Body.enabled):$($Body.rollout)")
    return [pscustomobject]@{ id = "target-$($Body.instance_id)" }
  }
  if ($Method -eq "GET" -and $Uri -like "*/rollout-targets") {
    return [pscustomobject]@{ instances = @(
      [pscustomobject]@{ instance_id = "instance-a"; enabled = $true; source = "instance" },
      [pscustomobject]@{ instance_id = "instance-b"; enabled = $true; source = "instance" },
      [pscustomobject]@{ instance_id = "instance-c"; enabled = $true; source = "instance" },
      [pscustomobject]@{ instance_id = "instance-other"; enabled = $false; source = "default" }
    ) }
  }
  throw "unexpected rollout fixture request: $Method $Uri"
}
$rolloutPreview = Ensure-InstancePoolRollout @{} "pool-seed" 101 @("instance-a", "instance-b", "instance-c")
Assert-True ($script:rolloutCalls.Count -eq 3) "seed did not create three precise rollout targets"
Assert-True (@($rolloutPreview.instances | Where-Object enabled).Count -eq 3) "rollout fixture enabled a non-target instance"

$generatedA = New-EphemeralDeliveryKey
$generatedB = New-EphemeralDeliveryKey
try {
  Assert-True ($generatedA.StartsWith("sk-e2m-e2e-")) "generated Key prefix is invalid"
  Assert-True ($generatedA -ne $generatedB) "generated Keys must be unique"
  Assert-True ((Get-SecretFingerprint $generatedA) -ne (Get-SecretFingerprint $generatedB)) "Key fingerprints must differ"
} finally {
  $generatedA = ""
  $generatedB = ""
}

$env:E2M_REAL_E2E_TEST_KEY = ""
$resolved = Resolve-DeliveryKey $null "E2M_REAL_E2E_TEST_KEY" "test"
try {
  Assert-True ($resolved.StartsWith("sk-e2m-e2e-")) "missing delivery input did not produce a private ephemeral Key"
} finally {
  $resolved = ""
}

$source = Get-Content -LiteralPath $scriptPath -Raw
foreach ($forbidden in @(
  'Write-Host\s+.*\$deliveryValues',
  'Write-Output\s+.*\$deliveryValues',
  'ConvertTo-Json\s+.*deliveryValues',
  'checks\s*=.*masked_value',
  'RotateDeliveryKeys',
  'return\s+,\(\[object\[\]\].*Get-SeedSourceDefinitions',
  'Invoke-Json\s+"PUT"\s+"\$E2MBaseUrl/api/v1/upstream-channels/\$ChannelID/delivery-key"(?![^\r\n]*-Sensitive)',
  'scope\s*=\s*"user"'
)) {
  Assert-True (-not [regex]::IsMatch($source, $forbidden, [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)) "seed script may expose a delivery value: $forbidden"
}

Assert-True ([regex]::IsMatch($source, 'inventory-state[^\r\n]*state\s*=\s*"ready"')) "seed does not explicitly admit draft inventory"
Assert-True ([regex]::IsMatch($source, 'Ensure-InstancePoolRollout[^\r\n]*\$adminHeaders[^\r\n]*\$poolID[^\r\n]*\$ownerID[^\r\n]*\$instanceIDs')) "seed does not install precise instance rollout targets"

Write-Host "seed-onboarding-real-gateways.ps1 static checks passed"
