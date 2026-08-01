# Current Implementation State

Updated: 2026-08-01

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
- OpenAI-compatible `/v1/chat/completions`, metering, accounting, scheduling,
  and retry/failure transfer among compatible upstreams;
- platform usage records and summaries exposed through E2M APIs.

The first vertical-slice management contract is:

```text
POST /api/v1/platform/groups
POST /api/v1/platform/upstreams
POST /api/v1/platform/keys
GET  /api/v1/platform/keys/{id}/value
POST /api/v1/platform/wallet-adjustments
GET  /api/v1/platform/usage
POST /v1/chat/completions
```

These are E2M contracts. A client must never call a hidden third-party
management API as part of provisioning or acceptance.

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
3. select a healthy compatible upstream in the E2M group;
4. forward the request while preserving compatible request and response
   semantics, including SSE;
5. retry or transfer only for transport errors, 408, 429, and 5xx; forward
   deterministic 4xx rejections without broadcasting the request to another
   channel;
6. atomically record usage and apply the final charge/refund outcome in E2M.

The downstream sees only E2M domains, E2M error contracts, E2M keys, and E2M
usage. Real upstream credentials are encrypted and remain server-side.
Customer group reads use a product-catalog DTO containing only group ID, name,
description, economy/stable class, models, and status. Provider identity,
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

`scripts/bootstrap-real-gateways.ps1` starts the stack with
`--remove-orphans`, logs into E2M, and calls only E2M APIs to create:

- one local platform group;
- two OpenAI-compatible mock platform upstreams in one group (first 503,
  second succeeds);
- one idempotent local wallet adjustment;
- one E2M downstream API key;
- one non-stream and one stream request through E2M;
- usage evidence proving the failed reservation was released and the second
  attempt was settled by E2M.

It writes the bootstrap-retrieved plaintext test key to
`deployments/runtime/platform-forwarding/downstream.key`. It does not delete
volumes, migrations, or historical data.

## Native Platform Slice Status

The exact `/api/v1/platform/*` management contract and the E2M-hosted
`/v1/chat/completions` route are registered in e2m-core. The native slice uses
the same Core store and Vault as the console and keeps upstream credentials,
wallet journals, virtual keys, and usage records inside that boundary. The
PowerShell bootstrap and Compose template exercise this contract; a failed
local Docker run is an environment/integration failure, not a reason to
restore a separate Sub2API process.

The repository already contains mature Connector management, authentication,
audit, notification, store, and upstream-related building blocks. Production
hardening (capacity policy, richer model catalogs, payment, and later pool
allocation) remains outside this first native forwarding slice.

## Explicitly Out of Scope

The following must not be presented as current accepted work:

- percentage allocation across owner/economy/stable pools;
- routing-preference overlays or automatic ratio compilation;
- a separately operated Sub2API runtime, UI, admin port, user store, wallet,
  downstream key, database, or Redis;
- Connector-managed platform upstreams or platform request proxying;
- duplicated E2M/third-party users, balances, keys, usage, or management calls;
- supplier dynamic Offers and dynamic acceptance ratios;
- online payment callbacks, supplier payable, withdrawal, or settlement;
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
