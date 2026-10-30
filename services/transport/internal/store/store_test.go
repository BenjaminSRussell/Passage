package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
	"github.com/BenjaminSRussell/passage/services/transport/migrations"
)

// setup migrates a throwaway schema as the admin role and returns a store on
// the application role, so row-level security applies exactly as in production.
// It needs PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL.
func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(app), admin
}

func tenantCtx(n int) context.Context {
	return testkit.Ctx(testkit.TenantID(n))
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func problemCode(t *testing.T, err error) string {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) {
		t.Fatalf("want a problem, got %v", err)
	}
	return p.Code
}

func newCard(carrier string, from time.Time, to *time.Time) domain.RateCard {
	return domain.RateCard{
		CarrierID: carrier, ServiceID: ids.New(), Basis: domain.BasisLane, Currency: "USD", ValidFrom: from, ValidTo: to,
		MinimumCharge: dec("15"), DimDivisor: 5000, FuelPct: dec("18.5"),
		Rows:         []domain.RateRow{{Origin: "*", Destination: "*", BreakFrom: dec("0"), Rate: dec("0.50"), Unit: domain.UnitKG}},
		Accessorials: []domain.Accessorial{{Code: "LIFTGATE", Flat: dec("25")}},
	}
}

func newBill(carrier string, amount string) domain.FreightBill {
	return domain.FreightBill{LoadID: ids.New(), CarrierID: carrier, Currency: "USD",
		ExpectedLines: []domain.Line{{ChargeCode: "BASE", Amount: dec(amount)}}}
}

func seedLoad(t *testing.T, s *store.Postgres, ctx context.Context) string {
	t.Helper()
	loads, err := s.BuildLoads(ctx, built(t, shipments(1, ids.New(), "10")), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	return loads[0].ID
}

func billOn(t *testing.T, s *store.Postgres, ctx context.Context, carrier, amount string) domain.FreightBill {
	t.Helper()
	bill := newBill(carrier, amount)
	bill.LoadID = seedLoad(t, s, ctx)
	return bill
}

func newInvoice(carrier, number string, bill *string, amount string) domain.CarrierInvoice {
	return domain.CarrierInvoice{CarrierID: carrier, InvoiceNo: number, InvoiceDate: "2026-06-03", Source: "EDI210", FreightBillID: bill,
		Currency: "USD", Lines: []domain.Line{{ChargeCode: "BASE", Amount: dec(amount)}}}
}

type outboxRow struct {
	Type    string
	Payload struct {
		Subject string                              `json:"subject"`
		Data    tmsevents.FreightBillMatchedPayload `json:"data"`
	}
}

func outboxRows(t *testing.T, admin *pgxpool.Pool) []outboxRow {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select type, payload from outbox order by created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		var raw []byte
		if err := rows.Scan(&r.Type, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &r.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestRateCard_RoundTripsAndRejectsOverlap(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	carrier := ids.New()
	june := day(2026, 6, 30)
	first := newCard(carrier, day(2026, 1, 1), &june)
	saved, err := s.CreateRateCard(ctx, first, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || !saved.FuelPct.Equal(dec("18.5")) || len(saved.Rows) != 1 || !saved.ValidTo.Equal(june) {
		t.Fatalf("saved = %+v", saved)
	}
	overlapping := first
	overlapping.ValidFrom, overlapping.ValidTo = june, nil
	if _, err := s.CreateRateCard(ctx, overlapping, store.Idem{}); problemCode(t, err) != domain.CodeRateCardOverlap {
		t.Fatalf("overlap error = %v", err)
	}
	next := first
	next.ValidFrom, next.ValidTo = day(2026, 7, 1), nil
	if _, err := s.CreateRateCard(ctx, next, store.Idem{}); err != nil {
		t.Fatalf("adjacent card: %v", err)
	}
	cards, err := s.ListRateCards(ctx, store.RateCardFilter{CarrierID: carrier})
	if err != nil || len(cards) != 2 {
		t.Fatalf("cards = %d, %v; want 2", len(cards), err)
	}
}

// TestRateCard_ConcurrentOverlapsSaveOnce checks that the advisory lock
// serialises overlap checks: of several concurrent saves of the same validity,
// exactly one wins (BR-34.10).
func TestRateCard_ConcurrentOverlapsSaveOnce(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	in := newCard(ids.New(), day(2026, 1, 1), nil)
	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.CreateRateCard(ctx, in, store.Idem{})
		}(i)
	}
	wg.Wait()
	saved := 0
	for _, err := range errs {
		if err == nil {
			saved++
		} else if problemCode(t, err) != domain.CodeRateCardOverlap {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if saved != 1 {
		t.Fatalf("saved %d overlapping cards, want 1", saved)
	}
}

func TestRateCard_IdempotentReplayAndReuse(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	in := newCard(ids.New(), day(2026, 1, 1), nil)
	idem := store.Idem{Key: "card-1", Fingerprint: "fp-a"}
	first, err := s.CreateRateCard(ctx, in, idem)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateRateCard(ctx, in, idem)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %s, %v; want %s", replay.ID, err, first.ID)
	}
	if _, err := s.CreateRateCard(ctx, in, store.Idem{Key: "card-1", Fingerprint: "fp-b"}); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("reuse error = %v", err)
	}
}

func TestFreightBill_IdempotentAndTenantIsolated(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	idem := store.Idem{Key: "bill-1", Fingerprint: "fp"}
	bill, err := s.CreateFreightBill(ctx, billOn(t, s, ctx, ids.New(), "412.50"), idem)
	if err != nil {
		t.Fatal(err)
	}
	if bill.MatchStatus != domain.BillUnmatched || !bill.ExpectedTotal.Equal(dec("412.50")) || bill.Version != 1 || bill.CreatedAt.IsZero() {
		t.Fatalf("bill = %+v", bill)
	}
	replay, err := s.CreateFreightBill(ctx, billOn(t, s, ctx, ids.New(), "1"), store.Idem{Key: "bill-1", Fingerprint: "fp"})
	if err != nil || replay.ID != bill.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	other := tenantCtx(2)
	if _, err := s.GetFreightBill(other, bill.ID); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("other tenant read = %v, want not found", err)
	}
	if list, err := s.ListFreightBills(other, store.FreightBillFilter{}); err != nil || len(list) != 0 {
		t.Fatalf("other tenant list = %v, %v", list, err)
	}
	// The same key in another tenant is a different command.
	if _, err := s.CreateFreightBill(other, billOn(t, s, other, ids.New(), "10"), store.Idem{Key: "bill-1", Fingerprint: "other"}); err != nil {
		t.Fatalf("same key in another tenant: %v", err)
	}
}

// TestRegisterCarrierInvoice_AC344 is AC-34.4 end to end: a bill of 412.50 and
// an invoice of 418.00 with tolerance $5 / 2 % is VARIANCE with 5.50, and the
// match event is written in the same transaction.
func TestRegisterCarrierInvoice_AC344(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	carrier := ids.New()
	bill, err := s.CreateFreightBill(ctx, billOn(t, s, ctx, carrier, "412.50"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	inv, updated, err := s.RegisterCarrierInvoice(ctx, newInvoice(carrier, "INV-1", &bill.ID, "418.00"))
	if err != nil {
		t.Fatal(err)
	}
	if inv.MatchStatus != domain.MatchVariance || !inv.VarianceTotal.Equal(dec("5.50")) || !inv.Total.Equal(dec("418.00")) || inv.CreatedAt.IsZero() {
		t.Fatalf("invoice = %+v", inv)
	}
	if updated == nil || updated.MatchStatus != domain.MatchVariance || *updated.CarrierInvoiceID != inv.ID || updated.Version != 2 {
		t.Fatalf("bill = %+v", updated)
	}
	stored, err := s.GetFreightBill(ctx, bill.ID)
	if err != nil || stored.MatchStatus != domain.MatchVariance || !stored.VarianceTotal.Equal(dec("5.50")) {
		t.Fatalf("stored bill = %+v, %v", stored, err)
	}
	events := outboxRows(t, admin)
	if len(events) != 1 || events[0].Type != tmsevents.FreightBillMatched {
		t.Fatalf("outbox = %+v", events)
	}
	data := events[0].Payload.Data
	if data.FreightBillID != bill.ID || data.CarrierInvoiceID != inv.ID || data.Variance != "5.50" || data.MatchStatus != domain.MatchVariance ||
		len(data.Lines) != 1 || data.Lines[0].WithinTolerance {
		t.Fatalf("event data = %+v", data)
	}
}

func TestRegisterCarrierInvoice_Rejections(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	carrier := ids.New()
	bill, err := s.CreateFreightBill(ctx, billOn(t, s, ctx, carrier, "100.00"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RegisterCarrierInvoice(ctx, newInvoice(carrier, "INV-1", &bill.ID, "100.00")); err != nil {
		t.Fatal(err)
	}
	unknown := ids.New()
	euro := newInvoice(carrier, "INV-EUR", &bill.ID, "100.00")
	euro.Currency = "EUR"
	freshBill, err := s.CreateFreightBill(ctx, billOn(t, s, ctx, carrier, "50.00"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	euro.FreightBillID = &freshBill.ID
	tests := []struct {
		name string
		in   domain.CarrierInvoice
		code string
	}{
		{"duplicate number per carrier (TMS-0108)", newInvoice(carrier, "INV-1", nil, "1"), domain.CodeDuplicateInvoice},
		{"bill already invoiced (TMS-0111)", newInvoice(carrier, "INV-2", &bill.ID, "100.00"), domain.CodeBillInvoiced},
		{"other carrier's bill (TMS-0112)", newInvoice(ids.New(), "INV-3", &freshBill.ID, "50.00"), domain.CodeInvoiceMismatch},
		{"other currency (TMS-0112)", euro, domain.CodeInvoiceMismatch},
		{"unknown bill (TMS-0100)", newInvoice(carrier, "INV-4", &unknown, "1"), domain.CodeNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.RegisterCarrierInvoice(ctx, tc.in); problemCode(t, err) != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
	if n := len(outboxRows(t, admin)); n != 1 {
		t.Fatalf("outbox rows = %d; rejected invoices must not publish", n)
	}
	if got, err := s.GetFreightBill(ctx, freshBill.ID); err != nil || got.MatchStatus != domain.BillUnmatched || got.Version != 1 {
		t.Fatalf("rejected invoices must leave the bill untouched: %+v, %v", got, err)
	}
}

func TestRegisterCarrierInvoice_UnexpectedIsStoredWithoutEvent(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	carrier := ids.New()
	inv, bill, err := s.RegisterCarrierInvoice(ctx, newInvoice(carrier, "INV-X", nil, "75.00"))
	if err != nil {
		t.Fatal(err)
	}
	if bill != nil || inv.MatchStatus != domain.MatchUnexpected || inv.VarianceTotal != nil {
		t.Fatalf("invoice = %+v, bill = %+v", inv, bill)
	}
	if n := len(outboxRows(t, admin)); n != 0 {
		t.Fatalf("outbox rows = %d, want 0", n)
	}
	queue, err := s.ListCarrierInvoices(ctx, store.InvoiceFilter{MatchStatus: domain.MatchUnexpected})
	if err != nil || len(queue) != 1 || queue[0].ID != inv.ID {
		t.Fatalf("exception queue = %+v, %v", queue, err)
	}
	got, err := s.GetCarrierInvoice(ctx, inv.ID)
	if err != nil || got.InvoiceNo != "INV-X" || got.InvoiceDate != "2026-06-03" || !got.Total.Equal(dec("75")) {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetCarrierInvoice(tenantCtx(2), inv.ID); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("other tenant read = %v", err)
	}
}

func TestListFreightBills_FiltersByStatusAndCarrier(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	a, b := ids.New(), ids.New()
	matched, err := s.CreateFreightBill(ctx, billOn(t, s, ctx, a, "100.00"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RegisterCarrierInvoice(ctx, newInvoice(a, "M-1", &matched.ID, "101.00")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFreightBill(ctx, billOn(t, s, ctx, b, "50.00"), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListFreightBills(ctx, store.FreightBillFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %d, %v", len(all), err)
	}
	onlyMatched, err := s.ListFreightBills(ctx, store.FreightBillFilter{MatchStatus: domain.MatchMatched})
	if err != nil || len(onlyMatched) != 1 || onlyMatched[0].ID != matched.ID {
		t.Fatalf("matched = %+v, %v", onlyMatched, err)
	}
	byCarrier, err := s.ListFreightBills(ctx, store.FreightBillFilter{CarrierID: b})
	if err != nil || len(byCarrier) != 1 || byCarrier[0].CarrierID != b {
		t.Fatalf("by carrier = %+v, %v", byCarrier, err)
	}
}
