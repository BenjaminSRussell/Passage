package iccharge_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/payables/internal/iccharge"
	"github.com/BenjaminSRussell/depot/services/payables/internal/ledger"
	"github.com/BenjaminSRussell/depot/services/payables/internal/store"
	"github.com/BenjaminSRussell/depot/services/payables/migrations"
)

// publishedSource is what intercompany's enqueue passes to events.New (services/intercompany/internal/events.Source).
const publishedSource = "/depot/intercompany"

// event builds the event the way intercompany's store enqueues it: events.New with the given source.
func event(t *testing.T, data map[string]any, source string) events.Event {
	t.Helper()
	evt, err := events.New(iccharge.ChargeInvoiceRequestedType, source, ids.New(), testkit.TenantID(1), data)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func consumerCtx(t *testing.T) context.Context {
	t.Helper()
	sp, err := tenant.ServicePrincipal(iccharge.ConsumerName, testkit.TenantID(1), "")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), sp)
}

// #40: an IC charge in Invoice mode becomes one intake row in the borrower's legal entity for the pairing's vendor, once.
func TestHandle_TakesTheBorrowersVendorInvoiceInOnce(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	le, vendor, charge := ids.New(), ids.New(), ids.New()
	data := map[string]any{"id": charge, "sourceId": "TS-2", "version": 1, "lender": "A", "borrower": "B", "category": "Hour", "transactionDate": "2026-11-05",
		"currency": "USD", "price": "550.00", "tax": "104.50", "borrowerLegalEntityId": le, "vendorAccount": vendor}
	first, err := iccharge.Handle(consumerCtx(t), s, nil, event(t, data, publishedSource))
	if err != nil {
		t.Fatal(err)
	}
	if first.LegalEntityID != le || first.VendorID != vendor || first.Source != "INTERCOMPANY" || first.InvoiceNumber != "IC-TS-2-v1" ||
		first.Net.String() != "550" || first.Gross.String() != "654.5" || first.ReceivedBy != "service:payables-iccharge" {
		t.Fatalf("intake %+v", first)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, nil, event(t, data, publishedSource))
	if err != nil || again.ID != first.ID {
		t.Fatalf("redelivery = %s, %v; want %s", again.ID, err, first.ID)
	}
	data["price"] = "600.00"
	if _, err := iccharge.Handle(consumerCtx(t), s, nil, event(t, data, publishedSource)); err == nil {
		t.Fatal("the same charge version with another price is refused")
	}
}

func TestHandle_RefusesWhatCannotBeTakenIn(t *testing.T) {
	good := map[string]any{"id": ids.New(), "version": 1, "lender": "A", "borrower": "B", "transactionDate": "2026-11-05", "currency": "USD", "price": "10",
		"borrowerLegalEntityId": ids.New(), "vendorAccount": ids.New()}
	for name, mut := range map[string]func(map[string]any){
		"no legal entity": func(d map[string]any) { delete(d, "borrowerLegalEntityId") },
		"no vendor":       func(d map[string]any) { d["vendorAccount"] = "V-1" },
		"bad tax":         func(d map[string]any) { d["tax"] = "-1" },
	} {
		d := map[string]any{}
		for k, v := range good {
			d[k] = v
		}
		mut(d)
		if _, err := iccharge.Handle(consumerCtx(t), nil, nil, event(t, d, publishedSource)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := iccharge.Handle(consumerCtx(t), nil, nil, event(t, good, "/depot/billing")); err == nil {
		t.Error("another source is refused")
	}
}

// fakeLedger keeps the entries it is asked to post, one per idempotency key, and refuses an unbalanced one the way the ledger does,
// and any date up to closedThrough as a closed period.
type fakeLedger struct {
	entries       map[string]ledger.Entry
	ids           map[string]string
	calls         int
	fail          error
	closedThrough string
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{entries: map[string]ledger.Entry{}, ids: map[string]string{}}
}

func (f *fakeLedger) Post(_ context.Context, e ledger.Entry) (string, error) {
	f.calls++
	if f.fail != nil {
		return "", f.fail
	}
	if f.closedThrough != "" && e.PostingDate <= f.closedThrough {
		return "", &ledger.StatusError{Status: 422, Code: "LEDGER_PERIOD_CLOSED"}
	}
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range e.Lines {
		if l.Credit {
			credit = credit.Add(l.Amount)
		} else {
			debit = debit.Add(l.Amount)
		}
	}
	if !debit.Equal(credit) {
		return "", &ledger.StatusError{Status: 422, Code: "LED-0001"}
	}
	if id, ok := f.ids[e.IdempotencyKey]; ok {
		return id, nil
	}
	id := ids.New()
	f.ids[e.IdempotencyKey], f.entries[e.IdempotencyKey] = id, e
	return id, nil
}

func autoCharge(le, vendor string) map[string]any {
	return map[string]any{"id": ids.New(), "sourceId": "TS-9", "version": 1, "lender": "A", "borrower": "B", "category": "Hour", "transactionDate": "2026-11-05",
		"currency": "USD", "price": "550.00", "tax": "104.50", "borrowerLegalEntityId": le, "vendorAccount": vendor, "autoPostInvoice": true}
}

// autoFor is the auto-post setup: the ledger and each borrower legal entity's own ledger and accounts, like its posting profile.
func autoFor(led *fakeLedger, entities map[string]iccharge.EntityProfile) *iccharge.AutoPost {
	return &iccharge.AutoPost{Ledger: led, PaymentTermDays: 30, Now: func() time.Time { return time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC) },
		Profiles: func(le string) (iccharge.EntityProfile, bool) { p, ok := entities[le]; return p, ok }}
}

func profileFor(ledgerID string) iccharge.EntityProfile {
	return iccharge.EntityProfile{LedgerID: ledgerID, ExpenseAccount: "6200", InputTaxAccount: "1400", ControlAccount: "2100"}
}

func migratedStore(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(app), admin
}

// #40: a pairing that opted in has its vendor invoice posted: worked example 550.00 net + 104.50 input tax = 654.50 payable. The
// entry balances (Dr 6200 550.00, Dr 1400 104.50, Cr 2100 654.50) in the borrower's own ledger, the AP item is for 654.50, the intake
// row is POSTED, and a redelivery posts and opens nothing new.
func TestHandle_AutoPostsTheVendorInvoiceOnceWhenThePairingOptedIn(t *testing.T) {
	s, admin := migratedStore(t)
	led := newFakeLedger()
	le, vendor := ids.New(), ids.New()
	auto := autoFor(led, map[string]iccharge.EntityProfile{le: profileFor("L-B")})
	data := autoCharge(le, vendor)

	first, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "POSTED" || first.LedgerEntryID == "" || first.Gross.String() != "654.5" || first.Net.String() != "550" || first.DecidedBy != "service:payables-iccharge" {
		t.Fatalf("intake %+v", first)
	}
	if led.calls != 1 || len(led.entries) != 1 {
		t.Fatalf("ledger calls = %d entries = %d, want one", led.calls, len(led.entries))
	}
	e := led.entries["post:payables:iccharge:"+data["id"].(string)+"-1:post"]
	got := map[string]string{}
	for _, l := range e.Lines {
		side := "D"
		if l.Credit {
			side = "C"
		}
		got[side+" "+l.Account] = l.Amount.String()
	}
	if len(got) != 3 || got["D 6200"] != "550" || got["D 1400"] != "104.5" || got["C 2100"] != "654.5" ||
		e.LegalEntityID != le || e.LedgerID != "L-B" || e.Currency != "USD" || e.PostingDate != "2026-11-05" || e.DocumentID != first.ID {
		t.Fatalf("entry %+v lines %v", e, got)
	}
	var items int
	var amount string
	if err := admin.QueryRow(context.Background(), `select count(*), coalesce(max(open_amount::text), '') from ap_open_item where source_invoice_id = $1`, first.ID).Scan(&items, &amount); err != nil {
		t.Fatal(err)
	}
	if items != 1 || amount != "654.500000" {
		t.Fatalf("open items = %d amount %s", items, amount)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource))
	if err != nil || again.ID != first.ID || again.Status != "POSTED" || again.LedgerEntryID != first.LedgerEntryID || led.calls != 1 {
		t.Fatalf("redelivery = %+v, %v, ledger calls %d", again, err, led.calls)
	}
}

// M2: two borrower entities post into their own ledgers and accounts; an entity with no profile is left for a clerk with the reason.
func TestHandle_AutoPostUsesTheBorrowersOwnProfile(t *testing.T) {
	s, _ := migratedStore(t)
	led := newFakeLedger()
	b1, b2, none := ids.New(), ids.New(), ids.New()
	other := profileFor("L-B2")
	other.ExpenseAccount, other.ControlAccount = "6900", "2900"
	auto := autoFor(led, map[string]iccharge.EntityProfile{b1: profileFor("L-B1"), b2: other})
	for _, le := range []string{b1, b2} {
		d := autoCharge(le, ids.New())
		d["sourceId"] = "S-" + le[:4]
		if _, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, d, publishedSource)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]string{}
	for _, e := range led.entries {
		seen[e.LegalEntityID] = e.LedgerID + "/" + e.Lines[0].Account
	}
	if seen[b1] != "L-B1/6200" || seen[b2] != "L-B2/6900" {
		t.Fatalf("entries by entity = %v", seen)
	}
	d := autoCharge(none, ids.New())
	d["sourceId"] = "S-NONE"
	row, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, d, publishedSource))
	if err != nil || row.Status != "RECEIVED" || !strings.Contains(row.AutoPostNote, "posting profile") {
		t.Fatalf("entity without a profile = %+v, %v; want RECEIVED with the reason", row, err)
	}
}

// M1: a charge dated in a closed period posts on today, the charge date kept in the line text and as the invoice date; when today is
// closed too the row is parked, not dead-lettered.
func TestHandle_AutoPostMovesPastAClosedPeriod(t *testing.T) {
	s, _ := migratedStore(t)
	led := newFakeLedger()
	led.closedThrough = "2026-10-31"
	le := ids.New()
	auto := autoFor(led, map[string]iccharge.EntityProfile{le: profileFor("L-B")})
	d := autoCharge(le, ids.New())
	d["transactionDate"] = "2026-10-30"
	row, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, d, publishedSource))
	if err != nil || row.Status != "POSTED" || row.InvoiceDate != "2026-10-30" {
		t.Fatalf("closed October = %+v, %v", row, err)
	}
	e := led.entries["post:payables:iccharge:"+d["id"].(string)+"-1:post"]
	if e.PostingDate != "2026-11-15" || !strings.Contains(e.Lines[0].Text, "dated 2026-10-30") {
		t.Fatalf("entry date %s text %q; want today with the charge date", e.PostingDate, e.Lines[0].Text)
	}
	led.closedThrough = "2026-12-31"
	d2 := autoCharge(le, ids.New())
	d2["sourceId"] = "S-CLOSED"
	parked, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, d2, publishedSource))
	if err != nil || parked.Status != "RECEIVED" || !strings.Contains(parked.AutoPostNote, "period is closed") {
		t.Fatalf("every period closed = %+v, %v; want parked on the row", parked, err)
	}
}

// A ledger that is down is retried; one that refuses the entry parks the row with its reason and nothing goes to the dead letter queue.
func TestHandle_AutoPostRetriesAnOutageAndParksARefusal(t *testing.T) {
	s, _ := migratedStore(t)
	led := newFakeLedger()
	le := ids.New()
	auto := autoFor(led, map[string]iccharge.EntityProfile{le: profileFor("L-B")})
	data := autoCharge(le, ids.New())

	led.fail = errors.New("ledger: connection refused")
	in, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource))
	if err == nil || eventing.IsPermanent(err) || in.Status != "RECEIVED" {
		t.Fatalf("ledger down = %+v, %v: want a retryable error and the row kept as RECEIVED", in, err)
	}
	led.fail = nil
	done, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource))
	if err != nil || done.ID != in.ID || done.Status != "POSTED" || len(led.entries) != 1 {
		t.Fatalf("retry = %+v, %v, entries %d", done, err, len(led.entries))
	}
	other := autoCharge(le, ids.New())
	other["sourceId"] = "TS-10"
	led.fail = &ledger.StatusError{Status: 404, Code: "LEDGER_NOT_FOUND"}
	kept, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, other, publishedSource))
	if err != nil || kept.Status != "RECEIVED" || !strings.Contains(kept.AutoPostNote, "LEDGER_NOT_FOUND") {
		t.Fatalf("refused entry = %+v, %v; want parked", kept, err)
	}
}

// M3: manual review is the default; an unconfigured deployment, or a missing signing key, parks the row instead of failing it; a row a
// clerk already posted (or rejected) is never auto-posted by a replay.
func TestHandle_ReviewIsTheDefaultAndAClerksRowIsNeverAutoPosted(t *testing.T) {
	s, admin := migratedStore(t)
	led := newFakeLedger()
	le := ids.New()
	auto := autoFor(led, map[string]iccharge.EntityProfile{le: profileFor("L-B")})

	review := autoCharge(le, ids.New())
	delete(review, "autoPostInvoice")
	in, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, review, publishedSource))
	if err != nil || in.Status != "RECEIVED" || in.AutoPostNote != "" || led.calls != 0 {
		t.Fatalf("default = %+v, %v, ledger calls %d", in, err, led.calls)
	}

	unconfigured := autoCharge(le, ids.New())
	unconfigured["sourceId"] = "TS-11"
	kept, err := iccharge.Handle(consumerCtx(t), s, nil, event(t, unconfigured, publishedSource))
	if err != nil || kept.Status != "RECEIVED" || kept.AutoPostNote == "" {
		t.Fatalf("no settings = %+v, %v; want parked with a reason", kept, err)
	}

	// A clerk posts the parked row by hand (batch D's decision: POSTED with the vendor invoice); replaying the event changes nothing.
	vendorInvoice := ids.New()
	if _, err := admin.Exec(context.Background(), `update ap_vendor_invoice_intake set status = 'POSTED', decided_at = now(), decided_by = 'clerk', vendor_invoice_id = $2 where id = $1`, kept.ID, vendorInvoice); err != nil {
		t.Fatal(err)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, unconfigured, publishedSource))
	if err != nil || again.Status != "POSTED" || again.LedgerEntryID != "" || led.calls != 0 {
		t.Fatalf("replay after a clerk posted = %+v, %v, ledger calls %d; want nothing posted again", again, err, led.calls)
	}

	noTaxAccount := profileFor("L-B")
	noTaxAccount.InputTaxAccount = ""
	taxed := autoCharge(le, ids.New())
	taxed["sourceId"] = "TS-12"
	row, err := iccharge.Handle(consumerCtx(t), s, autoFor(led, map[string]iccharge.EntityProfile{le: noTaxAccount}), event(t, taxed, publishedSource))
	if err != nil || row.Status != "RECEIVED" || !strings.Contains(row.AutoPostNote, "input tax") || led.calls != 0 {
		t.Fatalf("tax without an input tax account = %+v, %v", row, err)
	}
}

// H1: version 2 of a charge whose version 1 is posted is never auto-posted: it stays RECEIVED with the reason, so the lender is not
// paid twice for one charge while a clerk reverses version 1.
func TestHandle_AVersionAfterAPostedOneIsLeftForAClerk(t *testing.T) {
	s, _ := migratedStore(t)
	led := newFakeLedger()
	le := ids.New()
	auto := autoFor(led, map[string]iccharge.EntityProfile{le: profileFor("L-B")})
	v1 := autoCharge(le, ids.New())
	first, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, v1, publishedSource))
	if err != nil || first.Status != "POSTED" {
		t.Fatalf("version 1 = %+v, %v", first, err)
	}
	v2 := map[string]any{}
	for k, v := range v1 {
		v2[k] = v
	}
	v2["version"], v2["price"], v2["tax"] = 2, "500.00", "95.00"
	second, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, v2, publishedSource))
	if err != nil || second.ID == first.ID || second.Status != "RECEIVED" || second.Gross.String() != "595" || !strings.Contains(second.AutoPostNote, "earlier version") {
		t.Fatalf("version 2 = %+v, %v; want RECEIVED with the reason", second, err)
	}
	if led.calls != 1 {
		t.Fatalf("ledger calls = %d, want only version 1's", led.calls)
	}
}

// L1: a ledger client that cannot sign is refused before anything is posted.
func TestAutoPostValidate_NeedsASignedLedger(t *testing.T) {
	led := &unsignedLedger{}
	if err := (&iccharge.AutoPost{Ledger: led, Profiles: func(string) (iccharge.EntityProfile, bool) { return iccharge.EntityProfile{}, false }}).Validate(); err == nil {
		t.Fatal("an unsigned ledger client was accepted")
	}
}

type unsignedLedger struct{}

func (unsignedLedger) Post(context.Context, ledger.Entry) (string, error) { return "", nil }
func (unsignedLedger) Signed() bool                                       { return false }

// #40: the auto-posted entry names the lender's legal entity as the trading partner on the cost and control lines, not on tax, so
// the consolidation reads the borrower's payable per counterparty. A lender that is the borrower's own entity names none.
func TestHandle_AutoPostNamesTheLenderAsTradingPartner(t *testing.T) {
	s, _ := migratedStore(t)
	led := newFakeLedger()
	le, vendor, lender := ids.New(), ids.New(), ids.New()
	auto := autoFor(led, map[string]iccharge.EntityProfile{le: profileFor("L-B")})
	data := autoCharge(le, vendor)
	data["lenderLegalEntityId"] = lender
	if _, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource)); err != nil {
		t.Fatal(err)
	}
	e := led.entries["post:payables:iccharge:"+data["id"].(string)+"-1:post"]
	got := map[string]string{}
	for _, l := range e.Lines {
		got[l.Account] = l.TradingPartnerID
	}
	if got["6200"] != lender || got["2100"] != lender || got["1400"] != "" {
		t.Fatalf("partners by account %v, want cost and control lines to name %s and input tax none", got, lender)
	}
	if p := iccharge.PartnerOf(iccharge.Data{BorrowerLegalEntityID: le, LenderLegalEntityID: le}); p != "" {
		t.Fatalf("a lender that is the borrower's own entity must name no partner, got %q", p)
	}
}

// #40: the open item of an auto-posted charge keeps the lender as its trading partner, for the payment run; a charge left to review opens
// nothing, so the clerk's invoice and its payment stay untagged.
func TestHandle_AutoPostedOpenItemKeepsThePartnerForThePaymentRun(t *testing.T) {
	s, admin := migratedStore(t)
	led := newFakeLedger()
	le, vendor, lender := ids.New(), ids.New(), ids.New()
	auto := autoFor(led, map[string]iccharge.EntityProfile{le: profileFor("L-B")})
	data := autoCharge(le, vendor)
	data["lenderLegalEntityId"] = lender
	first, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource))
	if err != nil || first.Status != "POSTED" {
		t.Fatalf("auto-post: %+v %v", first, err)
	}
	var partner *string
	if err := admin.QueryRow(context.Background(), `select trading_partner_id::text from ap_open_item where source_invoice_id = $1`, first.ID).Scan(&partner); err != nil {
		t.Fatal(err)
	}
	if partner == nil || *partner != lender {
		t.Fatalf("open item partner = %v, want %s", partner, lender)
	}
	// Review mode: the row waits for a clerk and no open item exists, so nothing is tagged.
	review := autoCharge(le, ids.New())
	review["autoPostInvoice"], review["lenderLegalEntityId"] = false, lender
	row, err := iccharge.Handle(consumerCtx(t), s, nil, event(t, review, publishedSource))
	if err != nil || row.Status != "RECEIVED" {
		t.Fatalf("review: %+v %v", row, err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from ap_open_item where source_invoice_id = $1`, row.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("review mode opened %d items: %v", n, err)
	}
}
