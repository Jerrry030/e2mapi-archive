package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RecommendationRolloutAccountWeight is one exact gateway-native integer
// weight. AccountID is Connector-local routing metadata: it is persisted for
// recovery but deliberately excluded from JSON responses.
type RecommendationRolloutAccountWeight struct {
	AccountID string `json:"-"`
	Weight    int    `json:"weight"`
}

// CanonicalRecommendationRolloutWeights validates and orders a complete
// weight snapshot. Explicit zeroes are retained; unknown or duplicate account
// identities fail closed.
func CanonicalRecommendationRolloutWeights(values []RecommendationRolloutAccountWeight) ([]RecommendationRolloutAccountWeight, error) {
	if len(values) < 2 || len(values) > 4096 {
		return nil, errors.New("recommendation rollout: invalid account weight count")
	}
	out := append([]RecommendationRolloutAccountWeight(nil), values...)
	total := 0
	for i := range out {
		out[i].AccountID = strings.TrimSpace(out[i].AccountID)
		if out[i].AccountID == "" || len(out[i].AccountID) > 256 || LooksLikeConnectorSensitiveValue(out[i].AccountID) || out[i].Weight < 0 || out[i].Weight > 100 {
			return nil, errors.New("recommendation rollout: invalid account weight")
		}
		total += out[i].Weight
	}
	if total != 100 {
		return nil, errors.New("recommendation rollout: account weights must total 100")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	for i := 1; i < len(out); i++ {
		if out[i-1].AccountID == out[i].AccountID {
			return nil, errors.New("recommendation rollout: duplicate account weight")
		}
	}
	return out, nil
}

// RecommendationRolloutBaselineFingerprint hashes the exact sorted account
// identities and numerical weights used for rollback. It never exposes those
// Connector-local identities in logs or API payloads.
func RecommendationRolloutBaselineFingerprint(values []RecommendationRolloutAccountWeight) (string, error) {
	canonical, err := CanonicalRecommendationRolloutWeights(values)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, value := range canonical {
		_, _ = hash.Write([]byte(strconv.Itoa(len(value.AccountID))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(value.AccountID))
		_, _ = hash.Write([]byte{'='})
		_, _ = hash.Write([]byte(strconv.Itoa(value.Weight)))
		_, _ = hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// RecommendationRollout is the durable execution envelope around the pure
// staged state machine. Remote account IDs and the full rollback baseline are
// never serialized by the public JSON encoder; HTTP handlers must additionally
// project this type rather than embedding it wholesale.
type RecommendationRollout struct {
	State RecommendationRolloutState `json:"state"`

	InstanceID                   string `json:"instance_id"`
	FromChannelID                string `json:"from_channel_id"`
	ToChannelID                  string `json:"to_channel_id"`
	RecommendationPlanGeneration int64  `json:"recommendation_plan_generation"`

	FromAccountID   string                               `json:"-"`
	ToAccountID     string                               `json:"-"`
	BaselineWeights []RecommendationRolloutAccountWeight `json:"-"`

	Version         int64     `json:"version"`
	LastOperationID string    `json:"last_operation_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type RecommendationRolloutOperationAction string

const (
	RecommendationRolloutOperationApplyStage RecommendationRolloutOperationAction = "apply_stage"
	RecommendationRolloutOperationRollback   RecommendationRolloutOperationAction = "rollback"
)

func IsRecommendationRolloutOperationAction(value RecommendationRolloutOperationAction) bool {
	return value == RecommendationRolloutOperationApplyStage || value == RecommendationRolloutOperationRollback
}

type RecommendationRolloutOperationStatus string

const (
	RecommendationRolloutOperationPending    RecommendationRolloutOperationStatus = "pending"
	RecommendationRolloutOperationRunning    RecommendationRolloutOperationStatus = "running"
	RecommendationRolloutOperationSucceeded  RecommendationRolloutOperationStatus = "succeeded"
	RecommendationRolloutOperationFailed     RecommendationRolloutOperationStatus = "failed"
	RecommendationRolloutOperationSuperseded RecommendationRolloutOperationStatus = "superseded"
)

func IsRecommendationRolloutOperationStatus(value RecommendationRolloutOperationStatus) bool {
	switch value {
	case RecommendationRolloutOperationPending, RecommendationRolloutOperationRunning,
		RecommendationRolloutOperationSucceeded, RecommendationRolloutOperationFailed,
		RecommendationRolloutOperationSuperseded:
		return true
	default:
		return false
	}
}

func (value RecommendationRolloutOperationStatus) Terminal() bool {
	return value == RecommendationRolloutOperationSucceeded || value == RecommendationRolloutOperationFailed || value == RecommendationRolloutOperationSuperseded
}

// RecommendationRolloutOperationErrorCode is intentionally allowlisted. Raw
// gateway/database errors, URLs and credentials must never enter durable state.
type RecommendationRolloutOperationErrorCode string

const (
	RecommendationRolloutOperationErrorNone                  RecommendationRolloutOperationErrorCode = ""
	RecommendationRolloutOperationErrorCapabilityUnsupported RecommendationRolloutOperationErrorCode = "capability_unsupported"
	RecommendationRolloutOperationErrorOwnershipLost         RecommendationRolloutOperationErrorCode = "ownership_lost"
	RecommendationRolloutOperationErrorPlanChanged           RecommendationRolloutOperationErrorCode = "plan_changed"
	RecommendationRolloutOperationErrorMappingInvalid        RecommendationRolloutOperationErrorCode = "mapping_invalid"
	RecommendationRolloutOperationErrorWeightUnknown         RecommendationRolloutOperationErrorCode = "weight_unknown"
	RecommendationRolloutOperationErrorBaselineChanged       RecommendationRolloutOperationErrorCode = "baseline_changed"
	RecommendationRolloutOperationErrorRevalidationBlocked   RecommendationRolloutOperationErrorCode = "revalidation_blocked"
	RecommendationRolloutOperationErrorGatewayUnavailable    RecommendationRolloutOperationErrorCode = "gateway_unavailable"
	RecommendationRolloutOperationErrorWriteFailed           RecommendationRolloutOperationErrorCode = "write_failed"
	RecommendationRolloutOperationErrorReadbackFailed        RecommendationRolloutOperationErrorCode = "readback_failed"
	RecommendationRolloutOperationErrorVerificationFailed    RecommendationRolloutOperationErrorCode = "verification_failed"
	RecommendationRolloutOperationErrorInternal              RecommendationRolloutOperationErrorCode = "internal_error"
)

func IsRecommendationRolloutOperationErrorCode(value RecommendationRolloutOperationErrorCode) bool {
	switch value {
	case RecommendationRolloutOperationErrorNone,
		RecommendationRolloutOperationErrorCapabilityUnsupported,
		RecommendationRolloutOperationErrorOwnershipLost,
		RecommendationRolloutOperationErrorPlanChanged,
		RecommendationRolloutOperationErrorMappingInvalid,
		RecommendationRolloutOperationErrorWeightUnknown,
		RecommendationRolloutOperationErrorBaselineChanged,
		RecommendationRolloutOperationErrorRevalidationBlocked,
		RecommendationRolloutOperationErrorGatewayUnavailable,
		RecommendationRolloutOperationErrorWriteFailed,
		RecommendationRolloutOperationErrorReadbackFailed,
		RecommendationRolloutOperationErrorVerificationFailed,
		RecommendationRolloutOperationErrorInternal:
		return true
	default:
		return false
	}
}

type RecommendationRolloutOperation struct {
	ID          string                                  `json:"id"`
	RolloutID   string                                  `json:"rollout_id"`
	UserID      int64                                   `json:"user_id"`
	PlanID      string                                  `json:"plan_id"`
	Action      RecommendationRolloutOperationAction    `json:"action"`
	TargetStage RecommendationRolloutStage              `json:"target_stage"`
	Status      RecommendationRolloutOperationStatus    `json:"status"`
	Attempts    int                                     `json:"attempts"`
	ErrorCode   RecommendationRolloutOperationErrorCode `json:"error_code,omitempty"`
	Version     int64                                   `json:"version"`
	LeaseOwner  string                                  `json:"-"`
	LeaseUntil  *time.Time                              `json:"lease_until,omitempty"`
	CreatedAt   time.Time                               `json:"created_at"`
	UpdatedAt   time.Time                               `json:"updated_at"`
}

type RecommendationRolloutFilter struct {
	UserID int64
	Status RecommendationRolloutStatus
	PlanID string
	Limit  int
}

// RecommendationRolloutCreate carries an already revalidated first staged
// decision. The store atomically proves ExpectedPlanGeneration, advances the
// plan generation, patches State to the claimed generation and inserts the
// first operation before returning.
type RecommendationRolloutCreate struct {
	Rollout                RecommendationRollout
	ExpectedPlanGeneration int64
	FirstAction            RecommendationRolloutOperationAction
	FirstTargetStage       RecommendationRolloutStage
}

type RecommendationRolloutCompletion struct {
	OperationID              string
	WorkerID                 string
	ExpectedOperationVersion int64
	ExpectedRolloutVersion   int64
	OperationStatus          RecommendationRolloutOperationStatus
	ErrorCode                RecommendationRolloutOperationErrorCode
	NextState                RecommendationRolloutState
}
