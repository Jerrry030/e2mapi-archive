// mock-newapi is a tiny stand-in for a new-api instance's admin API, used to
// exercise the newapi adapter end to end: Bearer+New-Api-User auth, the
// {success,message,data} envelope, GET /api/channel/ and PUT /api/channel/.
// A /debug/status endpoint lets tests flip a channel's status (e.g. to 3 =
// auto-disabled) to trigger the health checker.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
)

type channel struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Type      int      `json:"type"`
	Status    int      `json:"status"` // 1 enabled, 2 manual off, 3 auto off
	Priority  int64    `json:"priority"`
	Group     string   `json:"group"`
	Tag       string   `json:"tag,omitempty"`
	Balance   *float64 `json:"balance,omitempty"`
	UsedQuota *float64 `json:"used_quota,omitempty"`
}

func fptr(v float64) *float64 { return &v }

func main() {
	addr := getenv("MOCK_NEWAPI_ADDR", ":8091")
	token := getenv("MOCK_NEWAPI_TOKEN", "mock-newapi-token")
	uid := getenv("MOCK_NEWAPI_UID", "1")

	var mu sync.Mutex
	channels := []*channel{
		{ID: 1, Name: "主渠道 Anthropic", Type: 14, Status: 1, Priority: 10, Group: "default", Balance: fptr(88.40), UsedQuota: fptr(311.60)},
		{ID: 2, Name: "备用渠道 Anthropic", Type: 14, Status: 2, Priority: 5, Group: "default", Balance: fptr(200.00), UsedQuota: fptr(0)},
		{ID: 3, Name: "OpenAI 渠道", Type: 1, Status: 1, Priority: 0, Group: "default,vip", Balance: fptr(4.20), UsedQuota: fptr(95.80)},
	}
	nextID := int64(4)

	ok := func(w http.ResponseWriter, data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "", "data": data})
	}
	fail := func(w http.ResponseWriter, msg string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": msg})
	}
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("New-Api-User") != uid {
			w.WriteHeader(http.StatusUnauthorized)
			fail(w, "无权进行此操作，未登录且未提供 access token")
			return false
		}
		return true
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/channel/", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		items := make([]channel, 0, len(channels))
		for _, c := range channels {
			items = append(items, *c)
		}
		ok(w, map[string]any{"items": items, "total": len(items)})
	})

	mux.HandleFunc("POST /api/channel/", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		var body struct {
			Name     string `json:"name"`
			Type     int    `json:"type"`
			Status   int    `json:"status"`
			Priority int64  `json:"priority"`
			Group    string `json:"group"`
			Tag      string `json:"tag"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, "invalid channel body")
			return
		}
		if body.Status == 0 {
			body.Status = 1
		}
		if body.Name == "" {
			body.Name = "managed-channel"
		}
		mu.Lock()
		defer mu.Unlock()
		created := &channel{ID: nextID, Name: body.Name, Type: body.Type, Status: body.Status, Priority: body.Priority, Group: body.Group, Tag: body.Tag}
		nextID++
		channels = append(channels, created)
		log.Printf("channel %d created tag=%s", created.ID, created.Tag)
		ok(w, *created)
	})

	mux.HandleFunc("PUT /api/channel/", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		var body struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Type     int    `json:"type"`
			Status   *int   `json:"status"`
			Priority int64  `json:"priority"`
			Group    string `json:"group"`
			Tag      string `json:"tag"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, "invalid channel body")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, c := range channels {
			if c.ID == body.ID {
				if body.Name != "" {
					c.Name = body.Name
				}
				if body.Type != 0 {
					c.Type = body.Type
				}
				if body.Status != nil {
					c.Status = *body.Status
				}
				if body.Priority != 0 {
					c.Priority = body.Priority
				}
				if body.Group != "" {
					c.Group = body.Group
				}
				if body.Tag != "" {
					c.Tag = body.Tag
				}
				log.Printf("channel %d updated status=%d tag=%s", c.ID, c.Status, c.Tag)
				ok(w, *c)
				return
			}
		}
		fail(w, "channel not found")
	})

	mux.HandleFunc("DELETE /api/channel/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		mu.Lock()
		defer mu.Unlock()
		for i, c := range channels {
			if c.ID == id {
				channels = append(channels[:i], channels[i+1:]...)
				log.Printf("channel %d deleted", id)
				ok(w, map[string]any{"id": id})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		fail(w, "channel not found")
	})
	// Test-only: set a channel's balance without auth (exercise balance alerts).
	mux.HandleFunc("POST /debug/balance", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		bal, err := strconv.ParseFloat(r.URL.Query().Get("balance"), 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, c := range channels {
			if c.ID == id {
				c.Balance = &bal
				log.Printf("debug: channel %d balance -> %.2f", c.ID, bal)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Test-only: flip a channel's status without auth (harness use).
	mux.HandleFunc("POST /debug/status", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		status, _ := strconv.Atoi(r.URL.Query().Get("status"))
		mu.Lock()
		defer mu.Unlock()
		for _, c := range channels {
			if c.ID == id {
				c.Status = status
				log.Printf("debug: channel %d status -> %d", c.ID, c.Status)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	log.Printf("mock-newapi listening on %s (uid=%s)", addr, uid)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getenv(k, fb string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fb
}
