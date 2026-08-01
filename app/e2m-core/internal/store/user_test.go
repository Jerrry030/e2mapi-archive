package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryStoreConcurrentAdminDemotionKeepsOneEnabledAdmin(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC))
	adminA, err := st.CreateUser(ctx, contracts.User{
		Email: "admin-a@example.com", PasswordHash: "hash-a",
		Roles: []contracts.UserRole{contracts.UserRoleAdmin}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create admin A: %v", err)
	}
	adminB, err := st.CreateUser(ctx, contracts.User{
		Email: "admin-b@example.com", PasswordHash: "hash-b",
		Roles: []contracts.UserRole{contracts.UserRoleAdmin}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create admin B: %v", err)
	}

	inputs := []contracts.User{adminA, adminB}
	results := make(chan error, len(inputs))
	var wg sync.WaitGroup
	for _, input := range inputs {
		input.Roles = []contracts.UserRole{contracts.UserRoleClient}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, updateErr := st.UpdateUser(ctx, input)
			results <- updateErr
		}()
	}
	wg.Wait()
	close(results)

	var succeeded, protected int
	for updateErr := range results {
		switch {
		case updateErr == nil:
			succeeded++
		case errors.Is(updateErr, ErrLastEnabledAdmin):
			protected++
		default:
			t.Fatalf("unexpected update error: %v", updateErr)
		}
	}
	if succeeded != 1 || protected != 1 {
		t.Fatalf("demotions: succeeded=%d protected=%d, want 1 each", succeeded, protected)
	}

	users, err := st.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	enabledAdmins := 0
	for _, user := range users {
		if user.Enabled && userHasRole(user.Roles, contracts.UserRoleAdmin) {
			enabledAdmins++
		}
	}
	if enabledAdmins != 1 {
		t.Fatalf("enabled admins=%d, want 1", enabledAdmins)
	}
}

func TestMemoryStoreUserSecurityChangesRevokeSessions(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC))
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "user@example.com", PasswordHash: "old-hash",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	createSession := func(token string, expected contracts.User) {
		t.Helper()
		err := st.CreateSession(ctx, contracts.Session{
			TokenHash: token, UserID: user.ID, ExpiresAt: time.Now().Add(time.Hour),
		}, expected)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
	}

	createSession("profile-token", user)
	profile := user
	profile.DisplayName = "Renamed"
	updated, err := st.UpdateUser(ctx, profile)
	if err != nil {
		t.Fatalf("profile update: %v", err)
	}
	if _, err := st.GetSession(ctx, "profile-token"); err != nil {
		t.Fatalf("profile-only update must keep sessions: %v", err)
	}

	createSession("role-token", updated)
	roleUpdate := updated
	roleUpdate.Roles = []contracts.UserRole{contracts.UserRoleSupplier}
	updated, err = st.UpdateUser(ctx, roleUpdate)
	if err != nil {
		t.Fatalf("role update: %v", err)
	}
	for _, token := range []string{"profile-token", "role-token"} {
		if _, err := st.GetSession(ctx, token); !errors.Is(err, ErrNotFound) {
			t.Fatalf("role update session %q error=%v, want ErrNotFound", token, err)
		}
	}

	createSession("password-token", updated)
	if err := st.UpdateUserPasswordHash(ctx, user.ID, "new-hash"); err != nil {
		t.Fatalf("password update: %v", err)
	}
	if _, err := st.GetSession(ctx, "password-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("password update session error=%v, want ErrNotFound", err)
	}
	if err := st.CreateSession(ctx, contracts.Session{
		TokenHash: "stale-login", UserID: user.ID, ExpiresAt: time.Now().Add(time.Hour),
	}, updated); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale login error=%v, want ErrConflict", err)
	}
}

func TestMemoryStoreRejectsStaleUserUpdate(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	user, err := st.CreateUser(ctx, contracts.User{
		Email: "user@example.com", PasswordHash: "hash",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	stale := user
	first := user
	first.Enabled = false
	if _, err := st.UpdateUser(ctx, first); err != nil {
		t.Fatalf("first update: %v", err)
	}
	stale.DisplayName = "stale profile edit"
	if _, err := st.UpdateUser(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error=%v, want ErrConflict", err)
	}
	stored, err := st.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if stored.Enabled {
		t.Fatal("stale profile update re-enabled the user")
	}
}
