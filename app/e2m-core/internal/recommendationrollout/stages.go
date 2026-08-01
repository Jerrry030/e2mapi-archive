package recommendationrollout

import (
	"errors"

	"e2m.local/contracts"
)

var forwardRolloutStages = [...]contracts.RecommendationRolloutStage{
	contracts.RecommendationRolloutStage10,
	contracts.RecommendationRolloutStage25,
	contracts.RecommendationRolloutStage50,
	contracts.RecommendationRolloutStage100,
}

// sourceBaselineMoved converts a rollout stage into the integer amount moved
// from the source's original baseline. All callers use the same round-half-up
// rule, and 100% always drains the source exactly.
func sourceBaselineMoved(sourceWeight int, stage contracts.RecommendationRolloutStage) (int, error) {
	if sourceWeight <= 0 || sourceWeight > 100 ||
		stage != contracts.RecommendationRolloutStage10 && stage != contracts.RecommendationRolloutStage25 &&
			stage != contracts.RecommendationRolloutStage50 && stage != contracts.RecommendationRolloutStage100 {
		return 0, errors.New("invalid source baseline or rollout stage")
	}
	if stage == contracts.RecommendationRolloutStage100 {
		return sourceWeight, nil
	}
	return (sourceWeight*int(stage) + 50) / 100, nil
}

// sourceBaselineSupportsEveryStage rejects integer baselines for which a
// nominal stage would move no traffic or repeat the preceding stage.
func sourceBaselineSupportsEveryStage(sourceWeight int) bool {
	previous := 0
	for _, stage := range forwardRolloutStages {
		moved, err := sourceBaselineMoved(sourceWeight, stage)
		if err != nil || moved <= previous {
			return false
		}
		previous = moved
	}
	return true
}
