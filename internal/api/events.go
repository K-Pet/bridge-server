package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Event is a server-sent event published to connected clients.
type Event struct {
	Type    string         `json:"type"`
	Purchase string        `json:"purchase_id,omitempty"`
	Task    string         `json:"task_id,omitempty"`
	Status  string         `json:"status,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// EventHub fans events out to any number of connected SSE subscribers.
// Publish() is non-blocking — slow subscribers drop events rather than stalling
// the downloader. Safe for concurrent use.
type EventHub struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func NewEventHub() *EventHub {
	return &EventHub{subs: map[chan Event]struct{}{}}
}

func (h *EventHub) Publish(e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
			// Subscriber is too slow; drop this event for them rather than
			// blocking the publisher. They'll catch up on the next one.
		}
	}
}

func (h *EventHub) subscribe() chan Event {
	ch := make(chan Event, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *EventHub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	close(ch)
}

// handleEvents streams events as Server-Sent Events. The stream stays open for
// the life of the request; we emit a comment heartbeat every 20s to keep
// proxies from closing idle connections.
func handleEvents(hub *EventHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // tell nginx to not buffer
		w.WriteHeader(http.StatusOK)

		ch := hub.subscribe()
		defer hub.unsubscribe(ch)

		// Initial hello so clients know the stream is live.
		writeSSE(w, flusher, Event{Type: "hello"})

		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				// SSE comment line — clients ignore it; proxies see traffic.
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				flusher.Flush()
			case e, ok := <-ch:
				if !ok {
					return
				}
				writeSSE(w, flusher, e)
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, e Event) {
	payload, err := json.Marshal(e)
	if err != nil {
		slog.Warn("events: marshal failed", "error", err)
		return
	}
	// event: <type>\ndata: <json>\n\n
	if _, err := w.Write([]byte("event: " + e.Type + "\ndata: ")); err != nil {
		return
	}
	if _, err := w.Write(payload); err != nil {
		return
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return
	}
	flusher.Flush()
}
