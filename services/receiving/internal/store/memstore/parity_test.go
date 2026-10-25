package memstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	st        store.Store
	events    func(tenant.Principal) int
	expireKey func(tenant.Principal, string)
}

func principal(n int) tenant.Principal {
	return tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: testkit.TenantID(9)}
}

func ctxOf(p tenant.Principal) context.Context { return tenant.With(context.Background(), p) }

func newMemstore(t *testing.T) backend {
	st := memstore.New()
	return backend{st: st, events: func(p tenant.Principal) (n int) {
		for _, e := range st.Events() {
			if e.TenantID == p.TenantID {
				n++
			}
		}
		return n
	}, expireKey: func(p tenant.Principal, key string) {
		if err := st.ExpireKey(ctxOf(p), key); err != nil {
			t.Fatal(err)
		}
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
	}, expireKey: func(p tenant.Principal, key string) {
		err := dbx.WithTenantTx(ctxOf(p), pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `update idempotency_keys set expires_at = now() - interval '1 hour' where key = $1`, key)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
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

const (
	otherVendorID = "00000000-0000-0000-0000-0000000000a2"
	otherPOID     = "00000000-0000-0000-0000-0000000000b2"
)

func directReceipt(t *testing.T, st store.Store, p tenant.Principal, slip, serial string, mutate func(*store.Receipt)) store.Receipt {
	t.Helper()
	in := arrival(p, slip)
	if mutate != nil {
		mutate(&in)
	}
	r, err := st.DirectReceipt(ctxOf(p), in, registerWith(serial))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func numbers(rs []store.Receipt) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Number
	}
	return out
}

func wantNumbers(t *testing.T, label string, got []store.Receipt, want ...store.Receipt) {
	t.Helper()
	w := make([]string, len(want))
	for i, r := range want {
		w[i] = r.Number
	}
	if g := numbers(got); strings.Join(g, ",") != strings.Join(w, ",") {
		t.Errorf("%s=%v, want %v", label, g, w)
	}
}

func day(month, d int) time.Time { return time.Date(2026, time.Month(month), d, 0, 0, 0, 0, time.UTC) }

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
		if got := claim("k", "h", time.Minute); got.State != store.ClaimCompleted || got.Status != 201 || strings.ReplaceAll(string(got.Body), " ", "") != `{"id":"r1"}` {
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
	{"list receipts filters, orders newest first and returns one extra row", func(t *testing.T, b backend) {
		p := principal(1)
		r1 := directReceipt(t, b.st, p, "PS-1", "S1", func(r *store.Receipt) { r.Date = day(3, 1) })
		r2 := directReceipt(t, b.st, p, "PS-2", "S2", func(r *store.Receipt) {
			r.Date, r.VendorID, r.PurchaseOrderID = day(3, 10), otherVendorID, otherPOID
		})
		r3, err := b.st.CreateArrival(ctxOf(p), func() store.Receipt { in := arrival(p, "PS-3"); in.Date = day(3, 20); return in }())
		if err != nil {
			t.Fatal(err)
		}
		list := func(f store.ReceiptFilter) []store.Receipt {
			out, err := b.st.ListReceipts(ctxOf(p), f)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		wantNumbers(t, "all", list(store.ReceiptFilter{}), r3, r2, r1)
		wantNumbers(t, "vendor", list(store.ReceiptFilter{VendorID: otherVendorID}), r2)
		wantNumbers(t, "purchase order", list(store.ReceiptFilter{PurchaseOrderID: otherPOID}), r2)
		wantNumbers(t, "arrived", list(store.ReceiptFilter{State: domain.Arrived}), r3)
		wantNumbers(t, "registered", list(store.ReceiptFilter{State: domain.Registered}), r2, r1)
		wantNumbers(t, "from", list(store.ReceiptFilter{From: day(3, 5)}), r3, r2)
		wantNumbers(t, "to", list(store.ReceiptFilter{To: day(3, 15)}), r2, r1)
		wantNumbers(t, "from and to", list(store.ReceiptFilter{From: day(3, 5), To: day(3, 15)}), r2)
		wantNumbers(t, "limit 1", list(store.ReceiptFilter{Limit: 1}), r3, r2)
		wantNumbers(t, "limit above rows", list(store.ReceiptFilter{Limit: 5}), r3, r2, r1)
	}},
	{"list grir filters, totals per currency and paging", func(t *testing.T, b backend) {
		p := principal(1)
		post := func(r store.Receipt) store.Receipt {
			posted, err := b.st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-"+r.Number))
			if err != nil {
				t.Fatal(err)
			}
			return posted
		}
		r1 := post(directReceipt(t, b.st, p, "PS-1", "S1", nil))
		post(directReceipt(t, b.st, p, "PS-2", "S2", func(r *store.Receipt) { r.VendorID, r.PurchaseOrderID = otherVendorID, otherPOID }))
		r3 := post(directReceipt(t, b.st, p, "PS-3", "S3", func(r *store.Receipt) { r.Currency = "EUR" }))
		grir := func(f store.GrirFilter) store.GrirPage {
			page, err := b.st.ListGrir(ctxOf(p), f)
			if err != nil {
				t.Fatal(err)
			}
			return page
		}
		all := grir(store.GrirFilter{})
		if len(all.Items) != 3 || all.Totals.Count != 3 || all.Items[0].ReceiptNumber != r3.Number || all.Items[2].ReceiptNumber != r1.Number {
			t.Fatalf("all=%+v", all)
		}
		want := []store.CurrencyAmount{{Currency: "EUR", Amount: decimal.RequireFromString("7.5")}, {Currency: "USD", Amount: decimal.RequireFromString("15")}}
		if len(all.Totals.OpenAmount) != 2 {
			t.Fatalf("totals=%+v, want one row per currency", all.Totals.OpenAmount)
		}
		for i, w := range want {
			if g := all.Totals.OpenAmount[i]; g.Currency != w.Currency || !g.Amount.Equal(w.Amount) {
				t.Errorf("total[%d]=%+v, want %+v", i, g, w)
			}
		}
		for label, tc := range map[string]struct {
			f     store.GrirFilter
			items int
		}{
			"vendor":         {store.GrirFilter{VendorID: otherVendorID}, 1},
			"purchase order": {store.GrirFilter{PurchaseOrderID: otherPOID}, 1},
			"open status":    {store.GrirFilter{Status: "OPEN"}, 3},
			"cleared status": {store.GrirFilter{Status: "CLEARED"}, 0},
			"age over zero":  {store.GrirFilter{AgeGt: ageOver(0)}, 3},
			"age over one":   {store.GrirFilter{AgeGt: ageOver(1)}, 0},
		} {
			got := grir(tc.f)
			if len(got.Items) != tc.items || got.Totals.Count != tc.items {
				t.Errorf("%s: items=%d count=%d, want %d", label, len(got.Items), got.Totals.Count, tc.items)
			}
		}
		if none := grir(store.GrirFilter{Status: "CLEARED"}); len(none.Totals.OpenAmount) != 0 {
			t.Errorf("empty filter totals=%+v, want none", none.Totals.OpenAmount)
		}
		if page := grir(store.GrirFilter{Limit: 1}); len(page.Items) != 2 || page.Totals.Count != 3 {
			t.Errorf("limit 1: items=%d count=%d, want 2 and 3", len(page.Items), page.Totals.Count)
		}
	}},
	{"direct receipt registers on creation and records its serials", func(t *testing.T, b backend) {
		p := principal(1)
		r := directReceipt(t, b.st, p, "PS-1", "S2", nil)
		if r.State != domain.Registered || r.Number != "RCV-00000001" || len(r.Lines) != 1 || r.Lines[0].LocationID != locationA ||
			!r.Lines[0].Qty.Equal(decimal.NewFromInt(1)) || len(r.Lines[0].Serials) != 1 {
			t.Fatalf("receipt=%+v", r)
		}
		got, err := b.st.GetReceipt(ctxOf(p), r.ID)
		if err != nil || got.State != domain.Registered || got.Number != r.Number || len(got.Lines) != 1 || got.Lines[0].Serials[0] != "S2" {
			t.Fatalf("stored=%+v err=%v", got, err)
		}
		if recorded, err := b.st.RecordedSerials(ctxOf(p), []string{productID}); err != nil || len(recorded) != 1 {
			t.Fatalf("recorded=%v err=%v, want 1", recorded, err)
		}
		if b.events(p) != 0 {
			t.Fatalf("events=%d, want 0 before posting", b.events(p))
		}
	}},
	{"complete and release on a missing key are no-ops", func(t *testing.T, b backend) {
		p := principal(1)
		if err := b.st.CompleteKey(ctxOf(p), "missing", 200, []byte(`{}`)); err != nil {
			t.Fatalf("CompleteKey err=%v", err)
		}
		if err := b.st.ReleaseKey(ctxOf(p), "missing"); err != nil {
			t.Fatalf("ReleaseKey err=%v", err)
		}
		if got, err := b.st.ClaimKey(ctxOf(p), "missing", "h", time.Minute); err != nil || got.State != store.ClaimNew {
			t.Fatalf("claim=%+v err=%v, want new", got, err)
		}
	}},
	{"expired completed key is reclaimed as new and keys are per tenant", func(t *testing.T, b backend) {
		one, two := principal(1), principal(2)
		if _, err := b.st.ClaimKey(ctxOf(one), "k", "h", time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := b.st.CompleteKey(ctxOf(one), "k", 201, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		if got, err := b.st.ClaimKey(ctxOf(two), "k", "other", time.Minute); err != nil || got.State != store.ClaimNew {
			t.Fatalf("other tenant=%+v err=%v, want new", got, err)
		}
		b.expireKey(one, "k")
		if got, err := b.st.ClaimKey(ctxOf(one), "k", "other", time.Minute); err != nil || got.State != store.ClaimNew {
			t.Fatalf("expired=%+v err=%v, want new", got, err)
		}
	}},
	{"ping succeeds on a healthy store", func(t *testing.T, b backend) {
		if err := b.st.Ping(context.Background()); err != nil {
			t.Fatal(err)
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

func ageOver(days int) *int { return &days }
