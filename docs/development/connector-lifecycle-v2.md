# Connector lifecycle protocol v2

Connector v2 executes gateway account lifecycle operations without exposing
gateway admin credentials or upstream secrets to Core.

## Ownership rules

- `platform_managed`: Core may create and update the remote account. Retirement
  first disables scheduling; only after a successful drain is a delete task
  queued for 30 minutes later.
- `owner_provided`: update-only. Create and delete are rejected independently by
  the publish engine, Core adapter, task validator, and Connector executor.
  Updates are credential-blind: Core does not require a credential binding,
  Core and Connector reject an owner update that carries a credential or proxy
  binding, Connector never resolves one, and zero-value fields mean "leave the
  native value unchanged". Scheduling remains a separate typed operation.
- `account_ownership` is immutable after channel creation. Orphaned published
  bindings keep their original ownership so deleting catalog data cannot weaken
  the rule.

## Scheduling ownership fence

A remote account already claimed by a RoutePlan can only receive schedulable
writes carrying that plan's current generation. Failed, pending, disabled, and
revoked bindings remain fenced because a lost response can hide a remote side
effect. The sole exception is a failed owner-provided metadata binding with
durable proof that Core rejected the operation before a Connector task was
created. Core records that proof with a stable marker; a narrowly matched legacy
Core fence error is accepted only to repair rows written before the marker was
introduced.

## Deferred delete

The delete is a normal persisted `connector_tasks` row with `available_at`, not
an in-process timer. It survives Core and Connector restarts. Its scheduling
fence uses the route-plan generation and sequence from the retirement apply. If
the plan is republished before the delay ends, the newer scheduling barrier
makes the old delete stale and Connector refuses it before calling the gateway.

## Local bindings

Lifecycle tasks carry only `credential_binding_id` and `proxy_binding_id`.
Installations write the actual values to the Connector loopback API:

```http
PUT /api/local/connector/bindings
X-E2M-Local-Token: <local-ui-token>
Origin: http://127.0.0.1:<local-ui-port>
Content-Type: application/json

{"bindings":{"binding-id":"secret-or-json","proxy-id":"https://proxy.example"}}
```

The endpoint is write-only and returns only the saved count. Values are kept in
the Connector private data directory and are resolved only while the native
gateway request is built. This binding path applies to platform-managed
accounts; owner-provided updates neither require nor read a binding.

Gateway-specific owner update behavior is deliberately narrow:

- Sub2API sends only explicitly non-zero name, type, priority, and group IDs.
- NewAPI uses its patch-like channel update and omits key, tag, proxy, and
  status, so the native row retains credentials and unmanaged fields.
- CPA uses only `PATCH /auth-files/fields`; it never downloads or uploads the
  owner's auth file.

## Idempotency and receipts

Create adopts a remote account with the channel's stable E2M marker instead of
creating a duplicate. Every mutation uses the Connector task idempotency key,
lease nonce, typed completion result, scheduling generation fence, reconcile
run, and operation audit. Delete treats an already absent resource as success.
