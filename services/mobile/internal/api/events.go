package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const keepAlive = 15 * time.Second

type flowEvent struct {
	FlowID  string `json:"flow_id"`
	Code    string `json:"code"`
	Version int    `json:"version"`
}

// hub fans a publish out to the devices of the same tenant connected to this
// pod. Devices on other pods learn of it on their next menu load.
type hub struct {
	mu   sync.Mutex
	subs map[string]map[chan flowEvent]struct{}
}

func newHub() *hub { return &hub{subs: map[string]map[chan flowEvent]struct{}{}} }

func (h *hub) subscribe(tenantID string) (chan flowEvent, func()) {
	ch := make(chan flowEvent, 16)
	h.mu.Lock()
	if h.subs[tenantID] == nil {
		h.subs[tenantID] = map[chan flowEvent]struct{}{}
	}
	h.subs[tenantID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[tenantID], ch)
		if len(h.subs[tenantID]) == 0 {
			delete(h.subs, tenantID)
		}
		h.mu.Unlock()
	}
}

func (h *hub) publish(tenantID string, e flowEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[tenantID] {
		select {
		case ch <- e:
		default:
		}
	}
}

// events streams flow.published to the tenant's devices. The request timeout
// recycles the stream; EventSource reconnects on its own.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, stop := a.hub.subscribe(tenantOf(r))
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ":\n\n")
	fl.Flush()
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			data, _ := json.Marshal(e)
			_, _ = fmt.Fprintf(w, "event: flow.published\ndata: %s\n\n", data)
			fl.Flush()
		case <-tick.C:
			_, _ = fmt.Fprint(w, ":\n\n")
			fl.Flush()
		}
	}
}
