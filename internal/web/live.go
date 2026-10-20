package web

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Live updates: the dashboard process watches the database for commits
// from any process (store.Watch) and tells each open page "something
// changed" over server-sent events. The page then fetches what it shows.

// heartbeat keeps idle event streams from being closed along the way.
const heartbeat = 25 * time.Second

// hub fans change notices out to the open pages.
type hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]bool
}

func newHub() *hub { return &hub{subs: map[chan struct{}]bool{}} }

func (h *hub) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// notify tells every page; a page that hasn't read the last notice yet
// doesn't need another.
func (h *hub) notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// events streams change notices until the page goes away or the server
// stops.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming isn't supported", http.StatusInternalServerError)
		return
	}
	ch, done := s.hub.subscribe()
	defer done()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	t := time.NewTicker(heartbeat)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			fmt.Fprint(w, "event: change\ndata: {}\n\n")
		case <-t.C:
			fmt.Fprint(w, ": still here\n\n")
		}
		flusher.Flush()
	}
}
