# Control Plane Development

The current MVP uses the product-owned React console in `web/console`, served by
`app/e2m-core`. Authenticated gateway administration follows one path:

```text
Portal / Console
-> E2M Core API
-> Connector task queue
-> per-instance outbound Connector
-> local gateway adapter
```

Core stores instance metadata, task intent, audit records, and normalized
results. The Connector's enrollment is bound to its user and instance before
installation. Gateway coordinates, authentication mode, and management
credentials stay in that Connector's local data volume.

## Current Direction

- The active console is React + TypeScript + Vite + Ant Design Pro.
- E2M Core API is the stable product-owned backend.
- Every gateway instance has its own Connector, enrollment file, local UI port,
  and persistent volume.
- Connector liveness and gateway readiness are carried in authenticated task
  lease runtime state. There is no standalone observation process.
- Backstage, NocoBase, and go-admin remain outside the active product path.

See:

- `docs/development/current-state.md`
- `docs/development/e2m-core.md`
- `docs/development/e2m-agent.md`
- `docs/development/platform-boundaries.md`

## Repository Layout

```text
app/e2m-core                  product-owned Core API
app/e2m-agent                 customer-side per-instance Connector
packages/e2m-contracts        shared Go domain/API contracts
web/console                   React operations console
deployments/templates         Compose and deployment templates
```

## Local Startup

```powershell
docker compose -f deployments/templates/compose/e2m-core-dev.compose.yml up --build -d
```

Create a gateway instance in the console, then use its generated Connector
install guide. The dev stack starts PostgreSQL, Redis, Core, and the three mock
gateways; Connectors are installed per instance rather than sharing a global
development credential.

## MVP Boundary

The current side-car control path covers instance inventory, gateway account
operations, health and automatic switching, audit, notification, approval,
billing, authentication, and per-instance Connector enrollment and lifecycle.

External secret backends, mTLS/task signing, deployment hosting, and a unified
traffic gateway remain outside the current scope.
