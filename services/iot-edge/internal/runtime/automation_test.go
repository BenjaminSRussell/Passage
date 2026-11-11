package runtime

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/mfs"
)

const (
	sscc1 = "000000000000000011" // checked below
)

type fakeWarehouse struct {
	mu       sync.Mutex
	orders   []TransportOrder
	reports  map[string]string
	reasons  map[string]string
	events   []mfs.Event
	failPost bool
}

func (w *fakeWarehouse) PullOrders(_ context.Context, _ string) ([]TransportOrder, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []TransportOrder
	for _, o := range w.orders {
		if w.reports[o.ID] == "" {
			out = append(out, o)
		}
	}
	return out, nil
}

func (w *fakeWarehouse) ReportOrder(_ context.Context, id, status, reason string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reports == nil {
		w.reports, w.reasons = map[string]string{}, map[string]string{}
	}
	w.reports[id], w.reasons[id] = status, reason
	return nil
}

func (w *fakeWarehouse) PostEvents(_ context.Context, _ string, ev []mfs.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failPost {
		return errors.New("warehouse unavailable")
	}
	w.events = append(w.events, ev...)
	return nil
}

func (w *fakeWarehouse) snapshot() (map[string]string, []mfs.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := map[string]string{}
	for k, v := range w.reports {
		r[k] = v
	}
	return r, append([]mfs.Event(nil), w.events...)
}

func goodSSCC(t *testing.T, body string) string {
	t.Helper()
	for d := 0; d < 10; d++ {
		s := body + string(rune('0'+d))
		if mfs.ValidSSCC(s) {
			return s
		}
	}
	t.Fatal("no check digit")
	return ""
}

func startAutomation(t *testing.T, w *fakeWarehouse, plc *mfs.SimPLC) (*Automation, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go plc.Serve(ctx, ln)
	a := &Automation{Plane: w, Station: "PLC", PollEvery: 20 * time.Millisecond, BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
		Dial: func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		},
		Link: mfs.Config{AckWait: 300 * time.Millisecond, KeepAlive: time.Second}}
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	return a, func() { cancel(); <-done }
}

// A transport order becomes a MOVE telegram, the PLC's arrival goes back to the warehouse as an event, and
// an order that cannot be a telegram is refused with the reason.
func TestAutomationSendsMovesAndForwardsEvents(t *testing.T) {
	lu := goodSSCC(t, "00012345670000001")
	w := &fakeWarehouse{orders: []TransportOrder{
		{ID: "o1", TransportOrder: mfs.TransportOrder{LoadUnit: lu, From: "ASRS-01", To: "DOCK-03", Priority: 2}},
		{ID: "o2", TransportOrder: mfs.TransportOrder{LoadUnit: "123", From: "A", To: "B"}}, // not an SSCC
	}}
	plc := &mfs.SimPLC{ArriveAfter: 10 * time.Millisecond}
	a, stop := startAutomation(t, w, plc)
	defer stop()
	eventually(t, "the arrival event", func() bool { _, ev := w.snapshot(); return len(ev) == 1 })
	rep, ev := w.snapshot()
	if rep["o1"] != OrderSent || rep["o2"] != OrderRefused {
		t.Fatalf("reports %v", rep)
	}
	if ev[0].Kind != mfs.KindArrived || ev[0].LoadUnit != lu || ev[0].Location != "DOCK-03" || ev[0].Station != "PLC" {
		t.Fatalf("event %+v", ev[0])
	}
	if mv := plc.Moves(); len(mv) != 1 || mv[0].From != "ASRS-01" || mv[0].To != "DOCK-03" || mv[0].Priority != 2 {
		t.Fatalf("PLC moves %+v", mv)
	}
	if s := a.Stats(); s.OrdersSent != 1 || s.OrdersRefused != 1 || s.EventsForwarded != 1 {
		t.Fatalf("stats %+v", s)
	}
}

// While the warehouse cannot take events the connector NACKs busy, the PLC keeps the telegram and sends
// it again, and the event arrives exactly once when the warehouse is back.
func TestAutomationLosesNoEventWhileTheWarehouseIsAway(t *testing.T) {
	lu := goodSSCC(t, "00012345670000002")
	w := &fakeWarehouse{failPost: true, orders: []TransportOrder{{ID: "o1", TransportOrder: mfs.TransportOrder{LoadUnit: lu, From: "A", To: "B"}}}}
	plc := &mfs.SimPLC{ArriveAfter: 10 * time.Millisecond}
	_, stop := startAutomation(t, w, plc)
	defer stop()
	eventually(t, "the move", func() bool { return len(plc.Moves()) == 1 })
	time.Sleep(200 * time.Millisecond)
	if _, ev := w.snapshot(); len(ev) != 0 {
		t.Fatalf("event arrived while the warehouse was away: %+v", ev)
	}
	w.mu.Lock()
	w.failPost = false
	w.mu.Unlock()
	eventually(t, "the event after the warehouse is back", func() bool { _, ev := w.snapshot(); return len(ev) >= 1 })
	time.Sleep(100 * time.Millisecond)
	if _, ev := w.snapshot(); len(ev) != 1 {
		t.Fatalf("event delivered %d times, want once", len(ev))
	}
}

func TestAutomationReportsAFaultAndSurvivesAMissingPLC(t *testing.T) {
	lu := goodSSCC(t, "00012345670000003")
	w := &fakeWarehouse{orders: []TransportOrder{{ID: "o1", TransportOrder: mfs.TransportOrder{LoadUnit: lu, From: "A", To: "B"}}}}
	plc := &mfs.SimPLC{ArriveAfter: 10 * time.Millisecond, Faults: map[string]string{lu: "0042"}}
	_, stop := startAutomation(t, w, plc)
	defer stop()
	eventually(t, "the fault", func() bool { _, ev := w.snapshot(); return len(ev) == 1 })
	if _, ev := w.snapshot(); ev[0].Kind != mfs.KindFault || ev[0].Code != "0042" || ev[0].LoadUnit != lu {
		t.Fatalf("event %+v", ev[0])
	}

	// A PLC nobody listens on: Run keeps trying and ends cleanly with the context.
	ctx, cancel := context.WithCancel(context.Background())
	down := &Automation{Plane: &fakeWarehouse{}, Station: "PLC", BackoffMin: 5 * time.Millisecond, BackoffMax: 10 * time.Millisecond,
		Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("connection refused") }}
	done := make(chan error, 1)
	go func() { done <- down.Run(ctx) }()
	eventually(t, "redials", func() bool { return down.Stats().LinkDrops >= 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The link drops and the connector dials the PLC again: the PLC's sequence starts at 1 again, so the
// events of the new session carry another session name and the warehouse does not take the second
// arrival, seq 1 again, for a repeat of the first.
func TestAutomationNamesEachLinkSession(t *testing.T) {
	lu1, lu2 := goodSSCC(t, "00012345670000004"), goodSSCC(t, "00012345670000005")
	w := &fakeWarehouse{orders: []TransportOrder{{ID: "o1", TransportOrder: mfs.TransportOrder{LoadUnit: lu1, From: "A", To: "B"}}}}
	plc := &mfs.SimPLC{ArriveAfter: 10 * time.Millisecond}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go plc.Serve(ctx, ln)
	var mu sync.Mutex
	var conns []net.Conn
	a := &Automation{Plane: w, Station: "PLC", PollEvery: 20 * time.Millisecond, BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
		Dial: func(ctx context.Context) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
			if err == nil {
				mu.Lock()
				conns = append(conns, c)
				mu.Unlock()
			}
			return c, err
		},
		Link: mfs.Config{AckWait: 300 * time.Millisecond, KeepAlive: time.Second}}
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	eventually(t, "the first arrival", func() bool { _, ev := w.snapshot(); return len(ev) == 1 })
	mu.Lock()
	_ = conns[0].Close() // the link drops
	mu.Unlock()
	eventually(t, "the reconnect", func() bool { mu.Lock(); defer mu.Unlock(); return a.Stats().LinkDrops >= 1 && len(conns) >= 2 })
	w.mu.Lock()
	w.orders = append(w.orders, TransportOrder{ID: "o2", TransportOrder: mfs.TransportOrder{LoadUnit: lu2, From: "A", To: "B"}})
	w.mu.Unlock()
	eventually(t, "the second arrival", func() bool { _, ev := w.snapshot(); return len(ev) == 2 })

	_, ev := w.snapshot()
	if ev[0].LoadUnit != lu1 || ev[1].LoadUnit != lu2 {
		t.Fatalf("events %+v", ev)
	}
	if ev[0].Seq != 1 || ev[1].Seq != 1 {
		t.Fatalf("the PLC's sequence did not restart with the link: %d then %d", ev[0].Seq, ev[1].Seq)
	}
	if ev[0].Session == "" || ev[1].Session == "" || ev[0].Session == ev[1].Session {
		t.Fatalf("link sessions %q and %q, want two different names", ev[0].Session, ev[1].Session)
	}
}
