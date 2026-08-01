# Platform Boundaries

Updated: 2026-08-01

E2M is the sole customer-facing product and the sole owner of platform
distribution data. Connector remains a narrowly scoped customer-owned-pool
operations component. Proven Sub2API management and forwarding patterns may be
absorbed into E2M-native code, but Sub2API is not a deployed subsystem.

## Responsibility Matrix

| Capability | E2M Core | Customer Connector | Separate Sub2API |
|---|---:|---:|---:|
| Product identity, users, roles, console | Owns | No | Must not exist |
| Connector enrollment and task audit | Owns | Executes | Must not exist |
| Customer gateway URL/admin credential | Never receives | Owns locally | Must not exist |
| Customer-owned account health/list | Orchestrates and displays | Reads locally | Must not exist |
| Customer-owned schedulable/switch actions | Queues typed task | Applies locally | Must not exist |
| Customer-owned request traffic | Never handles | Never proxies | Must not exist |
| Platform upstream accounts/credentials | Owns and encrypts | Never receives | Must not exist |
| Platform groups/model access | Owns | No | Must not exist |
| Downstream platform API keys | Issues and validates | No | Must not exist |
| Downstream unified wallet | Owns | No | Must not exist |
| Platform metering and usage | Owns | No | Must not exist |
| Platform scheduling/retry/forwarding | Owns | No | Must not exist |
| Three-pool ratios | Not supported in V1 | Not supported | Must not exist |

“E2M Core owns” describes the E2M product and trust boundary. Hot-path code may
be internally modularized for isolation and scaling, but it must use E2M
contracts and E2M state and must not expose a second product login, console,
admin API, key namespace, balance, hostname, or operational workflow.

## Trust and Failure Boundaries

- A customer's gateway management credential stays in that Connector's
  private volume. It is not sent to E2M Core or any platform upstream.
- Platform upstream credentials stay encrypted in E2M server-side storage.
  They are never sent to a customer Connector or downstream client.
- Customer-owned traffic remains between the customer's gateway and model
  provider. An E2M platform-data-plane outage does not add a dependency to it.
- Platform traffic depends on E2M Core and the configured E2M upstream group,
  but never on a Connector or separately operated Sub2API instance.
- E2M user identity, wallet, API keys, group membership, and usage form one
  authoritative transaction boundary. There is no synchronization or mirror.

## Connector Contract

Connector accepts only five owner-pool management tasks:

```text
gateway.health
gateway.accounts.list
gateway.schedulable.set
gateway.switch
gateway.scheduling.barrier
```

All other historical task kinds fail closed as unsupported. Connector does not
accept platform-account provisioning, downstream-key delivery, traffic-share
compilation, platform metering, or wallet mutations.

The local API provides configuration, gateway connectivity testing, Core
connectivity testing, and bounded diagnostics. It does not expose platform
resource administration.

## Platform Distribution Contract

All provisioning and use flow through E2M:

```text
E2M login
  -> E2M creates a platform group
  -> E2M records an encrypted upstream credential
  -> E2M adjusts the downstream wallet
  -> E2M creates a downstream key and stores its value encrypted in the Vault
  -> authorized users can show or quick-copy that key again through E2M
  -> downstream calls E2M /v1/chat/completions
  -> E2M meters, forwards, retries if eligible, and settles
  -> operator/downstream reads usage from E2M
```

The initial route contract is:

```text
POST /api/v1/platform/groups
POST /api/v1/platform/upstreams
POST /api/v1/platform/keys
GET  /api/v1/platform/keys/{id}/value
POST /api/v1/platform/wallet-adjustments
GET  /api/v1/platform/usage
POST /v1/chat/completions
```

Platform groups are the V1 product/access boundary. A downstream key belongs
to one E2M user and one group; it never reveals an upstream credential. Normal
OpenAI-compatible response semantics, including SSE, are preserved. Failure
transfer is limited to retryable failures and healthy, compatible accounts in
the same group.

Ordinary key list and detail responses contain only a prefix and non-secret
metadata; they never return the full key, Vault reference, or validation hash.
The full value is available only through the E2M-native
`GET /api/v1/platform/keys/{id}/value` endpoint (and its
`/api/v1/platform/api-keys/{id}/value` compatibility alias). E2M decrypts the
value from its Vault only after ownership, enabled-user, and current
`client`/`admin` role checks. The response disables caching with
`Cache-Control: no-store, private`, and every successful read writes a
`platform_key.view` audit event. The Console may therefore offer persistent
show/hide and quick-copy controls without adopting Sub2API's plaintext-key
list-response design.

The data path revalidates that the key owner is still enabled and still has a
`client` or `admin` role, so removing customer access invalidates already
issued keys without waiting for key rotation. Customer catalog responses omit
provider, region, labels, delivery mode, safety stock, and all upstream/channel
metadata. Operators retain the full group view through the same E2M API under
administrator authorization.

The current V1 may start with one group. Economy/stable group separation is a
subsequent product configuration. Percentage allocation across owner,
economy, and stable resources is explicitly deferred.

## Sub2API Learning Policy

E2M may learn from or port Sub2API's domain patterns for groups, upstream
accounts, API-key safety, balance accounting, concurrency, metering, retry,
and failure transfer. The result must be an E2M-native capability:

- E2M API and UI only;
- E2M authentication and authorization;
- E2M identifiers and authoritative storage;
- E2M secret encryption and audit rules;
- E2M request hostname and error contract;
- no direct management calls from scripts, browsers, or customers to Sub2API;
- no Sub2API container or externally reachable management port in the active
  deployment topology.

Any direct source reuse must receive a separate LGPL-3.0 architecture and
distribution review. Reusing ideas and contracts is not permission to copy
code without tracking license obligations.

## Source and Data Compatibility

The repository contains historical migrations, DTOs, stores, tests, and helper
packages for supply, payment, Hybrid Supply, key delivery, intelligence, and
quality work. They remain to avoid mixing this product correction with a
destructive migration.

A capability is active only when it is reachable through the current E2M API,
current E2M console, Connector's five-task contract, or the minimal E2M Compose
stack. New work must not reconnect dormant percentage-routing or duplicate-key
flows without an explicit later decision.

## Current Non-goals

- no percentage or weighted three-pool allocator;
- no duplicate identity, key, balance, or usage lifecycle;
- no Connector in the platform request path;
- no separate Sub2API data plane or management plane;
- no payment or supplier-finance workflow in this vertical slice;
- no upstream content/cyber review or MaiBot runtime;
- no promise that error transfer can succeed without a healthy compatible
  upstream.

For the verification baseline, see [current-state.md](current-state.md).
