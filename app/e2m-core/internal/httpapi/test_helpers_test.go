package httpapi

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/store"
)

var testUserSeq uint64

func createLoginUser(t *testing.T, authSvc *auth.Service, email string, roles ...contracts.UserRole) contracts.User {
	t.Helper()
	user, err := authSvc.CreateUser(context.Background(), email, "password123", "", roles)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func createStoreUser(t *testing.T, st store.Store, email string, roles ...contracts.UserRole) contracts.User {
	t.Helper()
	email = testUserEmail(t, email)
	user, err := st.CreateUser(context.Background(), contracts.User{
		Email:        strings.ToLower(strings.TrimSpace(email)),
		DisplayName:  email,
		PasswordHash: "test-only",
		Roles:        roles,
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("create store user: %v", err)
	}
	return user
}

func testUserEmail(t *testing.T, label string) string {
	t.Helper()
	label = strings.ToLower(strings.TrimSpace(label))
	if strings.Contains(label, "@") {
		return label
	}
	clean := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(t.Name()+"-"+label, "-")
	clean = strings.Trim(clean, "-")
	if clean == "" {
		clean = "user"
	}
	return fmt.Sprintf("%s-%d@example.com", clean, atomic.AddUint64(&testUserSeq, 1))
}

func userIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}
