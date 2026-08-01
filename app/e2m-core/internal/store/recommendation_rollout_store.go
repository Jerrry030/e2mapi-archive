package store

import (
	"context"
	"time"

	"e2m.local/contracts"
)

// RecommendationRolloutStore is the durable execution boundary for staged
// recommendation traffic. Generation ownership, operation enqueue and every
// worker completion are atomic store transitions.
type RecommendationRolloutStore interface {
	CreateRecommendationRollout(context.Context, contracts.RecommendationRolloutCreate) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error)
	GetRecommendationRollout(context.Context, string) (contracts.RecommendationRollout, error)
	ListRecommendationRollouts(context.Context, contracts.RecommendationRolloutFilter) ([]contracts.RecommendationRollout, error)
	ListRecommendationRolloutOperations(context.Context, string) ([]contracts.RecommendationRolloutOperation, error)
	TransitionRecommendationRolloutState(context.Context, string, int64, contracts.RecommendationRolloutState) (contracts.RecommendationRollout, error)
	EnqueueRecommendationRolloutOperation(context.Context, string, int64, contracts.RecommendationRolloutState, contracts.RecommendationRolloutOperationAction, contracts.RecommendationRolloutStage) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error)
	ClaimRecommendationRolloutOperation(context.Context, string, time.Duration) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, bool, error)
	RenewRecommendationRolloutOperation(context.Context, string, string, int64, time.Duration) (contracts.RecommendationRolloutOperation, error)
	CompleteRecommendationRolloutOperation(context.Context, contracts.RecommendationRolloutCompletion) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error)
}

var (
	_ RecommendationRolloutStore = (*MemoryStore)(nil)
)
