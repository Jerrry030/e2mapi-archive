package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

const maxUpstreamIntelligenceLinkWriteBytes int64 = 16 << 10

func (s *Server) handleListUpstreamIntelligenceLinks(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r,
		upstreamIntelligenceStringSet("user_id"), nil, nil)
	if !ok {
		return
	}
	snapshot, ok := s.readUpstreamIntelligenceLinkSnapshot(w, r, query.userID)
	if !ok {
		return
	}
	items := projectUpstreamIntelligenceLinks(snapshot)
	writeUpstreamIntelligenceReadJSON(w, contracts.UpstreamIntelligenceLinksResponse{
		UpstreamIntelligenceReadMetadata: contracts.UpstreamIntelligenceReadMetadata{
			FactVersion: snapshot.FactVersion.FactVersion,
			GeneratedAt: snapshot.GeneratedAt,
		},
		Items: items,
	})
}

func (s *Server) handleCreateUpstreamIntelligenceLink(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	setNoStore(w)
	request, ok := decodeUpstreamIntelligenceLinkWriteRequest(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(request.ID) != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "id must be omitted when creating a link")
		return
	}
	input, ok := buildCompleteUpstreamIntelligenceLinkInput(w, request, "")
	if !ok {
		return
	}
	writer, ok := s.upstreamIntelligenceLinkWriter(w)
	if !ok {
		return
	}
	created, err := writer.UpsertUpstreamIntelligenceLink(r.Context(), input)
	if err != nil {
		writeUpstreamIntelligenceLinkStoreError(w, err)
		return
	}
	view, ok := s.readUpstreamIntelligenceLinkView(w, r, created.UserID, created.ID)
	if !ok {
		return
	}
	s.auditUpstreamIntelligenceLinkChange(r, created, view.ChannelID, "upstream_intelligence_link.create")
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleUpdateUpstreamIntelligenceLink(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	setNoStore(w)
	linkID := strings.TrimSpace(r.PathValue("id"))
	if linkID == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "link id is required")
		return
	}
	request, ok := decodeUpstreamIntelligenceLinkWriteRequest(w, r)
	if !ok {
		return
	}
	if bodyID := strings.TrimSpace(request.ID); bodyID != "" && bodyID != linkID {
		writeError(w, http.StatusBadRequest, "validation_failed", "body id must match the path id")
		return
	}
	if request.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id must be a positive integer")
		return
	}
	if !validUpstreamIntelligenceLinkStatus(request.Status) {
		writeError(w, http.StatusBadRequest, "validation_failed", "status must be active or inactive")
		return
	}

	snapshot, ok := s.readUpstreamIntelligenceLinkSnapshot(w, r, request.UserID)
	if !ok {
		return
	}
	existing, found := findUpstreamIntelligenceLink(snapshot, linkID)
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "upstream intelligence link not found")
		return
	}
	previousSafeChannelID := safeUpstreamIntelligenceResolvedChannelID(snapshot, linkID)

	var input contracts.UpstreamIntelligenceLink
	if isSparseUpstreamIntelligenceLinkStatusUpdate(request) {
		if !validUpstreamIntelligencePriceDimension(existing.PriceDimension) {
			writeError(w, http.StatusConflict, "link_conflict", "the existing link has no supported price dimension")
			return
		}
		input = existing
		input.Status = request.Status
		input.VerifiedAt = nil
		if input.Status == contracts.UpstreamLinkActive {
			if existing.Status == contracts.UpstreamLinkActive && existing.VerifiedAt != nil && !existing.VerifiedAt.IsZero() {
				input.VerifiedAt = cloneTimePtr(existing.VerifiedAt)
			} else {
				verifiedAt := time.Now().UTC()
				input.VerifiedAt = &verifiedAt
			}
		}
	} else {
		input, ok = buildCompleteUpstreamIntelligenceLinkInput(w, request, linkID)
		if !ok {
			return
		}
		if sameUpstreamIntelligenceLinkTarget(existing, input) && existing.Status == contracts.UpstreamLinkActive &&
			existing.VerifiedAt != nil && !existing.VerifiedAt.IsZero() {
			input.VerifiedAt = cloneTimePtr(existing.VerifiedAt)
		}
	}

	writer, ok := s.upstreamIntelligenceLinkWriter(w)
	if !ok {
		return
	}
	updated, err := writer.UpsertUpstreamIntelligenceLink(r.Context(), input)
	if err != nil {
		writeUpstreamIntelligenceLinkStoreError(w, err)
		return
	}
	view, ok := s.readUpstreamIntelligenceLinkView(w, r, updated.UserID, updated.ID)
	if !ok {
		return
	}
	if !sameUpstreamIntelligenceLinkRecord(existing, updated) {
		auditChannelID := view.ChannelID
		if auditChannelID == "" {
			auditChannelID = previousSafeChannelID
		}
		if auditChannelID == "" && updated.Scope == contracts.UpstreamLinkChannel {
			auditChannelID = updated.ChannelID
		}
		s.auditUpstreamIntelligenceLinkChange(r, updated, auditChannelID, "upstream_intelligence_link.update")
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) upstreamIntelligenceLinkWriter(w http.ResponseWriter) (store.UpstreamIntelligenceStore, bool) {
	writer, ok := s.store.(store.UpstreamIntelligenceStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_intelligence_disabled", "upstream intelligence link persistence is not enabled")
	}
	return writer, ok
}

func (s *Server) readUpstreamIntelligenceLinkSnapshot(w http.ResponseWriter, r *http.Request, userID int64) (store.UpstreamIntelligenceCurrentSnapshot, bool) {
	if userID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id must be a positive integer")
		return store.UpstreamIntelligenceCurrentSnapshot{}, false
	}
	reader, ok := s.store.(store.UpstreamIntelligenceReadStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_intelligence_disabled", "upstream intelligence read model is not enabled")
		return store.UpstreamIntelligenceCurrentSnapshot{}, false
	}
	snapshot, err := reader.ReadUpstreamIntelligenceCurrent(r.Context(), userID, nil)
	if err != nil {
		writeUpstreamIntelligenceReadStoreError(w, err, "upstream intelligence links")
		return store.UpstreamIntelligenceCurrentSnapshot{}, false
	}
	if snapshot.UserID != userID || snapshot.FactVersion.UserID != userID {
		writeError(w, http.StatusConflict, "read_model_conflict", "upstream intelligence owner snapshot is inconsistent")
		return store.UpstreamIntelligenceCurrentSnapshot{}, false
	}
	return snapshot, true
}

func (s *Server) readUpstreamIntelligenceLinkView(w http.ResponseWriter, r *http.Request, userID int64, linkID string) (contracts.UpstreamIntelligenceLinkReadModel, bool) {
	snapshot, ok := s.readUpstreamIntelligenceLinkSnapshot(w, r, userID)
	if !ok {
		return contracts.UpstreamIntelligenceLinkReadModel{}, false
	}
	for _, item := range projectUpstreamIntelligenceLinks(snapshot) {
		if item.ID == linkID {
			return item, true
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "upstream intelligence link not found")
	return contracts.UpstreamIntelligenceLinkReadModel{}, false
}

func decodeUpstreamIntelligenceLinkWriteRequest(w http.ResponseWriter, r *http.Request) (contracts.UpstreamIntelligenceLinkWriteRequest, bool) {
	var request contracts.UpstreamIntelligenceLinkWriteRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpstreamIntelligenceLinkWriteBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeUpstreamIntelligenceLinkJSONError(w, err)
		return contracts.UpstreamIntelligenceLinkWriteRequest{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			writeUpstreamIntelligenceLinkJSONError(w, err)
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON value")
		}
		return contracts.UpstreamIntelligenceLinkWriteRequest{}, false
	}
	return request, true
}

func writeUpstreamIntelligenceLinkJSONError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 16 KiB")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object with only supported fields")
}

func buildCompleteUpstreamIntelligenceLinkInput(w http.ResponseWriter, request contracts.UpstreamIntelligenceLinkWriteRequest, id string) (contracts.UpstreamIntelligenceLink, bool) {
	request.IntelligenceSourceID = strings.TrimSpace(request.IntelligenceSourceID)
	request.ChannelID = strings.TrimSpace(request.ChannelID)
	request.UpstreamSourceIdentity = strings.TrimSpace(request.UpstreamSourceIdentity)
	if request.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id must be a positive integer")
		return contracts.UpstreamIntelligenceLink{}, false
	}
	if request.IntelligenceSourceID == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "intelligence_source_id is required")
		return contracts.UpstreamIntelligenceLink{}, false
	}
	if !validUpstreamIntelligenceLinkStatus(request.Status) {
		writeError(w, http.StatusBadRequest, "validation_failed", "status must be active or inactive")
		return contracts.UpstreamIntelligenceLink{}, false
	}
	if !validUpstreamIntelligencePriceDimension(request.PriceDimension) {
		writeError(w, http.StatusBadRequest, "validation_failed", "price_dimension must be input, output, cached_input, or request")
		return contracts.UpstreamIntelligenceLink{}, false
	}
	switch request.Scope {
	case contracts.UpstreamLinkChannel:
		if request.ChannelID == "" || request.UpstreamSourceIdentity != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "channel links require exactly one channel target")
			return contracts.UpstreamIntelligenceLink{}, false
		}
	case contracts.UpstreamLinkSourceIdentity:
		if request.UpstreamSourceIdentity == "" || request.ChannelID != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "source_identity links require exactly one opaque identity target")
			return contracts.UpstreamIntelligenceLink{}, false
		}
		if !contracts.IsUpstreamSourceIdentity(request.UpstreamSourceIdentity) {
			writeError(w, http.StatusBadRequest, "validation_failed", "link target must be a short opaque identifier")
			return contracts.UpstreamIntelligenceLink{}, false
		}
	default:
		writeError(w, http.StatusBadRequest, "validation_failed", "link_scope must be channel or source_identity")
		return contracts.UpstreamIntelligenceLink{}, false
	}
	input := contracts.UpstreamIntelligenceLink{
		ID: id, UserID: request.UserID, IntelligenceSourceID: request.IntelligenceSourceID,
		Scope: request.Scope, UpstreamSourceIdentity: request.UpstreamSourceIdentity,
		ChannelID: request.ChannelID, PriceDimension: request.PriceDimension, Status: request.Status,
	}
	if input.Status == contracts.UpstreamLinkActive {
		verifiedAt := time.Now().UTC()
		input.VerifiedAt = &verifiedAt
	}
	return input, true
}

func isSparseUpstreamIntelligenceLinkStatusUpdate(request contracts.UpstreamIntelligenceLinkWriteRequest) bool {
	return strings.TrimSpace(request.IntelligenceSourceID) == "" && request.Scope == "" &&
		request.UpstreamSourceIdentity == "" && strings.TrimSpace(request.ChannelID) == "" && request.PriceDimension == ""
}

func validUpstreamIntelligenceLinkStatus(status contracts.UpstreamIntelligenceLinkStatus) bool {
	return status == contracts.UpstreamLinkActive || status == contracts.UpstreamLinkInactive
}

func validUpstreamIntelligencePriceDimension(dimension contracts.UpstreamPriceDimension) bool {
	switch dimension {
	case contracts.UpstreamPriceInput, contracts.UpstreamPriceOutput, contracts.UpstreamPriceCachedInput, contracts.UpstreamPriceRequest:
		return true
	default:
		return false
	}
}

func findUpstreamIntelligenceLink(snapshot store.UpstreamIntelligenceCurrentSnapshot, id string) (contracts.UpstreamIntelligenceLink, bool) {
	for _, link := range snapshot.Links {
		if link.ID == id && link.UserID == snapshot.UserID {
			return link, true
		}
	}
	return contracts.UpstreamIntelligenceLink{}, false
}

func projectUpstreamIntelligenceLinks(snapshot store.UpstreamIntelligenceCurrentSnapshot) []contracts.UpstreamIntelligenceLinkReadModel {
	resolved := make(map[string]string, len(snapshot.LinkResolutions))
	invalid := make(map[string]bool)
	for _, resolution := range snapshot.LinkResolutions {
		channelID := strings.TrimSpace(resolution.ResolvedChannelID)
		if resolution.LinkID == "" || resolution.UserID != snapshot.UserID ||
			resolution.ResolvedChannelOwnerID != snapshot.UserID || !resolution.TargetVerified || channelID == "" {
			continue
		}
		if previous, exists := resolved[resolution.LinkID]; exists && previous != channelID {
			invalid[resolution.LinkID] = true
			delete(resolved, resolution.LinkID)
			continue
		}
		if !invalid[resolution.LinkID] {
			resolved[resolution.LinkID] = channelID
		}
	}
	items := make([]contracts.UpstreamIntelligenceLinkReadModel, 0, len(snapshot.Links))
	for _, link := range snapshot.Links {
		if link.UserID != snapshot.UserID {
			continue
		}
		items = append(items, contracts.UpstreamIntelligenceLinkReadModel{
			ID: link.ID, IntelligenceSourceID: link.IntelligenceSourceID, Scope: link.Scope,
			ChannelID: resolved[link.ID], PriceDimension: link.PriceDimension, Status: link.Status,
			VerifiedAt: cloneTimePtr(link.VerifiedAt), CreatedAt: link.CreatedAt, UpdatedAt: link.UpdatedAt,
		})
	}
	if items == nil {
		return []contracts.UpstreamIntelligenceLinkReadModel{}
	}
	return items
}

func safeUpstreamIntelligenceResolvedChannelID(snapshot store.UpstreamIntelligenceCurrentSnapshot, linkID string) string {
	resolved := ""
	for _, resolution := range snapshot.LinkResolutions {
		channelID := strings.TrimSpace(resolution.ResolvedChannelID)
		if resolution.LinkID != linkID || resolution.UserID != snapshot.UserID ||
			resolution.ResolvedChannelOwnerID != snapshot.UserID || !resolution.TargetVerified || channelID == "" {
			continue
		}
		if resolved != "" && resolved != channelID {
			return ""
		}
		resolved = channelID
	}
	return resolved
}

func sameUpstreamIntelligenceLinkTarget(left, right contracts.UpstreamIntelligenceLink) bool {
	return left.ID == right.ID && left.UserID == right.UserID &&
		left.IntelligenceSourceID == right.IntelligenceSourceID && left.Scope == right.Scope &&
		left.UpstreamSourceIdentity == right.UpstreamSourceIdentity && left.ChannelID == right.ChannelID &&
		left.PriceDimension == right.PriceDimension && left.Status == right.Status
}

func sameUpstreamIntelligenceLinkRecord(left, right contracts.UpstreamIntelligenceLink) bool {
	if !sameUpstreamIntelligenceLinkTarget(left, right) {
		return false
	}
	if left.VerifiedAt == nil || right.VerifiedAt == nil {
		return left.VerifiedAt == nil && right.VerifiedAt == nil
	}
	return left.VerifiedAt.Equal(*right.VerifiedAt)
}

func writeUpstreamIntelligenceLinkStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "the owner-scoped intelligence source, link, or allocated target was not found")
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, "link_conflict", "an active link already maps this target and price dimension")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "link_conflict", "the link target is ambiguous, immutable, or changed concurrently")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", "the upstream intelligence link is invalid")
	default:
		writeError(w, http.StatusInternalServerError, "store_error", "the upstream intelligence link could not be saved")
	}
}

func (s *Server) auditUpstreamIntelligenceLinkChange(r *http.Request, link contracts.UpstreamIntelligenceLink, channelID, action string) {
	actor := currentUser(r)
	details := map[string]string{
		"link_id":                link.ID,
		"intelligence_source_id": link.IntelligenceSourceID,
		"price_dimension":        string(link.PriceDimension),
		"status":                 string(link.Status),
	}
	if channelID = strings.TrimSpace(channelID); channelID != "" {
		details["channel_id"] = channelID
	}
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: link.UserID, ActorType: "user", ActorID: actor.Email,
		Action: action, RiskLevel: contracts.RiskLevelL1,
		TargetType: "upstream_intelligence_link", TargetID: link.ID,
		Result: "accepted", Details: details,
	})
}
