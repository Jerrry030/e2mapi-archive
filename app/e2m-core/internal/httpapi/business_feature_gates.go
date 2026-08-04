package httpapi

import (
	"net/http"
	"strings"
)

type BusinessFeatureFlags struct {
	Billing                   bool
	Payments                  bool
	Supply                    bool
	HybridSupply              bool
	UpstreamRecommendations   bool
	UpstreamOptimizationApply bool
}

// SetBusinessFeatureFlags controls incomplete commercial APIs. The zero value
// is deliberately fail-closed; every process that wants an experimental module
// must opt in explicitly.
func (s *Server) SetBusinessFeatureFlags(flags BusinessFeatureFlags) {
	s.businessFeatures = flags
}

func (s *Server) withBusinessFeatureGates(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		disabled := false
		switch {
		case pathWithin(path, "/api/v1/billing"):
			disabled = !s.businessFeatures.Billing
		// The platform wallet is a native product surface, so the whole top-up
		// loop (admin config, provider webhooks, recharge orders) hangs off the
		// payments switch alone. The retired hybrid routing experiment keeps its
		// own switch below.
		case pathWithin(path, "/api/v1/admin/payment"), pathWithin(path, "/api/v1/payment/webhooks"),
			pathWithin(path, "/api/v1/owner/hybrid-supply/recharge-orders"),
			pathWithin(path, "/api/v1/admin/redeem-codes"), pathWithin(path, "/api/v1/redeem"):
			disabled = !s.businessFeatures.Payments
		case pathWithin(path, "/api/v1/owner/hybrid-supply"), pathWithin(path, "/api/v1/admin/hybrid-supply"):
			disabled = !s.businessFeatures.HybridSupply
		case pathWithin(path, "/api/v1/supply-offers"), pathWithin(path, "/api/v1/supply-ledger"):
			disabled = !s.businessFeatures.Supply
		case isRecommendationRolloutStartPath(path):
			// This route is nested below recommendations, but starts real gateway
			// mutation. The narrower auto-apply gate must win over the broader
			// recommendation read/experiment gate.
			setNoStore(w)
			disabled = !s.businessFeatures.UpstreamRecommendations || !s.businessFeatures.UpstreamOptimizationApply
		case isRecommendationRolloutAdvancePath(path):
			// Advancing a rollout is a forward traffic mutation just like starting
			// one. Both switches must remain enabled for every forward stage.
			setNoStore(w)
			disabled = !s.businessFeatures.UpstreamRecommendations || !s.businessFeatures.UpstreamOptimizationApply
		case pathWithin(path, "/api/v1/upstream-intelligence/recommendations"), pathWithin(path, "/api/v1/upstream-intelligence/experiments"):
			// This middleware runs before authentication. Apply the cache policy
			// here so disabled and unauthenticated responses are protected just
			// like successful evidence-bearing responses.
			setNoStore(w)
			disabled = !s.businessFeatures.UpstreamRecommendations
		case pathWithin(path, "/api/v1/upstream-intelligence/execution-policies"):
			// Policies reveal current optimization authority. Protect failures
			// (including disabled and unauthenticated paths) with the same cache
			// policy as recommendation evidence.
			setNoStore(w)
			disabled = !s.businessFeatures.UpstreamOptimizationApply
		case pathWithin(path, "/api/v1/upstream-intelligence/rollouts"):
			// Rollout visibility and rollback form the recovery surface. They must
			// remain reachable through normal authentication and authorization when
			// either forward switch is disabled. The advance action was handled
			// above, before this broader namespace match.
			setNoStore(w)
		}
		if disabled {
			writeError(w, http.StatusNotFound, "feature_disabled", "feature is not enabled")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isRecommendationRolloutStartPath(path string) bool {
	const prefix = "/api/v1/upstream-intelligence/recommendations/"
	const suffix = "/rollout"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(path, prefix)
	if !strings.HasSuffix(remainder, suffix) {
		return false
	}
	id := strings.TrimSuffix(remainder, suffix)
	return id != "" && !strings.Contains(id, "/")
}

func isRecommendationRolloutAdvancePath(path string) bool {
	const prefix = "/api/v1/upstream-intelligence/rollouts/"
	const suffix = "/advance"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(path, prefix)
	if !strings.HasSuffix(remainder, suffix) {
		return false
	}
	id := strings.TrimSuffix(remainder, suffix)
	return id != "" && !strings.Contains(id, "/")
}

func pathWithin(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
