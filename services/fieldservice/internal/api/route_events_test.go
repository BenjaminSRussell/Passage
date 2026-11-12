package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/routefeed"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

func openStream(t *testing.T, h http.Handler, ctx context.Context, path string) (*http.Response, *bufio.Reader, context.CancelFunc) {
	t.Helper()
	principal, _ := tenant.FromContext(ctx)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(tenant.With(r.Context(), principal)))
	}))
	t.Cleanup(srv.Close)
	reqCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = res.Body.Close() })
	return res, bufio.NewReader(res.Body), cancel
}

// readEvent reads one server-sent event block: its event name and data.
func readEvent(t *testing.T, r *bufio.Reader) (name, data string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if name != "" || data != "" {
					return
				}
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 seconds")
	}
	return name, data
}

func TestAPI_RouteEventsStreamReadyThenRouteWhenTheFeedFires(t *testing.T) {
	a, h, ctx := dbAPI(t, nil)
	feed := routefeed.New()
	a.WithRouteFeed(feed)
	tech, _, err := a.store.CreateTechnician(ctx, store.Technician{Code: "SSE1", Name: "Tech SSE", LegalEntityID: ids.New(), TerritoryIDs: []string{ids.New()}, TimeZone: "America/Chicago",
		ShiftDays: []int{1, 2, 3, 4, 5}, ShiftStartMinute: 480, ShiftEndMinute: 1020, MaxDailyMinutes: 480, Active: true}, "k-sse", "h-sse")
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := tenant.FromContext(ctx)
	res, body, _ := openStream(t, h, ctx, "/v1/service/technicians/"+tech.ID+"/day/events")
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") || res.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("status %d headers %v", res.StatusCode, res.Header)
	}
	if name, data := readEvent(t, body); name != "ready" || !strings.Contains(data, tech.ID) {
		t.Fatalf("first event %q %q", name, data)
	}
	feed.Publish(routefeed.Payload(principal.TenantID, ids.New())) // someone else's route
	feed.Publish(routefeed.Payload(principal.TenantID, tech.ID))
	if name, data := readEvent(t, body); name != "route" || !strings.Contains(data, tech.ID) {
		t.Fatalf("route event %q %q", name, data)
	}
}

func TestAPI_RouteEventsRefusals(t *testing.T) {
	a, h, ctx := dbAPI(t, nil)
	// Without a feed the device is told to keep polling.
	if rec := call(t, h, ctx, http.MethodGet, "/v1/service/technicians/"+ids.New()+"/day/events", "", nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), CodeRouteEventsOff) {
		t.Fatalf("no feed: %d %s", rec.Code, rec.Body.String())
	}
	a.WithRouteFeed(routefeed.New())
	if rec := call(t, h, ctx, http.MethodGet, "/v1/service/technicians/not-an-id/day/events", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(t, h, ctx, http.MethodGet, "/v1/service/technicians/"+ids.New()+"/day/events", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown technician: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRouteEventsPathIsOnlyTheStream(t *testing.T) {
	for path, want := range map[string]bool{
		"/v1/service/technicians/me/day/events":   true,
		"/v1/service/technicians/abc/day/events":  true,
		"/v1/service/technicians/me/day":          false,
		"/v1/service/work-orders/x/day/events":    false,
		"/v1/service/technicians/me/day/events/x": false,
	} {
		if RouteEventsPath(path) != want {
			t.Errorf("RouteEventsPath(%q) = %v, want %v", path, !want, want)
		}
	}
}
