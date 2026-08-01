// mock-openai is a disposable OpenAI-compatible data-plane upstream used by
// the UI-17 release drill. It deliberately exposes no control-plane mutation
// and never logs credentials, prompts, request bodies, or raw responses.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const maxBodyBytes = 64 << 10

type requestRecord struct {
	CorrelationSHA256 string `json:"correlation_sha256"`
	Mode              string `json:"mode"`
	ObservedAt        string `json:"observed_at"`
}

type pathRecord struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	ObservedAt string `json:"observed_at"`
}

func main() {
	addr := strings.TrimSpace(os.Getenv("MOCK_OPENAI_ADDR"))
	if addr == "" {
		addr = ":8093"
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("MOCK_OPENAI_MODE")))
	if mode == "" {
		mode = "success"
	}
	if mode != "success" && mode != "retryable_failure" {
		log.Fatalf("unsupported MOCK_OPENAI_MODE %q", mode)
	}
	var mu sync.Mutex
	records := make([]requestRecord, 0, 32)
	paths := make([]pathRecord, 0, 32)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": mode})
	})
	mux.HandleFunc("GET /debug/requests", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		// Preserve an empty JSON array rather than serializing a nil slice as
		// null. The evidence endpoint keeps one strict schema before and after
		// the first relayed request.
		copyOfRecords := make([]requestRecord, len(records))
		copy(copyOfRecords, records)
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"count": len(copyOfRecords), "items": copyOfRecords})
	})
	mux.HandleFunc("GET /debug/paths", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		copyOfPaths := make([]pathRecord, len(paths))
		copy(copyOfPaths, paths)
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"count": len(copyOfPaths), "items": copyOfPaths})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "unauthorized", "type": "authentication_error"}})
			return
		}
		reader := http.MaxBytesReader(w, r.Body, maxBodyBytes)
		defer reader.Close()
		var input struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		decoder := json.NewDecoder(reader)
		if err := decoder.Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid request", "type": "invalid_request_error"}})
			return
		}
		model := strings.TrimSpace(input.Model)
		if model != "gpt-test" && model != "gpt-4o-mini" && model != "gpt-e2m-failover" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid request", "type": "invalid_request_error"}})
			return
		}
		correlation := strings.TrimSpace(r.Header.Get("X-E2M-Correlation"))
		if correlation == "" {
			correlation = "missing"
		}
		sum := sha256.Sum256([]byte(correlation))
		hash := hex.EncodeToString(sum[:])
		now := time.Now().UTC()
		mu.Lock()
		records = append(records, requestRecord{CorrelationSHA256: hash, Mode: mode, ObservedAt: now.Format(time.RFC3339Nano)})
		mu.Unlock()
		if mode == "retryable_failure" {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{
				"message": "temporary upstream failure", "type": "service_unavailable", "code": "temporarily_unavailable",
			}})
			return
		}
		if input.Stream {
			writeStream(w, hash, model, now)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl-" + hash[:16], "object": "chat.completion", "created": now.Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
		})
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && !strings.HasPrefix(r.URL.Path, "/debug/") {
			mu.Lock()
			if len(paths) == cap(paths) {
				copy(paths, paths[1:])
				paths = paths[:len(paths)-1]
			}
			paths = append(paths, pathRecord{
				Method:     r.Method,
				Path:       r.URL.EscapedPath(),
				ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
			})
			mu.Unlock()
		}
		mux.ServeHTTP(w, r)
	})
	log.Printf("mock-openai listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}

func writeStream(w http.ResponseWriter, hash, model string, now time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"message": "streaming unsupported"}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	id := "chatcmpl-" + hash[:16]
	chunks := []map[string]any{
		{
			"id": id, "object": "chat.completion.chunk", "created": now.Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}},
		},
		{
			"id": id, "object": "chat.completion.chunk", "created": now.Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "ok"}, "finish_reason": nil}},
		},
		{
			"id": id, "object": "chat.completion.chunk", "created": now.Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
		},
	}
	for _, chunk := range chunks {
		raw, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
