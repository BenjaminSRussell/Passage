package salesreport_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/salesreport"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
	"github.com/BenjaminSRussell/passage/services/fulfillment/migrations"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func idem(name string) store.Idem { return store.Idem{Key: name + "-" + ids.New(), Hash: "h-" + name} }

func today() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// setup migrates a throwaway schema and returns the store on the application
// role (row-level security applies) and the admin pool.
func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "user-test", Duties: testkit.ScopeDuties("")})
	return store.New(app, "0614141", 0), admin, ctx
}

type orderLine struct {
	qty     int64
	tracked bool
}

// shipment releases one order and confirms the pick of every line in full.
func shipment(t *testing.T, s *store.Postgres, ctx context.Context, lines ...orderLine) store.Shipment {
	t.Helper()
	order, customer, le, wh := ids.New(), ids.New(), ids.New(), ids.New()
	var rel []domain.ReleaseLine
	for _, l := range lines {
		rel = append(rel, domain.ReleaseLine{OrderID: order, OrderLineID: ids.New(), CustomerID: customer, LegalEntityID: le, WarehouseID: wh,
			ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(l.qty), ConfirmedShipDate: today(), ATPOK: true, TrackingRequired: l.tracked})
	}
	groups, _ := domain.Release(rel, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	res, err := s.RunRelease(ctx, wh, groups, idem("rel"))
	if err != nil || len(res.Shipments) != 1 {
		t.Fatalf("RunRelease: %+v, %v", res, err)
	}
	sh := res.Shipments[0]
	var picks []store.PickInput
	for _, l := range sh.Lines {
		picks = append(picks, store.PickInput{ShipmentLineID: l.ID, Picked: l.Planned})
	}
	picked, err := s.ConfirmPick(ctx, sh.ID, sh.Version, picks, idem("pick"))
	if err != nil {
		t.Fatalf("ConfirmPick: %v", err)
	}
	return picked
}

// packAndPost packs every line in one container and posts the slip; weights
// maps a line index to its catch weight.
func packAndPost(t *testing.T, s *store.Postgres, ctx context.Context, sh store.Shipment, weights map[int]string) store.PackingSlip {
	t.Helper()
	var packs []store.PackInput
	costs := map[string]store.Cost{}
	for i, l := range sh.Lines {
		packs = append(packs, store.PackInput{ShipmentLineID: l.ID, Qty: l.Planned})
		c := store.Cost{UnitCost: d("2.00"), CostSnapshotID: "snap-" + l.ID}
		if w, ok := weights[i]; ok {
			c.Weight = d(w)
		}
		costs[l.ID] = c
	}
	packed, err := s.PackContainer(ctx, sh.ID, sh.Version, "CARTON", packs, idem("pack"))
	if err != nil {
		t.Fatalf("PackContainer: %v", err)
	}
	slip, err := s.PostPackingSlip(ctx, sh.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs}, idem("post"))
	if err != nil {
		t.Fatalf("PostPackingSlip: %v", err)
	}
	return slip
}

// call is one request the fake sales server received.
type call struct {
	path, key string
	body      map[string]any
	claims    tenant.ServiceClaims
}

// fakeSales is sales' :record-progress: it verifies the service token as sales
// would and answers with the scripted statuses in turn (the last one repeats).
type fakeSales struct {
	t        *testing.T
	verifier *tenant.ServiceVerifier
	mu       sync.Mutex
	statuses []int
	calls    []call
}

func (f *fakeSales) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := f.verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		f.t.Errorf("body is not JSON: %s", raw)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{path: r.URL.Path, key: r.Header.Get("Idempotency-Key"), body: body, claims: claims})
	status := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	switch {
	case status >= 500:
		_, _ = w.Write([]byte(`{"code":"COM-0500","detail":"sales is down"}`))
	case status >= 400:
		_, _ = w.Write([]byte(`{"code":"SLS-0058","detail":"order is SHIPPED: nothing is left to deliver"}`))
	default:
		_, _ = w.Write([]byte(`{"id":"` + ids.New() + `"}`))
	}
}

func (f *fakeSales) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// newReporter starts a fake sales server trusting a fresh fulfillment key and
// returns a reporter that sends to it.
func newReporter(t *testing.T, s *store.Postgres, statuses ...int) (*salesreport.Reporter, *fakeSales) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("fulfillment", priv)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeSales{t: t, verifier: tenant.NewServiceVerifier("sales", map[string]ed25519.PublicKey{"fulfillment": pub}), statuses: statuses}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := salesreport.New(srv.URL, signer, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return &salesreport.Reporter{Store: s, Sales: client, MaxAttempts: 3}, fake
}

func runOnce(t *testing.T, r *salesreport.Reporter, ctx context.Context) int {
	t.Helper()
	n, err := r.RunOnce(context.WithoutCancel(ctx))
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return n
}

// makeDue moves every pending report's next attempt to now, as if the back-off had run out.
func makeDue(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), `update sales_delivery_report set next_attempt_at = now() where status = 'PENDING'`); err != nil {
		t.Fatal(err)
	}
}

func report(t *testing.T, s *store.Postgres, ctx context.Context, slipID string) store.SalesReport {
	t.Helper()
	items, err := s.ListSalesReports(ctx, store.SalesReportFilter{PackingSlipID: slipID})
	if err != nil || len(items) != 1 {
		t.Fatalf("reports of slip %s: %+v, %v", slipID, items, err)
	}
	return items[0]
}

// #496: posting a slip with batch and serial tracking queues one report; the
// reporter sends it to the order's :record-progress signed as fulfillment for
// service:fulfillment-sales-report with only sales.order.record-delivery, retries
// a 5xx with the same body and key, and stops once sales took it. The body names
// the slip as sourceRef and each order line by id with the slip's quantity, the
// catch weight, and the batch and serial entries (one batch picked from two
// locations is one entry; no location is sent).
func TestReporter_ReportsAPostedSlipWithTrackingOnce(t *testing.T) {
	s, admin, ctx := setup(t)
	sh := shipment(t, s, ctx, orderLine{3, true}, orderLine{10, true}, orderLine{5, true})
	capture := func(line int, entries ...domain.TrackingEntry) {
		t.Helper()
		next, err := s.CaptureTracking(ctx, sh.ID, sh.Lines[line].ID, sh.Version, entries, idem("cap"))
		if err != nil {
			t.Fatalf("capture line %d: %v", line, err)
		}
		sh = next
	}
	capture(0, domain.TrackingEntry{SerialNo: "SN-1", LocationCode: "A-01", Qty: d("1")}, domain.TrackingEntry{SerialNo: "SN-2", LocationCode: "A-01", Qty: d("1")},
		domain.TrackingEntry{BatchNo: "LOT-S", SerialNo: "SN-3", LocationCode: "A-02", Qty: d("1")})
	capture(1, domain.TrackingEntry{BatchNo: "LOT-1", LocationCode: "A-01", Qty: d("6")}, domain.TrackingEntry{BatchNo: "LOT-1", LocationCode: "A-02", Qty: d("2")},
		domain.TrackingEntry{BatchNo: "LOT-2", LocationCode: "B-01", Qty: d("2")})
	capture(2, domain.TrackingEntry{BatchNo: "LOT-CW", LocationCode: "C-01", Qty: d("5")})
	slip := packAndPost(t, s, ctx, sh, map[int]string{2: "51.5"})

	queued := report(t, s, ctx, slip.ID)
	if queued.Status != store.SalesReportPending || queued.SourceRef != slip.Number || queued.OrderID != sh.OrderID || queued.LegalEntityID != sh.LegalEntityID {
		t.Fatalf("queued report = %+v", queued)
	}

	r, fake := newReporter(t, s, http.StatusServiceUnavailable, http.StatusCreated)
	if n := runOnce(t, r, ctx); n != 1 {
		t.Fatalf("first run made %d attempts", n)
	}
	retrying := report(t, s, ctx, slip.ID)
	if retrying.Status != store.SalesReportPending || retrying.Attempts != 1 || retrying.LastStatus == nil || *retrying.LastStatus != 503 ||
		retrying.NextAttemptAt == nil || !retrying.NextAttemptAt.After(time.Now()) || retrying.LastError == nil || !strings.Contains(*retrying.LastError, "sales is down") {
		t.Fatalf("after a 503 = %+v", retrying)
	}
	if n := runOnce(t, r, ctx); n != 0 {
		t.Fatalf("a report in its back-off was sent again (%d attempts)", n)
	}
	makeDue(t, admin)
	if n := runOnce(t, r, ctx); n != 1 {
		t.Fatalf("retry run made %d attempts", n)
	}
	makeDue(t, admin)
	if n := runOnce(t, r, ctx); n != 0 {
		t.Fatalf("a reported slip was sent again (%d attempts)", n)
	}
	done := report(t, s, ctx, slip.ID)
	if done.Status != store.SalesReportReported || done.Attempts != 2 || done.ReportedAt == nil || done.LastStatus == nil || *done.LastStatus != 201 {
		t.Fatalf("after a 201 = %+v", done)
	}

	calls := fake.seen()
	if len(calls) != 2 {
		t.Fatalf("sales got %d calls, want 2", len(calls))
	}
	wantKey := store.SalesReportKeyPrefix + slip.ID + ":" + sh.OrderID
	want := map[string]any{"kind": "DELIVERED", "sourceRef": slip.Number, "lines": []any{
		map[string]any{"lineId": sh.Lines[0].OrderLineID, "quantity": "3", "tracking": []any{
			map[string]any{"serial": "SN-1", "quantity": "1"},
			map[string]any{"serial": "SN-2", "quantity": "1"},
			map[string]any{"batch": "LOT-S", "serial": "SN-3", "quantity": "1"},
		}},
		map[string]any{"lineId": sh.Lines[1].OrderLineID, "quantity": "10", "tracking": []any{
			map[string]any{"batch": "LOT-1", "quantity": "8"},
			map[string]any{"batch": "LOT-2", "quantity": "2"},
		}},
		map[string]any{"lineId": sh.Lines[2].OrderLineID, "quantity": "5", "actualWeight": "51.5", "tracking": []any{
			map[string]any{"batch": "LOT-CW", "quantity": "5", "actualWeight": "51.5"},
		}},
	}}
	wantJSON, _ := json.Marshal(want)
	for i, c := range calls {
		if c.path != "/v1/sales/orders/"+sh.OrderID+":record-progress" || c.key != wantKey {
			t.Errorf("call %d: %s key %q, want key %q", i, c.path, c.key, wantKey)
		}
		if got, _ := json.Marshal(c.body); string(got) != string(wantJSON) {
			t.Errorf("call %d body:\n got %s\nwant %s", i, got, wantJSON)
		}
		if c.claims.Iss != "fulfillment" || c.claims.Act != tenant.ServiceActor(salesreport.PrincipalName) || c.claims.Le != sh.LegalEntityID ||
			c.claims.Tid != testkit.TenantID(1) || !slices.Equal(c.claims.Duties, []string{"sales.order.record-delivery"}) {
			t.Errorf("call %d token = %+v", i, c.claims)
		}
	}

	// Cancelling a reported slip keeps the report: sales has no reversal of a delivery.
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel")); err != nil {
		t.Fatal(err)
	}
	if after := report(t, s, ctx, slip.ID); after.Status != store.SalesReportReported || after.CancelledAt == nil {
		t.Fatalf("reported then cancelled = %+v", after)
	}
	if n := runOnce(t, r, ctx); n != 0 || len(fake.seen()) != 2 {
		t.Fatalf("a cancel sent something to sales: %d attempts", n)
	}
}

// #496: a refusal (4xx) parks the report at once and nothing retries it until
// :retry queues it again with the same body and key; repeated 5xx park it once
// the attempts run out; a slip cancelled before it was reported is withdrawn
// and never sent; a stale attempt cannot overwrite a newer lease.
func TestReporter_ParksRefusalsAndWithdrawsCancelledSlips(t *testing.T) {
	s, admin, ctx := setup(t)

	refused := packAndPost(t, s, ctx, shipment(t, s, ctx, orderLine{qty: 4}), nil)
	r, fake := newReporter(t, s, http.StatusUnprocessableEntity, http.StatusOK)
	if n := runOnce(t, r, ctx); n != 1 {
		t.Fatalf("attempts = %d", n)
	}
	parked := report(t, s, ctx, refused.ID)
	if parked.Status != store.SalesReportParked || parked.Attempts != 1 || parked.LastStatus == nil || *parked.LastStatus != 422 ||
		parked.LastError == nil || !strings.Contains(*parked.LastError, "SLS-0058") || parked.NextAttemptAt != nil {
		t.Fatalf("after a 422 = %+v", parked)
	}
	makeDue(t, admin)
	if n := runOnce(t, r, ctx); n != 0 {
		t.Fatalf("a parked report was retried on its own (%d attempts)", n)
	}
	key := idem("retry")
	queued, err := s.RetrySalesReport(ctx, parked.ID, key)
	if err != nil || queued.Status != store.SalesReportPending || queued.Attempts != 0 {
		t.Fatalf("retry: %+v, %v", queued, err)
	}
	if again, err := s.RetrySalesReport(ctx, parked.ID, key); err != nil || again.ID != parked.ID {
		t.Fatalf("replayed retry: %+v, %v", again, err)
	}
	if n := runOnce(t, r, ctx); n != 1 {
		t.Fatalf("attempts after retry = %d", n)
	}
	if got := report(t, s, ctx, refused.ID); got.Status != store.SalesReportReported {
		t.Fatalf("after retry = %+v", got)
	}
	calls := fake.seen()
	if len(calls) != 2 || calls[0].key != calls[1].key {
		t.Fatalf("calls = %+v", calls)
	}
	first, _ := json.Marshal(calls[0].body)
	second, _ := json.Marshal(calls[1].body)
	if string(first) != string(second) || strings.Contains(string(first), "tracking") {
		t.Fatalf("retried body changed or carries tracking: %s vs %s", first, second)
	}
	if _, err := s.RetrySalesReport(ctx, parked.ID, idem("retry-reported")); err == nil {
		t.Fatal("a reported report was queued again")
	}

	// Outage: three attempts (MaxAttempts) and the report parks.
	outage := packAndPost(t, s, ctx, shipment(t, s, ctx, orderLine{qty: 2}), nil)
	r2, fake2 := newReporter(t, s, http.StatusBadGateway)
	for i := range 3 {
		makeDue(t, admin)
		if n := runOnce(t, r2, ctx); n != 1 {
			t.Fatalf("outage attempt %d: %d", i+1, n)
		}
	}
	if got := report(t, s, ctx, outage.ID); got.Status != store.SalesReportParked || got.Attempts != 3 || got.LastError == nil || !strings.Contains(*got.LastError, "gave up after 3 attempts") {
		t.Fatalf("after the attempts ran out = %+v", got)
	}
	if len(fake2.seen()) != 3 {
		t.Fatalf("outage calls = %d", len(fake2.seen()))
	}

	// Cancelled before it was reported: withdrawn, never sent, not retryable.
	cancelled := packAndPost(t, s, ctx, shipment(t, s, ctx, orderLine{qty: 1}), nil)
	if _, err := s.CancelPackingSlip(ctx, cancelled.ID, idem("cancel")); err != nil {
		t.Fatal(err)
	}
	withdrawn := report(t, s, ctx, cancelled.ID)
	if withdrawn.Status != store.SalesReportWithdrawn || withdrawn.CancelledAt == nil {
		t.Fatalf("cancelled before reporting = %+v", withdrawn)
	}
	makeDue(t, admin)
	before := len(fake2.seen())
	if n := runOnce(t, r2, ctx); n != 0 || len(fake2.seen()) != before {
		t.Fatalf("a withdrawn report was sent: %d", n)
	}
	if _, err := s.RetrySalesReport(ctx, withdrawn.ID, idem("retry-withdrawn")); err == nil {
		t.Fatal("a withdrawn report was queued again")
	}
	// The post's reversal slip is not reported: it is no delivery.
	if items, err := s.ListSalesReports(ctx, store.SalesReportFilter{}); err != nil || len(items) != 3 {
		t.Fatalf("reports = %d, %v; want one per posted slip", len(items), err)
	}

	// Lease: a claimed report is not claimed again, and a stale finish is refused.
	leased := packAndPost(t, s, ctx, shipment(t, s, ctx, orderLine{qty: 1}), nil)
	sys := store.SalesReportSystemContext(ctx, testkit.TenantID(1))
	claimed, err := s.ClaimSalesReports(sys, 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].SourceRef != leased.Number || claimed[0].Attempts != 1 {
		t.Fatalf("claim: %+v, %v", claimed, err)
	}
	if again, err := s.ClaimSalesReports(sys, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("a leased report was claimed twice: %+v, %v", again, err)
	}
	if err := s.FinishSalesReport(sys, claimed[0].ID, 0, store.SalesReportOutcome{Status: store.SalesReportReported, HTTPStatus: 201}); err != store.ErrStaleSalesReport {
		t.Fatalf("stale finish: %v", err)
	}
	// Cancelled while the attempt is in flight: sales took it, so it ends REPORTED.
	if _, err := s.CancelPackingSlip(ctx, leased.ID, idem("cancel-leased")); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishSalesReport(sys, claimed[0].ID, 1, store.SalesReportOutcome{Status: store.SalesReportReported, HTTPStatus: 201}); err != nil {
		t.Fatal(err)
	}
	if got := report(t, s, ctx, leased.ID); got.Status != store.SalesReportReported || got.CancelledAt == nil {
		t.Fatalf("cancelled in flight, then reported = %+v", got)
	}

	// Another tenant sees none of these reports.
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "user-test", Duties: testkit.ScopeDuties("")})
	if items, err := s.ListSalesReports(other, store.SalesReportFilter{}); err != nil || len(items) != 0 {
		t.Fatalf("tenant 2 sees %d reports, %v", len(items), err)
	}
}

func TestBackoff(t *testing.T) {
	for n, want := range map[int]time.Duration{0: 30 * time.Second, 1: 30 * time.Second, 2: time.Minute, 7: 32 * time.Minute, 8: time.Hour, 20: time.Hour} {
		if got := salesreport.Backoff(n); got != want {
			t.Errorf("Backoff(%d) = %s, want %s", n, got, want)
		}
	}
}
