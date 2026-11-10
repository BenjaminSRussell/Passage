// Package runtime is the iot-edge connector's running part (PSG-25 D9, FR-25.9): it takes raw
// readings from a Source, turns them into BR-25.4 machine events with the domain rules, keeps them in
// the store-and-forward buffer and ships them to the shopfloor ingest endpoint with Peek and Ack, so
// a broker or network outage costs duplicates (dropped downstream by seq) and never events. It also
// reports heartbeats and applies newer configuration versions. Protocol adapters (OPC-UA, MQTT) are
// Sources; this slice ships a simulator only.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// Reading is one raw value from a tag. For a count tag Number is the absolute 32-bit counter and
// the runtime computes the delta; for a measurement it is the value; for state, downtime and
// alarm tags Text is the state or reason code.
type Reading struct {
	Address string
	TS      time.Time
	Number  float64
	Text    string
}

// Source produces readings until ctx ends. A protocol adapter implements it.
type Source interface {
	Run(ctx context.Context, emit func(Reading)) error
}

// SendResult is what the ingest endpoint reported for a batch.
type SendResult struct {
	Accepted int
	Dropped  int
	Gaps     []domain.Gap
}

// ErrRejected marks a batch the server refused as invalid (4xx other than auth and rate limits).
// Retrying cannot help, so the runtime drops it and counts it.
var ErrRejected = errors.New("batch rejected by the server")

// Transport sends machine events to the shopfloor ingest endpoint.
type Transport interface {
	Send(ctx context.Context, events []domain.MachineEvent) (SendResult, error)
}

// Heartbeat is what the connector reports every interval (FR-25.19).
type Heartbeat struct {
	ConnectorVersion     string
	BufferFillPct        float64
	Machines             map[string]string // machine code to last reported state
	AppliedConfigVersion int
	CertSerial           string
	CertExpiresAt        time.Time
}

// HeartbeatReply tells the connector what the server has.
type HeartbeatReply struct {
	LatestConfigVersion int
	IntervalSeconds     int
}

// ControlPlane is the connector's side of the shopfloor edge fleet API.
type ControlPlane interface {
	PullConfig(ctx context.Context) (domain.EdgeConfig, error)
	Heartbeat(ctx context.Context, hb Heartbeat) (HeartbeatReply, error)
}

// Options configures a Runtime.
type Options struct {
	Site, EdgeID     string
	ConnectorVersion string
	Source           Source
	Transport        Transport
	Control          ControlPlane
	// Certs and Renew run the mTLS certificate life (BR-25.14): Beat starts a renewal when fewer than
	// 30 days remain, reports the serial and expiry, and publishing stops when the certificate expired.
	Certs           *CertStore
	Renew           func(ctx context.Context) error
	State           SeqState
	BufferAge       time.Duration
	BufferCapacity  int
	BatchSize       int           // events per send, at most 500 (the ingest limit)
	FlushEvery      time.Duration // how often the publisher looks at the buffer
	HeartbeatEvery  time.Duration // default 30 s, replaced by the server's answer
	RetryBackoff    time.Duration // first wait after a failed send, doubling to RetryBackoffMax
	RetryBackoffMax time.Duration
	Now             func() time.Time
	Log             *slog.Logger
}

// Stats are the counters the admin /metrics endpoint serves.
type Stats struct {
	Accepted, DroppedDuplicate, DroppedStale, Invalid, Evicted int64
	Sent, ServerAccepted, ServerDropped, Rejected              int64
	SendFailures, SeqGaps, HeartbeatFailures                   int64
	CertRenewals, CertRenewFailures                            int64
	BufferLen                                                  int
	BufferFillPct                                              float64
	AppliedConfigVersion                                       int
}

// Runtime wires a Source to the buffer and the server.
type Runtime struct {
	o   Options
	buf *domain.Buffer
	log *slog.Logger

	mu      sync.Mutex
	cfg     domain.EdgeConfig
	applied int
	seqr    *domain.Sequencer
	prev    map[string]uint32 // last absolute counter per count address
	hasPrev map[string]bool
	states  map[string]string
	seqs    *seqAllocator

	accepted, dupDropped, staleDropped, invalid atomic.Int64
	sent, srvAccepted, srvDropped, rejected     atomic.Int64
	sendFailures, seqGaps, heartbeatFailures    atomic.Int64
	certRenewals, certRenewFailures             atomic.Int64
	intervalNanos                               atomic.Int64
}

// New returns a runtime. Zero options take their defaults.
func New(o Options) (*Runtime, error) {
	if o.Site == "" || o.EdgeID == "" {
		return nil, errors.New("site and edge id are required")
	}
	if o.Transport == nil {
		return nil, errors.New("a transport is required")
	}
	if o.State == nil {
		o.State = &MemorySeqState{}
	}
	if o.BufferAge <= 0 {
		o.BufferAge = 24 * time.Hour
	}
	if o.BufferCapacity <= 0 {
		o.BufferCapacity = 200_000
	}
	if o.BatchSize <= 0 || o.BatchSize > 500 {
		o.BatchSize = 500
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = 200 * time.Millisecond
	}
	if o.HeartbeatEvery <= 0 {
		o.HeartbeatEvery = 30 * time.Second
	}
	if o.RetryBackoff <= 0 {
		o.RetryBackoff = time.Second
	}
	if o.RetryBackoffMax <= 0 {
		o.RetryBackoffMax = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	seqs, err := newSeqAllocator(o.State, 1000)
	if err != nil {
		return nil, err
	}
	r := &Runtime{o: o, buf: domain.NewBuffer(o.BufferAge, o.BufferCapacity), log: o.Log, seqr: domain.NewSequencer(),
		prev: map[string]uint32{}, hasPrev: map[string]bool{}, states: map[string]string{}, seqs: seqs}
	r.intervalNanos.Store(int64(o.HeartbeatEvery))
	return r, nil
}

// Apply installs a configuration if it is valid and newer than the applied one (FR-25.19: one
// applied version at a time). It reports the version now applied.
func (r *Runtime) Apply(c domain.EdgeConfig) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, err := domain.NextApplied(r.applied, c)
	if err != nil {
		return r.applied, err
	}
	r.cfg, r.applied = c, v
	// A new mapping starts its counters from the next reading, never from a stale baseline.
	r.prev, r.hasPrev = map[string]uint32{}, map[string]bool{}
	return v, nil
}

// Applied returns the applied configuration version (0 before the first).
func (r *Runtime) Applied() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applied
}

// Ingest handles one reading: a count reading becomes the delta since the previous one (the first
// reading of a tag only sets the baseline), every event gets the machine's next seq, passes the
// stale check, and is wrapped in its CloudEvent and pushed to the buffer.
func (r *Runtime) Ingest(rd Reading) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.o.Now()
	var tag *domain.TagMapping
	for i := range r.cfg.Tags {
		if r.cfg.Tags[i].Address == rd.Address {
			tag = &r.cfg.Tags[i]
			break
		}
	}
	if tag == nil {
		r.invalid.Add(1)
		return
	}
	s := domain.Sample{Address: rd.Address, TS: rd.TS, Number: rd.Number, Text: rd.Text}
	if tag.Kind == domain.KindCount {
		cur := uint32(rd.Number)
		if rd.Number < 0 || rd.Number != float64(cur) {
			r.invalid.Add(1)
			return
		}
		had, prev := r.hasPrev[rd.Address], r.prev[rd.Address]
		r.prev[rd.Address], r.hasPrev[rd.Address] = cur, true
		if !had {
			return
		}
		s.Number = float64(domain.CounterDelta(prev, cur))
	}
	seq, err := r.seqs.next(tag.MachineID)
	if err != nil {
		r.log.Error("sequence state not saved; event dropped", "machine", tag.MachineID, "err", err)
		r.invalid.Add(1)
		return
	}
	s.Seq = seq
	ev, err := r.cfg.Normalize(s)
	if err != nil {
		r.invalid.Add(1)
		r.log.Warn("reading dropped", "address", rd.Address, "err", err)
		return
	}
	switch dec, _ := r.seqr.Accept(ev.MachineID, ev.Seq, ev.TS, now); dec {
	case domain.DuplicateDropped:
		r.dupDropped.Add(1)
		return
	case domain.StaleDropped:
		r.staleDropped.Add(1)
		return
	}
	ce, err := domain.Envelope(r.o.Site, r.o.EdgeID, ev)
	if err != nil {
		r.invalid.Add(1)
		return
	}
	payload, err := json.Marshal(ce)
	if err != nil {
		r.invalid.Add(1)
		return
	}
	if ev.Kind == domain.KindState || ev.Kind == domain.KindDowntime {
		r.states[ev.MachineID] = ev.State
	}
	r.buf.Push(now, payload)
	r.accepted.Add(1)
}

// Flush sends buffered events until the buffer is empty or a send fails. It returns the number of
// events the server acknowledged (or rejected as invalid) and the first send error. Events leave
// the buffer only after the acknowledgement.
func (r *Runtime) Flush(ctx context.Context) (int, error) {
	total := 0
	if r.o.Certs != nil && !domain.PublishAllowed(r.o.Certs.Action(r.o.Now())) {
		r.sendFailures.Add(1)
		return 0, errors.New("client certificate expired; events stay buffered until the connector is enrolled again")
	}
	for ctx.Err() == nil {
		payloads, through := r.buf.Peek(r.o.Now(), r.o.BatchSize)
		if len(payloads) == 0 {
			return total, nil
		}
		events := make([]domain.MachineEvent, 0, len(payloads))
		for _, p := range payloads {
			var ce domain.CloudEvent
			if err := json.Unmarshal(p, &ce); err != nil {
				continue // cannot happen for what Ingest wrote; never block the queue on it
			}
			events = append(events, ce.Data)
		}
		res, err := r.o.Transport.Send(ctx, events)
		switch {
		case errors.Is(err, ErrRejected):
			r.rejected.Add(int64(len(events)))
			r.log.Error("batch rejected by the server and dropped", "events", len(events), "err", err)
		case err != nil:
			r.sendFailures.Add(1)
			return total, err
		default:
			r.srvAccepted.Add(int64(res.Accepted))
			r.srvDropped.Add(int64(res.Dropped))
			if len(res.Gaps) > 0 {
				r.seqGaps.Add(int64(len(res.Gaps)))
				r.log.Warn("server reported sequence gaps (SF-0008)", "gaps", len(res.Gaps))
			}
		}
		r.buf.Ack(through)
		r.sent.Add(int64(len(events)))
		total += len(events)
	}
	return total, ctx.Err()
}

// Beat sends one heartbeat and, when the server holds a newer configuration, pulls and applies it.
func (r *Runtime) Beat(ctx context.Context) error {
	if r.o.Control == nil {
		return nil
	}
	r.mu.Lock()
	machines := make(map[string]string, len(r.states))
	for k, v := range r.states {
		machines[k] = v
	}
	for _, m := range r.cfg.Machines {
		if _, ok := machines[m.MachineID]; !ok {
			machines[m.MachineID] = "UNKNOWN"
		}
	}
	applied := r.applied
	r.mu.Unlock()
	hb := Heartbeat{ConnectorVersion: r.o.ConnectorVersion, BufferFillPct: r.buf.FillPct(r.o.Now()), Machines: machines, AppliedConfigVersion: applied}
	if c := r.o.Certs; c != nil {
		r.renewIfDue(ctx, c)
		hb.CertSerial, hb.CertExpiresAt = c.Serial(), c.NotAfter()
	}
	reply, err := r.o.Control.Heartbeat(ctx, hb)
	if err != nil {
		r.heartbeatFailures.Add(1)
		return err
	}
	if reply.IntervalSeconds > 0 {
		r.intervalNanos.Store(int64(time.Duration(reply.IntervalSeconds) * time.Second))
	}
	if reply.LatestConfigVersion > applied {
		return r.pullConfig(ctx)
	}
	return nil
}

// renewIfDue starts the renewal BR-25.14 asks for. A failure is remembered, so a renewal failing for
// seven days is reported as an alert (SF-0018) while the old certificate still works.
func (r *Runtime) renewIfDue(ctx context.Context, c *CertStore) {
	now := r.o.Now()
	action := c.Action(now)
	if action == domain.CertAlert {
		r.log.Error("client certificate renewal has failed for 7 days (SF-0018)", "expiresAt", c.NotAfter())
	}
	if (action != domain.CertRenew && action != domain.CertAlert) || r.o.Renew == nil {
		return
	}
	if err := r.o.Renew(ctx); err != nil {
		c.RenewFailed(now)
		r.certRenewFailures.Add(1)
		r.log.Warn("client certificate renewal failed", "expiresAt", c.NotAfter(), "err", err)
		return
	}
	r.certRenewals.Add(1)
	r.log.Info("client certificate renewed", "serial", c.Serial(), "expiresAt", c.NotAfter())
}

func (r *Runtime) pullConfig(ctx context.Context) error {
	c, err := r.o.Control.PullConfig(ctx)
	if err != nil {
		return fmt.Errorf("pull configuration: %w", err)
	}
	v, err := r.Apply(c)
	if err != nil {
		return fmt.Errorf("apply configuration: %w", err)
	}
	r.log.Info("configuration applied", "version", v)
	return nil
}

// Run pulls the first configuration, then runs the source, the publisher and the heartbeat until ctx
// ends. A failing source stops the runtime with its error.
func (r *Runtime) Run(ctx context.Context) error {
	if r.o.Control != nil {
		for r.Applied() == 0 {
			if err := r.pullConfig(ctx); err == nil {
				break
			} else {
				r.log.Warn("waiting for a configuration", "err", err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(r.o.RetryBackoff):
			}
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- r.publishLoop(ctx) }()
	go func() { errs <- r.heartbeatLoop(ctx) }()
	if r.o.Source == nil {
		<-ctx.Done()
		return nil
	}
	// A source that ends (the simulator with a tick limit) leaves its events to the publisher:
	// the runtime keeps running until the buffer is empty, then stops.
	srcDone := make(chan error, 1)
	go func() { srcDone <- r.o.Source.Run(ctx, r.Ingest) }()
	select {
	case err := <-srcDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		for r.buf.Len() > 0 && ctx.Err() == nil {
			time.Sleep(r.o.FlushEvery)
		}
		return nil
	case err := <-errs:
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}

func (r *Runtime) publishLoop(ctx context.Context) error {
	wait := r.o.RetryBackoff
	t := time.NewTicker(r.o.FlushEvery)
	defer t.Stop()
	var backoffUntil time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		if r.o.Now().Before(backoffUntil) {
			continue
		}
		if _, err := r.Flush(ctx); err != nil && ctx.Err() == nil {
			r.log.Warn("send failed; events stay buffered", "buffered", r.buf.Len(), "retryIn", wait, "err", err)
			backoffUntil = r.o.Now().Add(wait)
			wait = min(wait*2, r.o.RetryBackoffMax)
		} else {
			wait = r.o.RetryBackoff
		}
	}
}

func (r *Runtime) heartbeatLoop(ctx context.Context) error {
	for {
		if err := r.Beat(ctx); err != nil && ctx.Err() == nil {
			r.log.Warn("heartbeat failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(r.intervalNanos.Load())):
		}
	}
}

// Stats returns the counters.
func (r *Runtime) Stats() Stats {
	now := r.o.Now()
	return Stats{Accepted: r.accepted.Load(), DroppedDuplicate: r.dupDropped.Load(), DroppedStale: r.staleDropped.Load(),
		Invalid: r.invalid.Load(), Evicted: int64(r.buf.Evicted()), Sent: r.sent.Load(), ServerAccepted: r.srvAccepted.Load(),
		ServerDropped: r.srvDropped.Load(), Rejected: r.rejected.Load(), SendFailures: r.sendFailures.Load(),
		SeqGaps: r.seqGaps.Load(), HeartbeatFailures: r.heartbeatFailures.Load(), CertRenewals: r.certRenewals.Load(), CertRenewFailures: r.certRenewFailures.Load(), BufferLen: r.buf.Len(),
		BufferFillPct: r.buf.FillPct(now), AppliedConfigVersion: r.Applied()}
}
