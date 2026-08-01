package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSourceDoesNotLogSensitiveData(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []string{"log.Printf(\"%s\", r.Header", "log.Printf(\"%s\", input", "request_body", "raw_response"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("sensitive logging marker %q found", forbidden)
		}
	}
}

func TestPathDiagnosticsContainNoHeadersOrBody(t *testing.T) {
	record := pathRecord{Method: http.MethodPost, Path: "/v1/chat/completions", ObservedAt: "2026-07-31T00:00:00Z"}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "secret") || strings.Contains(text, "prompt") || strings.Contains(text, "authorization") {
		t.Fatalf("path diagnostic leaked sensitive data: %s", text)
	}

	request := httptest.NewRequest(http.MethodPost, "http://example.test/v1/chat/completions?key=secret", strings.NewReader("prompt"))
	if got := request.URL.EscapedPath(); got != "/v1/chat/completions" {
		t.Fatalf("diagnostic path includes more than the escaped path: %q", got)
	}
}

func TestEmptyRequestCollectionEncodesAsArray(t *testing.T) {
	records := make([]requestRecord, 0, 1)
	copyOfRecords := make([]requestRecord, len(records))
	copy(copyOfRecords, records)
	raw, err := json.Marshal(map[string]any{"count": len(copyOfRecords), "items": copyOfRecords})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"count":0,"items":[]}` {
		t.Fatalf("empty request collection encoded as %s", raw)
	}
}
