# Current Implementation State

Updated: 2026-08-09

> **退役声明（2026-08-09）**：本仓库已停止迭代，基座迁往 `e2m-platform`。本文
> 描述的一切**在退役时刻是准确的**——它记录的是这套代码实际能做什么，而不是
> 计划做什么，因此对日后的移植参考仍然有效。但它**不再随时间更新**：`e2m-platform`
> 的现状以那边的 `e2m/docs/capability-surpass-execution-plan.md` 为准。搬家已完成
> 的两项（Connector 控制面、密钥级路由偏好）在 fork 上有各自的设计与验收文档；
> 本文的商业化章节（Platform Commerce）是尚未移植部分的权威描述。

This document defines the factual product and runtime baseline for the current
E2M vertical slice. Older roadmap, progress, ADR, architecture, migration, and
source files may describe retired or not-yet-connected capabilities; they do
not override this document.

## Product Shape

E2M is the only product, identity boundary, management surface, and platform
request data plane:

```text
customer-owned pool -> customer gateway -> model upstream
                              ^
                              | management API only
                       Connector -> E2M Core

downstream E2M key -> E2M Core /v1/* -> E2M platform upstream pool
                            ^
                            |
               E2M Console and management API
```

Connector manages a customer's own gateway without joining its request path.
E2M Core natively owns platform groups, upstreams, downstream API keys, wallet
balance, metering, scheduling, failure transfer, and protocol forwarding.

Patterns learned from Sub2API are implemented behind E2M-native contracts. The
production runtime does not deploy Sub2API, expose it to users/operators, or
create a second identity, balance, key, or database boundary.

There is no percentage allocation among owner, economy, and stable resources
in the first release.

## Active Runtime Surface

### E2M Core

Core is responsible for these product surfaces:

- health, local authentication, users, roles, and registration/security;
- managed customer instances and Connector installation;
- owner-pool health, account reads, schedulable changes, switching, and audit;
- Connector enrollment, token lifecycle, task lease/execute/complete, and
  execution resolution;
- notification routes, durable deliveries, and SSE operational events;
- E2M platform groups and their allowed models;
- encrypted platform upstream credentials, capacity, status, and group
  membership;
- E2M downstream API key creation, validation, and controlled Vault-backed
  value retrieval;
- a single E2M wallet balance per downstream user and admin adjustments;
- an authoritative `GET /v1/models` catalog listing exactly the models the
  presented key can call, computed from the scheduler's own eligibility
  predicate, reserving nothing and independent of wallet balance;
- OpenAI-compatible `/v1/chat/completions`, metering, accounting, scheduling,
  and retry/failure transfer among compatible upstreams;
- an Anthropic-compatible `/v1/messages` bridge that serves Messages-protocol
  clients from the same OpenAI-compatible upstream pool, reservation path,
  and failover loop (see Platform forwarding);
- platform usage records and summaries exposed through E2M APIs.

The native platform management contract is:

```text
GET/POST       /api/v1/platform/groups
GET/PUT/DELETE /api/v1/platform/groups/{id}
GET/POST       /api/v1/platform/upstreams
GET/PUT/DELETE /api/v1/platform/upstreams/{id}
POST           /api/v1/platform/upstreams/{id}/test
GET            /api/v1/platform/upstreams/{id}/stats
POST /api/v1/platform/keys
GET  /api/v1/platform/keys/{id}/value
POST /api/v1/platform/wallet-adjustments
GET  /api/v1/platform/usage
GET  /v1/models
POST /v1/chat/completions
POST /v1/messages
```

Core owns the entire `/v1/` subtree, not just the endpoints it implements.
An unimplemented path answers `404 unknown_endpoint` as JSON and a wrong method
answers `405` with an `Allow` header. Routing only the implemented endpoints
previously let everything else reach the console SPA, which replied `200` with
`index.html`; a client then parsed HTML as JSON and reported an unrelated
failure.

These are E2M contracts. A client must never call a hidden third-party
management API as part of provisioning or acceptance.

Administrators can create and edit groups and OpenAI-compatible upstreams,
toggle lifecycle state, safely retire groups or take upstreams offline, and
test stored credentials against the upstream model catalog. Connection tests
resolve the credential only inside Core, reject redirects, bound response size
and latency, and return only sanitized status and model identifiers. Deletes
preserve accounting and audit history instead of hard-deleting rows. Core's
durable retirement worker automatically resumes group drains after restart.

Platform key list and ordinary detail responses expose only the key prefix and
non-secret metadata. The full value remains encrypted in the E2M Vault and is
returned only by `GET /api/v1/platform/keys/{id}/value` (also available through
the compatibility alias `GET /api/v1/platform/api-keys/{id}/value`) after E2M
verifies key ownership, the current user's enabled state, and the current
`client` or `admin` role. Successful reads disable caching with
`Cache-Control: no-store, private` and create a `platform_key.view` audit
record. This supports persistent show/hide and quick-copy controls in the E2M
Console without returning plaintext keys from normal list or detail APIs.

### Connector

One outbound Connector manages exactly one customer-owned gateway instance.
The Connector advertises only:

1. `gateway.health`;
2. `gateway.accounts.list`;
3. `gateway.schedulable.set`;
4. `gateway.switch`;
5. `gateway.scheduling.barrier`.

The local UI owns the customer's gateway type, management URL, authentication,
request timeout, and log level. The management credential is stored only in
the Connector private data directory. Local API responses expose configured
flags, never plaintext credentials.

Connector does not receive platform upstream credentials, issue platform API
keys, proxy platform traffic, adjust E2M balances, collect platform usage, or
create/delete platform resources. Historical helper code or persisted fields
do not expand this contract.

### Platform forwarding

The platform request enters E2M directly:

1. authenticate the E2M API key;
2. validate key state, current user enabled state and current `client`/`admin`
   role, wallet balance, group, and model access;
3. enforce the per-user platform concurrency and RPM limits inside the
   reservation transaction (zero means unlimited, an idempotent replay is
   exempt); exceeding either returns `429 rate_limited`;
4. select a healthy compatible upstream in the E2M group, skipping channels
   currently parked by an operator cooldown rule; when the key carries a
   routing preference, eligible candidates are re-ranked by it first (see
   below);
5. rewrite the request body model when the selected channel declares a model
   mapping, then forward while preserving compatible request and response
   semantics, including SSE; usage snapshots keep the requested model;
6. retry or transfer only for transport errors, 408, 429, and 5xx; forward
   deterministic 4xx rejections without broadcasting the request to another
   channel; a failure matching the channel's cooldown rules parks that channel
   for the configured duration;
7. atomically record usage and apply the final charge/refund outcome in E2M.

Every attempt finalization also records data-plane telemetry (2026-08-07):
the usage row keeps the attempt's time to first upstream byte and total
duration, and the same settlement transaction adds one sample to the serving
channel's five-minute reliability bucket (`supply_channel_stats`). Only
outcomes that say something about the channel count — delivered responses and
channel failures such as transport errors, retryable statuses, and truncated
streams. Client disconnects and deterministic upstream rejections stay
neutral, and an idempotent replay never re-counts. Administrators read the
aggregate through `GET /api/v1/platform/upstreams/{id}/stats`, which reports
absent rates for an empty window instead of fabricating 0% or 100%.

Key-scoped routing preference shipped on top of those buckets (2026-08-07):
`virtual_keys.routing_preference` holds one of `smart_auto`, `price_first`,
`speed_first`, or `success_first`; NULL follows the platform default order,
so every pre-existing key behaves exactly as before. A preference only
re-orders candidates that already passed the hard gates — health, cooldown,
capacity, model, concurrency — and can never re-admit an excluded channel or
fail an otherwise-servable request. `price_first` sorts by blended sell price
(prompt weighted twice completion, the hold-ceiling reference shape);
`speed_first` and `success_first` sort by the last 30 minutes of reliability
buckets under Bayesian smoothing, so a channel with no evidence ranks
mid-pack rather than being pinned to the top or bottom by missing data.
Failure transfer walks the same preference order. The preference is edited
through `PUT /api/v1/platform/keys/{id}` (`routing_preference`: one of the
four values, or the empty string to clear back to the platform default) under
the usual customer/admin ownership rules, and through a row-level 智能路由
drawer on the platform-distribution key table. Because the preference steers
which channel serves and therefore what a request costs, every actual change
appends a `platform_key.routing_preference.update` audit entry alongside the
generic key-update audit, and the drawer copy states that the choice can
select a more expensive channel.

`POST /v1/messages` accepts the Anthropic Messages protocol (both `x-api-key`
and bearer credentials) and bridges it onto the same OpenAI-compatible
upstream pool: one reservation/settlement path, one failover loop, one
cooldown state. The request body, response document, SSE event grammar
(`message_start` through `message_stop`), and error shape are translated at
the edge; the upstream still receives `/v1/chat/completions`. Requests using
capabilities the bridge does not translate yet — tool use, or non-text
content blocks — are rejected with a 400 up front instead of being forwarded
in a shape the upstream would misread. Streams that end without an upstream
usage frame settle conservatively, exactly like the OpenAI route.

Model mapping is configured in the upstream edit form; cooldown rules are
edited from a row-level action on the upstream list, kept out of the already
dense edit modal. Both structured editors are backed by the
`e2m.model_mapping` and `e2m.error_cooldown_rules` channel labels, and each
save rewrites only its own label so every other stored label passes through
untouched. Cooldown state is in-process and
clears on restart; durable circuit state remains the parked quality-circuit
subsystem's responsibility.

The downstream sees only E2M domains, E2M error contracts, E2M keys, and E2M
usage. Real upstream credentials are encrypted and remain server-side.
Customer group reads use a product-catalog DTO containing only group ID, name,
description, an internal default resource class, models, and status. A group is
a named, sellable pool: operators do not choose a resource class when creating
one, and economy/stable is not a customer-facing product tier — that concept
belongs to the deferred three-pool ratio work. The group sell-price multiplier
is administrator-only and never appears in the customer view. Provider identity,
region, operational labels, safety stock, and delivery-mode internals remain
administrator-only. Every accepted gateway request receives an E2M-generated
`X-E2M-Request-ID`, including deterministic upstream 4xx responses.

## Minimal Local Stack

`deployments/templates/compose/e2m-core-real-gateways.compose.yml` contains
four services for the failover acceptance drill:

| Service | Responsibility |
|---|---|
| `postgres` | All E2M product state for the local acceptance stack |
| `mock-openai` | Compose-internal disposable OpenAI-compatible upstream |
| `mock-openai-fail` | Compose-internal deterministic 503 upstream used to prove failure transfer |
| `e2m-core` | E2M management surface and platform request data plane |

The topology deliberately contains no Sub2API service, Sub2API database,
Sub2API Redis, or externally published mock-upstream port.

`scripts/bootstrap-real-gateways.ps1` starts the stack under an explicit
compose project name (`e2m-real-gateways`; without one, compose derives the
project from the shared templates directory and `--remove-orphans` would
delete a production stack started from a sibling file), logs into E2M, and
calls only E2M APIs to create:

- one local platform group;
- three OpenAI-compatible mock platform upstreams in one group: a 503
  upstream, a standard-priced upstream, and an economy upstream that shares
  the standard mock server but is ten times cheaper at a worse priority;
- one idempotent local wallet adjustment;
- one E2M downstream API key;
- one non-stream and one stream request through E2M;
- usage evidence proving the failed reservation was released and the second
  attempt was settled by E2M;
- routing-preference evidence: the same key lands on the standard upstream
  without a preference, on the economy upstream (settled at the economy
  price) under `price_first`, and back on the standard upstream after the
  preference is cleared, with the admin stats endpoint showing the economy
  channel's delivered sample.

It writes the bootstrap-retrieved plaintext test key to
`deployments/runtime/platform-forwarding/downstream.key`. It does not delete
volumes, migrations, or historical data.

## Platform Commerce

Shipped 2026-08-04 (execution plan: `platform-commerce-execution-plan.md`).
**The entire loop is closed by default.** With `E2M_ENABLE_PAYMENTS` unset,
every path below fails closed with `404 feature_disabled` before
authentication: `/api/v1/admin/payment/*`, `/api/v1/payment/webhooks/*`,
`/api/v1/owner/hybrid-supply/recharge-orders`, `/api/v1/admin/redeem-codes/*`,
and `/api/v1/redeem`. The console mirrors this with the
`VITE_E2M_ENABLE_PAYMENTS` build flag.

Active capabilities when the switch is on:

- self-serve top-up through Stripe and EasyPay: hosted checkout, signature
  verified callbacks with exactly-once wallet credit, and an expiry sweeper
  that queries the provider before expiring an order so a missed callback
  becomes a recovered credit;
- redeem codes as hash-only bearer instruments: balance and invitation types,
  batch generation (plaintext returned exactly once), admin list/disable/
  delete, a per-user failure limiter on redemption, and a `create-and-redeem`
  idempotent endpoint for external fulfillment systems;
- an invitation-code registration gate (admin toggle; the code is consumed
  atomically, and a concurrently stolen code disables the mistakenly created
  account fail-closed);
- base-price-table pricing (LiteLLM format, embedded bootstrap snapshot
  overridable via `E2M_PRICE_TABLE_PATH`) with a per-group sell-price
  multiplier; upstreams created without explicit prices materialize
  base x rate x multiplier, and unknown models fail closed;
- a customer-facing model market listing each group's best current sell price
  without exposing upstream identity, supplier cost, or capacity;
- per-user platform concurrency and RPM limits, platform wallet low-balance
  alerts, and platform key expiry;
- a unified settings module (`internal/settings`): commerce runtime values
  live in the shared `system_settings` store, the database value is
  authoritative and hot-applies without a restart, and `E2M_USD_TO_CNY_RATE`
  and `E2M_PLATFORM_BALANCE_THRESHOLD` only seed the first boot.

Console surfaces: the sidebar splits into an admin section (groups, upstream
accounts, customer instances, redeem codes, payment orders, users, system
settings) and a common section shared by every role (overview, platform
distribution, model market, recharge, redeem, connectors, pool health,
notifications, audits). System settings is one page with three tabs
(registration & security, commerce & pricing, payment channels).

`scripts/bootstrap-commerce.ps1` fixes the commerce-loop MVP scenario as a
repeatable acceptance run on the same stack (it materializes the gitignored
commerce override when absent): hot-applied pricing settings, redeem-code
lifecycle with duplicate rejection, `create-and-redeem` idempotency and
payload-conflict rejection, the customer model market, and metered forwarding
through both `/v1/chat/completions` and the `/v1/messages` bridge with settled
usage records. Hosted checkout needs provider credentials, so the script
probes the recharge-order route behind the payments gate and reports it
skipped when no provider is enabled.

## Native Platform Slice Status

The exact `/api/v1/platform/*` management contract and the E2M-hosted
`/v1/chat/completions` route are registered in e2m-core. The native slice uses
the same Core store and Vault as the console and keeps upstream credentials,
wallet journals, virtual keys, and usage records inside that boundary. The
PowerShell bootstrap and Compose template exercise this contract; a failed
local Docker run is an environment/integration failure, not a reason to
restore a separate Sub2API process.

The repository already contains mature Connector management, authentication,
audit, notification, store, and upstream-related building blocks. Provider-
specific protocol adapters and pool allocation remain outside this native
forwarding slice. Per-model base-table pricing and the payment/redeem top-up
loop shipped on 2026-08-04 and are described under Platform Commerce below;
they are gated by `E2M_ENABLE_PAYMENTS` and closed by default.

## Explicitly Out of Scope

The following must not be presented as current accepted work:

- percentage allocation across owner/economy/stable pools, and automatic
  ratio compilation (key-scoped routing preferences shipped 2026-08-08 and
  are no longer out of scope; the pool-ratio machinery remains so);
- a separately operated Sub2API runtime, UI, admin port, user store, wallet,
  downstream key, database, or Redis;
- Connector-managed platform upstreams or platform request proxying;
- duplicated E2M/third-party users, balances, keys, usage, or management calls;
- supplier dynamic Offers and dynamic acceptance ratios;
- supplier payable, withdrawal, or settlement;
- subscription plans and quota windows, or OAuth subscription upstream
  accounts (2026-08-04 decisions; the `/v1/messages` protocol bridge was
  un-deferred and shipped on 2026-08-05);
- MaiBot, cyber-risk review, or content-review services.

Historical database migrations and implementation packages are retained to
avoid a destructive migration. A historical type, table, binary, or helper is
not proof that a feature is active.

The historical `cmd/e2m-supply-gateway` source is retained temporarily for
non-destructive compatibility, but no active Compose template builds, starts,
or exposes it; E2M Core owns `/v1/chat/completions` in-process.

## Honest Delivery Guarantee

The platform objective is to maximize successful downstream delivery using
health-aware selection and bounded failure transfer across compatible E2M
upstreams. It cannot guarantee success when all upstreams are unavailable or
exhausted, a requested model is unsupported, the user has insufficient funds,
the request is invalid, or a provider rejects it. Product copy and SLAs must
state measurable availability and retry behavior, not unconditional success.

## Verification Commands

```powershell
cd app/e2m-core
go test ./...

cd ../e2m-agent
go test ./...

cd ../../web/console
npm test
npm run build

cd ../..
docker compose -f deployments/templates/compose/e2m-core-real-gateways.compose.yml config
.\scripts\bootstrap-real-gateways.ps1
```
