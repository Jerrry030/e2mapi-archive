# A6API Competitive Capability Matrix

Date: 2026-07-28

## Scope and evidence

This comparison uses the public `https://a6api.com/models` page observed on
2026-07-28 and A6API's unauthenticated `GET /api/status` announcements/FAQ.
The market loaded 1,712 merchant-model rows at the observation time. The public
page exposed model, billing mode, sort and display-currency controls plus input,
cached-input and output prices, live multiplier, last-100-call success rate,
latency, tags, recent success, fixed-merchant and personal-route actions.

Anonymous access cannot prove authenticated routing execution or private model
catalog APIs: `GET /api/models` returned `401`. Protocol and routing statements
below are therefore public product claims, not an audit of A6API's internal
implementation, reliability or security controls. Counts and live metrics are
a point-in-time observation rather than production-SLO evidence. Comparative
assessments below are necessarily asymmetric: E2M is code-inspected, while
A6API is assessed only from its public surface and public product statements.

## Capability matrix

| Capability | A6API public evidence | E2M current state | Assessment |
| --- | --- | --- | --- |
| Market breadth and discovery | 1,712 merchant-model rows displayed at observation time; brand/model/billing/currency filters and pagination | Owner-scoped model aggregation from collected upstream facts; no global exchange catalog | A6API leads in marketplace breadth; E2M now partially closes the owner decision-surface gap |
| Price comparison | Input/cache/output or per-request prices and real-time multiplier | Exact canonical-decimal effective-cost ranges by dimension/currency/unit; unknown, stale and incomparable evidence is excluded | E2M is stronger on evidence honesty and unit safety; A6API is faster to scan |
| Quality signals | Last-100-call success, latency, recent success, status/tags | Verified five-minute instance observations: success, TTFT P95, total-duration P95, sample count, health and freshness | E2M exposes tail/P95 metrics, provenance and instance relevance; A6API exposes a simpler latency signal, so the values are not directly comparable |
| User routing control | Fixed merchant; token+model personal pool; manual or multiplier-based auto inclusion; optional platform fallback | Four owner choices (`smart_auto`, `price_first`, `speed_first`, `success_first`) mapped to internal balanced/cost/latency/stability strategies, plus automatic health switching | A6API leads on per-model and per-token merchant selection; E2M currently applies preference at owner scope |
| Route precedence | Fixed merchant → personal pool → platform smart routing → system default | Pool admission, published plans, automatic switching and policy preference; no equivalent model-level precedence editor | Highest-value remaining product gap |
| Failure handling | Public claims for channel probing, cooldown, switching and fallback | Health state machine, fail-closed empty pool, capability-specific recovery, 10/25/50/100 rollout, generation fence, lease/CAS, verification and atomic rollback | E2M has a deeper documented and auditable control loop; this is not production-SLO proof |
| Decision integrity | User-facing metrics and tags; public material does not expose evidence lineage | Fact version, freshness, completeness, evidence links, exact decimals, Pareto and no cross-route metric splicing | E2M structural advantage |
| Credentials and topology | Not verifiable anonymously | Connector-local admin/source credentials, Vault references, anonymous owner-market DTO, strict owner isolation | E2M structural advantage |
| Governance | Not verifiable anonymously | RBAC, approvals, operation audits, durable tasks, uncertain-result freeze and operator recovery | E2M structural advantage |
| Protocol/data plane | OpenAI compatible plus Claude/Gemini native; image/audio/rerank/realtime and task APIs are publicly documented | Intentionally a sidecar control plane; does not proxy inference requests | Different product boundary; do not copy this into Core |
| Commerce | Supplier onboarding, recharge/balance, metering, settlement and traffic sharing are publicly advertised | Commercial code exists only behind disabled preview flags; no production wallet/debit/settlement loop | A6API leads; this is not the immediate managed-pool MVP objective |
| SLO transparency | Live success/latency/recent-success signals | Rich operational facts, but no owner-facing SLO/error-budget product | Important next step for leadership |

## What to learn first

1. Make the decision surface model-first. A6API lets a user start with the model
   and immediately see price, quality and available actions. E2M had richer
   facts but kept them in an administrator intelligence workbench. The new
   owner model market is the first correction.
2. Add model-scoped routing intent. The next iteration should let an owner set a
   preference and fallback policy for a model (and later a delivery key), while
   compiling that intent into E2M's existing guarded route-plan machinery.
3. Turn evidence into an SLO contract. Show target success/TTFT, error-budget
   burn and the exact fallback/rollback consequence. This would surpass a live
   percentage by making reliability actionable and auditable.
4. Publish a protocol-capability catalog. E2M should report which managed
   gateway supports each model/protocol/task and the verification level without
   becoming the inference proxy itself.
5. Preserve the architectural advantage. Do not copy A6API's centralized data
   plane into Core. E2M should win through private topology, verified local
   evidence and reversible automation rather than through opaque request
   intermediation.

## Implemented in this change

- `GET /api/v1/owner/model-market`, strictly scoped to the current owner and
  filtered by `q`, `price_dimension` and bounded `limit`.
- Anonymous model aggregation with `ready`, `price_only` and
  `insufficient_evidence` states, comparable price ranges, quality/Pareto counts
  and one same-route quality record; internal identities and unknown values are
  not leaked or synthesized.
- Owner-only `/model-market` page with server-side model search, evidence/status
  filters, dimension/currency/unit-safe price sorting, quality/latency sorting,
  responsive cards, bilingual copy and the existing routing-preference control.

## P1 and P2 path to comprehensive leadership

P1: model-scoped preferences and explicit fallback tiers; SLO/error-budget
cards and policy gates; protocol/task capability catalog; saved comparisons and
decision explanations tied to fact versions.

P2: delivery-key-scoped policy overrides; capacity/concurrency and cache-quality
constraints; outcome-based recommendation evaluation; tenant-exportable audit
reports. Commerce and a public global marketplace should remain a separate
business decision because they introduce custody, settlement, fraud and data-
plane obligations rather than merely extending the current control plane.
