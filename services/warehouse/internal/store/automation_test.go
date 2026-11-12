package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

// ssccFor returns the SSCC with the 17 digits of body and its check digit.
func ssccFor(t *testing.T, body string) string {
	t.Helper()
	for d := 0; d < 10; d++ {
		if s := fmt.Sprintf("%s%d", body, d); domain.ValidSSCC(s) {
			return s
		}
	}
	t.Fatalf("no check digit for %s", body)
	return ""
}

// sentPutAway plans a put-away of qty item into the A zone and gives it to station PLC as a SENT order.
func sentPutAway(t *testing.T, st *Store, ctx context.Context, wh, item, qty, unit string) AutomationOrder {
	t.Helper()
	w := plan(t, st, ctx, wh, "PURCHASE_PUTAWAY", domain.NewLine{ItemID: item, Direction: domain.DirectionPut, Qty: dq(qty), Weight: dq(qty)})
	return sendOrder(t, st, ctx, OrderInput{WorkID: w.ID, LineNo: 1, Station: "PLC", LoadUnit: unit, FromLocation: "INFEED-1"})
}

func sendOrder(t *testing.T, st *Store, ctx context.Context, in OrderInput) AutomationOrder {
	t.Helper()
	in.User, in.Now = "super", time.Now()
	o, err := st.CreateAutomationOrder(ctx, in)
	if err != nil {
		t.Fatalf("CreateAutomationOrder: %v", err)
	}
	if o, err = st.ReportAutomationOrder(ctx, o.ID, OrderSent, "", time.Now()); err != nil {
		t.Fatalf("report sent: %v", err)
	}
	return o
}

func arrival(session string, seq int, unit string) []AutomationEvent {
	return []AutomationEvent{{Kind: "arrived", Session: session, Seq: seq, LoadUnit: unit, Location: "ASRS-01", At: time.Now().UTC()}}
}

func orderStatus(t *testing.T, st *Store, ctx context.Context, id string) AutomationOrder {
	t.Helper()
	all, err := st.ListAutomationOrders(ctx, OrderFilter{Station: "PLC", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range all {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("no order %s", id)
	return AutomationOrder{}
}

// The PLC numbers its telegrams per link session: after the connector dials it again its sequence starts at 1, and
// after 99999 it wraps. The same number in a new session, or in the same session long after, is a new arrival and
// books stock; only a repeat in the same session within the window is a duplicate.
func TestStore_AutomationArrivalAfterReconnectBooksStock(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("ASRS-01", "A", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A"))
	item := ids.New()
	now := time.Now()
	ingest := func(events []AutomationEvent, at time.Time) AutomationIngest {
		t.Helper()
		out, err := st.IngestAutomationEvents(ctx, "PLC", events, at)
		if err != nil {
			t.Fatalf("IngestAutomationEvents: %v", err)
		}
		return out
	}

	first := sentPutAway(t, st, ctx, wh, item, "10", ssccFor(t, "30614141000000001"))
	if got := ingest(arrival("link-1", 1, first.LoadUnit), now); got.Accepted != 1 || got.Confirmed != 1 {
		t.Fatalf("first arrival: %+v", got)
	}
	if got := ingest(arrival("link-1", 1, first.LoadUnit), now.Add(time.Second)); got.Duplicates != 1 || got.Accepted != 0 || got.Confirmed != 0 {
		t.Fatalf("the PLC repeating the telegram: %+v", got)
	}
	if q := stockAt(t, st, ctx, wh, "ASRS-01", item); !q.Equal(dq("10")) {
		t.Fatalf("stock after the first arrival %s, want 10", q)
	}

	// The link drops and the connector dials again: the PLC's next arrival is seq 1 again, in a new session.
	second := sentPutAway(t, st, ctx, wh, item, "5", ssccFor(t, "30614141000000002"))
	if got := ingest(arrival("link-2", 1, second.LoadUnit), now.Add(2*time.Second)); got.Accepted != 1 || got.Duplicates != 0 || got.Confirmed != 1 {
		t.Fatalf("the arrival after the reconnect: %+v", got)
	}
	if q := stockAt(t, st, ctx, wh, "ASRS-01", item); !q.Equal(dq("15")) {
		t.Fatalf("stock after the reconnect %s, want 15", q)
	}

	// The sequence wraps within one long session: the same number long after is a new event.
	third := sentPutAway(t, st, ctx, wh, item, "1", ssccFor(t, "30614141000000003"))
	if got := ingest(arrival("link-2", 1, third.LoadUnit), now.Add(EventDedupeWindow+time.Minute)); got.Accepted != 1 || got.Confirmed != 1 {
		t.Fatalf("the arrival after the sequence wrapped: %+v", got)
	}
	if q := stockAt(t, st, ctx, wh, "ASRS-01", item); !q.Equal(dq("16")) {
		t.Fatalf("stock after the wrap %s, want 16", q)
	}
	for _, o := range []AutomationOrder{first, second, third} {
		if got := orderStatus(t, st, ctx, o.ID); got.Status != OrderArrived || got.ConfirmError != "" {
			t.Fatalf("order %s: %s %q", o.ID, got.Status, got.ConfirmError)
		}
	}
}

// flakyHolds is an inventory claim service that can be away.
type flakyHolds struct {
	issueclaim.Fake
	mu   sync.Mutex
	down bool
}

func (f *flakyHolds) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *flakyHolds) Claim(ctx context.Context, c issueclaim.Claim) error {
	f.mu.Lock()
	down := f.down
	f.mu.Unlock()
	if down {
		return errors.New("inventory unavailable")
	}
	return f.Fake.Claim(ctx, c)
}

// The arrival, the ARRIVED status and the stock move commit together. A confirmation the warehouse refuses leaves
// the order ARRIVED with the reason; a failure that may pass (inventory away) stores nothing and leaves the order
// SENT, so the PLC's resend of the same telegram confirms it.
func TestStore_AutomationArrivalConfirmsInOneTransaction(t *testing.T) {
	st, ctx, wh := setup(t)
	holds := &flakyHolds{}
	st.WithHolds(holds)
	item := ids.New()
	pickSetup(t, st, ctx, wh, item, "10")
	holds.Set(item, dq("10"), decimal.Zero)
	pickOrder := func(qty, unit string) AutomationOrder {
		w := plan(t, st, ctx, wh, "SALES_PICK", domain.NewLine{ItemID: item, Direction: domain.DirectionPick, Qty: dq(qty), Weight: dq(qty)})
		return sendOrder(t, st, ctx, OrderInput{WorkID: w.ID, LineNo: 1, Station: "PLC", LoadUnit: unit, ToLocation: "OUTFEED-1"})
	}

	// Inventory is away: the batch fails, nothing is stored, the order stays SENT and no stock moves.
	o := pickOrder("4", ssccFor(t, "30614141000000011"))
	holds.setDown(true)
	if _, err := st.IngestAutomationEvents(ctx, "PLC", arrival("link-1", 1, o.LoadUnit), time.Now()); err == nil {
		t.Fatal("an arrival whose confirmation could not reach inventory was accepted")
	}
	if got := orderStatus(t, st, ctx, o.ID); got.Status != OrderSent {
		t.Fatalf("order after the failed batch: %s", got.Status)
	}
	if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq("10")) {
		t.Fatalf("stock after the failed batch %s, want 10", q)
	}
	// The PLC sends the same telegram again once inventory is back: it is not a duplicate and stock moves once.
	holds.setDown(false)
	got, err := st.IngestAutomationEvents(ctx, "PLC", arrival("link-1", 1, o.LoadUnit), time.Now())
	if err != nil || got.Accepted != 1 || got.Duplicates != 0 || got.Confirmed != 1 {
		t.Fatalf("the resend: %+v %v", got, err)
	}
	if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq("6")) {
		t.Fatalf("stock after the resend %s, want 6", q)
	}
	if c := holds.Claimed(item, issueclaim.Pick); !c.Equal(dq("4")) {
		t.Fatalf("claimed %s, want 4", c)
	}

	// A refusal (the stock is held) leaves the order ARRIVED with the reason, the event stored and no stock moved.
	holds.Set(item, dq("6"), dq("5"))
	refused := pickOrder("3", ssccFor(t, "30614141000000012"))
	got, err = st.IngestAutomationEvents(ctx, "PLC", arrival("link-1", 2, refused.LoadUnit), time.Now())
	if err != nil || got.Accepted != 1 || got.Confirmed != 0 {
		t.Fatalf("a refused confirmation: %+v %v", got, err)
	}
	if o := orderStatus(t, st, ctx, refused.ID); o.Status != OrderArrived || !strings.Contains(o.ConfirmError, domain.CodeHoldBlocked) {
		t.Fatalf("refused order: %s %q", o.Status, o.ConfirmError)
	}
	if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq("6")) {
		t.Fatalf("stock after the refusal %s, want 6", q)
	}
	if c := holds.Claimed(item, issueclaim.Pick); !c.Equal(dq("4")) {
		t.Fatalf("a refused confirmation left a claim: %s claimed, want 4", c)
	}
	if got, err := st.IngestAutomationEvents(ctx, "PLC", arrival("link-1", 2, refused.LoadUnit), time.Now()); err != nil || got.Duplicates != 1 {
		t.Fatalf("the repeat of the refused arrival: %+v %v", got, err)
	}
}

// Two batches from one reader posted at the same time (a portal resending after a timeout) store a tag read once.
func TestStore_RFIDConcurrentRepeatIsOneRead(t *testing.T) {
	st, ctx, _ := setup(t)
	reader := "portal-" + ids.New()[:8]
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	results := make([]RFIDIngest, 8)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = st.IngestRFID(ctx, []RFIDRead{{ReaderID: reader, EPC: "3074257BF7194E4000001A85", ReadAt: at.Add(time.Duration(i) * time.Millisecond)}})
		}(i)
	}
	wg.Wait()
	accepted := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("batch %d: %v", i, errs[i])
		}
		accepted += r.Accepted
	}
	stored, err := st.ListRFIDReads(ctx, RFIDFilter{ReaderID: reader, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || len(stored) != 1 {
		t.Fatalf("accepted %d, stored %d; want one read", accepted, len(stored))
	}
}
