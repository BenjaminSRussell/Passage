package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

var t0 = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

const resA = "0195f0a0-0000-7000-8000-0000000000a1"

// fakeServer is the shopfloor ingest as the connector sees it: it stores each (machine, seq) once
// (BR-25.4), counts what it received twice, and can refuse, lose an answer or reject a batch.
type fakeServer struct {
	mu        sync.Mutex
	stored    map[string]domain.MachineEvent
	received  int
	down      bool
	loseReply bool // store the batch, then fail the answer (crash between write and ack)
	reject    bool
	batches   int
}

func newServer() *fakeServer { return &fakeServer{stored: map[string]domain.MachineEvent{}} }

func (s *fakeServer) Send(_ context.Context, ev []domain.MachineEvent) (SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return SendResult{}, errors.New("broker unavailable")
	}
	if s.reject {
		return SendResult{}, fmt.Errorf("%w: 422", ErrRejected)
	}
	s.batches++
	var res SendResult
	for _, e := range ev {
		s.received++
		k := fmt.Sprintf("%s|%d", e.MachineID, e.Seq)
		if _, dup := s.stored[k]; dup {
			res.Dropped++
			continue
		}
		s.stored[k] = e
		res.Accepted++
	}
	if s.loseReply {
		s.loseReply = false
		return SendResult{}, errors.New("connection reset before the answer")
	}
	return res, nil
}

func simRuntime(t *testing.T, srv Transport, machines int, o Options) (*Runtime, []SimMachine) {
	t.Helper()
	cfg, sims := SimConfig(1, machines, func(int) string { return resA })
	now := t0
	o.Site, o.EdgeID, o.Transport = "demo-plant", "edge-1", srv
	if o.Now == nil {
		o.Now = func() time.Time { return now }
	}
	r, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	return r, sims
}

func count(i int) string { return fmt.Sprintf("sim/%d/count", i) }

// A count tag sends the delta since its previous reading, across the 32-bit rollover; its first
// reading is only the baseline; events carry consecutive seq per machine (BR-25.4).
func TestIngest_CountDeltasRolloverAndSeq(t *testing.T) {
	srv := newServer()
	r, _ := simRuntime(t, srv, 1, Options{})
	for i, v := range []float64{4294967290, 4294967294, 3, 10} {
		r.Ingest(Reading{Address: count(1), TS: t0.Add(time.Duration(i) * time.Second), Number: v})
	}
	r.Ingest(Reading{Address: "sim/1/state", TS: t0, Text: "RUNNING"})
	if _, err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []int64{4, 5, 7} // 4294967294-4294967290, wrap to 3 (4+... = 5), 10-3
	var got []int64
	for seq := int64(1); seq <= 3; seq++ {
		e := srv.stored[fmt.Sprintf("SIM-1|%d", seq)]
		if e.Kind != domain.KindCount || e.GoodCount == nil {
			t.Fatalf("seq %d = %+v", seq, e)
		}
		got = append(got, *e.GoodCount)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("count deltas = %v, want %v", got, want)
	}
	if e := srv.stored["SIM-1|4"]; e.State != "RUNNING" {
		t.Fatalf("state event = %+v", e)
	}
	if s := r.Stats(); s.Accepted != 4 || s.BufferLen != 0 || s.Sent != 4 {
		t.Fatalf("stats = %+v", s)
	}
}

// AC2, edge and server side with a simulated broker: 4 machines publish 10,000 events in a
// simulated minute while the broker is down for 5 s and loses one answer after storing a batch. Every
// (machine, seq) is stored, none twice as a different event, and nothing is evicted.
func TestRuntime_BrokerRestartLosesNothing(t *testing.T) {
	srv := newServer()
	now := t0
	r, _ := simRuntime(t, srv, 4, Options{Now: func() time.Time { return now }, BatchSize: 100})
	ctx := context.Background()
	const perMachine = 1250 // count + state per tick: 4 machines x 1250 ticks x 2 = 10,000 events
	counters := make([]uint32, 5)
	for k := 0; k < perMachine; k++ {
		now = t0.Add(time.Duration(k) * 48 * time.Millisecond) // 1250 ticks over 60 s
		for m := 1; m <= 4; m++ {
			counters[m]++
			r.Ingest(Reading{Address: count(m), TS: now, Number: float64(counters[m])})
			r.Ingest(Reading{Address: fmt.Sprintf("sim/%d/state", m), TS: now, Text: "RUNNING"})
		}
		switch {
		case k == 400:
			srv.mu.Lock()
			srv.down = true
			srv.mu.Unlock()
		case k == 500:
			srv.mu.Lock()
			srv.down, srv.loseReply = false, true
			srv.mu.Unlock()
		}
		if k%10 == 0 {
			_, _ = r.Flush(ctx)
		}
	}
	for i := 0; i < 5; i++ {
		_, _ = r.Flush(ctx)
	}
	s := r.Stats()
	// The first count reading of each tag is a baseline, so 4 events fewer than 10,000.
	want := 4*perMachine*2 - 4
	if len(srv.stored) != want {
		t.Fatalf("server stored %d events, want %d (stats %+v)", len(srv.stored), want, s)
	}
	for m := 1; m <= 4; m++ {
		for seq := 1; seq <= perMachine*2-1; seq++ {
			if _, ok := srv.stored[fmt.Sprintf("SIM-%d|%d", m, seq)]; !ok {
				t.Fatalf("SIM-%d seq %d lost", m, seq)
			}
		}
	}
	if s.Evicted != 0 || s.BufferLen != 0 || s.SendFailures == 0 {
		t.Fatalf("stats = %+v: want nothing evicted or left, and the outage seen", s)
	}
	if srv.received <= len(srv.stored) {
		t.Fatalf("received %d, stored %d: the lost answer should have cost a resend", srv.received, len(srv.stored))
	}
}

// A batch the server rejects as invalid is dropped and counted; it does not block what follows.
func TestFlush_RejectedBatchIsDropped(t *testing.T) {
	srv := newServer()
	r, _ := simRuntime(t, srv, 1, Options{})
	r.Ingest(Reading{Address: "sim/1/state", TS: t0, Text: "DOWN"})
	srv.reject = true
	if n, err := r.Flush(context.Background()); err != nil || n != 1 {
		t.Fatalf("flush = %d, %v", n, err)
	}
	srv.reject = false
	r.Ingest(Reading{Address: "sim/1/state", TS: t0, Text: "RUNNING"})
	if _, err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.Stats(); s.Rejected != 1 || s.BufferLen != 0 || len(srv.stored) != 1 {
		t.Fatalf("stats = %+v, stored %d", s, len(srv.stored))
	}
}

// Readings that cannot be events are counted, never sent; an unmapped address is invalid.
func TestIngest_InvalidReadingsAreCounted(t *testing.T) {
	srv := newServer()
	r, _ := simRuntime(t, srv, 1, Options{})
	r.Ingest(Reading{Address: "nope", TS: t0, Number: 1})
	r.Ingest(Reading{Address: count(1), TS: t0, Number: -4})
	r.Ingest(Reading{Address: "sim/1/state", TS: t0, Text: "EXPLODING"})
	r.Ingest(Reading{Address: "sim/1/state", TS: t0.Add(-8 * 24 * time.Hour), Text: "RUNNING"})
	if s := r.Stats(); s.Invalid != 3 || s.DroppedStale != 1 || s.BufferLen != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

type fakeControl struct {
	mu      sync.Mutex
	latest  int
	cfg     domain.EdgeConfig
	beats   []Heartbeat
	pulled  int
	failing bool
}

func (c *fakeControl) PullConfig(context.Context) (domain.EdgeConfig, error) {
	c.pulled++
	return c.cfg, nil
}

func (c *fakeControl) Heartbeat(_ context.Context, hb Heartbeat) (HeartbeatReply, error) {
	if c.failing {
		return HeartbeatReply{}, errors.New("no route to the server")
	}
	c.mu.Lock()
	c.beats = append(c.beats, hb)
	c.mu.Unlock()
	return HeartbeatReply{LatestConfigVersion: c.latest, IntervalSeconds: 30}, nil
}

// A heartbeat carries version, buffer fill and machine states; a newer configuration is pulled and
// applied, an older or invalid one is refused and the applied version stays.
func TestBeat_ReportsAndAppliesNewerConfig(t *testing.T) {
	srv := newServer()
	ctl := &fakeControl{}
	r, _ := simRuntime(t, srv, 1, Options{Control: ctl, ConnectorVersion: "0.2.0", BufferCapacity: 10})
	r.Ingest(Reading{Address: "sim/1/state", TS: t0, Text: "DOWN"})
	if err := r.Beat(context.Background()); err != nil {
		t.Fatal(err)
	}
	hb := ctl.beats[0]
	if hb.ConnectorVersion != "0.2.0" || hb.AppliedConfigVersion != 1 || hb.BufferFillPct != 10 || hb.Machines["SIM-1"] != "DOWN" {
		t.Fatalf("heartbeat = %+v", hb)
	}
	next, _ := SimConfig(2, 2, func(int) string { return resA })
	ctl.latest, ctl.cfg = 2, next
	if err := r.Beat(context.Background()); err != nil || r.Applied() != 2 {
		t.Fatalf("applied = %d, %v", r.Applied(), err)
	}
	old, _ := SimConfig(1, 1, func(int) string { return resA })
	ctl.latest, ctl.cfg = 3, old
	if err := r.Beat(context.Background()); err == nil || r.Applied() != 2 {
		t.Fatalf("stale config: applied = %d, err = %v", r.Applied(), err)
	}
	ctl.failing = true
	if err := r.Beat(context.Background()); err == nil || r.Stats().HeartbeatFailures != 1 {
		t.Fatalf("failing heartbeat: %v %+v", err, r.Stats())
	}
}

// After a restart the connector resumes above every seq it issued, so the server does not drop its
// new events as duplicates; the cost is a gap of less than one reservation block.
func TestSeq_ResumesAboveIssuedAfterRestart(t *testing.T) {
	st := FileSeqState{Path: filepath.Join(t.TempDir(), "seq.json")}
	srv := newServer()
	r1, _ := simRuntime(t, srv, 1, Options{State: st})
	for i := 0; i < 5; i++ {
		r1.Ingest(Reading{Address: "sim/1/state", TS: t0.Add(time.Duration(i) * time.Second), Text: "RUNNING"})
	}
	if _, err := r1.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	r2, _ := simRuntime(t, srv, 1, Options{State: st})
	r2.Ingest(Reading{Address: "sim/1/state", TS: t0.Add(time.Minute), Text: "DOWN"})
	if _, err := r2.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(srv.stored) != 6 {
		t.Fatalf("stored %d events, want 6: the restart's event was dropped", len(srv.stored))
	}
	e, ok := srv.stored["SIM-1|1001"]
	if !ok || e.State != "DOWN" {
		t.Fatalf("restart event not at the next block: %v", srv.stored)
	}
}

// The simulator drives the whole runtime: Run pulls a configuration, ships events and beats.
func TestRun_SimulatorEndToEnd(t *testing.T) {
	srv := newServer()
	ctl := &fakeControl{}
	cfg, sims := SimConfig(1, 2, func(int) string { return resA })
	ctl.latest, ctl.cfg = 1, cfg
	sims[0].Start = 4294967290
	sims[0].PerTick = 3 // crosses 2^32 on the third tick
	sims[0].DownFrom, sims[0].DownTicks = 4, 2
	r, err := New(Options{Site: "demo-plant", EdgeID: "edge-1", Transport: srv, Control: ctl,
		Source: &Simulator{Machines: sims, Tick: time.Millisecond, Ticks: 8}, FlushEvery: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.stored)
		srv.mu.Unlock()
		// 2 machines x 8 ticks x 2 tags, less the 2 count baselines.
		if n == 30 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored %d events, want 30; stats %+v", n, r.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run = %v", err)
	}
	ctl.mu.Lock()
	beats := len(ctl.beats)
	ctl.mu.Unlock()
	if r.Applied() != 1 || beats == 0 {
		t.Fatalf("applied %d, beats %d", r.Applied(), beats)
	}
	sawDown := false
	for _, e := range srv.stored {
		if e.MachineID == "SIM-1" && e.State == "DOWN" {
			sawDown = true
		}
	}
	if !sawDown {
		t.Fatal("no DOWN state reached the server")
	}
}

func TestClient_SendPullAndHeartbeat(t *testing.T) {
	var gotAuth, gotBody string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/shopfloor/machine-events", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var in struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		b, _ := json.Marshal(in.Events[0])
		gotBody = string(b)
		if in.Events[0]["machineId"] == "BAD" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"validation_failed","detail":"seq must be at least 1"}`))
			return
		}
		if in.Events[0]["machineId"] == "BUSY" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"accepted":1,"dropped":0,"staleDropped":2,"gaps":[{"from":4,"to":6}]}`))
	})
	mux.HandleFunc("GET /v1/shopfloor/edge-nodes/{id}/config", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":3,"document":{"version":3,"machines":[{"machineId":"M1","resourceId":"` + resA + `"}],"tags":[]}}`))
	})
	mux.HandleFunc("POST /v1/shopfloor/edge-nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["bufferFillPct"] != "12.50" || in["appliedConfigVersion"] != float64(3) {
			t.Errorf("heartbeat body = %v", in)
		}
		_, _ = w.Write([]byte(`{"latestConfigVersion":4,"heartbeatIntervalSeconds":45}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/", EdgeID: "edge-7:x", Token: "tok"}
	ctx := context.Background()
	good := int64(5)
	res, err := c.Send(ctx, []domain.MachineEvent{{MachineID: "M1", Seq: 3, Kind: domain.KindCount, TS: t0, GoodCount: &good}})
	if err != nil || res.Accepted != 1 || res.Dropped != 2 || len(res.Gaps) != 1 || res.Gaps[0] != (domain.Gap{From: 4, To: 6}) {
		t.Fatalf("send = %+v, %v", res, err)
	}
	if gotAuth != "Bearer tok" || gotBody == "" {
		t.Fatalf("auth %q body %s", gotAuth, gotBody)
	}
	if _, err := c.Send(ctx, []domain.MachineEvent{{MachineID: "BAD", Seq: 1}}); !errors.Is(err, ErrRejected) {
		t.Fatalf("400 = %v, want ErrRejected", err)
	}
	if _, err := c.Send(ctx, []domain.MachineEvent{{MachineID: "BUSY", Seq: 1}}); err == nil || errors.Is(err, ErrRejected) {
		t.Fatalf("503 = %v, want a retryable error", err)
	}
	cfg, err := c.PullConfig(ctx)
	if err != nil || cfg.Version != 3 || len(cfg.Machines) != 1 {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	c.EdgeID = "e1"
	hb, err := c.Heartbeat(ctx, Heartbeat{ConnectorVersion: "0.2.0", BufferFillPct: 12.5, AppliedConfigVersion: 3, Machines: map[string]string{"M1": "RUNNING"}})
	if err != nil || hb.LatestConfigVersion != 4 || hb.IntervalSeconds != 45 {
		t.Fatalf("heartbeat = %+v, %v", hb, err)
	}
}
