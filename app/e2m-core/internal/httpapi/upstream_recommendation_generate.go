package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/upstreamrecommendation"
)

// upstreamRecommendationGenerationResponse is an explicit browser projection.
// In particular it never exposes the recommendation's downstream instance
// identifiers or the owner identity embedded in the persisted domain object.
type upstreamRecommendationGenerationResponse struct {
	Recommendations []upstreamRecommendationResponse                       `json:"recommendations"`
	Blocked         []contracts.UpstreamRecommendationGenerationDiagnostic `json:"blocked"`
}

func (s *Server) handleGenerateUpstreamRecommendations(w http.ResponseWriter, r *http.Request) {
	// This action returns evidence-bound operational detail. Set the policy
	// before every validation/authorization branch so an error response cannot
	// be cached either.
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet("user_id"), nil, nil)
	if !ok {
		return
	}
	userID, ok := s.scopeOwnerUser(w, r, query.values["user_id"])
	if !ok || userID != query.userID {
		return
	}
	if !decodeEmptyRecommendationGenerationRequest(w, r) {
		return
	}

	inputs, inputsOK := s.store.(store.UpstreamRecommendationInputStore)
	recommendations, recommendationsOK := s.store.(store.UpstreamRecommendationStore)
	if !inputsOK || !recommendationsOK {
		writeError(w, http.StatusServiceUnavailable, "upstream_recommendations_disabled", "recommendation generation is not enabled")
		return
	}
	snapshot, err := inputs.ReadUpstreamRecommendationInputs(r.Context(), query.userID)
	if err != nil {
		writeUpstreamRecommendationStoreError(w, err, "recommendation input snapshot")
		return
	}
	generatorInput := upstreamRecommendationGeneratorInputs(snapshot)
	if generatorInput.UserID != query.userID {
		writeError(w, http.StatusConflict, "read_model_conflict", "recommendation input snapshot is owner-inconsistent")
		return
	}

	var identityErr error
	generated, err := upstreamrecommendation.Generate(generatorInput, func() string {
		if identityErr != nil {
			return ""
		}
		var id string
		id, identityErr = newUpstreamRecommendationID()
		return id
	})
	if identityErr != nil {
		writeError(w, http.StatusInternalServerError, "identity_error", "could not create recommendation identity")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "generation_blocked", "trusted recommendation inputs are inconsistent")
		return
	}
	diagnostics, ok := safeUpstreamRecommendationGenerationDiagnostics(generated.Blocked)
	if !ok {
		writeError(w, http.StatusInternalServerError, "generation_error", "recommendation diagnostics are invalid")
		return
	}

	response := upstreamRecommendationGenerationResponse{
		Recommendations: make([]upstreamRecommendationResponse, 0, len(generated.Recommendations)),
		Blocked:         diagnostics,
	}
	persistedBatch := make([]contracts.UpstreamRecommendation, 0, len(generated.Recommendations))
	for _, generatedRecommendation := range generated.Recommendations {
		persisted, err := recommendations.CreateUpstreamRecommendation(r.Context(), generatedRecommendation)
		if err != nil {
			writeUpstreamRecommendationGenerationStoreError(w, err)
			return
		}
		// CreateUpstreamRecommendation is idempotent on the stable owner-scoped
		// fingerprint. Always return the persisted object so a replay cannot
		// advertise a fresh random id that was never stored.
		persistedBatch = append(persistedBatch, persisted)
	}
	// The generator returns fingerprint order. A replay may substitute older
	// persisted objects whose IDs/timestamps differ from the newly generated
	// candidates, but retaining the same index still preserves fingerprint
	// order and makes response ordering deterministic.
	for _, persisted := range persistedBatch {
		response.Recommendations = append(response.Recommendations, projectUpstreamRecommendation(persisted))
	}
	writeJSON(w, http.StatusOK, response)
}

func decodeEmptyRecommendationGenerationRequest(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var request map[string]any
	if err := decodeStrictJSON(r, &request); err != nil || request == nil || len(request) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be an empty JSON object")
		return false
	}
	return true
}

// upstreamRecommendationGeneratorInputs is the only HTTP adapter from the
// store consistency boundary into the pure generator. Every ordinary fact is
// copied from that one snapshot. A link resolution is executable evidence only
// when the same snapshot proves an exact, verified channel allocation owned by
// the requested user; ambiguous and foreign targets are deliberately omitted.
func upstreamRecommendationGeneratorInputs(snapshot store.UpstreamRecommendationInputs) upstreamrecommendation.GeneratorInputs {
	intelligence := snapshot.Intelligence
	if snapshot.UserID <= 0 || intelligence.UserID != snapshot.UserID || intelligence.FactVersion.UserID != snapshot.UserID ||
		snapshot.GeneratedAt.IsZero() || intelligence.GeneratedAt.IsZero() || !snapshot.GeneratedAt.Equal(intelligence.GeneratedAt) {
		return upstreamrecommendation.GeneratorInputs{}
	}
	input := upstreamrecommendation.GeneratorInputs{
		UserID:                  snapshot.UserID,
		GeneratedAt:             snapshot.GeneratedAt,
		IntelligenceFactVersion: intelligence.FactVersion.FactVersion,
		CostLedgerFactVersion:   snapshot.CostLedgerFactVersion,
		Sources:                 append([]contracts.UpstreamIntelligenceSource(nil), intelligence.Sources...),
		LatestRuns:              append([]contracts.UpstreamCollectionRun(nil), intelligence.LatestRuns...),
		Wallets:                 append([]contracts.UpstreamWalletObservation(nil), intelligence.Wallets...),
		Offers:                  append([]contracts.UpstreamOfferObservation(nil), intelligence.Offers...),
		Links:                   append([]contracts.UpstreamIntelligenceLink(nil), intelligence.Links...),
		QualitySnapshots:        append([]contracts.ChannelHealthSnapshot(nil), intelligence.QualitySnapshots...),
		CostFacts:               append([]contracts.UpstreamCostFact(nil), snapshot.CostFacts...),
		RoutePlans:              append([]contracts.RoutePlan(nil), snapshot.RoutePlans...),
		AllocatedChannels:       append([]contracts.UpstreamChannel(nil), snapshot.Channels...),
		Bindings:                append([]contracts.PublishedBinding(nil), snapshot.Bindings...),
	}
	for _, resolution := range intelligence.LinkResolutions {
		if resolution.UserID != snapshot.UserID {
			return upstreamrecommendation.GeneratorInputs{}
		}
		// An unresolved/ambiguous row carries no owner-safe channel identity.
		// Keep every uniquely owner-resolved row, including an explicit false
		// TargetVerified bit, so the generator receives the complete trusted
		// snapshot and can apply its own verification gate.
		if resolution.ResolvedChannelID == "" && resolution.ResolvedChannelOwnerID == 0 && !resolution.TargetVerified {
			continue
		}
		if resolution.LinkID == "" || resolution.ResolvedChannelID == "" || resolution.ResolvedChannelOwnerID != snapshot.UserID {
			return upstreamrecommendation.GeneratorInputs{}
		}
		input.LinkResolutions = append(input.LinkResolutions, upstreamrecommendation.GeneratorLinkResolution{
			LinkID: resolution.LinkID, UserID: snapshot.UserID, ChannelID: resolution.ResolvedChannelID, TargetVerified: resolution.TargetVerified,
		})
	}
	return input
}

func safeUpstreamRecommendationGenerationDiagnostics(values []contracts.UpstreamRecommendationGenerationDiagnostic) ([]contracts.UpstreamRecommendationGenerationDiagnostic, bool) {
	result := make([]contracts.UpstreamRecommendationGenerationDiagnostic, 0, len(values))
	for _, value := range values {
		if value.Count <= 0 || !contracts.IsUpstreamRecommendationGenerationReason(value.Reason) {
			return nil, false
		}
		result = append(result, value)
	}
	return result, true
}

func newUpstreamRecommendationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "rec-" + hex.EncodeToString(raw[:]), nil
}

func writeUpstreamRecommendationGenerationStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "state_conflict", "recommendation persistence conflicted; retry generation")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusConflict, "generation_blocked", "generated recommendation failed persistence validation")
	default:
		writeError(w, http.StatusInternalServerError, "store_error", "recommendation could not be persisted")
	}
}
