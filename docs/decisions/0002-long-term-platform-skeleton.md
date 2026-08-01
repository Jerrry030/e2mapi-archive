# ADR 0002: Establish the Long-term Platform Skeleton

Date: 2026-07-02

## Status

Accepted

## Context

E2M should not start by embedding its business core inside go-admin, Backstage, NocoBase, or any other Portal framework. The long-term direction is a platform composed of:

- Portal.
- E2M Core API.
- Per-instance Connector.
- Workflow.
- Notification.
- Deployment.

The first engineering skeleton should prove boundaries rather than deliver a full UI.

## Decision

Create the first-stage repository skeleton with these ownership boundaries:

- `app/e2m-core`: self-owned Go Core API.
- `app/e2m-agent`: self-owned Go Connector executable.
- `packages/e2m-contracts`: shared API and domain contracts.
- `packages/backstage-plugin-e2m`: placeholder for a future Backstage plugin.
- `packages/nocobase-plugin-e2m`: placeholder for a future NocoBase plugin.
- `deployments/templates`: placeholder for Dokploy and Compose templates.

The Core API owns users, instances, Connector enrollments/tasks/runtime state, Adapter capabilities, policy state, audit events, notification routes, and future metering events.

The Portal must call the Core API. It must not own Connector protocol, Adapter state, workflow state, audit state, or billing events.

## Why

- Keeps E2M business logic independent from Portal and low-code framework choices.
- Allows Backstage and NocoBase to be evaluated without delaying Core/Connector work.
- Keeps Connector and Adapter protocol stable across future UI changes.
- Makes external components such as Temporal, Windmill, Novu, Infisical, Keycloak, and Dokploy integration boundaries explicit.

## Consequences

- The first UI will be minimal or absent until Portal selection is finalized.
- The first data store is in-memory, only to make API contracts and tests executable.
- No third-party framework source code is copied at this stage.
- The next implementation step is persistence and authentication for E2M Core, not a full admin UI.
