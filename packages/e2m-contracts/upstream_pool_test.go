package contracts

import (
	"strings"
	"testing"
)

func TestUpstreamSourceIdentityRejectsConnectionMaterial(t *testing.T) {
	for _, value := range []string{
		"", " source-a", "source-a ", "source\nname",
		"https://supplier.example", "Bearer secret", "Authorization: secret",
		"token=secret", "Cookie: session=secret", "cookie=session-secret", "Set-Cookie: session=secret",
		"header: secret", "headers=secret", "raw_response=secret", "sk-secret",
		"credential=super-secret", "credential: super-secret", "secret=abc",
		"Authorization : abc", "token = abc", `{"token":"abc"}`, `{"cookie":"abc"}`,
		"token abc", "Authorization abc", "credential super-secret", "raw response abc",
		"token-abc", "credential-super-secret", "raw-response-abc", "api.supplier.example/v1",
		"https-supplier-example", "endpoint-url-supplier",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature",
		strings.Repeat("a1", 20),
		strings.Repeat("a", MaxUpstreamSourceIdentityBytes+1),
	} {
		if IsUpstreamSourceIdentity(value) {
			t.Fatalf("unsafe source identity accepted: %q", value)
		}
	}
	for _, value := range []string{"source-a", "opaque_owner_region_1", "来源-a-香港"} {
		if !IsUpstreamSourceIdentity(value) {
			t.Fatalf("ordinary source identity rejected: %q", value)
		}
	}
}

func TestPublishedBindingRequiresSchedulingFenceUsesDurableNoDispatchProof(t *testing.T) {
	tests := []struct {
		name      string
		ownership GatewayAccountOwnership
		state     PublishedBindingState
		lastError string
		want      bool
	}{
		{"explicit marker", GatewayAccountOwnerProvided, BindingFailed,
			OwnerMetadataUpdateNotDispatchedMarker + ": gateway validation failed", false},
		{"legacy exact sentinel", GatewayAccountOwnerProvided, BindingFailed,
			LegacyManagedAccountSchedulingFencePrefix + "account-9 belongs to route plan plan-1 at generation 2", false},
		{"legacy embedded text", GatewayAccountOwnerProvided, BindingFailed,
			"gateway timeout after " + LegacyManagedAccountSchedulingFencePrefix + "account-9 belongs to route plan plan-1", true},
		{"legacy incomplete sentinel", GatewayAccountOwnerProvided, BindingFailed,
			LegacyManagedAccountSchedulingFencePrefix + "account-9 timed out", true},
		{"generic timeout", GatewayAccountOwnerProvided, BindingFailed, "context deadline exceeded", true},
		{"owner active", GatewayAccountOwnerProvided, BindingActive,
			OwnerMetadataUpdateNotDispatchedMarker + ": stale marker", true},
		{"platform failed", GatewayAccountPlatformManaged, BindingFailed,
			LegacyManagedAccountSchedulingFencePrefix + "account-9 belongs to route plan plan-1", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := (PublishedBinding{AccountOwnership: test.ownership, State: test.state, LastError: test.lastError}).RequiresSchedulingFence()
			if got != test.want {
				t.Fatalf("RequiresSchedulingFence()=%v want=%v", got, test.want)
			}
		})
	}
}
