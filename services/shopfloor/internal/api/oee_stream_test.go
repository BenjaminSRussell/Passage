package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// liveFake hands out a stream that the test changes while the SSE stream runs.
type liveFake struct {
	*floorFake
	mu     sync.Mutex
	stream *domain.OEEStream
	err    error
	loads  int
}

func (f *liveFake) set(s *domain.OEEStream) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stream = s
}

func (f *liveFake) LoadOEEStreams(_ context.Context, _ []string, _, _ time.Time) (map[string]domain.OEEStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.err != nil {
		return nil, f.err
	}
	if f.stream == nil {
		return map[string]domain.OEEStream{}, nil
	}
	return map[string]domain.OEEStream{resourceID: *f.stream}, nil
}

func streamServer(t *testing.T, fs *liveFake, now func() time.Time) *httptest.Server {
	t.Helper()
	r := httpx.NewRouter()
	a := New(fs)
	a.now = now
	a.streamPoll = 20 * time.Millisecond
	a.streamLife = 2 * time.Second
	a.Register(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func openStream(t *testing.T, srv *httptest.Server, query string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/shopfloor/oee/stream?"+query, nil)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", "supervisor-1")
	req.Header.Set("X-Passage-Duties", DutyView)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

// nextTile reads SSE lines up to the next oee event and decodes it.
func nextTile(t *testing.T, sc *bufio.Scanner) oeeTile {
	t.Helper()
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && event == "oee":
			var tile oeeTile
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &tile); err != nil {
				t.Fatal(err)
			}
			return tile
		}
	}
	t.Fatalf("stream ended without an oee event: %v", sc.Err())
	return oeeTile{}
}

// AC-25.4: the live tile gets the current bucket at once and again within the poll
// interval of a new machine count, without waiting for the 15-minute rollup.
func TestOEEStream_PushesCurrentBucketAndChanges(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 6, 0, 0, 0, time.UTC)
	now := t0.Add(10 * time.Minute)
	running := domain.OEEStream{
		IdealCycleSeconds: decimal.NewFromInt(60),
		Initial:           domain.MachineRunning,
		Events:            []domain.MachineEvent{{Seq: 1, Kind: domain.EventCount, At: t0.Add(5 * time.Minute), GoodCount: 4, RejectCount: 1}},
	}
	fs := &liveFake{floorFake: newFloorFake(), stream: &running}
	srv := streamServer(t, fs, func() time.Time { return now })

	res := openStream(t, srv, "resourceId="+resourceID+"&bucket=15m")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" || res.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("status %d, headers %v", res.StatusCode, res.Header)
	}
	sc := bufio.NewScanner(res.Body)
	first := nextTile(t, sc)
	if first.Bucket == nil || !first.BucketStart.Equal(t0) || !first.BucketEnd.Equal(t0.Add(15*time.Minute)) || first.Bucket.TotalCount != 5 || first.Bucket.GoodCount != 4 {
		t.Fatalf("first tile %+v", first)
	}

	more := running
	more.Events = append(append([]domain.MachineEvent{}, running.Events...), domain.MachineEvent{Seq: 2, Kind: domain.EventCount, At: t0.Add(8 * time.Minute), GoodCount: 3})
	fs.set(&more)
	start := time.Now()
	second := nextTile(t, sc)
	if second.Bucket == nil || second.Bucket.TotalCount != 8 || second.Bucket.GoodCount != 7 {
		t.Fatalf("second tile %+v", second)
	}
	if lag := time.Since(start); lag > 5*time.Second {
		t.Fatalf("tile lag %s, want under 5 s", lag)
	}

	fs.set(nil)
	if third := nextTile(t, sc); third.Bucket != nil {
		t.Fatalf("a resource without a machine streams a null bucket, got %+v", third.Bucket)
	}
}

// A repeated computation of the same bucket sends nothing; the stream ends at its
// lifetime so the browser reconnects with the retry hint.
func TestOEEStream_SendsOnlyChangesAndEnds(t *testing.T) {
	now := time.Date(2026, 10, 21, 6, 10, 0, 0, time.UTC)
	fs := &liveFake{floorFake: newFloorFake()}
	r := httpx.NewRouter()
	a := New(fs)
	a.now = func() time.Time { return now }
	a.streamPoll = 10 * time.Millisecond
	a.streamLife = 150 * time.Millisecond
	a.Register(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	res := openStream(t, srv, "resourceId="+resourceID)
	body := new(strings.Builder)
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		body.WriteString(sc.Text() + "\n")
	}
	out := body.String()
	if !strings.HasPrefix(out, "retry: 1000\n") || strings.Count(out, "event: oee") != 1 {
		t.Fatalf("stream body %q", out)
	}
	fs.mu.Lock()
	loads := fs.loads
	fs.mu.Unlock()
	if loads < 3 {
		t.Fatalf("stream recomputed %d times, want a poll", loads)
	}
}

func TestOEEStream_ValidationAndStoreFailure(t *testing.T) {
	fs := &liveFake{floorFake: newFloorFake()}
	h := newRouter(fs)
	for _, q := range []string{"", "resourceId=x", "resourceId=" + resourceID + "&bucket=10m"} {
		if rec := call(t, h, http.MethodGet, "/v1/shopfloor/oee/stream?"+q, "", DutyView); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d", q, rec.Code)
		}
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/oee/stream?resourceId="+resourceID, "", ""); rec.Code != http.StatusForbidden {
		t.Errorf("without duty: status %d", rec.Code)
	}
	fs.err = errors.New("db down")
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/oee/stream?resourceId="+resourceID, "", DutyView); rec.Code != http.StatusInternalServerError {
		t.Errorf("store failure: status %d", rec.Code)
	}
}
