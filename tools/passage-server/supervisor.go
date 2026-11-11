package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Process is one supervised child: a service executable on loopback ports.
type Process struct {
	Name      string
	Route     string
	Exe       string
	Args      []string
	Env       map[string]string
	Optional  bool
	Port      int
	AdminPort int
}

// Handle is a running child.
type Handle interface {
	// Wait blocks until the child exits and returns its exit error.
	Wait() error
	// Stop asks the child to stop and kills it after grace.
	Stop(grace time.Duration) error
}

// Runner starts children. The real one uses os/exec; tests use a fake.
type Runner interface {
	Start(ctx context.Context, p Process) (Handle, error)
}

// Prober checks one child's health (its admin /readyz in production).
type Prober func(ctx context.Context, p Process) error

// State is the supervisor's view of one child.
type State string

const (
	StateStarting  State = "starting"
	StateHealthy   State = "healthy"
	StateUnhealthy State = "unhealthy"
	StateBackoff   State = "backoff"
	StateFailed    State = "failed"
	StateStopped   State = "stopped"
)

// RestartPolicy restarts a child that exits, with exponential backoff, and gives up on it after MaxRestarts exits inside Window.
type RestartPolicy struct {
	MaxRestarts    int
	Window         time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// DefaultRestartPolicy tolerates a flapping service for a while, then reports failure so the Windows recovery actions take over.
func DefaultRestartPolicy() RestartPolicy {
	return RestartPolicy{MaxRestarts: 5, Window: 5 * time.Minute, InitialBackoff: time.Second, MaxBackoff: 30 * time.Second}
}

// Status is one child's reported health.
type Status struct {
	Name     string `json:"name"`
	State    State  `json:"state"`
	Restarts int    `json:"restarts"`
	Optional bool   `json:"optional"`
	LastErr  string `json:"lastError,omitempty"`
}

// Supervisor runs children in order, restarts them on exit and aggregates their health.
type Supervisor struct {
	Runner        Runner
	Probe         Prober
	Policy        RestartPolicy
	ProbeInterval time.Duration
	StopGrace     time.Duration
	Log           *slog.Logger
	After         func(time.Duration) <-chan time.Time
	Now           func() time.Time
	mu            sync.Mutex
	status        map[string]*Status
	order         []string
	handles       map[string]Handle
	fatal         chan error
	fatalOnce     sync.Once
}

// NewSupervisor fills defaults.
func NewSupervisor(r Runner, probe Prober, log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{Runner: r, Probe: probe, Policy: DefaultRestartPolicy(), ProbeInterval: 2 * time.Second, StopGrace: 10 * time.Second, Log: log,
		After: time.After, Now: time.Now}
}

// Run starts every process and blocks until ctx is cancelled (returns nil after stopping them all in reverse order)
// or a required process has exhausted its restarts (returns that error after stopping the rest).
func (s *Supervisor) Run(ctx context.Context, procs []Process) error {
	s.mu.Lock()
	s.status = map[string]*Status{}
	s.handles = map[string]Handle{}
	s.fatal = make(chan error, 1)
	s.fatalOnce = sync.Once{}
	s.order = s.order[:0]
	for _, p := range procs {
		s.status[p.Name] = &Status{Name: p.Name, State: StateStarting, Optional: p.Optional}
		s.order = append(s.order, p.Name)
	}
	s.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.supervise(runCtx, p)
		}()
	}
	if s.Probe != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.probeLoop(runCtx, procs)
		}()
	}
	var result error
	select {
	case <-ctx.Done():
	case result = <-s.fatal:
	}
	cancel()
	s.stopAll()
	wg.Wait()
	return result
}

func (s *Supervisor) supervise(ctx context.Context, p Process) {
	var exits []time.Time
	backoff := s.Policy.InitialBackoff
	for {
		if ctx.Err() != nil {
			s.set(p.Name, StateStopped, "")
			return
		}
		h, err := s.Runner.Start(ctx, p)
		started := s.Now()
		if err != nil {
			s.Log.Error("start failed", "service", p.Name, "err", err)
			s.set(p.Name, StateBackoff, err.Error())
		} else {
			s.mu.Lock()
			s.handles[p.Name] = h
			s.mu.Unlock()
			if ctx.Err() != nil {
				// Shutdown began while Start was running, after stopAll may have looked at the handle table.
				_ = h.Stop(s.StopGrace)
			}
			s.set(p.Name, StateStarting, "")
			err = h.Wait()
			s.mu.Lock()
			delete(s.handles, p.Name)
			s.mu.Unlock()
			if ctx.Err() != nil {
				s.set(p.Name, StateStopped, "")
				return
			}
			msg := "exited"
			if err != nil {
				msg = err.Error()
			}
			s.Log.Warn("service exited", "service", p.Name, "err", msg)
			s.set(p.Name, StateBackoff, msg)
			if s.Now().Sub(started) > s.Policy.Window {
				backoff = s.Policy.InitialBackoff
			}
		}
		now := s.Now()
		kept := exits[:0]
		for _, t := range exits {
			if now.Sub(t) <= s.Policy.Window {
				kept = append(kept, t)
			}
		}
		exits = append(kept, now)
		if len(exits) > s.Policy.MaxRestarts {
			s.set(p.Name, StateFailed, fmt.Sprintf("gave up after %d exits in %s", len(exits), s.Policy.Window))
			if !p.Optional {
				s.fail(fmt.Errorf("required service %s keeps failing", p.Name))
			}
			return
		}
		s.bump(p.Name)
		select {
		case <-ctx.Done():
			s.set(p.Name, StateStopped, "")
			return
		case <-s.After(backoff):
		}
		backoff = min(backoff*2, s.Policy.MaxBackoff)
	}
}

func (s *Supervisor) probeLoop(ctx context.Context, procs []Process) {
	for {
		for _, p := range procs {
			if ctx.Err() != nil {
				return
			}
			if err := s.Probe(ctx, p); err != nil {
				s.setProbed(p.Name, StateUnhealthy, err.Error())
			} else {
				s.setProbed(p.Name, StateHealthy, "")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.After(s.ProbeInterval):
		}
	}
}

func (s *Supervisor) stopAll() {
	s.mu.Lock()
	var hs []Handle
	for i := len(s.order) - 1; i >= 0; i-- {
		if h := s.handles[s.order[i]]; h != nil {
			hs = append(hs, h)
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, h := range hs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Stop(s.StopGrace); err != nil {
				s.Log.Warn("stop failed", "err", err)
			}
		}()
	}
	wg.Wait()
}

func (s *Supervisor) set(name string, st State, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x := s.status[name]; x != nil {
		x.State = st
		if msg != "" || st == StateHealthy {
			x.LastErr = msg
		}
	}
}

// setProbed records a probe result unless the process is backing off, failed or stopped: those states belong to the restart loop, and a probe
// that raced with it must not overwrite them.
func (s *Supervisor) setProbed(name string, st State, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.status[name]
	if x == nil || x.State == StateBackoff || x.State == StateFailed || x.State == StateStopped {
		return
	}
	x.State = st
	if msg != "" || st == StateHealthy {
		x.LastErr = msg
	}
}

func (s *Supervisor) bump(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x := s.status[name]; x != nil {
		x.Restarts++
	}
}

func (s *Supervisor) fail(err error) {
	s.fatalOnce.Do(func() { s.fatal <- err })
}

// Statuses returns a snapshot sorted by name.
func (s *Supervisor) Statuses() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.status))
	for _, x := range s.status {
		out = append(out, *x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Health aggregates: ready when every required service is healthy. Optional services that are down are reported but do not fail it.
func (s *Supervisor) Health() (ready bool, statuses []Status) {
	statuses = s.Statuses()
	ready = len(statuses) > 0
	for _, st := range statuses {
		if !st.Optional && st.State != StateHealthy {
			ready = false
		}
	}
	return ready, statuses
}

// WaitReady blocks until Health reports ready, ctx ends, or the supervisor has none to wait for.
func (s *Supervisor) WaitReady(ctx context.Context, poll time.Duration) error {
	for {
		if ready, _ := s.Health(); ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(errors.New("services did not become ready"), ctx.Err())
		case <-s.After(poll):
		}
	}
}
