package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"e2m.local/contracts"
)

func TestListCapabilitiesReturnsCurrentExecutableGatewayMatrix(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	user := createLoginUser(t, authSvc, "capabilities@e2m.local", contracts.UserRoleAdmin)
	token, _, err := authSvc.Login(context.Background(), user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	w := do(t, srv.Routes(), http.MethodGet, "/api/v1/adapter-capabilities", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list capabilities: got status %d: %s", w.Code, w.Body.String())
	}

	var got []contracts.AdapterCapability
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}

	wantNames := map[contracts.CapabilityName]struct{}{
		contracts.CapabilityListAccounts:          {},
		contracts.CapabilitySetAccountSchedulable: {},
		contracts.CapabilitySwitchUpstream:        {},
		contracts.CapabilityCreateAccount:         {},
		contracts.CapabilityUpdateAccount:         {},
		contracts.CapabilityDeleteAccount:         {},
	}
	newAPIOnly := map[contracts.CapabilityName]struct{}{
		contracts.CapabilitySetAccountTrafficShare: {},
	}
	wantSystems := []contracts.InstanceKind{
		contracts.InstanceKindSub2API,
		contracts.InstanceKindNewAPI,
		contracts.InstanceKindCPA,
	}
	bySystem := make(map[contracts.InstanceKind]map[contracts.CapabilityName]struct{}, len(wantSystems))
	for _, capability := range got {
		if _, common := wantNames[capability.Name]; !common {
			if _, specific := newAPIOnly[capability.Name]; !specific || capability.System != contracts.InstanceKindNewAPI {
				t.Errorf("unexpected capability %q for %q", capability.Name, capability.System)
			}
		}
		if !capability.Supported {
			t.Errorf("current capability %q for %q is not supported", capability.Name, capability.System)
		}
		if bySystem[capability.System] == nil {
			bySystem[capability.System] = make(map[contracts.CapabilityName]struct{})
		}
		bySystem[capability.System][capability.Name] = struct{}{}
	}

	wantCount := len(wantSystems)*len(wantNames) + len(newAPIOnly)
	if len(got) != wantCount {
		t.Fatalf("unexpected capability count: got %d want %d", len(got), wantCount)
	}
	for _, system := range wantSystems {
		wantSystemCount := len(wantNames)
		if system == contracts.InstanceKindNewAPI {
			wantSystemCount += len(newAPIOnly)
		}
		if len(bySystem[system]) != wantSystemCount {
			t.Errorf("gateway %q capability count: got %v want %d", system, bySystem[system], wantSystemCount)
		}
		for name := range wantNames {
			if _, ok := bySystem[system][name]; !ok {
				t.Errorf("gateway %q is missing capability %q", system, name)
			}
		}
	}
	if _, ok := bySystem[contracts.InstanceKindNewAPI][contracts.CapabilitySetAccountTrafficShare]; !ok {
		t.Error("newapi is missing the validated traffic-share capability")
	}
}
