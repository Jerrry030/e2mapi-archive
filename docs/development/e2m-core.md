# E2M Core Development

`app/e2m-core` is the self-owned Core API. It is intentionally independent from
Backstage, NocoBase, go-admin, or any other Portal framework.

For the factual project baseline, see [current-state.md](current-state.md).
This file focuses on how to run and extend Core.

## Responsibilities

- Health check.
- User, instance, and supply-offer APIs.
- Per-instance Connector enrollment, task leasing, runtime state, and lifecycle APIs.
- Adapter capabilities API.
- Gateway account list, schedulable toggle, and account switch APIs.
- Health snapshot API.
- Operation audit model.
- Notification route model and Feishu/QQ notification dispatch.
- Approval request API for L2 actions.
- Billing statement API.

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

```text
GET    /healthz
GET    /api/v1/auth/public-config
POST   /api/v1/auth/login
POST   /api/v1/auth/register
POST   /api/v1/auth/logout
GET    /api/v1/auth/me
GET    /api/v1/system/auth-settings
PUT    /api/v1/system/auth-settings
GET    /api/v1/users
POST   /api/v1/users
GET    /api/v1/users/{id}
PUT    /api/v1/users/{id}
POST   /api/v1/users/{id}/reset-password
GET    /api/v1/instances
POST   /api/v1/instances
PUT    /api/v1/instances/{id}
POST   /api/v1/instances/{id}/connector-install
GET    /api/v1/health-snapshots
GET    /api/v1/instances/{id}/monitor-policy
PUT    /api/v1/instances/{id}/monitor-policy
POST   /api/v1/instances/{id}/health-check
GET    /api/v1/billing/statement
GET    /api/v1/approvals
POST   /api/v1/approvals
POST   /api/v1/approvals/{id}/approve
POST   /api/v1/approvals/{id}/reject
GET    /api/v1/instances/{id}/accounts
POST   /api/v1/instances/{id}/accounts/switch
POST   /api/v1/instances/{id}/accounts/{accountId}/schedulable
GET    /api/v1/supply-offers
POST   /api/v1/supply-offers
PUT    /api/v1/supply-offers/{id}
POST   /api/v1/supply-offers/{id}/revoke
POST   /api/v1/supply-offers/{id}/allocate
POST   /api/v1/supply-ledger/{id}/revoke
GET    /api/v1/supply-ledger
GET    /api/v1/connectors
POST   /api/v1/connectors/enrollments
POST   /api/v1/connectors/enroll
POST   /api/v1/connectors/{id}/rotate-token
POST   /api/v1/connectors/{id}/revoke
PUT    /api/v1/instances/{id}/connector
GET    /api/v1/connector-tasks
POST   /api/v1/connectors/tasks/lease
POST   /api/v1/connectors/tasks/{id}/complete
GET    /api/v1/adapter-capabilities
GET    /api/v1/audits
GET    /api/v1/events/stream
GET    /api/v1/notification-routes
POST   /api/v1/notification-routes
PUT    /api/v1/notification-routes/{id}
DELETE /api/v1/notification-routes/{id}
GET    /api/v1/secrets
POST   /api/v1/secrets
DELETE /api/v1/secrets
GET    /api/v1/upstream-pools
POST   /api/v1/upstream-pools
PUT    /api/v1/upstream-pools/{id}
GET    /api/v1/upstream-channels
POST   /api/v1/upstream-channels
PUT    /api/v1/upstream-channels/{id}
GET    /api/v1/route-plans
POST   /api/v1/route-plans
PUT    /api/v1/route-plans/{id}
POST   /api/v1/route-plans/{id}/reconcile
POST   /api/v1/route-plans/{id}/rollback
GET    /api/v1/published-bindings
GET    /api/v1/reconcile-runs
GET    /api/v1/channel-health-snapshots
GET    /api/v1/auto-switch-decisions
GET    /api/v1/auto-switch-decisions/{id}
GET    /api/v1/route-plans/{id}/auto-switch-summary
POST   /api/v1/route-plans/{id}/auto-switch/evaluate
POST   /api/v1/auto-switch-decisions/{id}/observe
GET    /api/v1/route-strategies
POST   /api/v1/route-strategies
DELETE /api/v1/route-strategies/{id}
```

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
