# Upstream intelligence operations runbook

This runbook covers UI-10 through UI-17 operational response. It assumes Core
stores only sanitized facts: upstream endpoints, tokens, cookies, headers and
raw responses remain Connector-local and must never be copied into tickets,
metrics, browser payloads, audit details or database diagnostics.

## Enable and scrape metrics

Core and Agent metrics are disabled by default. Set `E2M_METRICS_ADDR` on Core
and `E2M_AGENT_METRICS_ADDR` on each Agent to dedicated private listeners, for
example a loopback address for a same-host collector or an internal service
address protected by the deployment network. Do not publish either listener
through the console ingress. Scrape Core and Agent as separate jobs so a live
Core cannot hide an unavailable Connector exporter.

Each listener exposes only `GET /metrics`, has an independent lifecycle and
shares graceful shutdown with its process. A declared listener bind failure
fails that process startup so a deployment cannot silently run without its
monitoring contract. The Agent returns `503`, not synthetic zero gauges, when
its intelligence outbox is corrupt or unreadable. An accurately read empty
outbox emits zero for both depth and oldest age.

Application metric labels are restricted to closed status, result, kind and
rollout-stage sets. Owner, source, channel, plan, URL, token and arbitrary error
text are prohibited. Infrastructure labels such as the Prometheus scrape
`job`, `instance` and `pod` identify the private target and must not contain
upstream data. Alert rules live in
`deployments/monitoring/upstream-intelligence-alerts.yml`.

Core `*_total` metrics and the collection-duration histogram are
retention-independent, monotonic global aggregates stored in the database.
Every Core replica that shares that database exposes the same logical
counters. Counter alerts therefore calculate the range increase per scrape
series and use `max without(instance,pod,job)` to remove replica identity while
preserving closed business labels such as `kind`. Never sum those replicas:
that would multiply one database event by the Core replica count. Current
object counts, ages, coverage and rollout-stage age are gauges and are
de-duplicated the same way when a global predicate uses them.

Validate rules on a workstation that has Prometheus `promtool`:

~~~powershell
promtool check rules deployments/monitoring/upstream-intelligence-alerts.yml
promtool test rules deployments/monitoring/upstream-intelligence-alerts.test.yml
~~~

When the host has no `promtool` binary, the UI-17 verification guide provides
the equivalent pinned official-Prometheus-container commands. Run them from
the repository root and retain both the image digest and reported version.

## First response checklist

1. Disable new automatic optimization with the global apply feature flag or
   the scope policy kill switch. Both controls block only rollout `start` and
   `advance`; authenticated `list`, `get` and `rollback` remain available, and
   the rollout Worker and recovery Runner must remain live. Do not delete
   recommendations, experiments, rollouts, tasks, receipts, evidence or audits.
2. Preserve rollback authority. A kill switch, stale recommendation, expired
   recommendation or stale dry-run must never prevent restoring exact baseline
   weights.
3. Inspect sanitized status and reason codes in the admin console and audit
   trail. Never request or paste Connector credentials or raw provider output.
4. Confirm the Connector is online and that its durable scheduling fence has
   not been superseded by an operator publish or auto-switch action.
5. Run the relevant drill in `scripts/test-upstream-intelligence-failure-drills.ps1`
   against a disposable environment before changing recovery logic.

## Rollout controls and runtime defaults

`E2M_UPSTREAM_OPTIMIZATION_AUTO_APPLY` is fail-closed: only the explicit value
`true` enables forward rollout start/advance. It is not a master switch for the
recovery surface. Do not put list/get/rollback behind this flag, and do not stop
the Worker or recovery Runner when disabling it.

The checked runtime defaults are:

| Variable | Default | Accepted runtime range |
| --- | ---: | --- |
| `E2M_RECOMMENDATION_ROLLOUT_OBSERVATION` | `5m` | whole seconds from `1s` through `7d` |
| `E2M_RECOMMENDATION_ROLLOUT_WORKER_INTERVAL` | `1s` | `100ms` through `1m` |
| `E2M_RECOMMENDATION_ROLLOUT_WORKER_LEASE` | `2m` | `90s` through `10m` |
| `E2M_RECOMMENDATION_ROLLOUT_RUNNER_INTERVAL` | `1s` | `100ms` through `1m` |

Out-of-range or invalid values fall back to the safe defaults. Short observation
or lease values are test-only tuning; retain the defaults in production unless
an evidence-backed capacity review establishes a new value.

## Metrics missing

Confirm the deployment intentionally set `E2M_METRICS_ADDR` and
`E2M_AGENT_METRICS_ADDR`, both private ports are listening, the separate Core
and Agent scrape jobs use `/metrics`, and the network policy permits only the
collector. A bind error is a process-start failure and should be visible in the
sanitized process log. The checked `E2MUpstreamIntelligenceMetricsMissing` rule
detects complete loss of the Core source metric. Configure the platform's
per-target `up` alert for every Core and Agent target as well; one healthy
target must not hide another missing Agent. Restore scraping before treating
the other alerts as healthy. Apart from this explicit telemetry-loss alert,
business rules do not use `absent` or substitute zero: absent telemetry is
unknown, never healthy.

## Cost attribution backlog

`e2m_upstream_cost_job_oldest_age_seconds` measures pending, processing or
retrying jobs. Check database availability and the attribution worker. A
missing/unknown token count must stay null and a missing group must not be
guessed. Retry the durable job; do not synthesize zero-cost facts. After
recovery, confirm the oldest age returns toward zero and cost fact versions
advance once per idempotent completion.

## Connector task backlog

Check authenticated Connector task polling/last-seen state, capability
declaration, stored protocol version, task status and the closed error code.
Pending/leased tasks older than the threshold may indicate an offline Connector
or a poisoned lease. Treat `executing` separately: it is a durable side-effect
permit, ignores the old `lease_until`, never auto-expires or retries, and freezes
its route-plan generation until terminal completion or explicit resolution. Do
not manually re-create a weighted mutation: recovery must reuse the durable
operation and generation fence so a late task cannot overwrite rollback.

## Protocol v3 / migration 0074 upgrade preflight

Protocol v3 adds a durable `leased -> executing` permit immediately before a
route-plan-scoped gateway side effect. The Connector must hold its local
scheduling-fence and write-receipt locks, call
`POST /api/v1/connectors/tasks/{id}/execute`, receive an exact immutable task
identity in `executing`, and only then call the gateway. A 409, uncertain Core
response or identity drift performs neither gateway work nor Complete.

Migration 0074 intentionally does not rewrite a stored protocol-v2 Connector
as v3. During a rolling deployment the database admits both versions. A v2
token remains usable for authentication/readback, but v2 cannot lease or
execute; its lease request returns 426 with no task. Only a genuine protocol-3
runtime handshake through `RecordConnectorSeen` atomically upgrades the stored
Connector and nested runtime version before leasing is allowed.

Before applying 0074:

1. Disable new automatic optimization and stop every protocol-v2 Connector.
   Process liveness, not lease expiry, is the evidence that an old binary can
   no longer start another remote write.
2. Query the sanitized task queue for `status='leased'` gateway mutation tasks
   that have a mandatory fence, a non-null top-level `input.fence`, or any
   legacy `input.spec.fence`. Do not export their raw payloads.
3. Only after every protocol-v2 process is confirmed stopped, reconcile each
   match against the gateway's authoritative state, owning durable operation
   and audit trail. Terminalize the task according to any confirmed outcome;
   requeue an intent as pending only when the remote evidence proves it was not
   applied and the current desired state still requires it. If the outcome
   remains uncertain, leave the task unresolved and abort the migration. Never
   infer safety from an expired `lease_until`: the old Connector may already
   have crossed the remote side-effect boundary.
4. Apply 0074. The migration raises
   `cannot upgrade while a protocol v2 fenced connector task is leased` and
   rolls its schema/data transaction back if any unresolved row remains. A
   rejected golang-migrate attempt may leave only migration metadata dirty;
   repair that metadata only after independently confirming the transaction
   rolled back and repeating the preflight.
5. Start actual v3 Connectors and confirm their authenticated heartbeat records
   protocol 3 before re-enabling automatic optimization. Verify a fenced task
   reaches `executing` only through the Core execution endpoint and that a
   generation change is rejected until it completes or is explicitly resolved.

Downgrade is also fail-closed: 0074 refuses to run down while any task is
`executing` or any protocol-v3 Connector row exists. Stop and reconcile those
executions, then explicitly remove/re-enroll v3 Connector identities under the
approved downgrade procedure; never relabel version 3 as version 2.

## Resolve an uncertain executing task

Use this recovery only when the Connector acquired a permit but timeout,
disconnect or an invalid gateway response makes the remote result uncertain.
An `executing` task is not a stuck lease: do not wait for `lease_until`, change
the route-plan generation, return it to pending, retry the gateway write, or
edit its row directly.

1. Disable new automatic optimization for the scope and stop the owning
   Connector so it cannot finish concurrently. Preserve task, rollout,
   receipt, audit and remote evidence.
2. Read the sanitized admin task summary and confirm `status=executing`, owner,
   instance, Connector, task type, plan/generation and intended business effect.
   The summary intentionally omits input, result and lease nonce; obtain the
   exact execution nonce only from the approved private incident evidence, and
   never paste it into a ticket or audit note.
3. Reconcile the gateway using its authoritative read API and an independent
   operator. Record a concise evidence note containing identifiers and
   conclusion only—no URL, credential, header, cookie, raw response or nonce.
4. A session-authenticated platform administrator calls
   `POST /api/v1/connector-tasks/{id}/resolve-execution` with the exact
   `lease_nonce`, safe `evidence_note`, and exactly one resolution:
   - `confirmed_applied`: include the strict typed result matching the original
     task; the task becomes `succeeded`;
   - `confirmed_not_applied`: include no result; the task becomes
     `failed/execution_abandoned`;
   - `connector_revoked_unverifiable`: only after the Connector is revoked and
     the outcome cannot be proved; include no result; the task becomes
     `failed/execution_outcome_unknown`.
5. A 409 means nonce/status/Connector state no longer matches; restart the
   investigation and do not retry with guessed data. A successful resolution
   atomically clears permit fields and appends an L3 critical audit. Confirm the
   audit contains `lease_nonce_sha256=sha256:<hex>` and does not contain the raw
   nonce. Resolution is terminal and never returns work to pending.
6. Only after the terminal task and audit agree may the generation owner retry
   the blocked publish/rollback from current desired state. Re-enable forward
   optimization only after read-back and normal rollout gates pass.

## Stale evidence

Identify affected sources through the admin intelligence board, not metric
labels. Check the source's poll schedule and allowlisted collection error code.
One failed source must not stop other sources. Recommendations based on stale
facts must expire/fail closed; exact baseline rollback remains allowed.

## Low comparable coverage

The ratio excludes incomplete, unknown, stale, expired or currency-less price
facts. Check source collection completeness, explicit source/channel links,
settlement currency and price dimensions. Do not improve the ratio by treating
unknown as zero or inferring a missing group, currency or mapping.

## Global ingest failure

`E2MUpstreamGlobalIngestFailure` requires all three conditions for five
minutes: at least one active source; every active source has a current
collection failure; and either the newest successful collection is more than
15 minutes old or every active source has never succeeded. The closed gauges
are `e2m_upstream_sources{state="active|failed"}`,
`e2m_upstream_collection_last_success_age_seconds` and
`e2m_upstream_collection_sources_without_success`. `failed` counts only active
sources whose current sanitized collection status is failed. The age series is
intentionally absent until a success exists; the exact without-success count
covers the all-new-source case without converting missing age to zero. A
partial or single-source failure does not meet the global predicate.

1. Keep ingest and its durable replay worker running, but disable new automatic
   optimization while evidence is aging. Do not delete the last successful
   snapshot, absence counters, batches or outbox files.
2. Confirm Core and every affected Agent scrape target are healthy before using
   the gauges. Check Connector scheduling, Core reachability, enrollment/auth,
   quotas, request-size rejection and the allowlisted collection result codes.
3. Verify independent sources continue to schedule. A source failure must not
   cancel the global scheduler, and failed or partial runs must never advance
   absence counters or produce removal events.
4. Recover through the durable outbox and idempotent batch identity. Do not
   manufacture a successful run or change `last_success_at` manually.
5. Close only after a real finalized successful collection advances the source
   fact version, the failed count falls below the active count, and freshness
   recovers. Missing gauges remain unknown and are not recovery evidence.

## Intelligence outbox backlog

`E2MUpstreamIntelligenceOutboxStuck` fires per Agent target when both
`e2m_upstream_intelligence_outbox_depth` is non-zero and
`e2m_upstream_intelligence_outbox_oldest_age_seconds` exceeds five minutes for
ten minutes. These gauges come from the Agent's private metrics listener, not
Core. They carry only collector-added target labels; they never carry owner,
source, run, URL or error labels.

Check the Agent's Core connectivity, authentication, ingest response class,
rate/quota rejection and replay cadence. Preserve the outbox file and its
permissions while investigating. Replay the persisted, checksum-verified
batches with their original run, batch, manifest and payload hashes; do not
copy payloads into a ticket, edit the queue by hand or enqueue a replacement
run. A `503` scrape means the queue could not be read safely and must be handled
as an integrity incident, not as depth zero. Close only after acknowledgements
remove the original batches and both accurately read gauges return to zero.

## False-removal invariant

`E2MUpstreamFalseRemovalInvariantViolation` fires on any five-minute increase
of `e2m_upstream_false_removal_invariant_violations_total`. The counter has no
source or owner label. It is a global durable database counter, so the rule
takes the maximum replica increase instead of summing identical Core scrapes.
Disable recommendation generation and new auto-apply,
preserve the current and two preceding complete snapshots, and identify the
affected scope through the sanitized audit trail rather than metric labels.

Confirm that only a finalized `coverage=complete` success advanced absence,
that two consecutive complete snapshots omitted the same comparison identity,
that failed/partial/pagination-limit runs did not advance it, and that run and
snapshot replay stayed idempotent. Quarantine unconsumed removal-driven
recommendations or events through normal lifecycle controls; do not delete
evidence or repair counters with ad-hoc SQL. Treat already-applied removal as a
data-integrity incident and use exact baseline rollback where traffic changed.
Close only after the invariant test and transactional finalization evidence
pass and affected derived views have been rebuilt from preserved facts.

## Credential leak detected

`E2MUpstreamCredentialLeakDetected` fires on any five-minute increase of
`e2m_upstream_security_events_total{kind="credential_leak_detected"}`. The
event means a Connector-sensitive value was rejected at a prohibited Core
boundary; the metric and sanitized logs must never contain the rejected value.
The counter is global and database-backed; the rule preserves `kind` but
de-duplicates Core scrape targets before comparing the increase.

Isolate the offending intake path, stop its further submissions and disable
new automatic optimization. Use sanitized request/audit identifiers to find
the Connector locally; never paste payloads, URLs, headers, cookies or tokens
into Prometheus, logs, tickets or chat. If a real credential crossed the trust
boundary, revoke and rotate it at the upstream and replace only the local
Connector secret. Inspect Core database, browser DTO, audit and telemetry
boundaries for persistence without querying or exporting raw secret values.
Close only after rotation, a clean boundary scan and a regression test proving
the same class is rejected and counted without disclosure.

## Cross-owner rejection anomaly

`E2MUpstreamCrossOwnerRejectionAnomaly` fires when
`e2m_upstream_security_events_total{kind="cross_owner_rejected"}` increases by
more than five within five minutes. The threshold distinguishes an isolated
fail-closed denial from a scan, broken client or authorization regression; the
counter has no owner, connector or request label. Multiple replicas expose the
same database counter, so the rule takes the maximum per-replica increase and
must never add those identical series together.

Confirm the increase is real and not a counter/exporter fault, then use the
authenticated sanitized audit trail to identify the caller. Check tenant scope
resolution before object lookup, ensure the response remains the documented
non-oracle denial, and inspect other upstream endpoints for the same pattern.
Rate-limit or revoke the caller credential when abuse is suspected. Do not add
owner labels to make triage easier. For a broken trusted client, stop it and fix
its scope selection before re-enabling it. Close only when the five-minute
increase remains below threshold and targeted cross-owner tests still prove no
read, mutation or existence disclosure.

## Blocked rollout

Read the rollout's closed reason codes and current stage. Unknown remote
weights, duplicate/missing binding accounts, weights outside 0..100, a baseline
total other than 100, a zero-weight source, unsupported native weight
capability, changed generation, failed gates or unverified read-back all block
forward progress. The captured baseline covers every managed binding on the
plan/instance. The target may already have non-zero weight, and unrelated
accounts may also be non-zero: the 10/25/50/100 stages move that percentage of
the source's original baseline to the target while preserving every unrelated
weight exactly. If any traffic was already changed, transition to
rollback-required and restore the complete baseline; never leave a partially
applied stage as a terminal blocked state.

After each write, wait until `observe_until` and require fresh five-minute
quality evidence for both participating channels with timestamps at or after
that boundary. A normal observation refresh may advance only the global
intelligence/link fact version, quality constraint evidence IDs, and their
derived fingerprint/timestamps. Mapping, cost ledger, offer/cost/wallet/link/
binding evidence, plan, dimensions, savings, policy and formula changes make
the recommendation stale and must block forward progress.

## Stuck rollout action

Check the operation lease and Connector task receipt. A crashed worker should
be reclaimed after the rollout operation lease expires and resume idempotently,
but a Connector task already in `executing` must never be reclaimed or retried.
Verify the rollout uses the shared `auto-switch/plan/<planID>` ordering scope
and the current route plan scheduling generation. Do not create an independent
fence namespace. A rollback request atomically supersedes a pending/running
forward operation, increments the operation version and clears its operation
lease before acquiring a newer plan generation. If that plan has an
`executing` Connector task, generation advancement must return conflict and the
rollout remains frozen until normal Complete or the three-state operator
resolution above. An old non-executing worker must fail renew/complete CAS and
must not write or publish after losing the operation lease/generation fence.
The metric tracks only each non-terminal rollout's latest operation; a
historical failure followed by a newer success, or an operation on a completed
or rolled-back rollout, must not keep this alert firing.

## Rollback failure

This is critical because traffic may remain partially shifted.

1. Keep auto-apply disabled but do not disable rollback processing.
2. Confirm the persisted baseline contains every original account weight,
   including explicit zero, and contains no unknown weight.
3. Confirm the rollback uses a newer shared plan generation and writes every
   baseline weight.
4. Require read-back equality for the entire account set, including unrelated
   accounts and explicit zeros, not only the two recommendation accounts.
   Persist the proof as `weight-set-sha256:<baseline fingerprint>` only after
   that full comparison succeeds.
5. Retry the durable rollback operation. If the Connector is offline, restore
   connectivity before retrying; do not bypass the fence with a direct gateway
   write unless the incident commander explicitly accepts losing ordering and
   records that exceptional action.
6. Close the incident only after read-back verification, durable rolled-back
   state and an audit receipt all agree.

The rollback read-back proves weight restoration only. Rollback callability and
quality remain `unknown`; never report them as passed without independent,
post-rollback health evidence.

In the admin DTO, `rollback_verified=true` additionally requires durable
`rolled_back` state, `stage=none`, `pending_stage=none`, matching recommendation,
baseline and scheduling generation, a valid fresh read-back window, unknown
callability/quality, and exactly one evidence ID equal to
`weight-set-sha256:<baseline fingerprint>`. `last_after_verified` is a forward-
stage health indicator and therefore remains false for rollback/StageNone.

For release, retain the disposable-PostgreSQL rollback-preemption output, not
only MemoryStore or static SQL evidence: a running forward lease must become
superseded with its lease cleared, its former owner must fail both renew and
complete, and exactly one rollback operation must hold the newer plan
generation. This behavioral database check passed in the 2026-07-26 local
evidence run; repeat it for each release candidate.

## Ingest capacity response

Core applies a fixed-window quota per owner, shared by every Connector and Core
replica. Defaults are one minute, 120 batches and 60,000 facts; override only
with `E2M_UPSTREAM_INTELLIGENCE_INGEST_WINDOW`,
`E2M_UPSTREAM_INTELLIGENCE_OWNER_BATCH_QUOTA`, and
`E2M_UPSTREAM_INTELLIGENCE_OWNER_FACT_QUOTA`. A rejected request returns 429,
code `upstream_intelligence_ingest_rate_limited`, and `Retry-After` for the
remaining fixed window. The same canonical run/batch/payload is free within a
window, and a durable ingest receipt makes later cross-window replay free.

Do not resolve sustained 429s by raising global payload or batch shape limits.
First identify a runaway Connector or accidental duplicate run IDs, compare
owner batch/fact rate with configured polling, and honor `Retry-After`. Capacity
window/key rows are transient: admission removes expired windows in bounded,
index-ordered pages; durable receipts remain the long-lived idempotency proof.
If these tables grow while ingest continues, inspect expiry-index usage and
database lock pressure before changing quotas.

## Failure drills and acceptance evidence

The executable drill script validates local state-machine and store behavior,
strict HTTP actions, receipt mismatch rejection, crash/replay recovery and
secret/URL boundary tests. Passing it is local implementation evidence, not a
formal UI-17 signoff. PostgreSQL, race execution and real source/gateway drills
are separate release evidence: do not claim them if a disposable DSN with an
explicit P95 budget, a working race toolchain, or real disposable upstream
credentials are unavailable. The UI-17 verification guide records the accepted
Linux-container race path for Windows hosts without CGO. Prometheus rule tests
are also separate evidence, but may be captured with either local `promtool` or
the pinned official image documented there.

The 2026-07-27 UI-17 signoff combines all of those independent gates; it does
not promote the portable drill alone. The accepted current-schema release
artifacts are the schema-4 three-source project
`e2m-ui17-intel-29204-cabbadca3aea` (evidence SHA-256
`7016982ae6eb2eb5c95661c108c8158a1c17e0cb08c23a5c3c2c6248747de63b`)
and schema-6 NewAPI project `e2m-ui17-newapi-20260727164048-f95a6076`
(evidence SHA-256
`56ee92f60406fd454f54e62ff7a12eddbcdea01175ca7c93f835e73f40bc0abf`).
Both use profile `release`, explicitly acknowledge `SourceFrozen`, report
`release_pass=true`, bind immutable images to the same 849-file canonical input
SHA-256 `ee8eba1066b55a578e938ffb27d23dc3228545e49738d4fd99955d489474281b`,
and prove exact resource cleanup, environment restoration and protected-stack
identity preservation. Browser, console, PostgreSQL, race, Prometheus, security
and portable-drill artifacts remain separate members of the evidence set. This
signoff is for the frozen local/disposable candidate, not production deployment
or successful traffic to a production model provider.

Use `.tmp/ui17-evidence/ui17-final-signoff-20260727/manifest.json` (SHA-256
`3fbc011f5013f457d1e10dfefc06987e35596b65d6e40a8c3a4303b27a1f1d3c`) as the
machine-readable entry point for that evidence set. Verify `sha256.txt` in the
same directory before following its artifact paths; do not substitute an older,
test-only, failed, skipped or release-ineligible run.

Capture for release:

- exact commands, exit codes, Go/Node/PostgreSQL/Prometheus versions;
- PostgreSQL 100-source/5,000-fact P95 and `EXPLAIN (ANALYZE, BUFFERS)` output;
- rule-test output;
- two external plus one owned disposable-source collection evidence;
- weighted 10/25/50/100 apply, regression-triggered rollback and exact read-back;
- protocol-v3 `leased -> executing -> terminal` evidence, execute-conflict zero
  side effect, generation-bump conflict, and a three-state manual-resolution
  drill whose audit exposes only the nonce hash;
- explicit list of skipped release gates (empty for this UI-17 signoff); track
  production deployment, HA and real-provider traffic as separate future gates.
