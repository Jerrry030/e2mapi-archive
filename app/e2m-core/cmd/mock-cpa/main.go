// mock-cpa is a tiny stand-in for a CLIProxyAPI (CPA) instance's management
// API: Bearer auth, GET /v0/management/auth-files ({files:[...]}) and
// PATCH /v0/management/auth-files/status {name,disabled}. /debug/status flips a
// file's status for health-checker tests.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
)

type authFile struct {
	Name     string `json:"name"`
	Label    string `json:"label,omitempty"`
	Status   string `json:"status"`
	Disabled bool   `json:"disabled"`
	Provider string `json:"provider"`
	Type     string `json:"type"`
	Content  string `json:"content,omitempty"`
	Proxy    string `json:"proxy,omitempty"`
}

func main() {
	addr := getenv("MOCK_CPA_ADDR", ":8092")
	key := getenv("MOCK_CPA_KEY", "mock-cpa-key")

	var mu sync.Mutex
	files := []*authFile{
		{Name: "claude-main.json", Label: "Claude 主号", Status: "ok", Provider: "anthropic", Type: "oauth"},
		{Name: "claude-backup.json", Label: "Claude 备用", Status: "ok", Disabled: true, Provider: "anthropic", Type: "oauth"},
		{Name: "gemini-1.json", Label: "Gemini 号", Status: "ok", Provider: "gemini", Type: "oauth"},
	}

	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return false
		}
		return true
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /v0/management/auth-files", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		out := make([]authFile, 0, len(files))
		for _, f := range files {
			out = append(out, *f)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": out})
	})

	mux.HandleFunc("POST /v0/management/auth-files", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		var body authFile
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid auth file"})
			return
		}
		if body.Status == "" {
			body.Status = "ok"
		}
		mu.Lock()
		defer mu.Unlock()
		for _, f := range files {
			if f.Name == body.Name {
				*f = body
				log.Printf("auth file %s upserted", f.Name)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}
		}
		files = append(files, &body)
		log.Printf("auth file %s created", body.Name)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("PATCH /v0/management/auth-files/status", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		var body struct {
			Name     string `json:"name"`
			Disabled *bool  `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Disabled == nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "name and disabled are required"})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, f := range files {
			if f.Name == body.Name {
				f.Disabled = *body.Disabled
				log.Printf("auth file %s disabled -> %v", f.Name, f.Disabled)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "auth file not found"})
	})

	mux.HandleFunc("PUT /v0/management/auth-files/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		name := r.PathValue("name")
		var body authFile
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid auth file"})
			return
		}
		if body.Name == "" {
			body.Name = name
		}
		if body.Status == "" {
			body.Status = "ok"
		}
		mu.Lock()
		defer mu.Unlock()
		for _, f := range files {
			if f.Name == name {
				*f = body
				log.Printf("auth file %s updated", name)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "auth file not found"})
	})

	mux.HandleFunc("DELETE /v0/management/auth-files/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		name := r.PathValue("name")
		mu.Lock()
		defer mu.Unlock()
		for i, f := range files {
			if f.Name == name {
				files = append(files[:i], files[i+1:]...)
				log.Printf("auth file %s deleted", name)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "auth file not found"})
	})
	// Test-only: flip a file's status (no auth).
	mux.HandleFunc("POST /debug/status", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		status := r.URL.Query().Get("status")
		mu.Lock()
		defer mu.Unlock()
		for _, f := range files {
			if f.Name == name {
				f.Status = status
				log.Printf("debug: %s status -> %s", f.Name, f.Status)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	log.Printf("mock-cpa listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getenv(k, fb string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fb
}
