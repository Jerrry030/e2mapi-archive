param(
  [string]$ComposeFile = "",
  [string]$OverrideFile = "",
  [string]$E2MBaseUrl = "http://127.0.0.1:18080",
  [string]$PlatformUpstreamBaseUrl = "http://mock-openai:8093/v1",
  [string]$AdminEmail = "admin@local.dev",
  [string]$AdminPassword = "admin123456",
  [int]$TimeoutSeconds = 180,
  [switch]$SkipComposeUp
)

# Fixes the commerce-loop MVP scenario (execution plan section 2.1) as a
# repeatable acceptance run: redeem-code lifecycle with duplicate rejection,
# create-and-redeem idempotency, hot-applied pricing settings, the model
# market, and metered forwarding through both the OpenAI route and the
# Anthropic /v1/messages bridge. Hosted-checkout payment (Stripe/EasyPay)
# needs provider credentials and is probed but skipped when no provider is
# enabled, so the script stays runnable on a clean machine.

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($ComposeFile)) {
  $ComposeFile = Join-Path $RepoRoot "deployments/templates/compose/e2m-core-real-gateways.compose.yml"
}
if ([string]::IsNullOrWhiteSpace($OverrideFile)) {
  $OverrideFile = Join-Path $RepoRoot "deployments/runtime/acceptance/commerce-override.yml"
}

function Write-Step([string]$Message) {
  Write-Host "`n==> $Message"
}

function Invoke-Compose([string[]]$Arguments) {
  & docker compose -f $ComposeFile -f $OverrideFile @Arguments
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

# Invoke-ExpectStatus runs a request whose failure is the assertion: it
# returns the HTTP status code instead of throwing, so callers can require an
# exact rejection (for example a duplicate redeem must be a 400).
function Invoke-ExpectStatus(
  [string]$Method,
  [string]$Uri,
  $Body = $null,
  [hashtable]$Headers = @{}
) {
  try {
    [void](Invoke-Json $Method $Uri $Body $Headers)
    return 200
  } catch {
    $response = $_.Exception.Response
    if ($null -eq $response) { throw }
    return [int]$response.StatusCode
  }
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

function Ensure-OverrideFile {
  if (Test-Path $OverrideFile) { return }
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $OverrideFile) | Out-Null
  @"
# Local acceptance override: enables the platform commerce surface on top of
# deployments/templates/compose/e2m-core-real-gateways.compose.yml.
# This directory is gitignored; values here are local fixtures only.
services:
  e2m-core:
    build:
      args:
        VITE_E2M_ENABLE_PAYMENTS: "true"
    environment:
      E2M_ENABLE_PAYMENTS: "true"
      E2M_USD_TO_CNY_RATE: "7.20"
      E2M_PLATFORM_BALANCE_THRESHOLD: "5"
      E2M_AUTH_REGISTRATION_ENABLED: "true"
"@ | Set-Content -NoNewline -Path $OverrideFile -Encoding UTF8
  Write-Host "    Materialized $OverrideFile"
}

function Set-CommerceSettings([hashtable]$Headers) {
  $response = Invoke-Json "PUT" "$E2MBaseUrl/api/v1/admin/settings/commerce" @{
    usd_to_cny_rate = "7.20"
    balance_alert_threshold = "5.00"
  } $Headers
  $data = Get-Data $response "Update commerce settings"
  if ([string](Get-Value $data "usd_to_cny_rate") -ne "7.20") {
    throw "Commerce settings did not persist the exchange rate"
  }
}

function Create-CommerceFixtures([hashtable]$Headers, [long]$UserID) {
  $groupPayload = @{
    name = "e2m-commerce-acceptance"
    description = "Commerce-loop acceptance group"
    models = @("gpt-4o-mini")
    status = "active"
  }
  $response = Invoke-Json "POST" "$E2MBaseUrl/api/v1/platform/groups" $groupPayload `
    ($Headers + @{ "Idempotency-Key" = "e2m-commerce-group-v1" })
  $group = Get-Data $response "Create commerce group"
  $groupID = [string](Get-Value $group "id")
  if ([string]::IsNullOrWhiteSpace($groupID)) {
    $groupID = [string](Get-Value (Get-Value $group "group") "id")
  }
  if ([string]::IsNullOrWhiteSpace($groupID)) { throw "Commerce group has no id" }
  [void](Invoke-Json "PUT" "$E2MBaseUrl/api/v1/platform/groups/$groupID" $groupPayload $Headers)

  $upstreamPayload = @{
    group_id = $groupID
    name = "e2m-commerce-mock-openai"
    base_url = $PlatformUpstreamBaseUrl.TrimEnd('/')
    api_key = "mock-key"
    models = @("gpt-4o-mini")
    prices = @{ "gpt-4o-mini" = @{ input_micros_per_million = 1000; output_micros_per_million = 2000 } }
    capacity = @{ max_concurrency = 4 }
    priority = 10
    weight = 1
    status = "active"
  }
  $response = Invoke-Json "POST" "$E2MBaseUrl/api/v1/platform/upstreams" $upstreamPayload `
    ($Headers + @{ "Idempotency-Key" = "e2m-commerce-upstream-v1" })
  [void](Get-Data $response "Create commerce upstream")

  $response = Invoke-Json "POST" "$E2MBaseUrl/api/v1/platform/keys" @{
    user_id = $UserID
    group_id = $groupID
    name = "e2m-commerce-client-v1"
    models = @("gpt-4o-mini")
    status = "active"
  } ($Headers + @{ "Idempotency-Key" = "e2m-commerce-key-v1" })
  $record = Get-Data $response "Create commerce downstream key"
  foreach ($candidateRecord in @($record, (Get-Value $record "key"), (Get-Value $record "credential"))) {
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
  throw "Commerce key creation did not return the plaintext key"
}

function Verify-RedeemLifecycle([hashtable]$Headers) {
  $response = Invoke-Json "POST" "$E2MBaseUrl/api/v1/admin/redeem-codes" @{
    type = "balance"
    count = 2
    amount = "10.00"
    notes = "commerce acceptance batch"
  } $Headers
  $batch = Get-Data $response "Generate redeem codes"
  $codes = @(Get-Value $batch "codes")
  if ($codes.Count -ne 2) { throw "Batch generation did not return 2 plaintext codes" }

  $redeemed = Get-Data (Invoke-Json "POST" "$E2MBaseUrl/api/v1/redeem" @{ code = $codes[0] } $Headers) "Redeem code"
  if ([long](Get-Value $redeemed "amount_micros") -ne 10000000) {
    throw "Redeem did not credit the expected 10.00 CNY"
  }
  $wallet = Get-Value $redeemed "wallet"
  $balanceAfterRedeem = [long](Get-Value $wallet "available_micros")
  if ($balanceAfterRedeem -lt 10000000) {
    throw "Wallet balance did not reflect the redeemed credit: $balanceAfterRedeem"
  }

  $duplicateStatus = Invoke-ExpectStatus "POST" "$E2MBaseUrl/api/v1/redeem" @{ code = $codes[0] } $Headers
  if ($duplicateStatus -ne 400) {
    throw "A second redeem of the same code must be rejected with 400, got $duplicateStatus"
  }
  return $codes[1]
}

function Verify-CreateAndRedeemIdempotency([hashtable]$Headers, [long]$UserID) {
  $idempotencyKey = "e2m-commerce-car-v1"
  $body = @{ user_id = $UserID; amount = "3.50"; notes = "external fulfillment drill" }
  $carHeaders = $Headers + @{ "Idempotency-Key" = $idempotencyKey }

  $first = Get-Data (Invoke-Json "POST" "$E2MBaseUrl/api/v1/admin/redeem-codes/create-and-redeem" $body $carHeaders) "create-and-redeem"
  $replay = Get-Data (Invoke-Json "POST" "$E2MBaseUrl/api/v1/admin/redeem-codes/create-and-redeem" $body $carHeaders) "create-and-redeem replay"
  if (-not [bool](Get-Value $replay "replay")) {
    throw "A replay with the same Idempotency-Key must return replay=true"
  }
  $firstCodeID = [string](Get-Value (Get-Value $first "code") "id")
  $replayCodeID = [string](Get-Value (Get-Value $replay "code") "id")
  if ($firstCodeID -ne $replayCodeID) {
    throw "Replay returned a different code: $firstCodeID vs $replayCodeID"
  }

  $conflictStatus = Invoke-ExpectStatus "POST" "$E2MBaseUrl/api/v1/admin/redeem-codes/create-and-redeem" `
    @{ user_id = $UserID; amount = "9.99" } $carHeaders
  if ($conflictStatus -ne 409) {
    throw "The same Idempotency-Key with a different payload must be a 409, got $conflictStatus"
  }
}

function Verify-ModelMarket([hashtable]$Headers) {
  $response = Invoke-Json "GET" "$E2MBaseUrl/api/v1/platform/model-market" $null $Headers
  $market = Get-Data $response "Read model market"
  $items = Get-Value $market "items"
  if ($null -eq $items -and $market -is [array]) { $items = $market }
  $entries = @($items | Where-Object {
    [string](Get-Value $_ "group_name") -eq "e2m-commerce-acceptance" -or
    ([string](Get-Value $_ "model")) -eq "gpt-4o-mini"
  })
  if ($entries.Count -lt 1) {
    throw "Model market did not list the acceptance group's model offer"
  }
}

function Verify-PaymentOrderProbe([hashtable]$Headers) {
  $status = Invoke-ExpectStatus "POST" "$E2MBaseUrl/api/v1/owner/hybrid-supply/recharge-orders" `
    @{ amount = "10.00"; currency = "CNY"; payment_type = "stripe" } $Headers
  if ($status -eq 200 -or $status -eq 201) {
    Write-Host "    Payment provider is configured: hosted checkout order created"
    return
  }
  if ($status -eq 400 -or $status -eq 404) {
    Write-Host "    Skipped hosted checkout: no payment provider enabled (HTTP $status). The route is reachable behind the payments gate."
    return
  }
  throw "Recharge order probe returned unexpected HTTP $status"
}

function Verify-OpenAIForwarding([string]$APIKey) {
  $headers = @{ Authorization = "Bearer $APIKey" }
  $body = @{
    model = "gpt-4o-mini"
    messages = @(@{ role = "user"; content = "reply with ok" })
  }
  $response = Invoke-Json "POST" "$E2MBaseUrl/v1/chat/completions" $body $headers
  $choices = @(Get-Value $response "choices")
  $message = if ($choices.Count -gt 0) { Get-Value $choices[0] "message" } else { $null }
  if ([string](Get-Value $message "content") -ne "ok") {
    throw "OpenAI-route forwarding did not return the mock upstream response"
  }
}

function Verify-MessagesBridge([string]$APIKey) {
  $headers = @{ "x-api-key" = $APIKey }
  $body = @{
    model = "gpt-4o-mini"
    max_tokens = 32
    messages = @(@{ role = "user"; content = "reply with ok" })
  }
  $response = Invoke-Json "POST" "$E2MBaseUrl/v1/messages" $body $headers
  if ([string](Get-Value $response "type") -ne "message" -or [string](Get-Value $response "role") -ne "assistant") {
    throw "/v1/messages did not return an Anthropic message document"
  }
  $content = @(Get-Value $response "content")
  if ($content.Count -lt 1 -or [string](Get-Value $content[0] "text") -ne "ok") {
    throw "/v1/messages did not translate the upstream content"
  }
  $usage = Get-Value $response "usage"
  if ([long](Get-Value $usage "input_tokens") -le 0) {
    throw "/v1/messages did not report translated usage"
  }

  $streamBody = $body.Clone()
  $streamBody["stream"] = $true
  $streamResponse = Invoke-WebRequest -UseBasicParsing -Method POST `
    -Uri "$E2MBaseUrl/v1/messages" `
    -Headers $headers `
    -ContentType "application/json; charset=utf-8" `
    -Body (ConvertTo-JsonBody $streamBody) `
    -TimeoutSec 60
  $streamText = [string]$streamResponse.Content
  foreach ($marker in @("event: message_start", "event: content_block_delta", "event: message_delta", "event: message_stop")) {
    if (-not $streamText.Contains($marker)) {
      throw "/v1/messages stream is missing '$marker'"
    }
  }
  if ($streamText.Contains("[DONE]")) {
    throw "/v1/messages stream leaked OpenAI protocol markers"
  }
}

function Verify-SettledUsage([hashtable]$Headers, [long]$UserID, [int]$MinimumSettled) {
  $deadline = (Get-Date).AddSeconds(30)
  do {
    $response = Invoke-Json "GET" "$E2MBaseUrl/api/v1/platform/usage?user_id=$UserID&limit=50" $null $Headers
    $data = Get-Data $response "Read platform usage"
    $items = Get-Value $data "items"
    if ($null -eq $items -and $data -is [array]) { $items = $data }
    $settled = @($items | Where-Object { [string](Get-Value $_ "status") -eq "settled" })
    if ($settled.Count -ge $MinimumSettled) { return }
    Start-Sleep -Milliseconds 500
  } while ((Get-Date) -lt $deadline)
  throw "Usage ledger did not show at least $MinimumSettled settled commerce-loop requests"
}

Ensure-OverrideFile

if (-not $SkipComposeUp) {
  Write-Step "Starting the E2M commerce acceptance stack"
  Invoke-Compose @("up", "--build", "-d", "--remove-orphans")
}

Write-Step "Waiting for E2M Core"
Wait-Http "$E2MBaseUrl/healthz" "E2M Core"

Write-Step "Logging in and hot-applying commerce settings"
$session = Login-E2M
Set-CommerceSettings $session.Headers

Write-Step "Creating the commerce group, upstream, and downstream key"
$apiKey = Create-CommerceFixtures $session.Headers $session.UserID

Write-Step "Verifying redeem-code lifecycle and duplicate rejection"
$spareCode = Verify-RedeemLifecycle $session.Headers
Write-Host "    Spare unused code for manual drills: $spareCode"

Write-Step "Verifying create-and-redeem idempotency"
Verify-CreateAndRedeemIdempotency $session.Headers $session.UserID

Write-Step "Verifying the customer model market"
Verify-ModelMarket $session.Headers

Write-Step "Probing hosted-checkout order creation"
Verify-PaymentOrderProbe $session.Headers

Write-Step "Verifying metered forwarding on both protocol routes"
Verify-OpenAIForwarding $apiKey
Verify-MessagesBridge $apiKey
Verify-SettledUsage $session.Headers $session.UserID 3

Write-Step "Done"
Write-Host "    Verified: redeem lifecycle, duplicate rejection, create-and-redeem idempotency,"
Write-Host "              hot-applied pricing settings, model market, OpenAI route + /v1/messages bridge,"
Write-Host "              and settled usage records for every forwarded request"
