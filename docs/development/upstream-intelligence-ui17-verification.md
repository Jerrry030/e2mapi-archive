# Upstream Intelligence UI-17 verification

> **该子系统当前未挂载。** 截至 2026-08-04，本文描述的 HTTP 端点未在 `internal/httpapi/server.go` 的 `Routes()` 中注册，处于暗启动状态：代码与测试存在，但通过当前 E2M API 不可达。本文保留为设计与验收参考，不代表当前可用能力。现状见 [current-state.md](current-state.md)。

> Acceptance status: UI-17 was formally signed off on 2026-07-27 for the
> current frozen local/disposable candidate. The accepted candidate passed
> fresh module tests/vet, PostgreSQL 16, pinned Linux race, Prometheus, portable
> failure drills, current production-asset browser acceptance, schema-4
> three-source intelligence and schema-6 NewAPI weighted-rollout gates. Both
> release runners explicitly acknowledged `SourceFrozen`, emitted
> `release_pass=true`, and bound their images to the same 849-file canonical
> build-input SHA-256
> `ee8eba1066b55a578e938ffb27d23dc3228545e49738d4fd99955d489474281b`.
> Historical, test-only, failed or skipped runs are not part of this signoff.
> This is current-worktree/disposable evidence, not production deployment or a
> universal latency SLO.

This runbook covers the repeatable local performance, response-safety and
recommendation-rollout failure checks. It deliberately distinguishes portable
semantic evidence from machine-specific latency budgets and external release
gates.

## UI-16 and protocol-v3 invariants covered locally

The local Core/Connector/contracts tests and the base failure-drill script are
expected to prove all of the following without contacting a real gateway:

- the captured baseline contains every managed binding for the plan/instance,
  totals 100, preserves explicit zero, and never converts unknown to zero;
- a target may start non-zero and unrelated accounts may be non-zero; stages
  10/25/50/100 move only that fraction of the source's original baseline while
  preserving unrelated weights exactly;
- each forward stage has a generation fence, rollout-operation lease/CAS, full
  read-back and an observation boundary; evidence used after a stage is
  refreshed after `ObserveUntil` instead of reusing pre-write health samples;
- only global intelligence/link fact version, quality evidence IDs and their
  derived fingerprint/timestamps may refresh; mapping, cost, offer, wallet,
  binding, plan, dimensions, savings, policy or formula drift blocks advance;
- rollback atomically supersedes a pending/running forward operation and
  advances the generation so the former operation-lease owner cannot renew,
  complete or write late; an already `executing` Connector task is never
  silently superseded, and instead makes generation advancement conflict until
  its outcome is terminal;
- rollback writes and reads back the full baseline and records
  `weight-set-sha256:<baseline fingerprint>` only after exact equality;
  rollback callability/quality remain unknown because weight equality is not a
  health proof; the HTTP projection sets `rollback_verified=true` only when the
  durable rolled-back/StageNone/PendingNone state, recommendation, baseline,
  generation, fresh window, unknown gates and that single evidence ID match;
  `last_after_verified` stays false for StageNone;
- the global auto-apply flag and scope kill switch block only `start` and
  `advance`; authenticated `list`, `get`, `rollback`, Worker recovery and Runner
  recovery remain available.
- protocol v3 requires every route-plan fenced mutation to exchange its exact
  live lease for a durable `executing` permit before the local gateway side
  effect while holding the local scheduling-fence and write-receipt locks;
  execute 409, uncertain response or any drift in user/instance/Connector,
  type/schema/input/idempotency/plan/generation/nonce produces zero gateway call
  and zero Complete;
- fenced Complete accepts only `executing` plus the exact nonce and a typed
  result matching the original task; retryable completion or an uncertain
  gateway result cannot release the permit, and `executing` ignores the old
  lease deadline and blocks route-plan generation changes/deletes;
- platform-admin manual recovery is terminal and closed to
  `confirmed_applied`, `confirmed_not_applied`, and
  `connector_revoked_unverifiable`; task state and L3 critical audit commit
  atomically, and the audit stores only `lease_nonce_sha256=sha256:<hex>`, never
  the raw execution nonce;
- migration 0074 rejects every unresolved protocol-v2 leased fenced mutation,
  preserves only proven pending identities and fails malformed pending claims
  closed; it keeps stored protocol-v2 Connector identities visibly version 2,
  permits auth/read but no lease/execute, admits a genuine version-3 runtime
  handshake upgrade, and refuses down migration while any version-3 Connector
  or executing task remains.

## Protocol-v3 execution gate

Run the portable contracts, Agent and Core checks from their module roots:

~~~powershell
go test ./...
go vet ./...
~~~

For a focused non-PostgreSQL diagnosis, run from the repository root:

~~~powershell
go test ./app/e2m-agent/internal/connector -run 'ExecutionPermit|FencedMutationCannotBypass' -count=1 -v
go test ./app/e2m-core/internal/store -run 'MemoryConnector(TaskExecutionPermitLifecycle|ExecutionPermitSerializesGenerationBump|ProtocolV2HandshakeUpgrade)' -count=1 -v
go test ./app/e2m-core/internal/httpapi -run 'ConnectorTaskExecutionResolution|StoredProtocolV2ConnectorMustHandshake' -count=1 -v
~~~

With a fresh disposable PostgreSQL DSN, run from the Core module:

~~~powershell
$env:E2M_TEST_POSTGRES_DSN='postgres://...'
go test ./internal/store -run 'TestPostgresConnectorExecution|TestPostgresResolveConnectorTaskExecution|TestPostgresConnectorProtocolV2HandshakeUpgrade|TestPostgresConnectorTaskRoutePlanFence' -count=1 -v
go test ./internal/store -run 'TestPostgresConnectorExecution|TestPostgresResolveConnectorTaskExecution' -count=5
go test ./internal/store -count=1
~~~

The focused gate must prove permit/generation serialization in both winner
orders, executing completion rules, typed receipt validation, the three-state
resolution closed set and audit rollback, v2 auth/read-but-no-lease followed by
real v3 handshake, migration 0074 up recovery/rejection, and down rejection for
both `executing` tasks and protocol-v3 Connector rows. A DSN-less skip is not a
PostgreSQL pass.

## Workload

- 100 owner-scoped upstream sources.
- 5,000 current rate facts (50 models per source).
- Consistent-store read and deterministic cost-quality Pareto projection.
- Browser-facing DTO schema scan plus recursive JSON value scan for
  credentials, authorization/cookie material, URLs and deployment-local fields.

The scale fixtures are generated in memory and contain no production data or
credentials.

## Default acceptance run

From the Core module:

~~~powershell
go test ./internal/store ./internal/upstreamintelligence ./internal/httpapi -run 'UI17|Scale100Sources5000Facts|ReadDTOsExclude|ReadEndpointsDoNotExpose' -count=1 -v
~~~

The performance tests always validate completeness and correctness. By default
they report elapsed time without asserting a fixed duration, because shared CI
and developer hardware are not comparable.

## Explicit local budgets

Use positive integer millisecond budgets only on a controlled runner:

~~~powershell
$env:E2M_UI17_READ_MAX_MS='250'
$env:E2M_UI17_FRONTIER_MAX_MS='1000'
go test ./internal/store ./internal/upstreamintelligence -run 'Scale100Sources5000Facts' -count=1 -v
~~~

An unset budget means report-only; an invalid or exceeded explicit budget fails
the test. Record the machine/runner identity, Go version and command with any
number used as release evidence. Do not promote one laptop measurement to a
universal SLO.

## Benchmarks

~~~powershell
go test ./internal/store -run '^$' -bench 'ReadUpstreamIntelligenceCurrent100Sources5000Facts' -benchmem -count=5
go test ./internal/upstreamintelligence -run '^$' -bench 'BuildFrontier100Sources5000Facts' -benchmem -count=5
~~~

Both benchmarks report sources/op and rate_facts/op so accidental fixture
shrinkage is visible in captured output.

## PostgreSQL boundary

The portable default checks validate MemoryStore semantics, projection cost and
response safety. PostgreSQL migration/index and concurrency evidence requires a
disposable database via `E2M_TEST_POSTGRES_DSN`, the repository's PostgreSQL
test suite, and query-plan capture for the upstream-intelligence current-read
queries. The latest disposable-database result is recorded below.

The PostgreSQL release fixture can be run with:

~~~powershell
$env:E2M_TEST_POSTGRES_DSN='postgres://...'
$env:E2M_UI17_PG_P95_MAX_MS='2000'
go test ./internal/store -run 'TestPostgresUpstreamIntelligenceIngestCapacity|TestPostgresUpstreamIntelligenceRetention|TestPostgresUI17Scale' -count=1 -v
~~~

This gate proves owner-scoped atomic ingest admission across concurrent Core
instances, replay-free accounting, an accurate rejected-window `Retry-After`,
bounded expiry cleanup, owner-isolated/batched raw-history retention, current
and referenced evidence protection, and fact-version advancement. The scale
fixture writes 100 sources, 5,000 current offers, and 40,000 change events over
400 days. It records 20 current-read and 20 rollup samples, enforces the P95
budget, and logs `EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)` for both reads.
Retain the full output; never treat MemoryStore latency as PostgreSQL P95.

The retained authoritative PostgreSQL 16 run reported current-read P95 79.4694
ms and 400-day rollup P95 60.1667 ms, and passed ingest capacity plus retention
behavior under the explicit two-second budget. The directly supporting log is
`.tmp/ui17-evidence/postgres-store-gate-authoritative.log` (SHA-256
`2439ef5d050e169e9a5855fa8a9dbdd1990c908fa8289dc57622fef9c3f74b2b`);
the disposable database was removed after evidence capture. These figures are
machine-local diagnostics, not a universal production SLO.

Rollback preemption has MemoryStore coverage, a static PostgreSQL
transaction/lease-fence check and `E2M_TEST_POSTGRES_DSN`-gated behavioral
coverage. The database test exercises both pending and claimed/running forward
operations, verifies atomic supersession and lease clearing, rejects the former
owner's renew and completion, and proves that a rejected rollback leaves no
partial preemption. This is local disposable-database evidence, not a substitute
for the real weighted-gateway drill.

## Operational metrics and alerts

`E2M_METRICS_ADDR` is empty by default. Setting it starts an independent
private listener with only `GET /metrics`. The response is `no-store` and uses
closed low-cardinality labels. See
`upstream-intelligence-operations-runbook.md` for deployment and incident
response.

~~~powershell
promtool check rules deployments/monitoring/upstream-intelligence-alerts.yml
promtool test rules deployments/monitoring/upstream-intelligence-alerts.test.yml
~~~

If `promtool` is not installed on `PATH` but Docker is available, run the
official Prometheus image from the repository root. Keep the image digest and
reported `promtool` version with the evidence:

~~~powershell
$prometheusImage = 'prom/prometheus@sha256:3c42b892cf723fa54d2f262c37a0e1f80aa8c8ddb1da7b9b0df9455a35a7f893'
docker run --rm --entrypoint promtool -v "${PWD}:/workspace" -w /workspace $prometheusImage --version
docker run --rm --entrypoint promtool -v "${PWD}:/workspace" -w /workspace $prometheusImage check rules deployments/monitoring/upstream-intelligence-alerts.yml
docker run --rm --entrypoint promtool -v "${PWD}:/workspace" -w /workspace $prometheusImage test rules deployments/monitoring/upstream-intelligence-alerts.test.yml
~~~

`rule_files` entries in a Prometheus rule-test file are resolved relative to
that test file, not the shell working directory. The checked-in test therefore
references the adjacent `upstream-intelligence-alerts.yml` by filename. If
neither a local binary nor the pinned container can run, record rule tests as
skipped rather than signed off.

## Failure drills

~~~powershell
.\scripts\test-upstream-intelligence-failure-drills.ps1
.\scripts\test-upstream-intelligence-failure-drills.ps1 -IncludePostgres -IncludePrometheus -IncludeRace
~~~

The base run covers deterministic local failure and boundary tests. Optional
switches are release gates and fail immediately when their dependency is not
configured. `-IncludePostgres` requires both `E2M_TEST_POSTGRES_DSN` and a
positive integer `E2M_UI17_PG_P95_MAX_MS`; the base phase temporarily hides the
DSN so PostgreSQL tests cannot leak into the portable check. `-IncludeRace`
requires a working host `CGO_ENABLED=1` toolchain.

On a Windows host without CGO, use the pinned local Linux Go image from the
repository root and retain its image ID with the output:

~~~powershell
docker run --rm -v "${PWD}:/workspace" -w /workspace/app/e2m-core `
  golang@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 `
  go test -race `
  ./internal/store ./internal/httpapi ./internal/recommendationrollout `
  -run 'UI17|Collector|Evaluate|Recommendation|Shadow|DryRun|TrafficShare|Rollout|Connector.*Completion|Connector.*Execution|ResolveConnectorTaskExecution|ProtocolV2Handshake|StoredProtocolV2ConnectorMustHandshake|ReadDTOsExclude|ReadEndpointsDoNotExpose' `
  -count=1

docker run --rm -v "${PWD}:/workspace" -w /workspace/app/e2m-agent `
  golang@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 `
  go test -race `
  ./internal/connector ./internal/adapters/sub2api `
  -run 'UpstreamIntelligence|IntelligenceClient|ExecutionPermit|FencedMutationCannotBypass' `
  -count=1
~~~

These selectors include the protocol-v3 permit, generation serialization,
completion, manual resolution and v2-to-v3 handshake paths. They define the
current race gate. The 2026-07-27 run executed these selectors successfully in
the pinned Linux Go Alpine image with GCC; retain that output for release.

Runtime configuration used by the normal local/production path defaults to:

~~~text
E2M_UPSTREAM_RECOMMENDATIONS=false
E2M_UPSTREAM_OPTIMIZATION_AUTO_APPLY=false
E2M_RECOMMENDATION_ROLLOUT_OBSERVATION=5m
E2M_RECOMMENDATION_ROLLOUT_WORKER_INTERVAL=1s
E2M_RECOMMENDATION_ROLLOUT_WORKER_LEASE=2m
E2M_RECOMMENDATION_ROLLOUT_RUNNER_INTERVAL=1s
~~~

Only explicit `true` enables each feature flag. The disposable real-gateway
acceptance run must opt into both recommendations and auto-apply; normal dev,
production and real-gateway templates default both to false. Do not shorten the
observation window or lease in release evidence merely to make the drill
complete faster; use controlled test fixtures and label any overrides as
test-only.

## Latest local evidence

### Protocol-v3 delta (2026-07-27)

The settled tree passed contracts, Agent and Core `go test ./...` and `go vet
./...`. The full-package test commands reported cached results; they prove the
current build inputs match previously passing test cache entries, but the
evidence must retain that `(cached)` fact instead of presenting fresh execution
timings. Focused uncached Agent Connector, Core Store and HTTP protocol tests
also passed during the delta work.

A disposable PostgreSQL run passed the verbose
`postgres_connector_execution_protocol_test.go` cases in 7.557 seconds and a
`count=5` repeat in 28.259 seconds. A fresh database named
`e2m_fence_fullsuite_20260727` migrated through schema version 74 and passed the
full `internal/store` suite in 21.802 seconds. All three live 0074 cases passed:
up migration/recovery, down rejection with an executing task, and down
rejection with a protocol-v3 Connector. These are implementation and database
state-machine evidence, not real-gateway evidence.

The protocol-v3 selectors passed under the pinned Linux Go Alpine image with
GCC. Core `internal/store`, `internal/httpapi` and
`internal/recommendationrollout` passed (the slowest package reported 33.0s),
and Agent `internal/connector` plus `internal/adapters/sub2api` passed (the
slowest package reported 2.651s). This is the delta race evidence for the new
permit, generation, completion, resolution and handshake paths.

The retained forward-only migration 0069 live matrix also passed in 41.337s,
covering public and non-public recovery, corruption rejection, concurrent
startup and bounded reverse-lock failure. The separate retained reverse-lock
artifact additionally records fail-closed behavior followed by a successful
retry. These are available under `.tmp/ui17-evidence/0069-forward-only/`.

The authoritative self-contained race bundle is
`.tmp/ui17-evidence/linux-race-final-valid-20260727/manifest.json` (SHA-256
`7f4b6dbb4a41216e26ee0cd038c5061505963e5b93ae6ad78991252c88c3b267`).
It records exit code zero for Core Store/HTTP/rollout and Agent
Connector/Sub2API, source-tree stability during the run, no race/failure or
credential/URL finding, and zero residual containers. Its source-tree digest
uses the race-time manifest format; the formal release runners independently
bind the canonical 849-file build-input digest stated in the acceptance status.

The final production-asset browser gate passed on 2026-07-27. At a requested
390×844 viewport (document client width 375), both Chinese and English rendered
100 sources / 5,000 facts with zero page-level horizontal overflow; wide tables
scroll only inside labelled regions, and both locales expose an equivalent
`table` with a `caption` for the cost-quality visualization. The native evidence
button supports Enter-to-open, moves focus into the real Drawer, and Escape
removes `evidence_id`, closes the Drawer and restores focus to the identical
trigger. All XSS sentinels remained null and browser console errors were zero.
The accepted artifact is
`.tmp/ui17-evidence/browser-final-20260727/settled-browser-acceptance.json`
(SHA-256
`0b396b0ce98fd6c2ca93f5ff487da17f5092578e85428d3333f3d10af90cfd72`).
The matching source-build manifest at
`.tmp/ui17-evidence/console-final-source-20260727/manifest.json` records 58/58
test files and 201/201 tests, lint, format-check, production build and both npm
audits with zero vulnerabilities; its embedded assets match the tested dist.

### Baseline evidence (2026-07-26)

The following checks completed successfully on the settled local tree:

~~~text
.\scripts\test-upstream-intelligence-failure-drills.ps1
Core recommendation/experiment/rollout/security packages: PASS
Connector numeric traffic-share and fence packages: PASS
Contracts traffic-share/recommendation/intelligence packages: PASS
~~~

The corrected base failure drill completed with every selected Core package
executing tests, including the operational collector, authorization evaluator
and DTO/recursive-response safety checks. Full Go regression also passed for
contracts, Connector and Core with both `go test ./... -count=1` and `go vet
./...`. The console passed 51/51 test files (173/173 tests), lint with zero
warnings, Prettier checking and a production build. The build's roughly
2.59 MB main JavaScript chunk (about 778 KB gzip) is a non-blocking future
code-splitting opportunity.

The 100-source/5,000-fact MemoryStore semantic read also passed and reported a
single local sample of approximately 13 ms. That number is diagnostic only and
is not a PostgreSQL P95 or release SLO.

A fresh disposable PostgreSQL database passed the full `internal/store` suite
and the targeted rollback-preemption tests. The targeted cases covered pending
and running forward operations, stale owner renew/completion rejection, rejected
rollback atomicity, JSON-array persistence for nil slices, and recommendation
timestamp/immutable-fence concurrency. The corrected `-IncludePostgres` gate
then ran those verbose behavioral cases plus 20 100-source/5,000-fact current
reads under an explicitly configured 2-second local budget; its retained final
run reported P95 156.2385 ms, maximum 174.3356 ms and latest-offer `EXPLAIN
(ANALYZE, BUFFERS)` execution time 21.186 ms. Separate confirmation runs
reported P95 between 62.0693 ms and 97.0634 ms. These are machine-local release
diagnostics, not a universal service SLO. The disposable review databases were
removed after the tests.

For the 2026-07-26 baseline, the race detector passed in the Linux Go image pinned above (digest/ID prefix
`1ecb7edf62a0`) for `internal/store`, `internal/httpapi` and
`internal/recommendationrollout`, including the corrected UI-17 selector. The
Windows host itself remains `CGO_ENABLED=0`; the container result is the
reliable race evidence for that baseline run; the separate delta evidence above
covers the 2026-07-27 protocol-v3 paths.

Prometheus rule validation also completed successfully through Docker Engine
29.5.3 and the official image pinned above. The image reported `promtool
3.13.1` (`linux/amd64`): `check rules` found all eight rules and `test rules`
passed the missing-metrics, cost backlog, Connector backlog, stale evidence,
comparable coverage, blocked rollout, stuck rollout action and rollback failure
cases. No `promtool` binary was installed on the Windows host `PATH`.

The operational-metrics package passed both `go test
./internal/operationalmetrics -count=1` and `go vet
./internal/operationalmetrics`. Its collector tests prove that every labelled
metric has an independent closed allowlist, forbidden or sensitive-looking
persisted values cannot become labels, and missing or invalid comparable
coverage produces no sample instead of a guessed zero.

## Accepted current-schema release gates

- Three-source intelligence: project
  `e2m-ui17-intel-29204-cabbadca3aea`, schema 4, profile `release`, explicit
  `SourceFrozen`, `release_eligible=true`, `release_pass=true` and
  `test_pass=true`. It proves two external plus one owned pinned Sub2API,
  bearer-only collection after administrator-key rotation, durable outbox
  restart/replay/drain, single-source failure isolation with stale/recovery,
  two complete snapshots before removal, browser readback and boundary scans.
  Evidence:
  `.tmp/ui17-evidence/e2m-ui17-intel-29204-cabbadca3aea/redacted-evidence.json`,
  SHA-256
  `7016982ae6eb2eb5c95661c108c8158a1c17e0cb08c23a5c3c2c6248747de63b`.
- Weighted rollout and protocol-v3 ordering: project
  `e2m-ui17-newapi-20260727164048-f95a6076`, schema 6, profile `release`,
  explicit `SourceFrozen`, `release_eligible=true`, `release_pass=true` and
  `test_pass=true`. It proves 10/25/50/100 against real disposable NewAPI
  traffic, observation gates, exact complete-baseline readback and restoration,
  running-lease exclusion/stale CAS fencing, automatic `quality_failed`
  rollback, execute-conflict zero remote side effect, generation guarding and
  authenticated manual resolution. The uncertain-gateway invariant remains
  correctly labelled a directed unit proof rather than a real external network
  fault. Evidence:
  `.tmp/ui17-evidence/e2m-ui17-newapi-20260727164048-f95a6076-newapi-redacted-evidence.json`,
  SHA-256
  `56ee92f60406fd454f54e62ff7a12eddbcdea01175ca7c93f835e73f40bc0abf`.
- Visual/product acceptance: the browser and console artifacts above prove the
  bilingual, equivalent-table, tested 390×844 overflow, keyboard/focus, XSS and
  source-built production-asset gates.

Both release artifacts bind the unchanged current runner and Compose hashes,
the same canonical frozen input, and immutable built-image IDs. Each reports
exact isolated cleanup (`containers=0`, `volumes=0`, `networks=0`), runtime
directory removal, process-environment restoration and unchanged protected-stack
container IDs. No test-only, historical, failed or skipped artifact is counted
toward this signoff. Earlier schema-2 passes and failed schema-4 attempts remain
diagnostic history only.

The machine-readable signoff index is
`.tmp/ui17-evidence/ui17-final-signoff-20260727/manifest.json`, SHA-256
`3fbc011f5013f457d1e10dfefc06987e35596b65d6e40a8c3a4303b27a1f1d3c`.
It records the accepted scope, both current-schema releases, supporting artifact
hashes, cleanup/protected-stack assertions, exclusions and limitations; both
PowerShell `ConvertFrom-Json` and Node `JSON.parse` accept it.

The machine-readable signoff index is
`.tmp/ui17-evidence/ui17-final-signoff-20260727/manifest.json`, SHA-256
`3fbc011f5013f457d1e10dfefc06987e35596b65d6e40a8c3a4303b27a1f1d3c`.
It records the accepted scope, both current-schema releases, supporting artifact
hashes, cleanup/protected-stack assertions, exclusions and limitations; both
PowerShell `ConvertFrom-Json` and Node `JSON.parse` accept it.

## Interpreting failures

- Incomplete source/fact counts indicate a consistency or silent-truncation
  regression.
- A DTO schema failure means a browser response gained a sensitive-looking or
  deployment-local field and requires explicit redesign/review.
- A JSON value failure means a real endpoint emitted a URL or secret-shaped
  value even if the field name looked harmless.
- A budget failure is meaningful only when the budget was explicitly set on a
  controlled runner; rerun without the budget to separate correctness from
  environmental latency.
