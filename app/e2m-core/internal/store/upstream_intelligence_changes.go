package store

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

type falseRemovalInvariantError struct{ cause error }

func (err falseRemovalInvariantError) Error() string {
	return "store: false-removal invariant violation: " + err.cause.Error()
}

func (err falseRemovalInvariantError) Unwrap() error { return err.cause }

const (
	upstreamAbsenceGroupPrefix = "group:"
	upstreamAbsenceModelPrefix = "model:"
)

type upstreamSnapshotPresence struct {
	ComparisonKey string
	ObservationID string
	GroupKey      string
	ModelKey      string
	EventType     contracts.UpstreamChangeEventType
}

// reconcileCompleteSnapshotAbsences is the common Memory/PostgreSQL state
// transition for deletion detection. Callers must invoke it only for a
// succeeded+complete run that is strictly newer than every already finalized
// complete run for the source. It is pure: callers can persist every returned
// state and event in the same transaction as run finalization.
func reconcileCompleteSnapshotAbsences(
	run contracts.UpstreamCollectionRun,
	offers []contracts.UpstreamOfferObservation,
	previous []UpstreamSnapshotAbsence,
	recordedAt time.Time,
) ([]UpstreamSnapshotAbsence, []contracts.UpstreamChangeEvent, error) {
	if run.UserID <= 0 || strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.SourceID) == "" ||
		run.Status != contracts.UpstreamCollectionSucceeded || run.Coverage != contracts.UpstreamCoverageComplete || run.ObservedAt.IsZero() {
		return nil, nil, ErrInvalid
	}
	recordedAt = normalizeUpstreamTime(recordedAt)
	if recordedAt.IsZero() {
		recordedAt = normalizeUpstreamTime(run.ObservedAt)
	}
	present, err := completeSnapshotPresence(offers, run)
	if err != nil {
		return nil, nil, err
	}

	states := make(map[string]UpstreamSnapshotAbsence, len(previous)+len(present))
	for _, absence := range previous {
		if absence.UserID != run.UserID || absence.SourceID != run.SourceID ||
			strings.TrimSpace(absence.ComparisonKey) == "" || absence.ConsecutiveCompleteRuns < 0 {
			return nil, nil, ErrInvalid
		}
		if _, exists := states[absence.ComparisonKey]; exists {
			return nil, nil, ErrConflict
		}
		states[absence.ComparisonKey] = absence
	}

	for comparisonKey, presence := range present {
		state, exists := states[comparisonKey]
		if !exists {
			state = UpstreamSnapshotAbsence{
				UserID: run.UserID, SourceID: run.SourceID, ComparisonKey: comparisonKey,
			}
		}
		state.ConsecutiveCompleteRuns = 0
		state.LastPresentObservationID = presence.ObservationID
		state.LastPresentRunID = run.ID
		state.FirstAbsentAt = nil
		state.LastAbsentRunID = ""
		state.UpdatedAt = recordedAt
		states[comparisonKey] = state
	}

	confirmed := make([]UpstreamSnapshotAbsence, 0)
	for comparisonKey, state := range states {
		if _, exists := present[comparisonKey]; exists {
			continue
		}
		presence, parseErr := parseUpstreamAbsenceComparisonKey(comparisonKey)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		if presence.EventType == contracts.UpstreamChangeModelRemoved {
			// A model is only independently absent while its group is present.
			// Whole-group absence is tracked by the group state; resetting child
			// state here lets a later group recovery require two fresh complete
			// snapshots before declaring a model still absent.
			if _, groupPresent := present[upstreamGroupAbsenceKey(presence.GroupKey)]; !groupPresent {
				state.ConsecutiveCompleteRuns = 0
				state.FirstAbsentAt = nil
				state.LastAbsentRunID = ""
				state.UpdatedAt = recordedAt
				states[comparisonKey] = state
				continue
			}
		}
		// This makes the transition idempotent even before the caller's durable
		// finalized-run fence is consulted.
		if state.LastAbsentRunID == run.ID {
			continue
		}
		if state.ConsecutiveCompleteRuns == 0 {
			firstAbsent := normalizeUpstreamTime(run.ObservedAt)
			state.FirstAbsentAt = &firstAbsent
		}
		state.ConsecutiveCompleteRuns++
		state.LastAbsentRunID = run.ID
		state.UpdatedAt = recordedAt
		states[comparisonKey] = state
		if state.ConsecutiveCompleteRuns == 2 {
			confirmed = append(confirmed, state)
		}
	}

	ordered := make([]UpstreamSnapshotAbsence, 0, len(states))
	for _, state := range states {
		ordered = append(ordered, state)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ComparisonKey < ordered[j].ComparisonKey })

	confirmedAbsentGroups := make(map[string]struct{})
	for _, state := range confirmed {
		presence, parseErr := parseUpstreamAbsenceComparisonKey(state.ComparisonKey)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		if presence.EventType == contracts.UpstreamChangeGroupRemoved {
			confirmedAbsentGroups[presence.GroupKey] = struct{}{}
		}
	}
	events := make([]contracts.UpstreamChangeEvent, 0, len(confirmed))
	for _, state := range confirmed {
		presence, parseErr := parseUpstreamAbsenceComparisonKey(state.ComparisonKey)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		if presence.EventType == contracts.UpstreamChangeModelRemoved {
			if _, groupAbsent := confirmedAbsentGroups[presence.GroupKey]; groupAbsent {
				continue
			}
		}
		firstDetected := normalizeUpstreamTime(run.ObservedAt)
		if state.FirstAbsentAt != nil {
			firstDetected = normalizeUpstreamTime(*state.FirstAbsentAt)
		}
		events = append(events, contracts.UpstreamChangeEvent{
			UserID: run.UserID, SourceID: run.SourceID, Type: presence.EventType,
			Fingerprint:         upstreamRemovalFingerprint(run.SourceID, state),
			BeforeObservationID: state.LastPresentObservationID,
			FirstDetectedAt:     firstDetected, ConfirmedAt: normalizeUpstreamTime(run.ObservedAt),
			Severity:    contracts.UpstreamChangeWarning,
			ImpactScope: map[string]string{"comparison_key": state.ComparisonKey},
			GroupKey:    presence.GroupKey, ModelKey: presence.ModelKey, CreatedAt: recordedAt,
		})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Fingerprint < events[j].Fingerprint })
	return ordered, events, nil
}

func completeSnapshotPresence(offers []contracts.UpstreamOfferObservation, run contracts.UpstreamCollectionRun) (map[string]upstreamSnapshotPresence, error) {
	present := make(map[string]upstreamSnapshotPresence)
	for _, offer := range offers {
		if offer.UserID != run.UserID || offer.SourceID != run.SourceID || offer.RunID != run.ID ||
			strings.TrimSpace(offer.ID) == "" || strings.TrimSpace(offer.GroupKey) == "" || strings.TrimSpace(offer.ModelKey) == "" ||
			offer.Coverage != contracts.UpstreamCoverageComplete {
			return nil, ErrConflict
		}
		entries := []upstreamSnapshotPresence{
			{
				ComparisonKey: upstreamGroupAbsenceKey(offer.GroupKey), ObservationID: offer.ID,
				GroupKey: offer.GroupKey, EventType: contracts.UpstreamChangeGroupRemoved,
			},
			{
				ComparisonKey: upstreamModelAbsenceKey(offer.GroupKey, offer.ModelKey), ObservationID: offer.ID,
				GroupKey: offer.GroupKey, ModelKey: offer.ModelKey, EventType: contracts.UpstreamChangeModelRemoved,
			},
		}
		for _, entry := range entries {
			if len(entry.ComparisonKey) > 512 {
				return nil, ErrInvalid
			}
			if current, exists := present[entry.ComparisonKey]; !exists || entry.ObservationID < current.ObservationID {
				present[entry.ComparisonKey] = entry
			}
		}
	}
	return present, nil
}

func upstreamGroupAbsenceKey(groupKey string) string {
	return upstreamAbsenceGroupPrefix + strconv.Itoa(len(groupKey)) + ":" + groupKey
}

func upstreamModelAbsenceKey(groupKey, modelKey string) string {
	return upstreamAbsenceModelPrefix + strconv.Itoa(len(groupKey)) + ":" + groupKey + modelKey
}

func parseUpstreamAbsenceComparisonKey(value string) (upstreamSnapshotPresence, error) {
	var prefix string
	eventType := contracts.UpstreamChangeEventType("")
	switch {
	case strings.HasPrefix(value, upstreamAbsenceGroupPrefix):
		prefix, eventType = upstreamAbsenceGroupPrefix, contracts.UpstreamChangeGroupRemoved
	case strings.HasPrefix(value, upstreamAbsenceModelPrefix):
		prefix, eventType = upstreamAbsenceModelPrefix, contracts.UpstreamChangeModelRemoved
	default:
		return upstreamSnapshotPresence{}, ErrInvalid
	}
	rest := strings.TrimPrefix(value, prefix)
	separator := strings.IndexByte(rest, ':')
	if separator <= 0 {
		return upstreamSnapshotPresence{}, ErrInvalid
	}
	groupLength, err := strconv.Atoi(rest[:separator])
	payload := rest[separator+1:]
	if err != nil || groupLength <= 0 || groupLength > len(payload) {
		return upstreamSnapshotPresence{}, ErrInvalid
	}
	groupKey, modelKey := payload[:groupLength], payload[groupLength:]
	if eventType == contracts.UpstreamChangeGroupRemoved && modelKey != "" ||
		eventType == contracts.UpstreamChangeModelRemoved && modelKey == "" {
		return upstreamSnapshotPresence{}, ErrInvalid
	}
	return upstreamSnapshotPresence{
		ComparisonKey: value, GroupKey: groupKey, ModelKey: modelKey, EventType: eventType,
	}, nil
}

func upstreamRemovalFingerprint(sourceID string, absence UpstreamSnapshotAbsence) string {
	firstAbsent := ""
	if absence.FirstAbsentAt != nil {
		firstAbsent = normalizeUpstreamTime(*absence.FirstAbsentAt).Format(time.RFC3339Nano)
	}
	payload := fmt.Sprintf("e2m.upstream-intelligence.removed.v1\x00%s\x00%s\x00%s\x00%s",
		sourceID, absence.ComparisonKey, absence.LastPresentRunID, firstAbsent)
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("removed:v1:%x", digest[:])
}

// validateRemovalEvents rechecks the evidence boundary immediately before a
// caller writes removal events. The reconciliation helper is intentionally
// pure; this final fence catches corrupt state or a future caller that bypasses
// one of the two-complete-snapshot rules.
func validateRemovalEvents(run contracts.UpstreamCollectionRun, states []UpstreamSnapshotAbsence, events []contracts.UpstreamChangeEvent) error {
	if len(events) == 0 {
		return nil
	}
	if run.Status != contracts.UpstreamCollectionSucceeded || run.Coverage != contracts.UpstreamCoverageComplete || run.FinalizedFactVersion > 0 {
		return ErrConflict
	}
	byKey := make(map[string]UpstreamSnapshotAbsence, len(states))
	for _, state := range states {
		byKey[state.ComparisonKey] = state
	}
	removedGroups := make(map[string]struct{})
	for _, event := range events {
		if event.Type == contracts.UpstreamChangeGroupRemoved {
			removedGroups[event.GroupKey] = struct{}{}
		}
	}
	for _, event := range events {
		if event.Type != contracts.UpstreamChangeGroupRemoved && event.Type != contracts.UpstreamChangeModelRemoved {
			return ErrConflict
		}
		key := upstreamGroupAbsenceKey(event.GroupKey)
		if event.Type == contracts.UpstreamChangeModelRemoved {
			key = upstreamModelAbsenceKey(event.GroupKey, event.ModelKey)
			if _, suppressed := removedGroups[event.GroupKey]; suppressed {
				return ErrConflict
			}
		}
		state, ok := byKey[key]
		if !ok || state.ConsecutiveCompleteRuns != 2 || state.FirstAbsentAt == nil ||
			state.LastAbsentRunID != run.ID || state.LastPresentRunID == "" || state.LastPresentRunID == run.ID ||
			state.LastPresentObservationID == "" || !event.FirstDetectedAt.Equal(*state.FirstAbsentAt) ||
			event.ConfirmedAt.IsZero() || !event.ConfirmedAt.Equal(run.ObservedAt) ||
			event.BeforeObservationID != state.LastPresentObservationID || event.AfterObservationID != "" ||
			event.Fingerprint != upstreamRemovalFingerprint(run.SourceID, state) {
			return ErrConflict
		}
	}
	return nil
}
