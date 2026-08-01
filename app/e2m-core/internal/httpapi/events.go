package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"e2m.local/core/internal/auth"
)

// StreamEvent is one console-facing realtime event (audit written, health
// check finished, approval transitioned, drift detected...).
type StreamEvent struct {
	Type    string    `json:"type"`
	UserID  int64     `json:"user_id,omitempty"`
	Payload any       `json:"payload,omitempty"`
	At      time.Time `json:"at"`
}

// EventBus is a small in-process pub/sub used to push events to SSE clients.
// Slow subscribers drop events rather than block publishers.
type EventBus struct {
	mu   sync.RWMutex
	subs map[chan StreamEvent]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{subs: map[chan StreamEvent]struct{}{}}
}

// Publish fans the event out to all subscribers, never blocking.
func (b *EventBus) Publish(ev StreamEvent) {
	if b == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default: // subscriber too slow; drop
		}
	}
}

func (b *EventBus) subscribe() chan StreamEvent {
	ch := make(chan StreamEvent, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *EventBus) unsubscribe(ch chan StreamEvent) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
	close(ch)
}

// handleEventStream streams the platform's detailed operational events as SSE.
// Owner-facing managed-upstream state is served by the anonymous pool-health
// summary; this feed also carries legacy account and instance payloads and is
// therefore restricted to platform administrators.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if s.events == nil {
		writeError(w, http.StatusNotImplemented, "unavailable", "event stream not enabled")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unsupported", "streaming unsupported")
		return
	}
	user := currentUser(r)
	if !auth.IsPlatformAdmin(user) {
		writeError(w, http.StatusForbidden, "forbidden", "platform administrator required for event stream")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	// Tell the client we're live before the first event.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ch := s.events.subscribe()
	defer s.events.unsubscribe(ch)

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-ch:
			raw, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, raw)
			flusher.Flush()
		}
	}
}
