package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeHandle struct {
	done    chan error
	stopped chan struct{}
	once    sync.Once
}

func (h *fakeHandle) Wait() error { err := <-h.done; h.done <- err; return err }

func (h *fakeHandle) Stop(time.Duration) error {
	h.once.Do(func() { close(h.stopped); h.done <- nil })
	return nil
}

func (h *fakeHandle) exit(err error) { h.done <- err }

type fakeRunner struct {
	mu      sync.Mutex
	starts  map[string]int
	handles map[string][]*fakeHandle
	failing map[string]error
	order   []string
	stops   []string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{starts: map[string]int{}, handles: map[string][]*fakeHandle{}, failing: map[string]error{}}
}

func (r *fakeRunner) Start(_ context.Context, p Process) (Handle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts[p.Name]++
	r.order = append(r.order, p.Name)
	if err := r.failing[p.Name]; err != nil {
		return nil, err
	}
	h := &fakeHandle{done: make(chan error, 2), stopped: make(chan struct{})}
	r.handles[p.Name] = append(r.handles[p.Name], h)
	return &stopRecorder{fakeHandle: h, r: r, name: p.Name}, nil
}

type stopRecorder struct {
	*fakeHandle
	r    *fakeRunner
	name string
}

func (s *stopRecorder) Stop(g time.Duration) error {
	s.r.mu.Lock()
	s.r.stops = append(s.r.stops, s.name)
	s.r.mu.Unlock()
	return s.fakeHandle.Stop(g)
}

func (r *fakeRunner) startCount(n string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts[n]
}

func (r *fakeRunner) handle(n string, i int) *fakeHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < len(r.handles[n]) {
		return r.handles[n][i]
	}
	return nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func testSupervisor(r Runner, probe Prober) *Supervisor {
	s := NewSupervisor(r, probe, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.After = func(time.Duration) <-chan time.Time { c := make(chan time.Time, 1); c <- time.Now(); return c }
	s.ProbeInterval = time.Millisecond
	return s
}

func state(s *Supervisor, name string) State {
	for _, st := range s.Statuses() {
		if st.Name == name {
			return st.State
		}
	}
	return ""
}

func TestStartsAllAndStopsInReverseOrder(t *testing.T) {
	r := newFakeRunner()
	s := testSupervisor(r, func(context.Context, Process) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, []Process{{Name: "a"}, {Name: "b"}, {Name: "c"}}) }()
	eventually(t, "all healthy", func() bool { ok, _ := s.Health(); return ok })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(r.stops) != 3 {
		t.Fatalf("stops = %v", r.stops)
	}
	if s := state(s, "a"); s != StateStopped {
		t.Fatalf("a state = %s", s)
	}
}

func TestRestartsAnExitedService(t *testing.T) {
	r := newFakeRunner()
	s := testSupervisor(r, func(context.Context, Process) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx, []Process{{Name: "a"}}) }()
	eventually(t, "first start", func() bool { return r.handle("a", 0) != nil })
	r.handle("a", 0).exit(errors.New("crash"))
	eventually(t, "second start", func() bool { return r.startCount("a") == 2 })
	eventually(t, "restart counted", func() bool { return s.Statuses()[0].Restarts == 1 })
}

func TestGivesUpAfterMaxRestartsAndFailsRequiredService(t *testing.T) {
	r := newFakeRunner()
	r.failing["a"] = errors.New("cannot start")
	s := testSupervisor(r, nil)
	s.Policy = RestartPolicy{MaxRestarts: 2, Window: time.Hour, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	err := s.Run(context.Background(), []Process{{Name: "a"}})
	if err == nil {
		t.Fatal("expected a fatal error")
	}
	if got := r.startCount("a"); got != 3 {
		t.Fatalf("starts = %d, want 3 (initial + 2 restarts)", got)
	}
	if st := state(s, "a"); st != StateFailed {
		t.Fatalf("state = %s", st)
	}
}

func TestOptionalServiceFailureDoesNotStopTheServer(t *testing.T) {
	r := newFakeRunner()
	r.failing["opt"] = errors.New("no runtime")
	s := testSupervisor(r, func(context.Context, Process) error { return nil })
	s.Policy = RestartPolicy{MaxRestarts: 1, Window: time.Hour, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, []Process{{Name: "req"}, {Name: "opt", Optional: true}}) }()
	eventually(t, "optional failed", func() bool { return state(s, "opt") == StateFailed })
	eventually(t, "required healthy", func() bool { return state(s, "req") == StateHealthy })
	ready, sts := s.Health()
	if !ready {
		t.Fatalf("an optional failure must not make the server unready: %+v", sts)
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	default:
	}
	cancel()
	<-done
}

func TestHealthAggregation(t *testing.T) {
	r := newFakeRunner()
	var mu sync.Mutex
	bad := map[string]bool{}
	s := testSupervisor(r, func(_ context.Context, p Process) error {
		mu.Lock()
		defer mu.Unlock()
		if bad[p.Name] {
			return errors.New("not ready")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx, []Process{{Name: "a"}, {Name: "b"}}) }()
	eventually(t, "ready", func() bool { ok, _ := s.Health(); return ok })
	mu.Lock()
	bad["b"] = true
	mu.Unlock()
	eventually(t, "unready", func() bool { ok, _ := s.Health(); return !ok })
	if st := state(s, "b"); st != StateUnhealthy {
		t.Fatalf("b = %s", st)
	}
	if st := state(s, "a"); st != StateHealthy {
		t.Fatalf("a = %s", st)
	}
}

func TestMigrateSucceedsWhenEveryServiceIsReady(t *testing.T) {
	r := newFakeRunner()
	s := testSupervisor(r, func(context.Context, Process) error { return nil })
	if err := migrateWith(context.Background(), s, []Process{{Name: "a"}, {Name: "b"}}, 5*time.Second); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(r.stops) != 2 {
		t.Fatalf("services must be stopped after migrating, stops = %v", r.stops)
	}
}

func TestMigrateFailsWhenAServiceCrashes(t *testing.T) {
	r := newFakeRunner()
	r.failing["b"] = errors.New("migration 0007 failed")
	s := testSupervisor(r, func(context.Context, Process) error { return nil })
	err := migrateWith(context.Background(), s, []Process{{Name: "a"}, {Name: "b"}}, 5*time.Second)
	if err == nil {
		t.Fatal("expected failure")
	}
}
