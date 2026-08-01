# ADR 0001: Re-evaluate go-admin as the Control Plane Shell

Date: 2026-07-01
Updated: 2026-07-02

## Status

Superseded by long-term platform-base evaluation.

## Context

E2M is currently an empty shell. The original MVP-0 goal was sub2api托管可视化, while preserving mature backend basics such as login, RBAC, menus, tenant-friendly organization structure, audit logs, CRUD generation, and admin layout.

The product direction has since shifted from "fast MVP shell" to "long-term expandable operations platform". It is acceptable to choose a heavier framework at the beginning if it avoids replacing the control-plane skeleton later.

## Decision

Do not treat go-admin-team/go-admin as the final control-plane platform by default.

go-admin remains a valid Go-native fallback shell:

- Backend: `app/control-plane/server`
- Frontend: `app/control-plane/web`

If it is used, the template should still be copied into a subdirectory instead of replacing the repository root.

For the long-term control-plane Portal, prioritize evaluating:

- Backstage, as the plugin-oriented operations/developer portal.
- NocoBase, as the business configuration, workflow, and admin-console platform.
- A self-owned E2M Core API, which keeps user, instance, Connector, Adapter, policy, audit, and billing logic outside any low-code/admin shell.

## Why

- go-admin is MIT licensed.
- It already provides Gin, GORM, Casbin RBAC, JWT auth, Swagger, operation logs, menus, and code generation.
- Keeping it under `app/control-plane` preserves E2M repository ownership and keeps future rollback simple.
- However, go-admin is primarily a traditional CRUD/admin scaffold. Its default UI stack and platform plugin story are weaker than Backstage/NocoBase for a long-lived operations platform.
- E2M's long-term value is not the admin shell; it is the Connector, Adapter, policy engine, workflow integration, audit model, and managed operations product.

## Alternatives Considered

- Direct fork/copy at repository root: faster, but it would bury E2M docs and future Connector/deployment code under a third-party layout.
- Git upstream merge: useful after product direction stabilizes, but too much ceremony for the first shell.
- gin-vue-admin: active and feature-rich, but authorization/commercial-use review and migration cost need deeper checking before choosing it.
- Backstage: heavier and requires TypeScript/React, but stronger as a plugin-oriented operations portal.
- NocoBase: heavier than a scaffold, but stronger for business data modeling, forms, approvals, workflows, and configuration consoles.
- Appsmith/Budibase: excellent for internal tools, but not recommended as the primary E2M product control plane.
- Directus/Strapi/Payload: useful data/CMS backends, but not a natural fit for operations hosting and Connector orchestration.

## Consequences

- The previous go-admin plan is no longer the only accepted direction.
- If go-admin code already exists locally, keep it isolated and disposable until the long-term Portal decision is finalized.
- E2M Core API should be designed as the stable ownership boundary regardless of Portal choice.
- Portal choice should not own the Connector protocol, Adapter model, workflow state, audit log, or billing events.
- If go-admin is retained, accept the Vue2/Element UI and Node 18 constraints as short-term tradeoffs only.
