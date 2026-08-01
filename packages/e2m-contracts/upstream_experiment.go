package contracts

import "time"

const UpstreamExperimentActionHashVersionV1 = "upstream-action-set-v1"

type UpstreamShadowCandidate struct {
	UserID             int64                              `json:"user_id"`
	SourceID           string                             `json:"source_id"`
	ChannelID          string                             `json:"channel_id"`
	GroupKey           string                             `json:"group_key"`
	ModelKey           string                             `json:"model_key"`
	PriceDimension     UpstreamPriceDimension             `json:"price_dimension"`
	SettlementCurrency string                             `json:"settlement_currency"`
	PerTokens          int64                              `json:"per_tokens"`
	Cost               CanonicalDecimal                   `json:"cost"`
	QualityScore       CanonicalDecimal                   `json:"quality_score"`
	Constraints        []UpstreamRecommendationConstraint `json:"constraints"`
	EvidenceIDs        []string                           `json:"evidence_ids"`
}

type UpstreamShadowResult struct {
	ID                        string                    `json:"id"`
	UserID                    int64                     `json:"user_id"`
	RecommendationID          string                    `json:"recommendation_id"`
	RecommendationFingerprint string                    `json:"recommendation_fingerprint"`
	Winner                    UpstreamShadowCandidate   `json:"winner"`
	Ranking                   []UpstreamShadowCandidate `json:"ranking"`
	EvidenceIDs               []string                  `json:"evidence_ids"`
	EvaluatedAt               time.Time                 `json:"evaluated_at"`
}

// UpstreamDryRunResult is persistence-boundary friendly: the action hash is
// canonical and stable, while ReconcilePlan retains the exact operator diff.
type UpstreamDryRunResult struct {
	ID                        string           `json:"id"`
	UserID                    int64            `json:"user_id"`
	RecommendationID          string           `json:"recommendation_id"`
	RecommendationFingerprint string           `json:"recommendation_fingerprint"`
	IntelligenceFactVersion   int64            `json:"intelligence_fact_version"`
	CostLedgerFactVersion     int64            `json:"cost_ledger_fact_version"`
	LinkFactVersion           int64            `json:"link_fact_version"`
	PlanGeneration            int64            `json:"plan_generation"`
	PlanID                    string           `json:"plan_id"`
	FromChannelID             string           `json:"from_channel_id"`
	ToChannelID               string           `json:"to_channel_id"`
	DesiredScheduling         map[string]bool  `json:"desired_scheduling"`
	ReconcileKind             ReconcileRunKind `json:"reconcile_kind"`
	Plan                      ReconcilePlan    `json:"plan"`
	ActionHashVersion         string           `json:"action_hash_version"`
	ActionSetHash             string           `json:"action_set_hash"`
	CreatedAt                 time.Time        `json:"created_at"`
}

type UpstreamDryRunCurrent struct {
	UserID                    int64           `json:"user_id"`
	RecommendationID          string          `json:"recommendation_id"`
	RecommendationFingerprint string          `json:"recommendation_fingerprint"`
	IntelligenceFactVersion   int64           `json:"intelligence_fact_version"`
	CostLedgerFactVersion     int64           `json:"cost_ledger_fact_version"`
	LinkFactVersion           int64           `json:"link_fact_version"`
	PlanGeneration            int64           `json:"plan_generation"`
	PlanID                    string          `json:"plan_id"`
	FromChannelID             string          `json:"from_channel_id"`
	ToChannelID               string          `json:"to_channel_id"`
	DesiredScheduling         map[string]bool `json:"desired_scheduling"`
	Plan                      ReconcilePlan   `json:"plan"`
}

type UpstreamDryRunStaleReason string

const (
	UpstreamDryRunStaleInvalidCurrent      UpstreamDryRunStaleReason = "invalid_current"
	UpstreamDryRunStaleOwnerScope          UpstreamDryRunStaleReason = "owner_scope_changed"
	UpstreamDryRunStaleFingerprint         UpstreamDryRunStaleReason = "recommendation_fingerprint_changed"
	UpstreamDryRunStaleIntelligenceVersion UpstreamDryRunStaleReason = "intelligence_fact_version_changed"
	UpstreamDryRunStaleCostVersion         UpstreamDryRunStaleReason = "cost_ledger_fact_version_changed"
	UpstreamDryRunStaleLinkVersion         UpstreamDryRunStaleReason = "link_fact_version_changed"
	UpstreamDryRunStalePlanGeneration      UpstreamDryRunStaleReason = "plan_generation_changed"
	UpstreamDryRunStaleIntent              UpstreamDryRunStaleReason = "scheduling_intent_changed"
	UpstreamDryRunStaleActionSet           UpstreamDryRunStaleReason = "action_set_changed"
)

type UpstreamDryRunValidity struct {
	Current bool                        `json:"current"`
	Reasons []UpstreamDryRunStaleReason `json:"reasons"`
}
