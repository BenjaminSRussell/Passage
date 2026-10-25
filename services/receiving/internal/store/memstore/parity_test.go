package memstore_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
	"github.com/BenjaminSRussell/passage/services/receiving/migrations"
)

const (
	vendorID  = "00000000-0000-0000-0000-0000000000a1"
	poID      = "00000000-0000-0000-0000-0000000000b1"
	productID = "00000000-0000-0000-0000-0000000000d1"
	poLineID  = "00000000-0000-0000-0000-0000000000e1"
	locationA = "00000000-0000-0000-0000-0000000000f1"
)

type backend struct {
	st     store.Store
	events func(tenant.Principal) int
}

func principal(n int) tenant.Principal {
	return tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: testkit.TenantID(9)}
}

func ctxOf(p tenant.Principal) context.Context { return tenant.With(context.Background(), p) }

func newMemstore(*testing.T) backend {
	st := memstore.New()
	return backend{st: st, events: func(p tenant.Principal) (n int) {
		for _, e := range st.Events() {
			if e.TenantID == p.TenantID {
				n++
			}
		}
		return n
	}}
}

func newPostgres(t *testing.T) backend {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(ctxOf(principal(1)), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return backend{st: store.New(pool), events: func(p tenant.Principal) (n int) {
		err := dbx.WithTenantTx(ctxOf(p), pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `select count(*) from outbox`).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}}
}

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
		l.Qty, l.Serials, l.LocationID = decimal.NewFromInt(int64(len(serials))), serials, locationA
		return []store.Line{l}, nil
	}
}

func postWith(t *testing.T, p tenant.Principal, ledgerID string) store.PostFunc {
	return func(r store.Receipt) (store.PostOutcome, error) {
		evt, err := events.New(events.Type("receiving", "receipt", "posted", 1), "/passage/receiving/receiving", r.ID, p.TenantID,
			map[string]string{"receiptId": r.ID})
		if err != nil {
			t.Fatal(err)
		}
		return store.PostOutcome{
			PostingDate: r.Date, LedgerEntryID: ledgerID, Event: evt,
			Amounts: []store.LineAmount{{LineNo: 1, Amount: decimal.RequireFromString("7.5"), AmountAcct: decimal.RequireFromString("8.25")}},
		}, nil
	}
}

func registered(t *testing.T, st store.Store, p tenant.Principal, slip string, serials ...string) store.Receipt {
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

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err=%v, want domain code %s", err, code)
	}
}

func nextNumber(t *testing.T, st store.Store, p tenant.Principal, slip string) string {
	t.Helper()
	r, err := st.CreateArrival(ctxOf(p), arrival(p, slip))
	if err != nil {
		t.Fatal(err)
	}
	return r.Number
}

var cases = []struct {
	name string
	run  func(t *testing.T, b backend)
}{
	{"another tenant gets ErrNotFound", func(t *testing.T, b backend) {
		one, two := principal(1), principal(2)
		r := registered(t, b.st, one, "PS-1", "S1", "S2", "S3")
		if _, err := b.st.GetReceipt(ctxOf(two), r.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetReceipt err=%v, want ErrNotFound", err)
		}
		if _, err := b.st.Register(ctxOf(two), r.ID, registerWith("S9")); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Register err=%v, want ErrNotFound", err)
		}
		if _, err := b.st.Post(ctxOf(two), r.ID, postWith(t, two, "e")); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Post err=%v, want ErrNotFound", err)
		}
		if list, err := b.st.ListReceipts(ctxOf(two), store.ReceiptFilter{}); err != nil || len(list) != 0 {
			t.Errorf("ListReceipts len=%d err=%v, want empty", len(list), err)
		}
		if got, err := b.st.RecordedSerials(ctxOf(two), []string{productID}); err != nil || len(got) != 0 {
			t.Errorf("RecordedSerials len=%d err=%v, want empty", len(got), err)
		}
		if got, err := b.st.RecordedSerials(ctxOf(one), []string{productID}); err != nil || len(got) != 3 {
			t.Errorf("owner RecordedSerials len=%d err=%v, want 3", len(got), err)
		}
		if got := nextNumber(t, b.st, two, "PS-X"); got != "RCV-00000001" {
			t.Errorf("second tenant number=%s, want RCV-00000001", got)
		}
	}},
	{"duplicate packing slip names the first number and burns none", func(t *testing.T, b backend) {
		p := principal(1)
		first, err := b.st.CreateArrival(ctxOf(p), arrival(p, "PS-1"))
		if err != nil {
			t.Fatal(err)
		}
		var dup *store.DuplicatePackingSlipError
		if _, err = b.st.CreateArrival(ctxOf(p), arrival(p, "PS-1")); !errors.As(err, &dup) || dup.ReceiptNumber != first.Number {
			t.Fatalf("err=%v, want DuplicatePackingSlipError naming %s", err, first.Number)
		}
		if got := nextNumber(t, b.st, p, "PS-2"); got != "RCV-00000002" {
			t.Fatalf("number=%s, want RCV-00000002", got)
		}
	}},
	{"recorded serial fails registration and the next number is unchanged", func(t *testing.T, b backend) {
		p := principal(1)
		registered(t, b.st, p, "PS-1", "S1", "S2", "S3")
		second, err := b.st.CreateArrival(ctxOf(p), arrival(p, "PS-2"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = b.st.Register(ctxOf(p), second.ID, registerWith("S2", "S4", "S5"))
		wantCode(t, err, domain.CodeDuplicateSerial)
		if got, _ := b.st.GetReceipt(ctxOf(p), second.ID); got.State != domain.Arrived {
			t.Fatalf("state=%s after failed registration, want ARRIVED", got.State)
		}
		if got, _ := b.st.RecordedSerials(ctxOf(p), []string{productID}); len(got) != 3 {
			t.Fatalf("recorded %d serials, want 3", len(got))
		}
		_, err = b.st.DirectReceipt(ctxOf(p), arrival(p, "PS-D"), registerWith("S1", "S6", "S7"))
		wantCode(t, err, domain.CodeDuplicateSerial)
		if got := nextNumber(t, b.st, p, "PS-3"); got != "RCV-00000003" {
			t.Fatalf("number=%s, want RCV-00000003", got)
		}
	}},
	{"post on an arrived receipt is an invalid transition", func(t *testing.T, b backend) {
		p := principal(1)
		r, err := b.st.CreateArrival(ctxOf(p), arrival(p, "PS-1"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = b.st.Post(ctxOf(p), r.ID, postWith(t, p, "e"))
		wantCode(t, err, domain.CodeInvalidTransition)
		if _, err = b.st.Register(ctxOf(p), r.ID, registerWith("S1", "S2", "S3")); err != nil {
			t.Fatal(err)
		}
		_, err = b.st.Register(ctxOf(p), r.ID, registerWith("S4", "S5", "S6"))
		wantCode(t, err, domain.CodeInvalidTransition)
	}},
	{"post yields one open grir item per line and one event, repost changes nothing", func(t *testing.T, b backend) {
		p := principal(1)
		r := registered(t, b.st, p, "PS-1", "S1", "S2", "S3")
		boom := errors.New("boom")
		if _, err := b.st.Post(ctxOf(p), r.ID, func(store.Receipt) (store.PostOutcome, error) { return store.PostOutcome{}, boom }); !errors.Is(err, boom) {
			t.Fatalf("err=%v, want boom", err)
		}
		if got, _ := b.st.GetReceipt(ctxOf(p), r.ID); got.State != domain.Registered || b.events(p) != 0 {
			t.Fatalf("failed post left state=%s events=%d", got.State, b.events(p))
		}
		posted, err := b.st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-1"))
		if err != nil || posted.State != domain.Posted || posted.LedgerEntryID != "entry-1" || !posted.Lines[0].AmountAcct.Decimal.Equal(decimal.RequireFromString("8.25")) {
			t.Fatalf("posted=%+v err=%v", posted, err)
		}
		page, err := b.st.ListGrir(ctxOf(p), store.GrirFilter{PurchaseOrderID: poID})
		if err != nil || len(page.Items) != 1 || page.Items[0].Status != "OPEN" || page.Items[0].ReceiptNumber != r.Number || page.Totals.Count != 1 {
			t.Fatalf("grir=%+v err=%v", page, err)
		}
		if b.events(p) != 1 {
			t.Fatalf("events=%d, want 1", b.events(p))
		}
		_, err = b.st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-2"))
		wantCode(t, err, domain.CodeInvalidTransition)
		if b.events(p) != 1 {
			t.Fatalf("events=%d after repost, want 1", b.events(p))
		}
	}},
	{"claim key reports new, in-flight, completed and mismatch", func(t *testing.T, b backend) {
		p := principal(1)
		claim := func(key, hash string, lockFor time.Duration) store.ClaimResult {
			res, err := b.st.ClaimKey(ctxOf(p), key, hash, lockFor)
			if err != nil {
				t.Fatal(err)
			}
			return res
		}
		if got := claim("k", "h", time.Minute); got.State != store.ClaimNew {
			t.Fatalf("first=%v, want new", got.State)
		}
		if got := claim("k", "h", time.Minute); got.State != store.ClaimInFlight {
			t.Fatalf("repeat=%v, want in-flight", got.State)
		}
		if got := claim("k", "other", time.Minute); got.State != store.ClaimMismatch {
			t.Fatalf("other hash=%v, want mismatch", got.State)
		}
		if err := b.st.CompleteKey(ctxOf(p), "k", 201, []byte(`{"id":"r1"}`)); err != nil {
			t.Fatal(err)
		}
		if got := claim("k", "h", time.Minute); got.State != store.ClaimCompleted || got.Status != 201 || string(got.Body) != `{"id":"r1"}` {
			t.Fatalf("completed=%+v, want 201 with stored body", got)
		}
		if err := b.st.ReleaseKey(ctxOf(p), "k"); err != nil {
			t.Fatal(err)
		}
		if got := claim("k", "other", time.Minute); got.State != store.ClaimNew {
			t.Fatalf("after release=%v, want new", got.State)
		}
		claim("lapsed", "h", 0)
		time.Sleep(10 * time.Millisecond)
		if got := claim("lapsed", "h", time.Minute); got.State != store.ClaimNew {
			t.Fatalf("lapsed lock=%v, want new", got.State)
		}
	}},
}

func TestStoreParity(t *testing.T) {
	backends := []struct {
		name string
		make func(*testing.T) backend
	}{{"memstore", newMemstore}, {"postgres", newPostgres}}
	for _, be := range backends {
		for _, tc := range cases {
			t.Run(be.name+"/"+tc.name, func(t *testing.T) { tc.run(t, be.make(t)) })
		}
	}
}

func TestMemstoreRejectsContextWithoutPrincipal(t *testing.T) {
	if _, err := memstore.New().GetReceipt(context.Background(), "x"); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err=%v, want a principal error", err)
	}
}

func TestMemstoreConcurrentRace(t *testing.T) {
	st := memstore.New()
	p := principal(1)
	const workers = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	won, numbers := 0, map[string]struct{}{}
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := st.ClaimKey(ctxOf(p), "same", "h", time.Minute)
			r, err2 := st.CreateArrival(ctxOf(p), arrival(p, fmt.Sprintf("PS-%d", i)))
			mu.Lock()
			defer mu.Unlock()
			if err != nil || err2 != nil {
				t.Errorf("claim err=%v arrival err=%v", err, err2)
			}
			if res.State == store.ClaimNew {
				won++
			}
			numbers[r.Number] = struct{}{}
		}()
	}
	wg.Wait()
	if won != 1 || len(numbers) != workers {
		t.Fatalf("claim winners=%d distinct numbers=%d, want 1 and %d", won, len(numbers), workers)
	}
}
