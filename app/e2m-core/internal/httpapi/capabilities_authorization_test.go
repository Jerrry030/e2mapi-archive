package httpapi

import (
	"context"
	"net/http"
	"testing"

	"e2m.local/contracts"
)

func TestListCapabilitiesRejectsBusinessRoles(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	for _, role := range []contracts.UserRole{contracts.UserRoleClient, contracts.UserRoleSupplier} {
		user := createLoginUser(t, authSvc, string(role)+"-capabilities@example.com", role)
		token, _, err := authSvc.Login(context.Background(), user.Email, "password123")
		if err != nil {
			t.Fatalf("login %s: %v", role, err)
		}
		response := do(t, srv.Routes(), http.MethodGet, "/api/v1/adapter-capabilities", token, nil)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s capabilities status=%d body=%s", role, response.Code, response.Body.String())
		}
	}
}
