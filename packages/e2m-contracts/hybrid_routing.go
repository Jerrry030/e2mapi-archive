package contracts

import (
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	// HybridRoutingExecutionScope is the only non-RoutePlan Connector execution
	// domain accepted by Core. ExecutionID is the managed instance id and the
	// generation is advanced whenever a new three-pool application takes over.
	HybridRoutingExecutionScope = "hybrid_routing"
	hybridRoutingFencePrefix    = "hybrid-routing/instance/"
)

// HybridRoutingFenceScope gives every account a stable local ordering scope.
// Per-account scopes let a crashed worker replay an already-applied account at
// sequence one while a newer generation still supersedes the old account
// intent. Core additionally fences the whole instance generation.
func HybridRoutingFenceScope(instanceID, accountID string) string {
	instanceID, instanceOK := normalizeConnectorIdentifier(strings.TrimSpace(instanceID), MaxConnectorIdentifierBytes)
	accountID, accountOK := normalizeConnectorIdentifier(strings.TrimSpace(accountID), MaxConnectorIdentifierBytes)
	if !instanceOK || !accountOK {
		return ""
	}
	scope := hybridRoutingFencePrefix + instanceID + "/account/" + accountID
	if len(scope) > 256 {
		return ""
	}
	return scope
}

// GatewaySchedulingFenceHybridIdentity extracts the stable instance/account
// identity of a Hybrid routing fence. The tuple is deliberately disjoint from
// all RoutePlan prefixes.
func GatewaySchedulingFenceHybridIdentity(fence GatewaySchedulingFence) (instanceID, accountID string, ok bool) {
	if !IsGatewaySchedulingFence(fence) || !strings.HasPrefix(fence.Scope, hybridRoutingFencePrefix) {
		return "", "", false
	}
	remainder := strings.TrimPrefix(fence.Scope, hybridRoutingFencePrefix)
	parts := strings.Split(remainder, "/account/")
	if len(parts) != 2 || HybridRoutingFenceScope(parts[0], parts[1]) != fence.Scope {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ValidHybridRoutingExecutionIdentity validates the Core-owned identity that
// accompanies a Hybrid per-account fence. The execution id is opaque; Store
// validates its user, instance and current generation against the durable
// HybridRoutingExecution row before admitting a task.
func ValidHybridRoutingExecutionIdentity(identity ConnectorExecutionIdentity) bool {
	id, ok := normalizeConnectorIdentifier(strings.TrimSpace(identity.ID), MaxConnectorIdentifierBytes)
	return identity.Scope == HybridRoutingExecutionScope && ok && id == identity.ID && identity.Generation > 0
}

type HybridGatewayBindingStatus string

const (
	HybridGatewayBindingPending    HybridGatewayBindingStatus = "pending"
	HybridGatewayBindingInstalling HybridGatewayBindingStatus = "installing"
	HybridGatewayBindingReady      HybridGatewayBindingStatus = "ready"
	HybridGatewayBindingError      HybridGatewayBindingStatus = "error"
)

func (status HybridGatewayBindingStatus) Valid() bool {
	switch status {
	case HybridGatewayBindingPending, HybridGatewayBindingInstalling, HybridGatewayBindingReady, HybridGatewayBindingError:
		return true
	default:
		return false
	}
}

// HybridGatewayBinding is the non-secret paper trail for one of the two E2M
// aggregate entries installed into an owner's NewAPI instance. The real
// virtual key stays in Core's Vault and the installed value stays Connector-
// local; only opaque identities and versions are persisted here.
type HybridGatewayBinding struct {
	ID                  string                     `json:"id"`
	UserID              int64                      `json:"user_id"`
	InstanceID          string                     `json:"instance_id"`
	ResourceClass       ResourceClass              `json:"resource_class"`
	ConnectorID         string                     `json:"connector_id"`
	CredentialBindingID string                     `json:"credential_binding_id"`
	RemoteAccountID     string                     `json:"remote_account_id,omitempty"`
	VirtualKeyID        string                     `json:"virtual_key_id"`
	VirtualKeyVersion   int64                      `json:"virtual_key_version"`
	Status              HybridGatewayBindingStatus `json:"status"`
	ErrorCode           string                     `json:"error_code,omitempty"`
	Version             int64                      `json:"version"`
	CreatedAt           time.Time                  `json:"created_at"`
	UpdatedAt           time.Time                  `json:"updated_at"`
}

func ValidHybridGatewayBinding(binding HybridGatewayBinding) bool {
	if strings.TrimSpace(binding.ID) == "" || binding.UserID <= 0 ||
		strings.TrimSpace(binding.InstanceID) == "" || !binding.ResourceClass.IsPlatformSupply() ||
		strings.TrimSpace(binding.ConnectorID) == "" || strings.TrimSpace(binding.CredentialBindingID) == "" ||
		strings.TrimSpace(binding.VirtualKeyID) == "" || binding.VirtualKeyVersion <= 0 || !binding.Status.Valid() ||
		binding.Version < 0 || len(strings.TrimSpace(binding.ErrorCode)) > 64 {
		return false
	}
	if binding.Status == HybridGatewayBindingReady && strings.TrimSpace(binding.RemoteAccountID) == "" {
		return false
	}
	for _, value := range []string{binding.ID, binding.InstanceID, binding.ConnectorID, binding.CredentialBindingID, binding.RemoteAccountID, binding.VirtualKeyID} {
		if value != "" && LooksLikeConnectorSensitiveValue(value) {
			return false
		}
	}
	return true
}

type HybridGatewayBindingRequest struct {
	VirtualKeyID    string `json:"virtual_key_id"`
	ExpectedVersion int64  `json:"expected_version"`
}

type HybridRoutingExecutionStatus string

const (
	HybridRoutingExecutionPending   HybridRoutingExecutionStatus = "pending"
	HybridRoutingExecutionApplying  HybridRoutingExecutionStatus = "applying"
	HybridRoutingExecutionSucceeded HybridRoutingExecutionStatus = "succeeded"
	HybridRoutingExecutionFailed    HybridRoutingExecutionStatus = "failed"
)

func (status HybridRoutingExecutionStatus) Valid() bool {
	switch status {
	case HybridRoutingExecutionPending, HybridRoutingExecutionApplying, HybridRoutingExecutionSucceeded, HybridRoutingExecutionFailed:
		return true
	default:
		return false
	}
}

type HybridAccountWeight struct {
	AccountID string        `json:"account_id"`
	Class     ResourceClass `json:"resource_class"`
	// Weight is the exact NewAPI native weight. NewAPI samples enabled
	// channels by Weight+10, so this is not itself a percentage.
	Weight      int  `json:"weight"`
	Schedulable bool `json:"schedulable"`
}

// HybridRoutingExecution is one durable application of an allocation version.
// Target is the owner intent, Effective is the capacity/budget adjusted plan,
// DesiredWeights is the exact per-account integer write set, and Actual is
// populated only from Connector read-back.
type HybridRoutingExecution struct {
	ID                string                       `json:"id"`
	UserID            int64                        `json:"user_id"`
	InstanceID        string                       `json:"instance_id"`
	AllocationVersion int64                        `json:"allocation_version"`
	Generation        int64                        `json:"generation"`
	Model             string                       `json:"model,omitempty"`
	Status            HybridRoutingExecutionStatus `json:"status"`
	Target            map[ResourceClass]int        `json:"target"`
	Effective         map[ResourceClass]int        `json:"effective"`
	Actual            map[ResourceClass]int        `json:"actual"`
	DesiredWeights    []HybridAccountWeight        `json:"desired_weights"`
	AdjustmentCodes   []string                     `json:"adjustment_codes"`
	ErrorCode         string                       `json:"error_code,omitempty"`
	LeaseOwner        string                       `json:"-"`
	LeaseUntil        *time.Time                   `json:"-"`
	Attempts          int                          `json:"attempts"`
	Version           int64                        `json:"version"`
	CreatedAt         time.Time                    `json:"created_at"`
	UpdatedAt         time.Time                    `json:"updated_at"`
	CompletedAt       *time.Time                   `json:"completed_at,omitempty"`
}

type HybridRoutingExecutionRequest struct {
	Model                     string `json:"model,omitempty"`
	ExpectedAllocationVersion int64  `json:"expected_allocation_version"`
}

// HybridRoutingExecutionPlan is persisted before the first remote write. A
// reclaimed worker therefore replays the exact same write set instead of
// recompiling it from newer capacity observations after a partial apply.
type HybridRoutingExecutionPlan struct {
	ID              string                `json:"id"`
	WorkerID        string                `json:"-"`
	ExpectedVersion int64                 `json:"expected_version"`
	Target          map[ResourceClass]int `json:"target"`
	Effective       map[ResourceClass]int `json:"effective"`
	DesiredWeights  []HybridAccountWeight `json:"desired_weights"`
	AdjustmentCodes []string              `json:"adjustment_codes"`
}

// HybridRoutingExecutionCompletion terminalizes the exact live worker lease.
// Successful executions must carry a complete read-back; failures expose only
// a bounded machine code and retain their persisted desired write set.
type HybridRoutingExecutionCompletion struct {
	ID              string `json:"id"`
	WorkerID        string `json:"-"`
	ExpectedVersion int64  `json:"expected_version"`
	Succeeded       bool   `json:"succeeded"`
	// ReadBackWeights is the complete trusted Connector read-back. Store
	// requires exact account/class/weight equality with the persisted write set
	// and derives the public Actual map itself.
	ReadBackWeights []HybridAccountWeight `json:"-"`
	ErrorCode       string                `json:"error_code,omitempty"`
}

// HybridWeightAccount is the pure compiler input. The caller determines the
// resource class from trusted binding/ownership state, never from a gateway
// display name. CurrentWeight=nil is an explicitly unknown owner weight.
// Schedulable is the current remote state, not durable pool membership. The
// caller supplies only owner accounts that are currently eligible or whose
// membership was proven by a prior Hybrid plan; those members may therefore
// be re-enabled after a previous allocation reduced the owner pool to zero.
type HybridWeightAccount struct {
	AccountID     string
	Class         ResourceClass
	CurrentWeight *int
	Schedulable   bool
}

var ErrHybridWeightsUnrepresentable = errors.New("hybrid routing weights are unrepresentable")

// CompileHybridAccountWeights compiles three resource percentages into exact
// NewAPI native weights and scheduling states. NewAPI samples every enabled
// channel by native Weight+10; disabled entries contribute no traffic. The
// compiler therefore enables only positive logical shares, reverse-solves one
// common integer sampling total, and fails closed when the requested ratio
// cannot be represented exactly inside NewAPI's 0..100 native range.
func CompileHybridAccountWeights(effective map[ResourceClass]int, accounts []HybridWeightAccount) ([]HybridAccountWeight, error) {
	if !validCompleteHybridPercentMap(effective) || len(accounts) > MaxConnectorAccounts {
		return nil, ErrHybridWeightsUnrepresentable
	}
	byClass := map[ResourceClass][]HybridWeightAccount{
		ResourceClassOwner: {}, ResourceClassEconomy: {}, ResourceClassStable: {},
	}
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		account.AccountID = strings.TrimSpace(account.AccountID)
		if account.AccountID == "" || !account.Class.Valid() {
			return nil, ErrHybridWeightsUnrepresentable
		}
		if _, duplicate := seen[account.AccountID]; duplicate {
			return nil, ErrHybridWeightsUnrepresentable
		}
		seen[account.AccountID] = struct{}{}
		byClass[account.Class] = append(byClass[account.Class], account)
	}
	if len(byClass[ResourceClassEconomy]) != 1 || len(byClass[ResourceClassStable]) != 1 ||
		effective[ResourceClassOwner] > 0 && len(byClass[ResourceClassOwner]) == 0 {
		return nil, ErrHybridWeightsUnrepresentable
	}

	owners := byClass[ResourceClassOwner]
	counts := map[ResourceClass]int{
		ResourceClassOwner:   len(owners),
		ResourceClassEconomy: boolIntHybrid(effective[ResourceClassEconomy] > 0),
		ResourceClassStable:  boolIntHybrid(effective[ResourceClassStable] > 0),
	}
	totalUnits, classUnits, err := compileNewAPIClassUnits(effective, counts)
	if err != nil || totalUnits <= 0 {
		return nil, ErrHybridWeightsUnrepresentable
	}
	ownerUnits, err := distributeHybridOwnerUnits(classUnits[ResourceClassOwner], owners)
	if err != nil {
		return nil, err
	}
	out := make([]HybridAccountWeight, 0, len(accounts))
	for _, class := range []ResourceClass{ResourceClassEconomy, ResourceClassStable} {
		account := byClass[class][0]
		schedulable := effective[class] > 0
		weight := dormantHybridWeight(account)
		if schedulable {
			weight = classUnits[class] - 10
		}
		out = append(out, HybridAccountWeight{AccountID: account.AccountID, Class: class, Weight: weight, Schedulable: schedulable})
	}
	for _, owner := range owners {
		weight, schedulable := dormantHybridWeight(owner), false
		if effective[ResourceClassOwner] > 0 {
			units, exists := ownerUnits[owner.AccountID]
			if !exists {
				return nil, ErrHybridWeightsUnrepresentable
			}
			weight, schedulable = units-10, true
		}
		out = append(out, HybridAccountWeight{AccountID: owner.AccountID, Class: ResourceClassOwner, Weight: weight, Schedulable: schedulable})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out, nil
}

func compileNewAPIClassUnits(effective map[ResourceClass]int, counts map[ResourceClass]int) (int, map[ResourceClass]int, error) {
	positive := 0
	step, lower, upper := int64(1), int64(1), int64(1<<30)
	for _, class := range []ResourceClass{ResourceClassOwner, ResourceClassEconomy, ResourceClassStable} {
		percent, count := effective[class], counts[class]
		if percent == 0 {
			if count != 0 && class != ResourceClassOwner {
				return 0, nil, ErrHybridWeightsUnrepresentable
			}
			continue
		}
		if count <= 0 {
			return 0, nil, ErrHybridWeightsUnrepresentable
		}
		positive++
		step = lcmHybrid(step, int64(100/gcdHybrid(percent, 100)))
		if candidate := (1000*int64(count) + int64(percent) - 1) / int64(percent); candidate > lower {
			lower = candidate
		}
		if candidate := 11000 * int64(count) / int64(percent); candidate < upper {
			upper = candidate
		}
	}
	if positive == 0 || lower > upper {
		return 0, nil, ErrHybridWeightsUnrepresentable
	}
	totalUnits := ((lower + step - 1) / step) * step
	if totalUnits > upper {
		return 0, nil, ErrHybridWeightsUnrepresentable
	}
	units := map[ResourceClass]int{ResourceClassOwner: 0, ResourceClassEconomy: 0, ResourceClassStable: 0}
	for _, class := range []ResourceClass{ResourceClassOwner, ResourceClassEconomy, ResourceClassStable} {
		numerator := int64(effective[class]) * totalUnits
		if numerator%100 != 0 {
			return 0, nil, ErrHybridWeightsUnrepresentable
		}
		units[class] = int(numerator / 100)
	}
	return int(totalUnits), units, nil
}

func distributeHybridOwnerUnits(total int, owners []HybridWeightAccount) (map[string]int, error) {
	if total == 0 {
		return map[string]int{}, nil
	}
	if len(owners) == 0 || total < 10*len(owners) || total > 110*len(owners) {
		return nil, ErrHybridWeightsUnrepresentable
	}
	type candidate struct {
		account   HybridWeightAccount
		base      int
		units     int
		remainder int
	}
	values := make([]candidate, len(owners))
	allKnown := true
	for index, owner := range owners {
		base := 1
		if owner.CurrentWeight == nil || *owner.CurrentWeight < 0 || *owner.CurrentWeight > 100 {
			allKnown = false
		} else {
			base = *owner.CurrentWeight + 10
		}
		values[index] = candidate{account: owner, base: base}
	}
	if !allKnown {
		for index := range values {
			values[index].base = 1
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].account.AccountID < values[j].account.AccountID })
	remaining := total
	active := make([]int, len(values))
	for index := range values {
		active[index] = index
	}
	for len(active) > 0 {
		baseTotal := 0
		for _, index := range active {
			baseTotal += values[index].base
		}
		clamped := make(map[int]int)
		for _, index := range active {
			numerator := remaining * values[index].base
			if numerator < 10*baseTotal {
				clamped[index] = 10
			} else if numerator > 110*baseTotal {
				clamped[index] = 110
			}
		}
		if len(clamped) > 0 {
			next := active[:0]
			for _, index := range active {
				if units, ok := clamped[index]; ok {
					values[index].units = units
					remaining -= units
				} else {
					next = append(next, index)
				}
			}
			if remaining < 0 {
				return nil, ErrHybridWeightsUnrepresentable
			}
			active = next
			continue
		}
		assigned := 0
		for _, index := range active {
			numerator := remaining * values[index].base
			values[index].units = numerator / baseTotal
			values[index].remainder = numerator % baseTotal
			assigned += values[index].units
		}
		extra := remaining - assigned
		sort.SliceStable(active, func(i, j int) bool {
			left, right := values[active[i]], values[active[j]]
			if left.remainder != right.remainder {
				return left.remainder > right.remainder
			}
			return left.account.AccountID < right.account.AccountID
		})
		for index := 0; index < extra; index++ {
			values[active[index]].units++
		}
		active = nil
	}
	out := make(map[string]int, len(values))
	assigned := 0
	for _, value := range values {
		if value.units < 10 || value.units > 110 {
			return nil, ErrHybridWeightsUnrepresentable
		}
		out[value.account.AccountID] = value.units
		assigned += value.units
	}
	if assigned != total {
		return nil, ErrHybridWeightsUnrepresentable
	}
	return out, nil
}

func dormantHybridWeight(account HybridWeightAccount) int {
	if account.CurrentWeight != nil && *account.CurrentWeight >= 0 && *account.CurrentWeight <= 100 {
		return *account.CurrentWeight
	}
	return 0
}

func gcdHybrid(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}

func lcmHybrid(left, right int64) int64 {
	originalLeft, originalRight := left, right
	for right != 0 {
		left, right = right, left%right
	}
	return originalLeft / left * originalRight
}

func boolIntHybrid(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validCompleteHybridPercentMap(values map[ResourceClass]int) bool {
	if len(values) != 3 {
		return false
	}
	total := 0
	for _, class := range []ResourceClass{ResourceClassOwner, ResourceClassEconomy, ResourceClassStable} {
		value, exists := values[class]
		if !exists || value < 0 || value > 100 {
			return false
		}
		total += value
	}
	return total == 100
}

// ValidCompleteHybridPercentMap is the persistence/API boundary validator for
// an explicit owner/economy/stable percentage map. Numeric zero must be
// present; extra resource classes fail closed.
func ValidCompleteHybridPercentMap(values map[ResourceClass]int) bool {
	return validCompleteHybridPercentMap(values)
}

// ValidHybridAccountWeights validates a complete native NewAPI write set and
// proves that its enabled Weight+10 sampling units resolve to exact integer
// percentages. Disabled accounts may retain a native weight, but contribute no
// traffic until explicitly enabled.
func ValidHybridAccountWeights(values []HybridAccountWeight) bool {
	_, ok := hybridAccountWeightPercent(values)
	return ok
}

func validHybridAccountWeightShape(values []HybridAccountWeight) bool {
	if len(values) < 2 || len(values) > MaxConnectorAccounts {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	classCount := map[ResourceClass]int{}
	enabled := 0
	for _, value := range values {
		accountID, ok := normalizeConnectorIdentifier(strings.TrimSpace(value.AccountID), MaxConnectorIdentifierBytes)
		if !ok || accountID != value.AccountID || !value.Class.Valid() || value.Weight < 0 || value.Weight > 100 {
			return false
		}
		if _, duplicate := seen[value.AccountID]; duplicate {
			return false
		}
		seen[value.AccountID] = struct{}{}
		classCount[value.Class]++
		if value.Schedulable {
			enabled++
		}
	}
	return enabled > 0 && classCount[ResourceClassEconomy] == 1 && classCount[ResourceClassStable] == 1
}

// HybridAccountWeightPercent aggregates a persisted exact write set by its
// trusted resource classification. It is intentionally stricter than a raw
// integer total so Store can prove that DesiredWeights represents Effective
// before admitting the first remote write.
func HybridAccountWeightPercent(values []HybridAccountWeight) (map[ResourceClass]int, bool) {
	return hybridAccountWeightPercent(values)
}

func hybridAccountWeightPercent(values []HybridAccountWeight) (map[ResourceClass]int, bool) {
	if !validHybridAccountWeightShape(values) {
		return nil, false
	}
	units := map[ResourceClass]int{ResourceClassOwner: 0, ResourceClassEconomy: 0, ResourceClassStable: 0}
	totalUnits := 0
	for _, value := range values {
		if value.Schedulable {
			unit := value.Weight + 10
			units[value.Class] += unit
			totalUnits += unit
		}
	}
	percent := map[ResourceClass]int{
		ResourceClassOwner: 0, ResourceClassEconomy: 0, ResourceClassStable: 0,
	}
	for _, class := range []ResourceClass{ResourceClassOwner, ResourceClassEconomy, ResourceClassStable} {
		numerator := units[class] * 100
		if totalUnits == 0 || numerator%totalUnits != 0 {
			return nil, false
		}
		percent[class] = numerator / totalUnits
	}
	return percent, validCompleteHybridPercentMap(percent)
}

// HybridAccountWeightsEqual compares two complete exact write/read-back sets
// independent of order. It rejects malformed sets before comparison.
func HybridAccountWeightsEqual(left, right []HybridAccountWeight) bool {
	if !ValidHybridAccountWeights(left) || !ValidHybridAccountWeights(right) || len(left) != len(right) {
		return false
	}
	left = append([]HybridAccountWeight(nil), left...)
	right = append([]HybridAccountWeight(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i].AccountID < left[j].AccountID })
	sort.Slice(right, func(i, j int) bool { return right[i].AccountID < right[j].AccountID })
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// HybridPercentMapsEqual compares explicit three-pool maps. Missing zero
// classes and extra keys are rejected instead of being treated as equivalent.
func HybridPercentMapsEqual(left, right map[ResourceClass]int) bool {
	if !validCompleteHybridPercentMap(left) || !validCompleteHybridPercentMap(right) {
		return false
	}
	for _, class := range []ResourceClass{ResourceClassOwner, ResourceClassEconomy, ResourceClassStable} {
		if left[class] != right[class] {
			return false
		}
	}
	return true
}

func HybridPercentMap(rule HybridAllocationRule) map[ResourceClass]int {
	rule = rule.Normalize()
	return map[ResourceClass]int{
		ResourceClassOwner: rule.OwnerPercent, ResourceClassEconomy: rule.EconomyPercent, ResourceClassStable: rule.StablePercent,
	}
}

// ActualHybridPercent aggregates exact read-back weights using the same trusted
// account classification used by the compiler. Unknown, duplicate, missing or
// out-of-range values fail closed; numeric zero remains present.
func ActualHybridPercent(accounts []HybridWeightAccount) (map[ResourceClass]int, error) {
	values := make([]HybridAccountWeight, 0, len(accounts))
	for _, account := range accounts {
		account.AccountID = strings.TrimSpace(account.AccountID)
		if account.AccountID == "" || !account.Class.Valid() || account.CurrentWeight == nil ||
			*account.CurrentWeight < 0 || *account.CurrentWeight > 100 {
			return nil, ErrHybridWeightsUnrepresentable
		}
		values = append(values, HybridAccountWeight{AccountID: account.AccountID, Class: account.Class, Weight: *account.CurrentWeight, Schedulable: account.Schedulable})
	}
	actual, ok := hybridAccountWeightPercent(values)
	if !ok {
		return nil, ErrHybridWeightsUnrepresentable
	}
	return actual, nil
}
