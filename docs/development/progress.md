# E2M Local Progress

> 历史开发日志：本文件记录多个已被后续产品决策取代的阶段。2026-07-31 起，当前
> 范围是 Connector 仅托管站长自有号池；E2M Core 原生承载平台分组、上游、下游
> Key、钱包、基础转发、故障转移与用量结算，Sub2API 仅作架构参考。首期暂不做
> 三池比例路由、供应商动态 Offer、在线支付或上游自动结算。请以
> [current-state.md](current-state.md) 为准。

Updated: 2026-07-02

## Direction Change (2026-07-02)

E2M has been repositioned from a "full managed control plane" to a **side-car
plugin** that does **not** forward requests. See
[ADR-0003](../decisions/0003-sidecar-plugin-repositioning.md),
[e2m-sidecar-architecture.md](../architecture/e2m-sidecar-architecture.md), and
the [roadmap](roadmap.md). The pending items and next steps below are updated to
match; the old blueprint (`e2mapi-platform-architecture.md`) is historical only.

Target stack: back end Go + PostgreSQL + Redis + River + sqlc +
golang-migrate + Infisical/OpenBao; front end React + TS + Vite + Ant Design Pro

- TanStack Query; OpenAPI-generated TS client. Current code uses Go `net/http`,
  pgx handwritten queries, an in-process ticker health checker, MemoryVault for
  local secrets, and a handwritten TypeScript API client. Temporal and low-code
  Portals (Backstage / NocoBase) are dropped from the near term.

## Current Repository State

The repository now contains a completed side-car MVP implementation. See
[current-state.md](current-state.md) for the factual implementation baseline.

Repository hygiene update on 2026-07-04:

- `.gitignore`, `LICENSE`, and `README.md` were restored/rebuilt.
- The root README now describes the side-car MVP, current gaps, and local run
  commands.
- Current implementation details are consolidated in
  `docs/development/current-state.md`.

## Completed Work

> 历史说明：2026-07-09 起，项目已从租户/工作区模型校正为个体账号模型。下方早期流水中出现的 `tenant`、租户、TenantSelect 等词只表示历史实现记录，不能作为后续运行时模型或界面设计依据。

### W1 — Ground-to-production (2026-07-02, done & verified)

Roadmap week 1 delivered and verified against a real PostgreSQL 16 container:

- **contracts**: tenants as organization/resource containers with account-level
  roles (`platform_admin` / `owner` / `supplier`), `SupplyOffer` (oauth_subscription / api_key, holds only
  credential_ref + proxy_ref), `Instance` owner-tenant scoping note.
- **store interface** (`app/e2m-core/internal/store/store.go`): narrow `Store`
  with every method `(ctx, ...) (T, error)`. Audit writes are explicit via
  `AppendAudit` — the store no longer records audits as a hidden side effect
  (fixes the skeleton's inline-audit coupling). Runtime-reported capabilities
  were removed; capabilities are control-plane declared.
- **MemoryStore** refactored to the interface; tests rewritten (tenant scoping,
  explicit-audit, supply-offer). All green.
- **PostgresStore** (`postgres.go`) via pgx v5 + embedded golang-migrate
  migrations (`internal/store/migrations/0001_init.*.sql`), applied on startup.
- **credential vault boundary** (`internal/vault`): `Vault` interface +
  `MemoryVault` local impl; plaintext only in vault, DB stores refs only.
  Infisical/OpenBao slot in behind the same interface later.
- **httpapi** depends on `store.Store` (not concrete type); ctx threaded;
  tenant-scoped list endpoints; new tenant + supply-offer endpoints.
- **main** selects backend by `E2M_CORE_STORE` (memory | postgres).
- **local dev compose** `deployments/templates/compose/e2m-core-dev.compose.yml`
  (PostgreSQL + Redis + Core in postgres mode).
- **Verified**: create supplier/owner tenants, supply offer, instance; explicit
  instance.create audit; tenant isolation (bogus tenant → empty); role filter;
  DB holds only credential_ref/proxy_ref, no plaintext. gofmt/vet/tests clean.

### Web console + local Docker deploy (2026-07-02, done & verified)

Pulled the W4 console forward so the W1 backend is usable in a browser, and
deployed the full stack to local Docker:

- **web/console/** — React + TS + Vite + Ant Design Pro + TanStack Query.
  The original ProLayout shell grouped pages as 概览 / 运营 / 市场 / 治理 / 计费;
  the current shell is task-oriented: 接入与资源 / 监控与处置 / 上游资源 / 账单与费用 /
  平台管理, with role-specific filtering and single-item group promotion. Current pages include
  instance and Connector management, supply management, account health, gateway capabilities,
  audit logs, alert notifications, approvals, billing, and platform administration.
  Shared: typed api client + endpoints + TanStack hooks, StatusTag/RiskLevelTag/
  KindTag colour language, TenantSelect, EmptyTeach, StubPage.
- **Same-origin embedding**: `internal/webui` embeds `dist/` via `//go:embed`;
  server serves `/api/` + `/healthz` as JSON and `/*` as the SPA with index.html
  fallback. `EnableDevCORS` + Vite proxy for dev. A `.gitkeep` placeholder keeps
  bare `go build` working; the console builds a no-op placeholder page until a
  real `npm run build` (or the Docker image) supplies `dist/`.
- **3-stage Dockerfile**: node builds the console → go builds with it embedded →
  alpine runtime. `0002_seed` migration seeds the control-plane-declared sub2api
  read capabilities.
- **Verified**: `docker compose -f deployments/templates/compose/e2m-core-dev.compose.yml up`
  brings up postgres + redis + core, all healthy; console served at
  http://localhost:8080 with real assets + SPA routing; all 10 console-bound
  endpoints return 200; capabilities matrix populated. `npm run build` passes
  type-check (4122 modules). gofmt/vet/go test clean.

Next: W2 — `AdminClient` interface + real sub2api adapter
(see [roadmap.md](roadmap.md)).

### W2 — 动态切换上游账号 (2026-07-02, done & verified)

First real gateway integration: manual switching of sub2api upstream accounts via
sub2api's own admin API. No data-path involvement; control-path only.

- **contracts**: `GatewayAccount`, `AccountSwitch`, capability consts
  `set_account_schedulable` / `switch_upstream`
  (`packages/e2m-contracts/adapter_gateway.go`).
- **Store**: added `GetInstance(ctx, id)` to interface + memory + postgres.
- **AdminClient** (`internal/adapters/adminclient.go`): interface separating
  gateway adapters from the task transport. The current authenticated path uses
  the instance-bound Connector and its connector-local gateway configuration.
- **sub2api adapter** (`internal/adapters/sub2api/adapter.go`): `GatewayAdapter`
  impl — `ListAccounts` (`GET /api/v1/admin/accounts`), `SetSchedulable`
  (`POST /api/v1/admin/accounts/:id/schedulable`), `{code,data,message}` envelope.
  Unit-tested with httptest fake (list/toggle/bad-key).
- **Orchestrator** (`internal/orchestrator/switch.go`): `SetSchedulable` /
  `SwitchUpstream` (disable problem → enable backup), RiskLevel L1, one
  `OperationAudit` per write, partial-failure surfaced not swallowed. Unit-tested.
- **Endpoints**: `GET /instances/{id}/accounts`,
  `POST /instances/{id}/accounts/switch`,
  `POST /instances/{id}/accounts/{accountId}/schedulable`.
- **Console**: `InstanceAccounts.tsx` — account ProTable with a per-row
  schedulable Switch + 一键切换 modal; wired from the 实例 table 账号 action.
- **mock-sub2api** (`cmd/mock-sub2api/`): standalone fake admin API + Dockerfile,
  wired into the dev compose for Connector-based local exercises.
- **Verified end-to-end (containerized, over compose network)**: create a
  sub2api instance pointing at `mock-sub2api:8090` → list accounts → disable one
  → one-click switch (disable+enable) → `schedulable` flips on the mock → 4 L1
  `account.*_schedulable` audits land in Postgres. Capabilities matrix shows the
  2 write caps (migration 0003). gofmt/vet/go test + `npm run build` all clean.

Next: W3 — health-check scheduler (River) that auto-triggers the switch on
account degradation, plus Feishu/QQ notify (see [roadmap.md](roadmap.md)).

### W3 — 体检器 + 自动切换 + 告警 (2026-07-02, done & verified)

The side-car now acts on its own: it health-checks accounts, auto-switches
degraded ones (L1, audited), and alerts over Feishu/QQ.

- **notify** (`internal/notify`): `Notifier` interface + `FeishuNotifier`
  (HmacSHA256-signed custom-bot webhook) + `QQNotifier` (OneBot 11
  send_group_msg, best-effort) + `Router` (route-selected channel, gated by
  route MinRiskLevel). Generic webhook targets are owner-scoped Vault refs and
  are resolved only at delivery time.
- **health** (`internal/health`): `Checker` — ticker-based scheduler (interval
  configurable) that polls each sub2api instance's accounts via the orchestrator,
  evaluates health (error/expired/rate_limited/banned = unhealthy), keeps a
  fail-streak per account, and — when a still-scheduled account passes the streak
  threshold and isn't in cooldown — auto-switches: disable the problem account +
  enable a healthy non-scheduled spare (reuses W2 `SwitchUpstream`, L1 audited),
  then notifies. Keeps in-memory snapshots for the console. Unit-tested
  (streak gating, auto-switch, cooldown, healthy-not-switched).
- **endpoint** `GET /health-snapshots?instance_id=` serves the latest snapshots.
- **console**: account health now combines per-instance monitoring and the global health summary
  under `PoolHealth.tsx`; the legacy `/health-check` URL redirects to the summary tab.
- **wiring** (`main.go`): notifier credentials still come from environment;
  per-instance health cadence, failure threshold, cooldown, auto-switch, and
  drift detection are persisted policies managed through the console. The
  checker runs in a goroutine and schedules enabled instances independently.
- **mock**: added `POST /debug/status` so an account can be flipped to error.
- **Verified end-to-end**: flip an account to error on the mock → checker detects
  → after 2 fail-streak checks → auto-switch (disable 主号 + enable 备用号, both L1
  audits in Postgres) → **Feishu receiver captured** "⚠️ E2M 自动切换 · … 已自动停用
  主号 Claude，启用备用 备用号 Claude" → mock ground truth + snapshot reflect the
  switch → cooldown prevents thrashing. gofmt/vet/go test + `npm run build` clean.

### W3.5 — 通知路由可配置 (2026-07-03, done & verified)

Notification routes are now operator-editable end to end (previously read-only
seed data):

- **store**: `CreateNotificationRoute` / `UpdateNotificationRoute` /
  `DeleteNotificationRoute` added to the `Store` interface with both memory and
  Postgres implementations (update preserves `created_at`; delete/update return
  `ErrNotFound` appropriately).
- **httpapi**: `POST /api/v1/notification-routes`, `PUT /notification-routes/{id}`,
  `DELETE /notification-routes/{id}` with validation (channel qq/feishu/webhook,
  risk L0-L3, required tenant/name/target_ref) and an L1
  `notification_route.create/update/delete` audit per change.
- **console**: `通知路由` page is fully editable — create ModalForm (channel /
  min-risk selects with explanations, vault-ref hint), inline enable/disable
  Switch, edit, delete with Popconfirm; teaching empty state.
- **Verified against the Docker stack**: create → toggle → validation reject
  (bad channel 400) → audits recorded → delete 204 → repeat delete 404.

### W4 — 计量结算 + 账单 (2026-07-03, done & verified)

The money week: per-tenant monthly statements from live data, matching the
trust model (fixed hosting fee + per-disposition fee; usage reference-only,
never billed — the data path is owner-controlled).

- **contracts**: `BillingStatement` / `BillingLine` (amounts as decimal strings,
  computed in integer cents to avoid float drift).
- **billing** (`internal/billing`): `Calculator.Statement(tenant, YYYY-MM)` —
  axis 1: instances existing during the period × monthly fee; axis 2: accepted
  `account.*` audit rows targeting accounts within the period × disposition fee
  (failed ops and non-account audits excluded). Pricing from env
  (`E2M_BILL_INSTANCE_CENTS` default 19900, `E2M_BILL_DISPOSITION_CENTS`
  default 100, `E2M_BILL_CURRENCY` default CNY). Unit-tested (counting rules,
  period bounds, cents rendering).
- **endpoint**: `GET /api/v1/billing/statement?tenant_id=&period=YYYY-MM`
  (400 on bad period, wired via `BillingSource` interface).
- **console**: `计量结算` is now real (`Billing.tsx`) — owner tenant + month
  pickers, three stat cards (instances / dispositions / total), line-item table
  with summary row; removed from stubs (approvals & provisioning remain).
- **Verified on the Docker stack**: 2 instances + 3 manual dispositions →
  statement shows 实例托管费 2×199.00=398.00 + 处置费 3×1.00=3.00 = **401.00 CNY**;
  bad period rejected; billing SPA route serves. gofmt/vet/tests (7 pkgs) clean.

### W5 — new-api 适配器 + 供给台账 (2026-07-03, done & verified)

The horizontal-replication week: the second gateway proved the adapter
abstraction — new-api plugged in behind the same `GatewayAdapter` interface with
zero orchestrator/health/console changes.

- **AdminClient auth styles**: `AuthStyle` on `AdminRequest` — `AuthXAPIKey`
  (sub2api) vs `AuthNewAPI` (`Authorization: Bearer <token>` + `New-Api-User:
<uid>`; vault secret stored as `"uid|token"`).
- **newapi adapter** (`internal/adapters/newapi`): channels map onto
  `GatewayAccount` — status 1=enabled→active/schedulable, 2=manual-off→disabled,
  3=auto-disabled→**error** (reads as unhealthy to the checker).
  `SetSchedulable` toggles status 1↔2 via `PUT /api/channel/`. Envelope
  `{success,message,data}`; handles both `{items:[...]}` and bare-array data.
  Unit-tested (mapping, auth style, toggle bodies, envelope failure).
- **Health checker emergency path**: new-api _self_-disables bad channels
  (status 3), leaving nothing schedulable-and-unhealthy for the normal switch
  path. Added a debounced instance-level fallback: when the pool has ZERO
  healthy scheduled accounts for FailStreak consecutive checks, enable one
  healthy spare (cooldown-limited, audited, notified). Unit-tested both ways
  (empty-pool acts once; serving pool never triggers it).
- **mock-newapi** (`cmd/mock-newapi` + Dockerfile + compose service on :8091):
  Bearer+uid auth, channel list/update, `/debug/status` for tests.
- **supply ledger**: `SupplyLedgerEntry` contract, store CRUD (memory + PG,
  migration 0005), `POST /supply-offers/{id}/allocate` (ledger entry + offer
  pending→active + L1 audit), `POST /supply-ledger/{id}/revoke`,
  `GET /supply-ledger`. Console 供给登记 now has 供给列表/供给台账 tabs with
  分配 modal (instance picker; "记账≠注入" note) and 回收 with Popconfirm.
- **capabilities**: migration 0004 seeds newapi list/schedulable/switch rows.
- **Verified on the Docker stack**: new-api instance bound → channels listed
  with correct mapping → disable via adapter flips mock to status 2 → breaking
  ALL channels (status 3) triggers the debounced emergency spare enable (mock
  ground truth: ch2 → status 1; audited "池内已无健康可调度账号，紧急启用备用") →
  offer allocate/revoke flow with pending→active transition, ledger rows, L1
  audits, 404 paths. gofmt/vet/tests (8 pkgs) clean; 5 containers healthy.

### W6 — CPA 适配器 + 审批中心 (2026-07-03, done & verified) — MVP 收官

The final roadmap week: the third gateway plugged in, and L2 actions now gate on
a human decision end to end.

- **CPA adapter** (`internal/adapters/cpa`): CLIProxyAPI auth files map onto
  `GatewayAccount` (`GET /v0/management/auth-files` → files[].name/label/status/
  disabled; schedulable = !disabled; toggle via `PATCH /auth-files/status
{name,disabled}`; `Authorization: Bearer <management-key>` = new `AuthBearer`
  style). Status normalization: ok/active→active, error/invalid→error,
  quota_exceeded→rate_limited (checker vocabulary). Unit-tested. Endpoints
  verified against the live CPA handler source (auth_files.go / server.go).
- **mock-cpa** (`cmd/mock-cpa`, :8092, compose service).
- **Approval engine** (`internal/approval`): `Submit` (validates
  batch_set_schedulable, forces L2, notifies Feishu/QQ) → `Approve` (executes
  per-account via orchestrator, each execution audited with the approval
  reason; approval-level audits carry ApprovalID) / `Reject` (never executes).
  Partial execution failure → status `failed`. Double-decide blocked.
  Unit-tested (gate holds, reject-never-executes, partial failure, validation).
- **Store**: `ApprovalRequest` contract + CRUD in memory & Postgres (migration
  0007); capabilities migration 0006 (CPA rows + batch_set_schedulable L2 rows
  for all three gateways).
- **Endpoints**: `GET/POST /api/v1/approvals`, `POST /approvals/{id}/approve`,
  `POST /approvals/{id}/reject`.
- **Console**: 审批中心 is real (pending/executed/rejected/failed segments,
  批准/驳回 with Popconfirm); instance accounts page got row selection + 「批量启
  停（L2 需审批）」which submits an approval; health checker covers CPA.
- **Verified on the Docker stack**: CPA bind → auth files listed & mapped →
  single switch flips mock `disabled` → L2 submit (pending, gate holds — mock
  unchanged) → reject path (never executes) → approve → batch executes (mock
  ground truth: both files disabled) → full audit chain (submit/approve/execute
  - per-account rows all carrying approval_id). gofmt/vet/tests (12 pkgs) clean;
    6 containers healthy.
- **Noted interaction (by design, watch in prod)**: approving a batch-disable
  that empties the pool triggers the W5 emergency spare-enable on the next
  check cycle — the health checker re-enabled one healthy file ("池内已无健康可
  调度账号，紧急启用备用"). Safety net vs. operator intent; if an owner truly
  wants a pool offline they should disable auto-switch for that instance
  (future per-instance toggle candidate).

**Roadmap W1-W6 complete.** Remaining deferred value-adds (Phase 2 connector,
deployment hosting, central supply gateway, Temporal, MaiBot) stay
trigger-gated per roadmap §4.

### Platform Direction

- Reframed E2M as a long-term platform: Portal + E2M Core API + Connector + Workflow + Notification + Deployment.
- Kept Backstage and NocoBase as Portal candidates without copying either framework into the repository.
- Kept go-admin as a fallback shell only, not as the long-term business core.

### Engineering Skeleton

- Added Go workspace: `go.work`.
- Added shared contracts: `packages/e2m-contracts`.
- Added Core API skeleton: `app/e2m-core`.
- Added the customer-side executable now used as the per-instance Connector:
  `app/e2m-agent`.
- Added Backstage plugin boundary: `packages/backstage-plugin-e2m`.
- Added NocoBase plugin boundary: `packages/nocobase-plugin-e2m`.
- Added deployment template boundary: `deployments/templates`.

### Implemented Core API Capabilities

- `GET /healthz`
- `GET /api/v1/instances`
- `POST /api/v1/instances`
- `GET /api/v1/adapter-capabilities`
- `GET /api/v1/audits`
- `GET /api/v1/notification-routes`

### Superseded Customer-side Prototype

The initial standalone observation prototype was replaced by one outbound
Connector per instance. Current deployment uses enrollment token files,
per-Connector tokens, local UI gateway configuration, and dedicated data
volumes. Container inspection is not part of the Connector model.

### Containerization

- Added `.dockerignore`.
- Added `app/e2m-core/Dockerfile`.
- Added `app/e2m-agent/Dockerfile`.
- Added local Docker Compose template: `deployments/templates/compose/e2m-skeleton.compose.yml`.
- Added Dokploy-oriented template: `deployments/templates/dokploy/e2m-skeleton.compose.yml`.
- Updated local templates to run a per-instance Connector without host runtime access.

## Verified Commands

```powershell
gofmt -w app/e2m-agent app/e2m-core packages/e2m-contracts
go test ./app/e2m-core/... ./app/e2m-agent/... ./packages/e2m-contracts/...
docker build -f app/e2m-core/Dockerfile -t e2m-core:dev .
docker build -f app/e2m-agent/Dockerfile -t e2m-agent:dev .
docker compose -f deployments/templates/compose/e2m-skeleton.compose.yml up --build -d
```

## Current Docker Notes

The previous smoke test used `docker compose down`, so E2M containers were removed after verification. The images remained:

- `e2m-core:dev`
- `e2m-agent:dev`

To keep containers visible in Docker Desktop, run:

```powershell
docker compose -f deployments/templates/compose/e2m-skeleton.compose.yml up --build -d
```

To stop and remove them:

```powershell
docker compose -f deployments/templates/compose/e2m-skeleton.compose.yml down
```

## Remaining Productionization Items

Full weekly breakdown with effort estimates lives in [roadmap.md](roadmap.md).
The original W1-W6 MVP is complete; the remaining work is production hardening
and deferred value-adds.

### Core / Center

- Add authentication, login, account-scoped authorization, and RBAC.
- Replace MemoryVault with Infisical/OpenBao or another real secret backend.
- Add OpenAPI spec and generated TypeScript client.
- Decide whether to adopt Gin and sqlc or keep the current `net/http` + pgx style.
- Move health checking from the in-process ticker to River if durable scheduling
  and retry semantics become necessary.
- Add CI for Go tests, frontend build, and Docker smoke tests.

### Adapters

- Contract tests + version probing to absorb upstream version drift.
- Phase 2 lightweight connector when owners will not expose management ports.
- Per-instance auto-switch controls so operators can intentionally drain pools.

### Notifier

- Richer Feishu approval-card callback flow for L2/L3 actions.
- Notification templates and escalation/silence policies if alert volume grows.

### Front end

- Bundle splitting; current Vite build emits a large main JS bundle.
- Login/session UX once backend auth exists.
- OpenAPI-generated client integration.

### Deferred value-adds (post-W6, trigger-gated — see roadmap §4)

- Phase 2 lightweight connector (when owner won't expose the management port).
- Deployment hosting via Komodo; central supply gateway via GPT-Load.
- MaiBot for owner-community chat/Q&A (isolated from the alerting path).
- Temporal only when Runbooks become multi-step with compensation.

## Recommended Next Steps

1. Add auth/RBAC and account-scoped authorization before exposing Core beyond local/dev.
2. Add OpenAPI generation so the console stops relying on handwritten API types.
3. Replace MemoryVault with a real secret backend and audit secret reads.
4. Add CI and Docker smoke coverage.
5. Harden per-instance Connector enrollment, task signing, and local secret UX.
