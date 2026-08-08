param(
  [string]$ComposeFile = "",
  # An explicit project name keeps this stack namespaced. Without it, compose
  # derives the project from the shared templates directory, and a production
  # stack started from a sibling file in that directory would be adopted into
  # the same project — where --remove-orphans deletes its containers.
  [string]$ComposeProject = "e2m-real-gateways",
  [string]$E2MBaseUrl = "http://127.0.0.1:18080",
  [string]$PlatformUpstreamBaseUrl = "http://mock-openai:8093/v1",
  [string]$FailingUpstreamBaseUrl = "http://mock-openai-fail:8093/v1",
  [string]$AdminEmail = "admin@local.dev",
  [string]$AdminPassword = "admin123456",
  [int]$TimeoutSeconds = 180,
  [switch]$SkipComposeUp
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($ComposeFile)) {
  $ComposeFile = Join-Path $RepoRoot "deployments/templates/compose/e2m-core-real-gateways.compose.yml"
}
$RuntimeDir = Join-Path $RepoRoot "deployments/runtime/platform-forwarding"
$KeyFile = Join-Path $RuntimeDir "downstream.key"

function Write-Step([string]$Message) {
  Write-Host "`n==> $Message"
}

function Invoke-Compose([string[]]$Arguments) {
  & docker compose -p $ComposeProject -f $ComposeFile @Arguments
  if ($LASTEXITCODE -ne 0) {
    throw "docker compose failed with exit code $LASTEXITCODE"
  }
}

function ConvertTo-JsonBody($Body) {
  return $Body | ConvertTo-Json -Depth 20 -Compress
}

function Invoke-Json(
  [string]$Method,
  [string]$Uri,
  $Body = $null,
  [hashtable]$Headers = @{}
) {
  $params = @{
    Method = $Method
    Uri = $Uri
    Headers = $Headers
    TimeoutSec = 60
  }
  if ($null -ne $Body) {
    $params.Body = ConvertTo-JsonBody $Body
    $params.ContentType = "application/json; charset=utf-8"
  }
  return Invoke-RestMethod @params
}

function Get-Value($Object, [string]$Name) {
  if ($null -eq $Object) { return $null }
  if ($Object -is [System.Collections.IDictionary] -and $Object.Contains($Name)) {
    return $Object[$Name]
  }
  $property = $Object.PSObject.Properties[$Name]
  if ($null -ne $property) { return $property.Value }
  return $null
}

function Get-Data($Response, [string]$Operation) {
  if ($null -eq $Response) { throw "$Operation returned an empty response" }
  $code = Get-Value $Response "code"
  if ($null -ne $code -and [int]$code -ne 0) {
    throw "$Operation failed: $(Get-Value $Response 'message')"
  }
  $data = Get-Value $Response "data"
  if ($null -ne $data) { return $data }
  return $Response
}

function Wait-Http([string]$Uri, [string]$Name) {
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $lastError = "not ready"
  while ((Get-Date) -lt $deadline) {
    try {
      $response = Invoke-WebRequest -UseBasicParsing -Uri $Uri -TimeoutSec 5
      if ([int]$response.StatusCode -ge 200 -and [int]$response.StatusCode -lt 500) { return }
    } catch {
      $lastError = $_.Exception.Message
    }
    Start-Sleep -Seconds 2
  }
  throw "Timed out waiting for $Name at $Uri. Last error: $lastError"
}

function Login-E2M {
  $response = Invoke-Json "POST" "$E2MBaseUrl/api/v1/auth/login" @{
    email = $AdminEmail
    password = $AdminPassword
  }
  $data = Get-Data $response "E2M login"
  $token = [string](Get-Value $data "token")
  if ([string]::IsNullOrWhiteSpace($token)) {
    $token = [string](Get-Value $data "access_token")
  }
  if ([string]::IsNullOrWhiteSpace($token)) { throw "E2M login returned no token" }
  $user = Get-Value $data "user"
  $userID = [long](Get-Value $user "id")
  if ($userID -le 0) { throw "E2M login returned no valid user id" }
  return [pscustomobject]@{
    Headers = @{ Authorization = "Bearer $token" }
    UserID = $userID
  }
}

function Create-PlatformGroup([hashtable]$Headers) {
  $groupPayload = @{
    name = "e2m-local-platform"
    description = "Local OpenAI-compatible acceptance group"
    models = @("gpt-4o-mini", "gpt-e2m-failover")
    status = "active"
  }
  $response = Invoke-Json "POST" "$E2MBaseUrl/api/v1/platform/groups" $groupPayload `
    ($Headers + @{ "Idempotency-Key" = "e2m-local-platform-group-v1" })
  $group = Get-Data $response "Create E2M platform group"
  $groupID = Get-Value $group "id"
  if ([string]::IsNullOrWhiteSpace([string]$groupID)) {
    $nestedGroup = Get-Value $group "group"
    $groupID = Get-Value $nestedGroup "id"
  }
  if ([string]::IsNullOrWhiteSpace([string]$groupID)) {
    throw "E2M platform group response has no valid id"
  }
  # POST is idempotent and may return an existing record from an earlier run.
  # Re-apply the current fixture definition without deleting persistent data.
  [void](Invoke-Json "PUT" "$E2MBaseUrl/api/v1/platform/groups/$groupID" $groupPayload $Headers)
  return [string]$groupID
}

function Create-PlatformUpstream(
  [hashtable]$Headers,
  [string]$GroupID,
  [string]$Name,
  [string]$BaseUrl,
  [string[]]$Models,
  [int]$Priority,
  [string]$IdempotencyKey,
  [long]$InputPriceMicrosPerMillion = 1000,
  [long]$OutputPriceMicrosPerMillion = 2000
) {
  $upstreamPayload = @{
    group_id = $GroupID
    name = $Name
    base_url = $BaseUrl.TrimEnd('/')
    api_key = "mock-key"
    models = $Models
    prices = @{}
    capacity = @{ max_concurrency = 4 }
    priority = $Priority
    weight = 1
    status = "active"
  }
  foreach ($model in $Models) {
    $upstreamPayload.prices[$model] = @{
        input_micros_per_million = $InputPriceMicrosPerMillion
        output_micros_per_million = $OutputPriceMicrosPerMillion
    }
  }
  $response = Invoke-Json "POST" "$E2MBaseUrl/api/v1/platform/upstreams" $upstreamPayload `
    ($Headers + @{ "Idempotency-Key" = $IdempotencyKey })
  $upstream = Get-Data $response "Create E2M platform upstream"
  $upstreamID = Get-Value $upstream "id"
  if ([string]::IsNullOrWhiteSpace([string]$upstreamID)) {
    $nestedUpstream = Get-Value $upstream "upstream"
    $upstreamID = Get-Value $nestedUpstream "id"
  }
  if ([string]::IsNullOrWhiteSpace([string]$upstreamID)) {
    throw "E2M platform upstream response has no valid id"
  }
  # Converge fixtures created by earlier runs to the exact current model and
  # priority policy while preserving their identity and accounting history.
  [void](Invoke-Json "PUT" "$E2MBaseUrl/api/v1/platform/upstreams/$upstreamID" $upstreamPayload $Headers)
  return [string]$upstreamID
}

function Add-TestBalance([hashtable]$Headers, [long]$UserID) {
  [void](Invoke-Json "POST" "$E2MBaseUrl/api/v1/platform/wallet-adjustments" @{
    user_id = $UserID
    amount_micros = 100000000
    reason = "local platform forwarding bootstrap"
  } ($Headers + @{ "Idempotency-Key" = "e2m-local-wallet-adjustment-v1" }))
}

function Create-DownstreamKey([hashtable]$Headers, [long]$UserID, [string]$GroupID) {
  # v3 includes the dedicated failover acceptance model. A new idempotency
  # identity preserves older keys and avoids widening their model permission.
  $response = Invoke-Json "POST" "$E2MBaseUrl/api/v1/platform/keys" @{
    user_id = $UserID
    group_id = $GroupID
    name = "e2m-local-client-v3"
    models = @("gpt-4o-mini", "gpt-e2m-failover")
    status = "active"
  } ($Headers + @{ "Idempotency-Key" = "e2m-local-downstream-key-v3" })
  $record = Get-Data $response "Create E2M downstream API key"
  $candidates = @($record, (Get-Value $record "key"), (Get-Value $record "credential"))
  foreach ($candidateRecord in $candidates) {
    if ($null -eq $candidateRecord) { continue }
    if ($candidateRecord -is [string] -and -not [string]::IsNullOrWhiteSpace($candidateRecord)) {
      return [string]$candidateRecord
    }
    foreach ($field in @("plaintext_key", "value", "api_key", "token", "secret")) {
      $candidate = Get-Value $candidateRecord $field
      if ($candidate -is [string] -and -not [string]::IsNullOrWhiteSpace($candidate)) {
        return [string]$candidate
      }
    }
  }
  throw "E2M key creation did not return the plaintext key"
}

function Verify-Forwarding([string]$APIKey) {
  $headers = @{
    Authorization = "Bearer $APIKey"
    "X-E2M-Correlation" = "e2m-local-forwarding"
  }
  $body = @{
    model = "gpt-4o-mini"
    messages = @(@{ role = "user"; content = "reply with ok" })
  }
  $response = Invoke-Json "POST" "$E2MBaseUrl/v1/chat/completions" $body $headers
  $choices = @(Get-Value $response "choices")
  $message = if ($choices.Count -gt 0) { Get-Value $choices[0] "message" } else { $null }
  if ([string](Get-Value $message "content") -ne "ok") {
    throw "E2M non-stream forwarding did not return the mock upstream response"
  }

  $streamBody = $body.Clone()
  $streamBody["stream"] = $true
  $streamResponse = Invoke-WebRequest -UseBasicParsing -Method POST `
    -Uri "$E2MBaseUrl/v1/chat/completions" `
    -Headers $headers `
    -ContentType "application/json; charset=utf-8" `
    -Body (ConvertTo-JsonBody $streamBody) `
    -TimeoutSec 60
  $streamText = [string]$streamResponse.Content
  if (-not $streamText.Contains('"content":"ok"') -or -not $streamText.Contains("[DONE]")) {
    throw "E2M streaming forwarding did not preserve the upstream SSE response"
  }
}

function Verify-Usage([hashtable]$Headers, [long]$UserID) {
  $deadline = (Get-Date).AddSeconds(30)
  do {
    $response = Invoke-Json "GET" "$E2MBaseUrl/api/v1/platform/usage?user_id=$UserID&limit=20" $null $Headers
    $data = Get-Data $response "Read E2M platform usage"
    $items = Get-Value $data "items"
    if ($null -eq $items -and $data -is [array]) { $items = $data }
    if (@($items).Count -ge 2) { return }
    Start-Sleep -Milliseconds 500
  } while ((Get-Date) -lt $deadline)
  throw "E2M usage API did not expose both accepted forwarding requests"
}

function Get-MockEvidence([string]$Service) {
  $raw = & docker compose -p $ComposeProject -f $ComposeFile exec -T $Service wget -qO- http://127.0.0.1:8093/debug/requests
  if ($LASTEXITCODE -ne 0) { throw "Could not read request evidence from $Service" }
  return $raw | ConvertFrom-Json
}

function Verify-Failover(
  [hashtable]$Headers,
  [long]$UserID,
  [string]$APIKey,
  [string]$FailingUpstreamID,
  [string]$SuccessfulUpstreamID
) {
  $beforeFailEvidence = Get-MockEvidence "mock-openai-fail"
  $beforeSuccessEvidence = Get-MockEvidence "mock-openai"
  $beforeFail = [int](Get-Value $beforeFailEvidence "count")
  $beforeSuccess = [int](Get-Value $beforeSuccessEvidence "count")

  $requestBody = @{
    model = "gpt-e2m-failover"
    messages = @(@{ role = "user"; content = "verify failover" })
  }
  $rawResponse = Invoke-WebRequest -UseBasicParsing -Method POST `
    -Uri "$E2MBaseUrl/v1/chat/completions" `
    -Headers @{
    Authorization = "Bearer $APIKey"
    "X-E2M-Correlation" = "e2m-local-failover"
    } `
    -ContentType "application/json; charset=utf-8" `
    -Body (ConvertTo-JsonBody $requestBody) `
    -TimeoutSec 60
  $response = $rawResponse.Content | ConvertFrom-Json
  $rootRequestID = [string]$rawResponse.Headers["X-E2M-Request-ID"]
  if ([string]::IsNullOrWhiteSpace($rootRequestID)) {
    throw "Failover response did not expose X-E2M-Request-ID"
  }
  $choices = @(Get-Value $response "choices")
  $message = if ($choices.Count -gt 0) { Get-Value $choices[0] "message" } else { $null }
  if ([string](Get-Value $message "content") -ne "ok") {
    throw "Failover request was not successfully delivered by the second upstream"
  }

  $afterFailEvidence = Get-MockEvidence "mock-openai-fail"
  $afterSuccessEvidence = Get-MockEvidence "mock-openai"
  $afterFail = [int](Get-Value $afterFailEvidence "count")
  $afterSuccess = [int](Get-Value $afterSuccessEvidence "count")
  if ($afterFail -ne $beforeFail + 1 -or $afterSuccess -ne $beforeSuccess + 1) {
    throw "Expected exactly one failing and one successful upstream attempt; observed fail=$($afterFail-$beforeFail), success=$($afterSuccess-$beforeSuccess)"
  }
  $newFailRecord = @((Get-Value $afterFailEvidence "items") | Select-Object -Skip $beforeFail)[0]
  $newSuccessRecord = @((Get-Value $afterSuccessEvidence "items") | Select-Object -Skip $beforeSuccess)[0]
  if ([string](Get-Value $newFailRecord "correlation_sha256") -ne [string](Get-Value $newSuccessRecord "correlation_sha256")) {
    throw "The two upstream attempts did not preserve one root request correlation"
  }

  $usageResponse = Invoke-Json "GET" "$E2MBaseUrl/api/v1/platform/usage?user_id=$UserID&limit=20" $null $Headers
  $usageData = Get-Data $usageResponse "Read failover usage"
  $items = @(Get-Value $usageData "items")
  $failures = @($items | Where-Object {
    [string](Get-Value $_ "request_id") -eq $rootRequestID -and
    [string](Get-Value $_ "channel_id") -eq $FailingUpstreamID -and
    [string](Get-Value $_ "status") -eq "released" -and
    [string](Get-Value $_ "settlement_reason") -eq "upstream_http_retryable"
  })
  $successes = @($items | Where-Object {
    [string](Get-Value $_ "request_id") -eq "${rootRequestID}_retry_1" -and
    [string](Get-Value $_ "channel_id") -eq $SuccessfulUpstreamID -and
    [string](Get-Value $_ "status") -eq "settled"
  })
  if ($failures.Count -lt 1 -or $successes.Count -lt 1) {
    throw "Usage ledger did not prove released first attempt and settled second attempt"
  }
}

function Get-DownstreamKeyID([hashtable]$Headers, [long]$UserID, [string]$KeyName) {
  $response = Invoke-Json "GET" "$E2MBaseUrl/api/v1/platform/keys?user_id=$UserID" $null $Headers
  $keys = Get-Data $response "List E2M downstream keys"
  foreach ($key in @($keys)) {
    if ([string](Get-Value $key "name") -eq $KeyName) { return [string](Get-Value $key "id") }
  }
  throw "Downstream key $KeyName was not found in the key list"
}

function Set-RoutingPreference([hashtable]$Headers, [long]$UserID, [string]$KeyID, [string]$Preference) {
  $saved = Invoke-Json "PUT" "$E2MBaseUrl/api/v1/platform/keys/${KeyID}?user_id=$UserID" @{
    routing_preference = $Preference
  } $Headers
  $stored = [string](Get-Value (Get-Data $saved "Update routing preference") "routing_preference")
  if ($stored -ne $Preference) {
    throw "Routing preference did not persist: wanted '$Preference', stored '$stored'"
  }
}

function Get-UsageRowChannel([hashtable]$Headers, [long]$UserID, [string]$RequestID) {
  $deadline = (Get-Date).AddSeconds(30)
  do {
    $response = Invoke-Json "GET" "$E2MBaseUrl/api/v1/platform/usage?user_id=$UserID&limit=50" $null $Headers
    $data = Get-Data $response "Read E2M platform usage"
    $items = Get-Value $data "items"
    if ($null -eq $items -and $data -is [array]) { $items = $data }
    foreach ($item in @($items)) {
      if ([string](Get-Value $item "request_id") -eq $RequestID -and [string](Get-Value $item "status") -eq "settled") {
        return $item
      }
    }
    Start-Sleep -Milliseconds 500
  } while ((Get-Date) -lt $deadline)
  throw "Usage ledger did not settle request $RequestID"
}

function Invoke-TrackedCompletion([string]$APIKey, [string]$Correlation) {
  $rawResponse = Invoke-WebRequest -UseBasicParsing -Method POST `
    -Uri "$E2MBaseUrl/v1/chat/completions" `
    -Headers @{ Authorization = "Bearer $APIKey"; "X-E2M-Correlation" = $Correlation } `
    -ContentType "application/json; charset=utf-8" `
    -Body (ConvertTo-JsonBody @{
      model = "gpt-4o-mini"
      messages = @(@{ role = "user"; content = "reply with ok" })
    }) `
    -TimeoutSec 60
  $requestID = [string]$rawResponse.Headers["X-E2M-Request-ID"]
  if ([string]::IsNullOrWhiteSpace($requestID)) {
    throw "Completion response did not expose X-E2M-Request-ID"
  }
  return $requestID
}

# The routing preference is customer-visible behavior: the same key, the same
# model, and the same wallet must land on a different channel — and settle at
# that channel's price — purely because the preference changed. Unit tests
# cannot prove this wiring; only the ledger can.
function Verify-RoutingPreference(
  [hashtable]$Headers,
  [long]$UserID,
  [string]$APIKey,
  [string]$KeyID,
  [string]$DefaultUpstreamID,
  [string]$EconomyUpstreamID,
  [long]$EconomyInputPriceMicrosPerMillion
) {
  # A rerun may inherit a preference from an interrupted earlier run; start
  # from the platform default so the baseline assertion is meaningful.
  Set-RoutingPreference $Headers $UserID $KeyID ""

  $defaultRequestID = Invoke-TrackedCompletion $APIKey "e2m-local-routing-default"
  $defaultRow = Get-UsageRowChannel $Headers $UserID $defaultRequestID
  if ([string](Get-Value $defaultRow "channel_id") -ne $DefaultUpstreamID) {
    throw "Without a preference the platform default order must select the standard upstream"
  }

  Set-RoutingPreference $Headers $UserID $KeyID "price_first"
  $priceRequestID = Invoke-TrackedCompletion $APIKey "e2m-local-routing-price"
  $priceRow = Get-UsageRowChannel $Headers $UserID $priceRequestID
  if ([string](Get-Value $priceRow "channel_id") -ne $EconomyUpstreamID) {
    throw "price_first must route the request to the cheaper economy upstream"
  }
  $settledInputPrice = [long](Get-Value $priceRow "input_price_micros_per_million")
  if ($settledInputPrice -ne $EconomyInputPriceMicrosPerMillion) {
    throw "price_first settlement must use the economy price; settled at $settledInputPrice"
  }

  # Clearing the preference restores the exact previous behaviour.
  Set-RoutingPreference $Headers $UserID $KeyID ""
  $restoredRequestID = Invoke-TrackedCompletion $APIKey "e2m-local-routing-restored"
  $restoredRow = Get-UsageRowChannel $Headers $UserID $restoredRequestID
  if ([string](Get-Value $restoredRow "channel_id") -ne $DefaultUpstreamID) {
    throw "Clearing the preference must return the key to the platform default order"
  }

  # The reliability buckets behind speed/success ranking fill from the same
  # settlements; the admin stats endpoint must show the delivered sample.
  $stats = Invoke-Json "GET" "$E2MBaseUrl/api/v1/platform/upstreams/$EconomyUpstreamID/stats?window_minutes=60" $null $Headers
  $statsData = Get-Data $stats "Read economy upstream stats"
  if ([long](Get-Value $statsData "requests") -lt 1) {
    throw "Channel statistics did not record the economy upstream's delivered request"
  }
}

if (-not $SkipComposeUp) {
  Write-Step "Starting the E2M platform acceptance stack"
  Invoke-Compose @("up", "--build", "-d", "--remove-orphans")
}

Write-Step "Waiting for E2M Core"
Wait-Http "$E2MBaseUrl/healthz" "E2M Core"

Write-Step "Creating the E2M group, upstream, balance, and downstream key"
$session = Login-E2M
$groupID = Create-PlatformGroup $session.Headers
$failingUpstreamID = Create-PlatformUpstream -Headers $session.Headers -GroupID $groupID `
  -Name "e2m-local-mock-openai-fail" -BaseUrl $FailingUpstreamBaseUrl `
  -Models @("gpt-e2m-failover") -Priority 0 -IdempotencyKey "e2m-local-mock-upstream-fail-v1"
$successfulUpstreamID = Create-PlatformUpstream -Headers $session.Headers -GroupID $groupID `
  -Name "e2m-local-mock-openai" -BaseUrl $PlatformUpstreamBaseUrl `
  -Models @("gpt-4o-mini", "gpt-e2m-failover") -Priority 10 -IdempotencyKey "e2m-local-mock-upstream-v1"
# Same mock server, separate channel identity: ten times cheaper at a worse
# priority, so only a price_first preference can ever select it.
$economyInputPriceMicrosPerMillion = 100
$economyUpstreamID = Create-PlatformUpstream -Headers $session.Headers -GroupID $groupID `
  -Name "e2m-local-mock-openai-economy" -BaseUrl $PlatformUpstreamBaseUrl `
  -Models @("gpt-4o-mini") -Priority 20 -IdempotencyKey "e2m-local-mock-upstream-economy-v1" `
  -InputPriceMicrosPerMillion $economyInputPriceMicrosPerMillion -OutputPriceMicrosPerMillion 200
Add-TestBalance $session.Headers $session.UserID
$apiKey = Create-DownstreamKey $session.Headers $session.UserID $groupID
$keyID = Get-DownstreamKeyID $session.Headers $session.UserID "e2m-local-client-v3"

Write-Step "Verifying E2M non-stream and streaming request forwarding"
Verify-Forwarding $apiKey
Verify-Usage $session.Headers $session.UserID

Write-Step "Verifying same-group retryable upstream failure transfer"
Verify-Failover $session.Headers $session.UserID $apiKey $failingUpstreamID $successfulUpstreamID

Write-Step "Verifying key routing preference steers channel selection and settlement price"
Verify-RoutingPreference $session.Headers $session.UserID $apiKey $keyID `
  $successfulUpstreamID $economyUpstreamID $economyInputPriceMicrosPerMillion

New-Item -ItemType Directory -Force -Path $RuntimeDir | Out-Null
[System.IO.File]::WriteAllText($KeyFile, $apiKey + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))

Write-Step "Done"
Write-Host "    E2M product: $E2MBaseUrl"
Write-Host "    API key:     $KeyFile"
Write-Host "    Verified:    JSON + SSE, platform usage, 503 -> second-upstream failover, and routing-preference channel steering"
