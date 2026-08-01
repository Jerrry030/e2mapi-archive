# ADR 0003: Reposition E2M as a Side-car Plugin, Not a Full Managed Control Plane

Date: 2026-07-02

## Status

Accepted. Supersedes the product framing in ADR 0002 and in
`docs/architecture/e2mapi-platform-architecture.md` (the 1091-line blueprint),
while keeping their contract discipline (RiskLevel L0-L3, OperationAudit,
credential_ref, outbound-only client) intact.

## Context

The original blueprint framed E2M as a heavy managed control plane that would
"fully take over" a site owner's operations: deploy their gateways, own their
runtime, and — in one design branch — sit in the data path as a central supply
gateway that forwards requests.

External research (13-agent workflow, 2026-07-02) and three adversarial reviews
(business / engineering / security) surfaced three structural risks that all
trace back to that "full takeover + in-data-path" framing:

1. Putting the platform in the data path turns it into a de-facto relay
   operator: bandwidth cost, plaintext request visibility, single point of
   failure, and China-mainland filing/compliance obligations.
2. "Full takeover" demands very high trust — the owner hands their production
   system to us — with blurred responsibility boundaries.
3. Owning deployment plus an inbound command queue makes the self-built surface
   large and the 6-week MVP unrealistic.

The product owner then narrowed the scope on two points:

- E2M does **not** forward requests. Request rewriting, bandwidth, and
  plaintext-traffic concerns are therefore out of scope entirely.
- E2M should behave as a **side-car plugin / assistant** next to the owner's
  scheduling system (sub2api / new-api / CPA), which remains the primary actor.

## Decision

Reposition E2M as a **side-car plugin** with a narrow, high-value job:

1. **One-click account provisioning** — call the gateway's own official admin
   API to batch-create/configure upstream accounts on the owner's behalf, so the
   owner never hand-fills accounts.
2. **Continuous health-checking** — the E2M center periodically polls each
   managed instance's account status; when an account degrades (e.g. repeated
   429s), the center decides and acts by risk tier.
3. **Notify / act by risk tier** — low-risk actions (disable a dead account,
   swap to a backup) run automatically and inform the owner afterward;
   high-risk actions (bulk disable, config/version change) require Feishu-card
   human approval; the rest is advisory push only.

E2M **never** sits in the request data path. Token traffic always flows from the
owner's instance directly to the upstream (via the account's own proxy). E2M
touches the control path only: provision credentials, poll status, act, notify.

Both "pool links" collapse to **configuration delivery** (write config through
the gateway's admin API); neither routes live traffic through E2M.

**Instance access uses a per-instance outbound Connector.** Enrollment is bound
to the owning user and gateway instance before installation. The Connector runs
beside that gateway, keeps its address and management credential in a dedicated
local volume, polls Core for tasks, and opens no inbound management port. Core
never receives the gateway management credential.

The adapter layer depends on an `AdminClient` abstraction while authenticated
requests are routed only through the bound Connector. This keeps gateway-specific
business logic independent from task transport without retaining a direct-access
credential path in Core.

**MaiBot** is scoped to community chat / Q&A in the owner social group only. It
is explicitly excluded from the operations alerting path (its LLM persona
behaviour is non-deterministic and unfit for deterministic alerting).

## Why

- Not forwarding requests removes the data-path risks (bandwidth, plaintext,
  single point, relay-operator compliance) at the root, not by mitigation.
- Side-car framing lowers the trust barrier: the owner's system stays theirs and
  our plugin can be removed at any time.
- The self-built surface shrinks to center + three adapters + notifier, which is
  achievable by 1-2 engineers in 6 weeks; deployment hosting, an outbound
  command queue, and a central supply gateway become deferred value-adds.
- Per-instance enrollment and local configuration make the credential boundary
  explicit while keeping installation replaceable and outbound-only.

## Consequences

- Deployment hosting (Komodo), the outbound command queue, and the central
  supply gateway (GPT-Load) move **out of the MVP** into deferred value-adds with
  explicit trigger conditions (see the roadmap).
- The earlier standalone Agent observation model is retired. The customer-side
  executable is the per-instance outbound Connector and reports only non-secret
  runtime state as part of authenticated task leasing.
- Front/back-end target stack is fixed here: back end Go + Gin + PostgreSQL +
  Redis + River (task queue) + sqlc + golang-migrate + Infisical/OpenBao; front
  end React + TypeScript + Vite + Ant Design Pro + TanStack Query; contract via
  OpenAPI with generated TS client. Temporal and low-code Portals (Backstage /
  NocoBase) are dropped from the near term.

Implementation note (2026-07-04): the MVP has landed the side-car behavior but
not every target technology. Current code uses Go `net/http`, handwritten pgx
queries, an in-process ticker health checker, MemoryVault, and handwritten
TypeScript API types. See `docs/development/current-state.md`.
- Two structural risks have no technical fix and must be handled in the hosting
  contract, not the code: (a) credentials landing inside the owner's gateway are
  readable by the owner (who has root), so upstream-side credential protection is
  ultimately contractual; (b) upstream-ToS / ban risk makes the platform a
  co-operator — a business risk, not a technical one.
- The 1091-line blueprint is retained for historical context but is no longer the
  governing design; `docs/architecture/e2m-sidecar-architecture.md` governs.
