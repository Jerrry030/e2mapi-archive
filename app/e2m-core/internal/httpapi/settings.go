package httpapi

import (
	"errors"
	"net/http"

	"e2m.local/contracts"
	"e2m.local/core/internal/settings"
)

// SetSettings wires the unified settings service. Nil hides the settings
// endpoints (embedders that manage configuration externally).
func (s *Server) SetSettings(service *settings.Service) { s.settings = service }

func (s *Server) handleGetCommerceSettings(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.settings == nil {
		writeError(w, http.StatusNotFound, "settings_unavailable", "the settings service is not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.settings.Commerce())
}

func (s *Server) handleUpdateCommerceSettings(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.settings == nil {
		writeError(w, http.StatusNotFound, "settings_unavailable", "the settings service is not configured")
		return
	}
	var input contracts.UpdateCommerceSettingsRequest
	if err := decodePaymentJSON(w, r, &input); err != nil {
		writePaymentDecodeError(w, err)
		return
	}
	saved, err := s.settings.SetCommerce(r.Context(), input)
	if err != nil {
		var validation settings.ValidationError
		if errors.As(err, &validation) {
			writeError(w, http.StatusBadRequest, "validation_failed", validation.Message)
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	actor := currentUser(r)
	_, _ = s.store.AppendAudit(r.Context(), contracts.OperationAudit{
		UserID: actor.ID, ActorType: "user", ActorID: actor.Email, Action: "settings.commerce.update",
		RiskLevel: contracts.RiskLevelL2, Result: "accepted", TargetType: "settings", TargetID: "commerce",
		Details: map[string]string{
			"usd_to_cny_rate":         saved.USDToCNYRate,
			"balance_alert_threshold": saved.BalanceAlertThreshold,
		},
	})
	writeJSON(w, http.StatusOK, saved)
}
