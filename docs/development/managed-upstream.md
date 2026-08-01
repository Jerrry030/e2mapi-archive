# Managed Upstream Workflow

> Connector protocol v2 executes typed create/update/delete tasks with local
> binding resolution. Platform-managed retirement drains immediately and
> enqueues a generation-fenced delete after 30 minutes; owner-provided accounts
> are update-only.

This document describes the platform-managed upstream layer: how E2M owns
upstream pools, publishes a routing plan onto an owner's gateway instance, and
reconciles that desired state with gradual rollout, reversible rollback, and
notifications.

The product stance is deliberate: **the platform curates a shared upstream
directory, allocates dedicated API keys, and switches quality across different
upstream sources.** The first version has no subscribe/unsubscribe workflow: all
active platform sources are eligible for every owner. It also has no billing or
finite stock model; upstream key capacity is treated as unlimited at the product
boundary.

Allocation and scheduling are separate. An owner receives at most one key from
each stable upstream source. Quality scheduling can enable or disable only those
already-allocated keys, and a replacement must have a different `source_id`
from the failing key. A quality event never assigns a new key, changes key
ownership, or creates a warm spare.

The owner's own-key surface may reveal that allocated key through the separate
time-limited reveal flow and is masked by default. The owner health endpoint
never carries key material, even for the owner who holds the allocation.

Delivery and gateway publication use a two-stage acknowledgement. A random
challenge proves only that Core's delivery Key matches the API Key extracted
from the Connector-local binding for that gateway kind. A separate
`channel_id + instance_id + key_version + connector_id` record becomes
`deployed` only after the typed create/update receives a successful gateway
write response. Reveal re-runs the local proof and requires the current version
to have a deployed acknowledgement for every active owner instance. Offline or
stale Connectors, mismatches, failed writes, and old versions fail closed. This
is a write acknowledgement, not a claim that the remote gateway supports
reading the secret back.

## Data Model

Contracts live in `packages/e2m-contracts/upstream_pool.go` and
`adapter_gateway.go`.

- `UpstreamPool` — a platform-curated shared directory. Status is `active` /
  `maintenance` / `retired`. The owner health surface does not expose its
  internal channels.
- `UpstreamChannel` — one concrete allocatable API key in a pool. `source_id`
  identifies the stable upstream source independently from model provider;
  several sources can serve the same provider. Core stores opaque
  `credential_binding_id` / `proxy_binding_id` references, never key plaintext.
  Status is `active` / `maintenance` / `retired`.
- `RoutePlan` — binds one pool to one owner instance. This is the desired-state
  declaration the engine reconciles onto the gateway. Status is `draft` /
  `published` / `suspended`. Carries `max_channels` (publish cap) and the
  rollout fields below.
- `PublishedBinding` — the paper trail linking a plan's channel to the actual
  gateway account (`remote_id`). State is `pending` / `active` / `disabled` /
  `failed` / `revoked`.
- `upstream_channel_allocations` — the permanent owner claim created atomically
  with the first Binding. A key belongs to only one owner, and an owner can hold
  only one key per `source_id`. The same owner may use that claim in another
  plan/instance; disabling, revoking, or isolating it does not release it to a
  different owner, so it can be restored only for its original owner.

Platform-side channel status is distinct from gateway health: the platform
decides whether a channel is *offered*; the health checker decides whether a
published channel is currently *schedulable*.

`source_id` is independent from `provider`: one model vendor may be reachable
through several separately scheduled upstream sources.

During publish, reconcile first reuses the user's existing key for a source. If
none exists, it selects the highest-priority unallocated key and skips keys
owned by other users. The allocation claim and binding upsert share one store
transaction and occur before any gateway mutation.

## Lifecycle Ownership

The target lifecycle policy depends on who supplied the upstream account:

- For a platform-provided account, E2M owns remote create, update, and delete.
  Deletion is a two-step operation: disable first, then delete after a 30-minute
  drain window.
- For an owner-provided account already present in the gateway, E2M may update
  it but must not create or delete it.

Connector protocol v2 enforces these ownership rules while executing typed
account lifecycle tasks. A platform-managed account may be created, updated,
and deprovisioned. An owner-provided account is update-only; any apply that
would create or delete one is rejected during complete-diff preflight before
the gateway or permanent allocation state is mutated. A Connector that does
not advertise a required lifecycle capability is rejected by the same
preflight rather than receiving a partial apply.

## Rollout Semantics

`RoutePlan.rollout` controls how fast a reconcile brings newly-activated
channels into scheduling, so an upstream switch ships as an observable change
rather than an all-at-once flip.

- `immediate` — enable every desired-active channel in one apply.
- `canary` — enable only a first wave of `rollout_canary_count` newly-activated
  channels per apply (default 1); observe, then apply again to widen.
- `batched` — enable at most `rollout_batch_size` newly-activated channels per
  apply (default 1), so a large pool rolls in over several applies.

Channels that are desired-active but held back by the rollout gate surface as a
`hold` action, so a dry-run makes the staging explicit before anything changes.

This plan-local rollout is different from quality ejection cohorts. A soft
source-quality event selects downstream plans by a stable `25% -> 50% -> 75%`
cohort across consecutive bad windows and never flips every owner at once. Only
selected plans then run their own reconcile/rollout. Instance-scoped auth or
balance failures bypass the cohort for the affected Binding only.

## Reconcile Actions

The engine (`app/e2m-core/internal/publish/engine.go`) diffs desired plan vs
actual gateway state and emits one action per channel:

- `create` — platform-managed channel in the plan but absent on the gateway;
  Connector v2 resolves its local credential binding and creates or adopts the
  stable E2M remote account idempotently.
- `enable` — present but not scheduling; bring it into rotation.
- `disable` — present and scheduling, but the plan wants it out.
- `revoke` — on the gateway but no longer in the plan (drained, not deleted).
- `update` — push a changed account specification. This is the only lifecycle
  write allowed for an owner-provided account.
- `deprovision` — remove a retired/orphaned platform-managed account by
  disabling it immediately and queuing a generation-fenced delete 30 minutes
  later.
- `hold` — desired-active but held back by the rollout policy.
- `noop` — already in the desired state.

`Plan` (dry-run) returns these actions without mutating gateway or binding
state. `Apply` first preflights the complete diff against the target
Connector's capabilities and the channel's immutable ownership. A forbidden or
unsupported lifecycle action rejects the complete apply; it does not execute a
supported enable/disable action from the same diff first. Successful applies
record bindings, and a draft becomes published only after every required
immediate write succeeds. A deferred delete is successful once its durable task
has been queued.

Core does not resolve gateway management credentials. Gateway URL,
authentication, and management credentials remain in the Connector's local
private configuration. Lifecycle tasks carry only opaque
`credential_binding_id` / `proxy_binding_id` values. Their actual values are
written through the Connector's loopback-only, write-only binding API, stored
in its private data directory with `0600` permissions, and resolved only while
the native gateway request is built.

## Rollback vs Deprovision

These are intentionally different so a managed switch is reversible:

- **Rollback** (`POST /route-plans/{id}/rollback`) suspends the plan and
  immediately reconciles. A suspended plan `revoke`s every published channel out
  of scheduling on the gateway. It does **not** delete the remote accounts, so
  re-publishing restores the previous state.
- **Deprovision** is reserved for retired or orphaned platform-provided
  accounts. Connector v2 disables first and executes the durable delete only
  after the 30-minute drain window. Owner-provided gateway accounts are never
  deprovisioned by E2M.

Quality isolation is neither rollback nor deprovision. It disables one
`PublishedBinding`, leaves the permanent key allocation intact, and records a
durable `(plan_id, channel_id)` circuit. Re-entry requires active probes; it is
not caused by a new allocation or by the passage of time alone.

Automatic scheduling mutations use a database-clock `applying` lease and a
monotonic fencing generation. Every reconcile item and circuit/final-state
write renews the current generation; stale workers cannot continue or run an
opposite compensation. Connector mutation tasks carry the same decision scope
and generation, so a durable task arriving after takeover is rejected before
it reaches the gateway. Failed work keeps its lease until natural expiration,
leaving enough time for already-leased Connector tasks to finish or expire.

Expired decisions are repaired in a runner pass independent of normal
published-plan evaluation. A suspended plan admits only a non-empty all-false
scheduling drain, and its status is rechecked before every item so a concurrent
republish cannot be drained by an old repair attempt.

## Quality Scheduling

The quality loop starts each downstream/key observation scope at 100 and only
deducts upstream-attributable error (up to 55), p95 first-token latency (up to
25), and p95 total duration (up to 20). The default ejection threshold is
`<=60`. Client errors and request cancellations are stored as facts but do not
affect upstream quality. Source-level aggregation is soft evidence only; the
durable circuit remains downstream-scoped.

Timeout, rate-limit, server, network, and source/provider capacity signals are
soft quality evidence. Once the score reaches the threshold, they follow the
stable downstream cohort described above; there is no provider-global kill
switch. Only explicit Binding-scoped authentication or balance failures eject
that Binding immediately. When a selected downstream has no healthy allocated
key from a different source, it disables the failing Binding immediately and
fails closed.

An isolated Binding first waits for a five-minute cooldown, with exponential
backoff capped at one hour after failed probes. `open` and `half_open` circuits
both block normal traffic. Three consecutive complete active probes scoring at
least 85 mark the source ready for guarded recovery; they do not restore every
downstream at once. Stable downstream cohorts return through
`10% -> 25% -> 50% -> 100%`. Each stage waits at least five minutes and requires
real passive traffic from members admitted after the stage began. Insufficient
evidence holds the current stage, and a score regression immediately
re-isolates the affected Binding.

Sub2API advertises automatic recovery only when the Connector-local probe
switch is explicitly enabled and its persistent hourly budget and minimum
interval are valid. Its account-scoped SSE probe records structured errors,
true first-token latency, and total duration for the configured capability and
endpoint path. NewAPI and CPA do not yet provide equally complete recovery
evidence, so they explicitly report manual recovery. Unsupported probes keep a
Binding isolated rather than manufacturing a healthy signal.

## Notifications

Reconcile and rollback dispatch a `upstream.reconcile` event through the user-scoped
notification router (`app/e2m-core/internal/notify`), which supports Feishu, QQ,
and generic per-route webhooks. The console live feed
(`web/console/src/pages/PoolHealth.tsx`) subscribes to the same event over SSE
and labels it "上游发布"; the reconcile handler also emits an SSE frame so the
console updates in real time.

## API Surface

Managed delivery catalog, plan, execution, and detailed history routes are
platform-admin only:

```text
GET    /api/v1/upstream-pools
POST   /api/v1/upstream-pools
PUT    /api/v1/upstream-pools/{id}
GET    /api/v1/upstream-channels
POST   /api/v1/upstream-channels
PUT    /api/v1/upstream-channels/{id}
GET    /api/v1/upstream-key-deliveries
PUT    /api/v1/upstream-channels/{id}/delivery-key
GET    /api/v1/route-plans
POST   /api/v1/route-plans
PUT    /api/v1/route-plans/{id}
POST   /api/v1/route-plans/{id}/reconcile   # ?dry_run=true (default) | false
POST   /api/v1/route-plans/{id}/rollback
GET    /api/v1/published-bindings
GET    /api/v1/operations-center
```

`reconcile` defaults to dry-run; pass `?dry_run=false` to apply. Both reconcile
and rollback are audited as L2 actions.

Owners have a separate redacted health surface and a controlled assigned-key
delivery surface:

```text
GET    /api/v1/owner/pool-health
GET    /api/v1/owner/assigned-keys
POST   /api/v1/owner/assigned-keys/{id}/reveal
```

The health route returns published/schedulable/isolated capacity, factual
five-minute SLA, anonymous incidents and recovery progress, and redacted switch
outcomes. It does not expose pool, plan, instance, channel, source,
remote-account, model, or key identifiers. The factual SLA includes all actual
requests, including client errors and cancellations; this intentionally differs
from upstream quality scoring, which excludes downstream responsibility.

The assigned-key routes are a separate controlled delivery surface. Lists are
masked by default. Plaintext reveal requires the current password, is limited
to the permanently assigned owner, uses a 60-second display window and
`Cache-Control: no-store, private`, and succeeds only after its audit has been
persisted. Connector credential bindings remain independent and are never
decoded by Core for this flow.

The RoutePlan, PublishedBinding, reconcile-run, dry-run/apply, and rollback
payloads remain private to platform administrators because they contain these
internal identifiers and channel-level actions.

## Console

`web/console/src/pages/Upstream.tsx` (menu: 上游托管) is the administrator
surface. It exposes plans, keys/channels, pools, dry-run/apply/rollback, quality
deductions, Binding state, circuit state, recovery probes, and decision history.
These states must be shown independently: a low score is a recommendation, a
disabled Binding is gateway fact, and only an `open` / `half_open` circuit means
an actual quality isolation.

Owners use `/pool-health`, which shows only capacity, factual five-minute SLA,
anonymous incidents/recovery and redacted switch results.

## Gateway Adapters

The production Connector adapters for Sub2API, NewAPI, and CPA expose account
listing, scheduling changes, and protocol-v2 create/update/delete mappings.
They resolve opaque lifecycle bindings only from the Connector's write-only
local store. Platform ownership and delayed-delete fences are enforced above
and below the adapter boundary.

Active recovery capability is intentionally narrower than lifecycle support.
Only Sub2API currently advertises automatic account-scoped quality probes, and
only after explicit local opt-in with budget and interval limits. NewAPI and CPA
remain manual-recovery adapters until they can return structured errors, true
TTFT, and total duration from a safe account-scoped probe.
