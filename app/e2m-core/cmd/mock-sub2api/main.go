// Command mock-sub2api is a minimal fake sub2api admin API for end-to-end
// verification of E2M's account-switching path. It implements just enough of the
// real admin surface: list accounts, toggle schedulable — with x-api-key auth
// and the {code,data,message} envelope. Not for production.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type account struct {
	ID          int      `json:"id"`
	Platform    string   `json:"platform"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	Schedulable bool     `json:"schedulable"`
	Priority    int      `json:"priority"`
	Name        string   `json:"name"`
	ExternalRef string   `json:"external_ref,omitempty"`
	Groups      []string `json:"groups,omitempty"`
}

func main() {
	addr := getenv("MOCK_SUB2API_ADDR", ":8090")
	adminKey := getenv("MOCK_SUB2API_KEY", "mock-admin-key")

	var mu sync.Mutex
	accounts := map[int]*account{
		1: {ID: 1, Platform: "anthropic", Type: "oauth", Status: "active", Schedulable: true, Priority: 50, Name: "主号 Claude"},
		2: {ID: 2, Platform: "anthropic", Type: "oauth", Status: "active", Schedulable: true, Priority: 55, Name: "备用号 Claude"},
		3: {ID: 3, Platform: "openai", Type: "apikey", Status: "error", Schedulable: false, Priority: 60, Name: "OpenAI Key"},
	}
	nextID := 4

	requireKey := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("x-api-key") != adminKey {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 1001, "message": "invalid admin key"})
			return false
		}
		return true
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		ids := make([]int, 0, len(accounts))
		for id := range accounts {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		list := make([]*account, 0, len(accounts))
		for _, id := range ids {
			list = append(list, accounts[id])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": list})
	})

	mux.HandleFunc("POST /api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		var body struct {
			Name        string   `json:"name"`
			Platform    string   `json:"platform"`
			Type        string   `json:"type"`
			Schedulable bool     `json:"schedulable"`
			Priority    int      `json:"priority"`
			ExternalRef string   `json:"external_ref"`
			Groups      []string `json:"groups"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 400, "message": "invalid account body"})
			return
		}
		if body.Name == "" {
			body.Name = "managed-account"
		}
		mu.Lock()
		defer mu.Unlock()
		created := &account{ID: nextID, Name: body.Name, Platform: body.Platform, Type: body.Type, Status: "active", Schedulable: body.Schedulable, Priority: body.Priority, ExternalRef: body.ExternalRef, Groups: body.Groups}
		nextID++
		accounts[created.ID] = created
		log.Printf("account %d created ref=%s", created.ID, created.ExternalRef)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": created})
	})

	mux.HandleFunc("POST /api/v1/admin/accounts/{id}/schedulable", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		var body struct {
			Schedulable bool `json:"schedulable"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		idStr := r.PathValue("id")
		mu.Lock()
		defer mu.Unlock()
		for _, ac := range accounts {
			if itoa(ac.ID) == idStr {
				ac.Schedulable = body.Schedulable
				log.Printf("account %d schedulable -> %v", ac.ID, body.Schedulable)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": nil})
	})

	mux.HandleFunc("PUT /api/v1/admin/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			Name        string   `json:"name"`
			Platform    string   `json:"platform"`
			Type        string   `json:"type"`
			Schedulable bool     `json:"schedulable"`
			Priority    int      `json:"priority"`
			ExternalRef string   `json:"external_ref"`
			Groups      []string `json:"groups"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		ac, ok := accounts[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "message": "account not found"})
			return
		}
		if body.Name != "" {
			ac.Name = body.Name
		}
		if body.Platform != "" {
			ac.Platform = body.Platform
		}
		if body.Type != "" {
			ac.Type = body.Type
		}
		ac.Schedulable = body.Schedulable
		if body.Priority != 0 {
			ac.Priority = body.Priority
		}
		if body.ExternalRef != "" {
			ac.ExternalRef = body.ExternalRef
		}
		if body.Groups != nil {
			ac.Groups = body.Groups
		}
		log.Printf("account %d updated ref=%s", ac.ID, ac.ExternalRef)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": ac})
	})

	mux.HandleFunc("DELETE /api/v1/admin/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requireKey(w, r) {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if _, ok := accounts[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "message": "account not found"})
			return
		}
		delete(accounts, id)
		log.Printf("account %d deleted", id)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": nil})
	})
	// Test-only: flip an account's status so the health checker can be exercised.
	// e.g. POST /debug/status?id=1&status=error  (no auth, test harness only)
	mux.HandleFunc("POST /debug/status", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.URL.Query().Get("id")
		status := r.URL.Query().Get("status")
		mu.Lock()
		defer mu.Unlock()
		for _, ac := range accounts {
			if itoa(ac.ID) == idStr {
				ac.Status = status
				log.Printf("account %d status -> %s", ac.ID, status)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	log.Printf("mock-sub2api listening on %s (x-api-key=%s)", addr, adminKey)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("mock-sub2api stopped: %v", err)
	}
}

func itoa(i int) string {
	return strings.TrimSpace(jsonNumber(i))
}

func jsonNumber(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
