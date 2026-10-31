package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/migrations"
)

const (
	productID = "00000000-0000-0000-0000-0000000000d1"
	poLineID  = "00000000-0000-0000-0000-0000000000e1"
)

type env struct {
	st    *store.Postgres
	pool  *pgxpool.Pool
	count func(tenant.Principal, string) int
}

func setup(t *testing.T) env {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(tenant.With(context.Background(), principal(1)), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	count := func(p tenant.Principal, table string) (n int) {
		err := dbx.WithTenantTx(tenant.With(context.Background(), p), pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `select count(*) from `+table).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	return env{st: store.New(pool), pool: pool, count: count}
}

func ctxOf(p tenant.Principal) context.Context { return tenant.With(context.Background(), p) }

func arrival(p tenant.Principal, slip string) store.Receipt {
	return store.Receipt{
		LegalEntityID: p.LegalEntityID, VendorID: vendorID, PurchaseOrderID: poID, PackingSlip: slip, Carrier: "DHL",
		Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Currency: "USD", ExchangeRate: decimal.RequireFromString("1.1"),
		Lines: []store.Line{{
			POLineID: poLineID, ProductID: productID, OrderedRemaining: decimal.NewFromInt(3), OverDeliveryPct: decimal.NewFromInt(10),
			UnitPrice: decimal.RequireFromString("2.5"), Unit: "EA", SerialTracked: true,
		}},
	}
}

func registerWith(serials ...string) store.RegisterFunc {
	return func(r store.Receipt, _ map[domain.SerialKey]struct{}) ([]store.Line, error) {
		l := r.Lines[0]
		l.Qty, l.Serials, l.LocationID = decimal.NewFromInt(int64(len(serials))), serials, "00000000-0000-0000-0000-0000000000f1"
		return []store.Line{l}, nil
	}
}

func postWith(t *testing.T, p tenant.Principal, ledgerID string) store.PostFunc {
	return func(r store.Receipt) (store.PostOutcome, error) {
		evt, err := events.New(events.Type("receiving", "receipt", "posted", 1), "/passage/receiving/receiving", r.ID, p.TenantID,
			map[string]string{"receiptId": r.ID, "ledgerEntryId": ledgerID})
		if err != nil {
			t.Fatal(err)
		}
		return store.PostOutcome{
			PostingDate: r.Date, LedgerEntryID: ledgerID, Event: evt,
			Amounts: []store.LineAmount{{LineNo: 1, Amount: decimal.RequireFromString("7.5"), AmountAcct: decimal.RequireFromString("8.25")}},
		}, nil
	}
}

func registered(t *testing.T, st *store.Postgres, p tenant.Principal, slip string, serials ...string) store.Receipt {
	t.Helper()
	r, err := st.CreateArrival(ctxOf(p), arrival(p, slip))
	if err != nil {
		t.Fatal(err)
	}
	if r, err = st.Register(ctxOf(p), r.ID, registerWith(serials...)); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCreateArrivalNumbersPerTenant(t *testing.T) {
	st := setup(t).st
	one, two := principal(1), principal(2)
	for i, want := range []string{"RCV-00000001", "RCV-00000002"} {
		r, err := st.CreateArrival(ctxOf(one), arrival(one, "PS-"+want))
		if err != nil || r.Number != want || r.State != domain.Arrived {
			t.Fatalf("arrival %d: number=%q state=%q err=%v, want %s", i, r.Number, r.State, err, want)
		}
	}
	r, err := st.CreateArrival(ctxOf(two), arrival(two, "PS-X"))
	if err != nil || r.Number != "RCV-00000001" {
		t.Fatalf("second tenant number=%q err=%v, want RCV-00000001", r.Number, err)
	}
	if len(r.Lines) != 1 || r.Lines[0].LineNo != 1 || !r.ExchangeRate.Equal(decimal.RequireFromString("1.1")) || r.Lines[0].Currency != "USD" {
		t.Fatalf("lines or rate not persisted: %+v", r)
	}
}

func TestCreateArrivalRollbackBurnsNoNumber(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	bad := arrival(p, "PS-BAD")
	bad.Lines[0].OrderedRemaining = decimal.Zero
	if _, err := st.CreateArrival(ctxOf(p), bad); err == nil {
		t.Fatal("zero quantity line must fail the qty CHECK")
	}
	r, err := st.CreateArrival(ctxOf(p), arrival(p, "PS-OK"))
	if err != nil || r.Number != "RCV-00000001" {
		t.Fatalf("number=%q err=%v, want RCV-00000001 after rollback", r.Number, err)
	}
}

func TestCreateArrivalDuplicatePackingSlip(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	first, err := st.CreateArrival(ctxOf(p), arrival(p, "PS-1"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateArrival(ctxOf(p), arrival(p, "PS-1"))
	var dup *store.DuplicatePackingSlipError
	if !errors.As(err, &dup) || dup.ReceiptNumber != first.Number {
		t.Fatalf("err=%v, want DuplicatePackingSlipError naming %s", err, first.Number)
	}
	next, err := st.CreateArrival(ctxOf(p), arrival(p, "PS-2"))
	if err != nil || next.Number != "RCV-00000002" {
		t.Fatalf("number=%q err=%v, want RCV-00000002", next.Number, err)
	}
}

func TestStoreTenantIsolation(t *testing.T) {
	e := setup(t)
	st, count := e.st, e.count
	one, two := principal(1), principal(2)
	r := registered(t, st, one, "PS-1", "S1", "S2", "S3")
	if _, err := st.Post(ctxOf(one), r.ID, postWith(t, one, "entry-1")); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"receiving_receipt", "receiving_receipt_line", "receiving_serial", "receiving_grir_open_item", "outbox"} {
		if got := count(one, table); got == 0 {
			t.Errorf("%s: owner sees no rows", table)
		}
		if got := count(two, table); got != 0 {
			t.Errorf("%s: other tenant sees %d rows", table, got)
		}
	}
	if _, err := st.GetReceipt(ctxOf(two), r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetReceipt err=%v, want ErrNotFound", err)
	}
	if _, err := st.Register(ctxOf(two), r.ID, registerWith("S9")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Register err=%v, want ErrNotFound", err)
	}
	if _, err := st.Post(ctxOf(two), r.ID, postWith(t, two, "entry-2")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Post err=%v, want ErrNotFound", err)
	}
	if list, err := st.ListReceipts(ctxOf(two), store.ReceiptFilter{}); err != nil || len(list) != 0 {
		t.Errorf("ListReceipts len=%d err=%v, want empty", len(list), err)
	}
	if recorded, err := st.RecordedSerials(ctxOf(two), []string{productID}); err != nil || len(recorded) != 0 {
		t.Errorf("RecordedSerials len=%d err=%v, want empty", len(recorded), err)
	}
	if recorded, err := st.RecordedSerials(ctxOf(one), []string{productID}); err != nil || len(recorded) != 3 {
		t.Errorf("owner RecordedSerials len=%d err=%v, want 3", len(recorded), err)
	}
	if _, err := st.GetReceipt(ctxOf(one), "not-a-uuid"); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Errorf("non-uuid id err=%v, want a database error", err)
	}
}

func TestRegisterRollsBackOnFnError(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	r, err := st.CreateArrival(ctxOf(p), arrival(p, "PS-1"))
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if _, err := st.Register(ctxOf(p), r.ID, func(store.Receipt, map[domain.SerialKey]struct{}) ([]store.Line, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err=%v, want boom", err)
	}
	if got, _ := st.GetReceipt(ctxOf(p), r.ID); got.State != domain.Arrived {
		t.Fatalf("state=%s, want ARRIVED", got.State)
	}
}

func TestRegisterPersistsLinesAndSerials(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	r := registered(t, st, p, "PS-1", "S1", "S2", "S3")
	l := r.Lines[0]
	if r.State != domain.Registered || !l.Qty.Equal(decimal.NewFromInt(3)) || len(l.Serials) != 3 || l.InventoryStatus != "AVAILABLE" || l.LocationID == "" {
		t.Fatalf("registration not persisted: %+v", r)
	}
	again, err := st.Register(ctxOf(p), r.ID, registerWith("S7"))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeInvalidTransition {
		t.Fatalf("second register err=%v (%+v), want RCV-STA-001", err, again)
	}
}

func TestRegisterDuplicateSerialAcrossReceipts(t *testing.T) {
	e := setup(t)
	st, count := e.st, e.count
	p := principal(1)
	registered(t, st, p, "PS-1", "S1", "S2", "S3")
	second, err := st.CreateArrival(ctxOf(p), arrival(p, "PS-2"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Register(ctxOf(p), second.ID, registerWith("S2", "S4", "S5"))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeDuplicateSerial {
		t.Fatalf("err=%v, want INV-SER-001", err)
	}
	if got := count(p, "receiving_serial"); got != 3 {
		t.Fatalf("serial rows=%d, want 3 (failed registration records nothing)", got)
	}
	if got, _ := st.GetReceipt(ctxOf(p), second.ID); got.State != domain.Arrived {
		t.Fatalf("state=%s, want ARRIVED", got.State)
	}
}

func TestRegisterConcurrentSameSerialOneWins(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	a, errA := st.CreateArrival(ctxOf(p), arrival(p, "PS-A"))
	b, errB := st.CreateArrival(ctxOf(p), arrival(p, "PS-B"))
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, id := range []string{a.ID, b.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = st.Register(ctxOf(p), id, registerWith("SX", "SY", "SZ"))
		}()
	}
	wg.Wait()
	var de *domain.Error
	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
			if !errors.As(err, &de) || de.Code != domain.CodeDuplicateSerial {
				t.Errorf("loser err=%v, want INV-SER-001", err)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("failed=%d, want exactly one loser", failed)
	}
}

func TestPostWritesGrirAndOutboxAtomically(t *testing.T) {
	e := setup(t)
	st, count := e.st, e.count
	p := principal(1)
	r := registered(t, st, p, "PS-1", "S1", "S2", "S3")
	boom := errors.New("ledger down")
	if _, err := st.Post(ctxOf(p), r.ID, func(store.Receipt) (store.PostOutcome, error) { return store.PostOutcome{}, boom }); !errors.Is(err, boom) {
		t.Fatalf("err=%v, want boom", err)
	}
	if got, _ := st.GetReceipt(ctxOf(p), r.ID); got.State != domain.Registered || count(p, "receiving_grir_open_item") != 0 || count(p, "outbox") != 0 {
		t.Fatalf("failed post left state=%s grir=%d outbox=%d", got.State, count(p, "receiving_grir_open_item"), count(p, "outbox"))
	}
	posted, err := st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-1"))
	if err != nil {
		t.Fatal(err)
	}
	l := posted.Lines[0]
	if posted.State != domain.Posted || posted.LedgerEntryID != "entry-1" || posted.PostingDate == nil || posted.PostedAt == nil ||
		!l.Amount.Decimal.Equal(decimal.RequireFromString("7.5")) || !l.AmountAcct.Decimal.Equal(decimal.RequireFromString("8.25")) {
		t.Fatalf("posting not persisted: %+v", posted)
	}
	var status, vendor string
	var amount decimal.Decimal
	err = dbx.WithTenantTx(ctxOf(p), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select status, vendor_id::text, received_amount from receiving_grir_open_item where po_line_id = $1`, poLineID).Scan(&status, &vendor, &amount)
	})
	if err != nil || status != "OPEN" || vendor != vendorID || !amount.Equal(decimal.RequireFromString("7.5")) {
		t.Fatalf("grir status=%q vendor=%q amount=%s err=%v", status, vendor, amount, err)
	}
	var payload []byte
	_ = dbx.WithTenantTx(ctxOf(p), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select payload from outbox where aggregate_type = 'receipt' and aggregate_id = $1`, r.ID).Scan(&payload)
	})
	var evt events.Event
	if err := json.Unmarshal(payload, &evt); err != nil || evt.Type != "dev.passage.receiving.receipt.posted.v1" || evt.TenantID != p.TenantID {
		t.Fatalf("outbox event=%+v err=%v", evt, err)
	}
	_, err = st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-2"))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeInvalidTransition || count(p, "outbox") != 1 {
		t.Fatalf("repost err=%v outbox=%d, want RCV-STA-001 and one outbox row", err, count(p, "outbox"))
	}
}

func TestPostRequiresRegistered(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	r, err := st.CreateArrival(ctxOf(p), arrival(p, "PS-1"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-1"))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeInvalidTransition {
		t.Fatalf("err=%v, want RCV-STA-001", err)
	}
}

func TestListReceiptsKeysetFiltersAndLegalEntityScope(t *testing.T) {
	st := setup(t).st
	nine := principal(1)
	eight := tenant.Principal{TenantID: nine.TenantID, LegalEntityID: testkit.TenantID(8)}
	all := tenant.Principal{TenantID: nine.TenantID, Duties: map[string]struct{}{tenant.DutyAllScope: {}}}
	registered(t, st, nine, "PS-1", "S1", "S2", "S3")
	for _, slip := range []string{"PS-2", "PS-3"} {
		if _, err := st.CreateArrival(ctxOf(nine), arrival(nine, slip)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateArrival(ctxOf(eight), arrival(eight, "PS-8")); err != nil {
		t.Fatal(err)
	}
	if list, err := st.ListReceipts(ctxOf(all), store.ReceiptFilter{}); err != nil || len(list) != 4 {
		t.Fatalf("unscoped len=%d err=%v, want 4", len(list), err)
	}
	if list, err := st.ListReceipts(ctxOf(eight), store.ReceiptFilter{}); err != nil || len(list) != 1 || list[0].LegalEntityID != eight.LegalEntityID {
		t.Fatalf("scoped list=%+v err=%v, want only legal entity 8", list, err)
	}
	if list, err := st.ListReceipts(ctxOf(all), store.ReceiptFilter{State: domain.Registered}); err != nil || len(list) != 1 {
		t.Fatalf("registered len=%d err=%v, want 1", len(list), err)
	}
	if list, err := st.ListReceipts(ctxOf(all), store.ReceiptFilter{VendorID: poID}); err != nil || len(list) != 0 {
		t.Fatalf("other vendor len=%d err=%v, want 0", len(list), err)
	}
	page1, err := st.ListReceipts(ctxOf(all), store.ReceiptFilter{Limit: 2})
	if err != nil || len(page1) != 3 {
		t.Fatalf("page1 len=%d err=%v, want limit+1 rows", len(page1), err)
	}
	last := page1[1]
	page2, err := st.ListReceipts(ctxOf(all), store.ReceiptFilter{Limit: 2, CursorCreatedAt: last.CreatedAt, CursorID: last.ID})
	if err != nil || len(page2) != 2 || page2[0].ID != page1[2].ID {
		t.Fatalf("page2=%+v err=%v, want the remaining two starting at %s", page2, err, page1[2].ID)
	}
}

func TestDirectReceiptRegistersInOneTx(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	r, err := st.DirectReceipt(ctxOf(p), arrival(p, "PS-D1"), registerWith("S1", "S2", "S3"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Number != "RCV-00000001" || r.State != domain.Registered || len(r.Lines[0].Serials) != 3 || !r.Lines[0].Qty.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("direct receipt not registered in one step: %+v", r)
	}
	next, err := st.CreateArrival(ctxOf(p), arrival(p, "PS-D2"))
	if err != nil || next.Number != "RCV-00000002" {
		t.Fatalf("next number=%q err=%v, want RCV-00000002", next.Number, err)
	}
}

func TestDirectReceiptFailuresLeaveNothingAndBurnNoNumber(t *testing.T) {
	e := setup(t)
	st, count := e.st, e.count
	p := principal(1)
	registered(t, st, p, "PS-0", "S9")
	before := count(p, "receiving_receipt")
	rejected := &domain.Error{Code: domain.CodeOverDelivery, Detail: "too many"}
	cases := []struct {
		name string
		fn   store.RegisterFunc
		code string
	}{
		{"duplicate serial in request", registerWith("S1", "S1", "S2"), domain.CodeDuplicateSerial},
		{"serial already recorded", registerWith("S9"), domain.CodeDuplicateSerial},
		{"register func rejects", func(store.Receipt, map[domain.SerialKey]struct{}) ([]store.Line, error) { return nil, rejected }, domain.CodeOverDelivery},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := st.DirectReceipt(ctxOf(p), arrival(p, "PS-F"), c.fn)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != c.code {
				t.Fatalf("err=%v, want %s", err, c.code)
			}
			if got := count(p, "receiving_receipt"); got != before {
				t.Fatalf("receipt rows=%d, want %d", got, before)
			}
		})
	}
	r, err := st.CreateArrival(ctxOf(p), arrival(p, "PS-NEXT"))
	if err != nil || r.Number != "RCV-00000002" {
		t.Fatalf("number=%q err=%v, want RCV-00000002 (failed direct receipts burn no number)", r.Number, err)
	}
}

func TestDirectReceiptDuplicatePackingSlip(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	first, err := st.DirectReceipt(ctxOf(p), arrival(p, "PS-1"), registerWith("S1"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DirectReceipt(ctxOf(p), arrival(p, "PS-1"), registerWith("S2"))
	var dup *store.DuplicatePackingSlipError
	if !errors.As(err, &dup) || dup.ReceiptNumber != first.Number {
		t.Fatalf("err=%v, want DuplicatePackingSlipError naming %s", err, first.Number)
	}
}

func TestDirectReceiptRequiresPrincipal(t *testing.T) {
	st := setup(t).st
	if _, err := st.DirectReceipt(context.Background(), arrival(principal(1), "PS-1"), registerWith("S1")); err == nil {
		t.Fatal("want error without a principal")
	}
}

func TestArrivalPersistsPOLineNo(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	in := arrival(p, "PS-PO-LINE")
	in.Lines[0].POLineNo = 3
	created, err := st.CreateArrival(ctxOf(p), in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetReceipt(ctxOf(p), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lines[0].POLineNo != 3 {
		t.Fatalf("po line number %d, want 3", got.Lines[0].POLineNo)
	}
	plain, err := st.CreateArrival(ctxOf(p), arrival(p, "PS-NO-PO-LINE"))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Lines[0].POLineNo != 0 {
		t.Fatalf("a line keyed without a PO line number reads back %d", plain.Lines[0].POLineNo)
	}
}

func TestRegisterSeesWhatOtherReceiptsTookFromThePOLine(t *testing.T) {
	st := setup(t).st
	p := principal(1)
	seen := func(slip string, qty int64) decimal.Decimal {
		t.Helper()
		in := arrival(p, slip)
		in.Lines[0].POLineNo, in.Lines[0].SerialTracked = 2, false
		r, err := st.CreateArrival(ctxOf(p), in)
		if err != nil {
			t.Fatal(err)
		}
		var other decimal.Decimal
		_, err = st.Register(ctxOf(p), r.ID, func(cur store.Receipt, _ map[domain.SerialKey]struct{}) ([]store.Line, error) {
			other = cur.Lines[0].OtherReceived
			l := cur.Lines[0]
			l.Qty, l.LocationID = decimal.NewFromInt(qty), "00000000-0000-0000-0000-0000000000f1"
			return []store.Line{l}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return other
	}
	if got := seen("PS-OTHER-1", 3); !got.IsZero() {
		t.Fatalf("first receipt saw %s", got)
	}
	if got := seen("PS-OTHER-2", 2); !got.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("second receipt saw %s, want 3", got)
	}
	got, err := st.ReceivedOnPurchaseOrder(ctxOf(p), poID)
	if err != nil || !got[2].Equal(decimal.NewFromInt(5)) {
		t.Fatalf("received on the order %v err %v", got, err)
	}
}

func TestErsPriceConfirmationKeepsHistoryAndIsScopedToTheEntity(t *testing.T) {
	e := setup(t)
	st := e.st
	p := principal(1)
	r := registered(t, st, p, "PS-CONFIRM", "S1", "S2", "S3")
	var olds []string
	confirm := func(price, by string) error {
		return st.ConfirmErsPrice(ctxOf(p), p.LegalEntityID, r.ID, 1, decimal.RequireFromString(price), by, func(old decimal.NullDecimal) (events.Event, error) {
			olds = append(olds, old.Decimal.String()+fmt.Sprint(old.Valid))
			return events.New(events.Type("receiving", "erspriceconfirmation", "confirmed", 1), "/passage/receiving/receiving", r.ID, p.TenantID, map[string]string{"receiptId": r.ID})
		})
	}

	if err := confirm("2.6", "buyer"); err != nil {
		t.Fatal(err)
	}
	if err := confirm("2.7", "buyer2"); err != nil {
		t.Fatal(err)
	}

	got, err := st.ErsPriceConfirmations(ctxOf(p), r.ID)
	if err != nil || len(got) != 1 || !got[1].Price.Equal(decimal.RequireFromString("2.7")) || got[1].By != "buyer2" {
		t.Fatalf("confirmations %v err %v", got, err)
	}
	if olds[0] != "0false" || olds[1] != "2.6true" {
		t.Fatalf("old prices seen by the event builder %v", olds)
	}
	if n := e.count(p, "receiving_ers_price_confirmation_history"); n != 2 {
		t.Fatalf("history rows %d, want 2", n)
	}
	if n := e.count(p, "outbox"); n != 2 {
		t.Fatalf("outbox rows %d, want 2", n)
	}
	nop := func(decimal.NullDecimal) (events.Event, error) { return events.Event{}, nil }
	for name, err := range map[string]error{
		"unknown line":  st.ConfirmErsPrice(ctxOf(p), p.LegalEntityID, r.ID, 9, decimal.NewFromInt(1), "buyer", nop),
		"other entity":  st.ConfirmErsPrice(ctxOf(p), "99999999-9999-4999-8999-999999999990", r.ID, 1, decimal.NewFromInt(1), "buyer", nop),
		"unknown order": st.ConfirmErsPrice(ctxOf(p), p.LegalEntityID, "99999999-9999-4999-8999-999999999991", 1, decimal.NewFromInt(1), "buyer", nop),
		"other tenant":  st.ConfirmErsPrice(ctxOf(principal(2)), principal(2).LegalEntityID, r.ID, 1, decimal.NewFromInt(1), "buyer", nop),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err %v, want ErrNotFound", name, err)
		}
	}
	if other, _ := st.ErsPriceConfirmations(ctxOf(principal(2)), r.ID); len(other) != 0 {
		t.Fatalf("another tenant sees %v", other)
	}
}
