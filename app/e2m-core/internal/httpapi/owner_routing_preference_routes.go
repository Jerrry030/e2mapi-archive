package httpapi

import "net/http"

// registerOwnerRoutingPreferenceRoutes keeps the narrow owner-facing routing
// preference surface together with its handlers.
func (s *Server) registerOwnerRoutingPreferenceRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/owner/routing-preference", s.handleGetOwnerRoutingPreference)
	api.HandleFunc("PUT /api/v1/owner/routing-preference", s.handleUpdateOwnerRoutingPreference)
}
