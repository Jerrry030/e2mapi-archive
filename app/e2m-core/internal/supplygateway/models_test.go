package supplygateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

func newModelsGatewayForTest(t *testing.T) (*Handler, *fakeStore) {
	t.Helper()
	v := vault.NewMemoryVault()
	fake := &fakeStore{}
	h, err := New(fake, v, Config{Currency: "CNY"})
	if err != nil {
		t.Fatal(err)
	}
	return h, fake
}

func TestModelsReturnsCatalogInOpenAIShapeAndHashesTheKey(t *testing.T) {
	h, fake := newModelsGatewayForTest(t)
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	fake.catalog = contracts.SupplyModelCatalog{Models: []contracts.SupplyModelEntry{
		{Model: "gpt-4o", CreatedAt: created, Channels: 2},
		{Model: "gpt-4o-mini", CreatedAt: created, Channels: 1},
	}}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
		Complete *bool `json:"e2m_catalog_complete"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Object != "list" || len(payload.Data) != 2 {
		t.Fatalf("payload=%+v", payload)
	}
	if payload.Data[0].ID != "gpt-4o" || payload.Data[0].Object != "model" ||
		payload.Data[0].Created != created.Unix() || payload.Data[0].OwnedBy != "e2m" {
		t.Fatalf("first entry=%+v", payload.Data[0])
	}
	// An exhaustive catalog must not carry the incompleteness marker.
	if payload.Complete != nil {
		t.Fatalf("unexpected completeness marker: %v", *payload.Complete)
	}
	// The plaintext key must never reach the store.
	if len(fake.catalogHashes) != 1 || fake.catalogHashes[0] != contracts.HashVirtualKey("e2m_v1_downstream") {
		t.Fatalf("hashes=%v", fake.catalogHashes)
	}
	// The gateway currency scopes the catalog, exactly as it scopes a
	// reservation; a channel priced in another currency cannot serve either.
	if len(fake.catalogCurrencies) != 1 || fake.catalogCurrencies[0] != "CNY" {
		t.Fatalf("currencies=%v", fake.catalogCurrencies)
	}
}

// The upstream is the authority on a model id. Folding case would make the
// advertised id differ from the declared one, which both misses the channel's
// case-sensitive model mapping and can be rejected by the upstream outright.
func TestModelsPreservesTheDeclaredModelSpelling(t *testing.T) {
	h, fake := newModelsGatewayForTest(t)
	fake.catalog = contracts.SupplyModelCatalog{Models: []contracts.SupplyModelEntry{
		{Model: "Qwen/Qwen2.5-7B-Instruct", Channels: 1},
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "Qwen/Qwen2.5-7B-Instruct" {
		t.Fatalf("data=%+v, want the declared spelling verbatim", payload.Data)
	}
}

// ServeMux matches on the escaped path, so a percent-encoded separator never
// reaches the real route. Reporting the decoded path here produced a
// self-contradictory "POST /v1/chat/completions accepts POST".
func TestUnknownEndpointDoesNotMistakeAnEncodedPathForARealRoute(t *testing.T) {
	h, _ := newModelsGatewayForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat%2Fcompletions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if allow := recorder.Header().Get("Allow"); allow != "" {
		t.Fatalf("unexpected Allow header: %q", allow)
	}
}

func TestModelsMarksAWildcardUpstreamCatalogIncomplete(t *testing.T) {
	h, fake := newModelsGatewayForTest(t)
	fake.catalog = contracts.SupplyModelCatalog{
		Models:       []contracts.SupplyModelEntry{{Model: "gpt-4o"}},
		Unenumerable: true,
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("x-api-key", "e2m_v1_downstream")
	recorder := httptest.NewRecorder()
	h.Routes().ServeHTTP(recorder, req)

	var payload struct {
		Data []struct {
			Created int64 `json:"created"`
		} `json:"data"`
		Complete *bool `json:"e2m_catalog_complete"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Complete == nil || *payload.Complete {
		t.Fatalf("want e2m_catalog_complete=false, got %v", payload.Complete)
	}
	// A zero timestamp must render as 0, not as a large negative epoch.
	if len(payload.Data) != 1 || payload.Data[0].Created != 0 {
		t.Fatalf("data=%+v", payload.Data)
	}
}

func TestModelsRejectsAnUnauthenticatedCallerBeforeTouchingTheStore(t *testing.T) {
	h, fake := newModelsGatewayForTest(t)
	for _, header := range []struct{ name, value string }{
		{"Authorization", ""},
		{"Authorization", "Bearer not-an-e2m-key"},
		{"x-api-key", "sk-someone-elses"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if header.value != "" {
			req.Header.Set(header.name, header.value)
		}
		recorder := httptest.NewRecorder()
		h.Routes().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s=%q: status=%d", header.name, header.value, recorder.Code)
		}
	}
	if len(fake.catalogHashes) != 0 {
		t.Fatalf("store was consulted for a rejected caller: %v", fake.catalogHashes)
	}
}

func TestModelsMapsStoreRejectionsOntoTheGatewayErrorContract(t *testing.T) {
	for _, test := range []struct {
		err        error
		wantStatus int
	}{
		{store.ErrNotFound, http.StatusUnauthorized},
		{store.ErrInvalid, http.StatusBadRequest},
	} {
		h, fake := newModelsGatewayForTest(t)
		fake.catalogErr = test.err
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
		recorder := httptest.NewRecorder()
		h.Routes().ServeHTTP(recorder, req)
		if recorder.Code != test.wantStatus {
			t.Fatalf("%v: status=%d want %d", test.err, recorder.Code, test.wantStatus)
		}
	}
}

// Listing models must never move money. Reserving to probe availability would
// write a reservation, a usage record and a balanced journal pair for a request
// the customer never made.
func TestModelsNeverReserves(t *testing.T) {
	h, fake := newModelsGatewayForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
	h.Routes().ServeHTTP(httptest.NewRecorder(), req)
	if len(fake.reserveHashes) != 0 || len(fake.settled) != 0 || len(fake.releasedReason) != 0 {
		t.Fatalf("reserved=%v settled=%v released=%v", fake.reserveHashes, fake.settled, fake.releasedReason)
	}
}

// The console SPA used to answer these paths with 200 and index.html, so an
// OpenAI client parsed HTML as JSON and reported an unrelated failure.
func TestUnimplementedDataPlanePathsAnswerJSONNotConsoleHTML(t *testing.T) {
	h, _ := newModelsGatewayForTest(t)
	for _, path := range []string{"/v1/embeddings", "/v1/completions", "/v1/audio/speech", "/v1/anything"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
		recorder := httptest.NewRecorder()
		h.Routes().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d", path, recorder.Code)
		}
		if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
			t.Fatalf("%s: content-type=%q", path, contentType)
		}
		var payload struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s: %v (body=%s)", path, err, recorder.Body.String())
		}
		if payload.Error.Type != "unknown_endpoint" || !strings.Contains(payload.Error.Message, path) {
			t.Fatalf("%s: error=%+v", path, payload.Error)
		}
	}
}

func TestImplementedEndpointsReportTheMethodTheyAccept(t *testing.T) {
	h, _ := newModelsGatewayForTest(t)
	for _, test := range []struct {
		method, path, wantAllow string
	}{
		{http.MethodGet, "/v1/chat/completions", http.MethodPost},
		{http.MethodGet, "/v1/messages", http.MethodPost},
		{http.MethodPost, "/v1/models", http.MethodGet},
	} {
		req := httptest.NewRequest(test.method, test.path, nil)
		req.Header.Set("Authorization", "Bearer e2m_v1_downstream")
		recorder := httptest.NewRecorder()
		h.Routes().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != test.wantAllow {
			t.Fatalf("%s %s: status=%d allow=%q", test.method, test.path,
				recorder.Code, recorder.Header().Get("Allow"))
		}
	}
}
