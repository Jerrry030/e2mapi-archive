# E2M Core Development

`app/e2m-core` is the self-owned Core API. It is intentionally independent from
Backstage, NocoBase, go-admin, or any other Portal framework.

For the factual project baseline, see [current-state.md](current-state.md).
This file focuses on how to run and extend Core.

## Responsibilities

- Health check, local authentication, users, roles, and registration security.
- Managed customer instances plus per-instance Connector enrollment, task
  leasing, runtime state, and lifecycle APIs.
- Adapter capabilities, gateway account list, schedulable toggle, account
  switch, and health snapshot APIs for the customer-owned pool.
- Operation audit model; notification routes with Feishu/QQ/webhook dispatch
  and a durable delivery outbox.
- Platform distribution: groups and model access, encrypted upstream accounts,
  downstream API keys, the unified wallet, metering, and the E2M-hosted
  OpenAI-compatible `/v1/chat/completions` data plane.
- Platform commerce behind `E2M_ENABLE_PAYMENTS`: payment channels/orders/
  callbacks, redeem codes and the invitation gate, base-table pricing with the
  group sell-price multiplier, and the customer model market.
- Unified runtime settings (`internal/settings`), hot-applied without a
  restart.

## Local Startup

From the repository root:

```powershell
go run ./app/e2m-core/cmd/e2m-core
```

By default Core uses the in-memory store. To use PostgreSQL:

```powershell
$env:E2M_CORE_STORE="postgres"
$env:E2M_CORE_DATABASE_URL="postgres://e2m:e2m@127.0.0.1:5432/e2m?sslmode=disable"
go run ./app/e2m-core/cmd/e2m-core
```

## Container Startup

Build and run the Core image:

```powershell
docker build -f app/e2m-core/Dockerfile -t e2m-core:dev .
docker run --rm -p 8080:8080 e2m-core:dev
```

The default address is:

```text
http://localhost:8080
```

Override it with:

```powershell
$env:E2M_CORE_ADDR=":8081"
go run ./app/e2m-core/cmd/e2m-core
```

## Current Endpoints

Do not maintain a hand-written endpoint list here — it has repeatedly drifted
from the code. The authoritative route table is the registration block in
`app/e2m-core/internal/httpapi/server.go` (`Routes()`), plus
`registerPlatformDistributionRoutes` in `platform_distribution.go` and the
data-plane route mounted by `cmd/e2m-core/main.go`.

For the product-level contract summary, see the route blocks in
[current-state.md](current-state.md). Note that many historical handlers exist
in `internal/httpapi` without being registered (billing, approvals, supply
offers, route plans, auto-switch, route strategies, upstream intelligence);
their presence in the source tree is not evidence that the capability is
reachable.

Example instance creation:

```powershell
Invoke-RestMethod `
  -Method Post `
  -Uri http://localhost:8080/api/v1/instances `
  -ContentType 'application/json' `
  -Headers @{ Authorization = "Bearer $token" } `
  -Body '{"user_id":1,"name":"Demo sub2api","kind":"sub2api"}'
```

## Current Persistence

Core supports both an in-memory store and a PostgreSQL store. The PostgreSQL
store uses pgx plus embedded golang-migrate migrations under
`internal/store/migrations`.

Current implementation note: sqlc is still a target choice, not yet used. The
PostgreSQL queries are handwritten in `internal/store/postgres.go`.

The local compose stack starts PostgreSQL and Core in postgres mode:

```powershell
docker compose -f deployments/templates/compose/e2m-core-dev.compose.yml up --build -d
```

## Boundaries

- Do not store plaintext tokens in the database.
- Store only `credential_ref` values.
- Do not implement business traffic proxying.
- Keep native gateway integration and credentials inside the outbound Connector.
- Do not put Core business ownership inside Portal plugins.
- Treat billing, audit, capability, health, task, binding, reconcile-run, and
  auto-switch-decision records as read-only facts; change their source settings
  or invoke explicit lifecycle actions instead of editing history.
