package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

const importCustomer = "00000000-0000-0000-0000-0000000000d1"

func openingRow(key, kind, side, original, open string) OpeningItemInput {
	return OpeningItemInput{Row: domain.OpeningRow{LegacyKey: key, DocumentNumber: "L-" + key, CustomerID: importCustomer, Kind: kind, Side: side,
		DocumentDate: "2026-09-01", DueDate: "2026-10-01", Currency: "USD", OriginalAmount: amt(original), OpenAmount: amt(open)}}
}

func openingBatch(items ...OpeningItemInput) OpeningImport {
	return OpeningImport{LegalEntityID: legalEntity, ImportSource: "SAP-PRD", Items: items}
}

func outcomes(r OpeningImportResult) string {
	var parts []string
	for _, x := range r.Results {
		parts = append(parts, x.Outcome+":"+x.Code)
	}
	return strings.Join(parts, " ")
}

func TestOpeningImport_CreatesDebitsAndCreditsAndIsRerunnable(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	batch := openingBatch(
		openingRow("1", "INVOICE", "DEBIT", "1000.00", "1000.00"),
		openingRow("2", "INVOICE", "DEBIT", "800.00", "300.00"), // partly paid in the legacy system
		openingRow("3", "CREDIT_NOTE", "CREDIT", "200.00", "200.00"),
		openingRow("4", "PAYMENT", "CREDIT", "50.00", "50.00"))
	first, err := s.ImportOpeningItems(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Committed || first.Created != 4 || first.Rejected != 0 {
		t.Fatalf("first: %+v", first)
	}
	if n := countOutbox(t, s, ctx, rcvevents.OpenItemsImported); n != 1 {
		t.Fatalf("outbox events = %d, want 1", n)
	}
	credit := itemByID(t, s, ctx, *first.Results[2].OpenItemID)
	if credit.Side != "CREDIT" || !credit.Amount.Equal(amt("-200")) || !credit.OpenAmount.Equal(amt("-200")) || credit.Status != "OPEN" {
		t.Fatalf("credit note stored as %+v", credit)
	}
	partly := itemByID(t, s, ctx, *first.Results[1].OpenItemID)
	if partly.Status != "PARTIALLY_SETTLED" || !partly.OpenAmount.Equal(amt("300")) || *partly.ImportSource != "SAP-PRD" || *partly.LegacyKey != "2" {
		t.Fatalf("legacy partial payment stored as %+v", partly)
	}

	// Unchanged rerun: same ids, nothing written, no new event.
	again, err := s.ImportOpeningItems(ctx, batch)
	if err != nil || again.Unchanged != 4 || again.Created != 0 || again.Updated != 0 {
		t.Fatalf("rerun: %+v %v", again, err)
	}
	if *again.Results[0].OpenItemID != *first.Results[0].OpenItemID {
		t.Fatal("a rerun must find the item it loaded before, not duplicate it")
	}
	if n := countOutbox(t, s, ctx, rcvevents.OpenItemsImported); n != 1 {
		t.Fatalf("an unchanged rerun published an event: %d", n)
	}

	// A later snapshot: the legacy open amount fell, the credit note was consumed (now smaller).
	batch.Items[0] = openingRow("1", "INVOICE", "DEBIT", "1000.00", "600.00")
	batch.Items[2] = openingRow("3", "CREDIT_NOTE", "CREDIT", "200.00", "120.00")
	third, err := s.ImportOpeningItems(ctx, batch)
	if err != nil || third.Updated != 2 || third.Unchanged != 2 {
		t.Fatalf("update: %+v %v", third, err)
	}
	inv := itemByID(t, s, ctx, *first.Results[0].OpenItemID)
	if !inv.OpenAmount.Equal(amt("600")) || inv.Status != "PARTIALLY_SETTLED" || inv.Version != 2 {
		t.Fatalf("updated invoice = %+v", inv)
	}
	if n := countOutbox(t, s, ctx, rcvevents.OpenItemsImported); n != 2 {
		t.Fatalf("events = %d, want 2", n)
	}

	var count int
	for _, r := range []string{"SAP-PRD", "OTHER"} {
		items, err := s.ListOpenItems(ctx, OpenItemFilter{ImportSource: r, OpenOnly: true}, 100)
		if err != nil {
			t.Fatal(err)
		}
		count += len(items)
		if r == "OTHER" && len(items) != 0 {
			t.Fatalf("filter leaked %d items", len(items))
		}
	}
	if count != 4 {
		t.Fatalf("imported items listed = %d", count)
	}
	sums, err := s.ListOpeningImports(ctx, 10)
	if err != nil || len(sums) != 1 {
		t.Fatalf("summary: %+v %v", sums, err)
	}
	// 600 + 300 debit, -120 - 50 credit.
	if sums[0].ItemCount != 4 || !sums[0].DebitOpen.Equal(amt("900")) || !sums[0].CreditOpen.Equal(amt("-170")) || !sums[0].NetOpen.Equal(amt("730")) {
		t.Fatalf("summary = %+v", sums[0])
	}
}

func TestOpeningImport_RefusesItemsSettledInPassage(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	batch := openingBatch(openingRow("1", "INVOICE", "DEBIT", "1000.00", "1000.00"), openingRow("2", "INVOICE", "DEBIT", "500.00", "500.00"),
		openingRow("3", "INVOICE", "DEBIT", "100.00", "100.00"))
	first, err := s.ImportOpeningItems(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	cust := importCustomer
	pay := newPayment(t, s, ctx, &cust, "1000.00", "USD", "imp-pay")
	// Pay item 1 in full and item 2 in part.
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{
		{OpenItemID: *first.Results[0].OpenItemID, Amount: amt("700.00")},
		{OpenItemID: *first.Results[1].OpenItemID, Amount: amt("300.00")}}, Residual: decimal.Zero}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Item 1 becomes partly settled too.
	again, err := s.ImportOpeningItems(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if again.Rejected != 2 || again.Unchanged != 1 || again.Committed != true {
		t.Fatalf("rerun after settlement: %s", outcomes(again))
	}
	for _, i := range []int{0, 1} {
		r := again.Results[i]
		if r.Code != domain.CodeOpeningSettled || !strings.Contains(r.Detail, "L-") {
			t.Errorf("row %d: %+v", i, r)
		}
	}
	// The item was not overwritten.
	if got := itemByID(t, s, ctx, *first.Results[1].OpenItemID); !got.OpenAmount.Equal(amt("200")) {
		t.Fatalf("settled item overwritten: %+v", got)
	}
}

// A payment applied and later undone leaves the open amount where the import put
// it, but the item was still worked in Passage: a changed snapshot is refused.
func TestOpeningImport_RefusesItemsOncePaidEvenIfUndone(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	first, err := s.ImportOpeningItems(ctx, openingBatch(openingRow("1", "INVOICE", "DEBIT", "500.00", "500.00")))
	if err != nil {
		t.Fatal(err)
	}
	id := *first.Results[0].OpenItemID
	cust := importCustomer
	pay := newPayment(t, s, ctx, &cust, "200.00", "USD", "imp-undone")
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: id, Amount: amt("200.00")}},
		Residual: decimal.Zero}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Stand-in for a reversal: the item is fully open again, the settlement row stays.
	execTenant(t, s, ctx, `update open_item set open_amount = amount, status = 'OPEN' where id = $1`, id)

	res, err := s.ImportOpeningItems(ctx, openingBatch(openingRow("1", "INVOICE", "DEBIT", "500.00", "350.00")))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Results[0]; r.Code != domain.CodeOpeningSettled || !strings.Contains(r.Detail, "paid, cleared or deducted") {
		t.Fatalf("rerun on a once-paid item: %+v", r)
	}
	if got := itemByID(t, s, ctx, id); !got.OpenAmount.Equal(amt("500")) {
		t.Fatalf("once-paid item overwritten: %+v", got)
	}
}

// An item a collector is working (an active dispute, a promise to pay that still
// holds) is not overwritten by a changed snapshot; a row that matches it is
// still UNCHANGED, so a full rerun reports only the real conflicts.
func TestOpeningImport_RefusesItemsInCollection(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	batch := openingBatch(openingRow("1", "INVOICE", "DEBIT", "400.00", "400.00"), openingRow("2", "INVOICE", "DEBIT", "300.00", "300.00"),
		openingRow("3", "INVOICE", "DEBIT", "200.00", "200.00"))
	first, err := s.ImportOpeningItems(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDispute(ctx, Dispute{LegalEntityID: legalEntity, OpenItemID: *first.Results[0].OpenItemID, ReasonCode: "PRICE",
		DisputedAmount: amt("100.00"), Owner: "ana"}, "imp-dsp"); err != nil {
		t.Fatalf("dispute: %v", err)
	}
	if _, err := s.CreatePromise(ctx, Promise{LegalEntityID: legalEntity, OpenItemID: *first.Results[1].OpenItemID, Amount: amt("300.00"),
		PromiseDate: time.Now().UTC().AddDate(0, 0, 10).Format(dateLayout)}, "imp-prm"); err != nil {
		t.Fatalf("promise: %v", err)
	}

	same, err := s.ImportOpeningItems(ctx, batch)
	if err != nil || same.Unchanged != 3 {
		t.Fatalf("identical rerun: %s (%v)", outcomes(same), err)
	}

	batch.Items[0] = openingRow("1", "INVOICE", "DEBIT", "400.00", "250.00")
	batch.Items[1] = openingRow("2", "INVOICE", "DEBIT", "300.00", "150.00")
	batch.Items[2] = openingRow("3", "INVOICE", "DEBIT", "200.00", "50.00")
	res, err := s.ImportOpeningItems(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(res); got != "REJECTED:"+domain.CodeOpeningInCollection+" REJECTED:"+domain.CodeOpeningInCollection+" UPDATED:" {
		t.Fatalf("changed rerun: %s", got)
	}
	if !strings.Contains(res.Results[0].Detail, "dispute") || !strings.Contains(res.Results[1].Detail, "promise") {
		t.Errorf("details: %q / %q", res.Results[0].Detail, res.Results[1].Detail)
	}
	if got := itemByID(t, s, ctx, *first.Results[0].OpenItemID); !got.OpenAmount.Equal(amt("400")) {
		t.Fatalf("disputed item overwritten: %+v", got)
	}
}

// The side of a loaded key cannot change: an ADJUSTMENT loaded as a debit that a
// later snapshot sends as a credit is refused, not flipped.
func TestOpeningImport_RefusesASideChange(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	first, err := s.ImportOpeningItems(ctx, openingBatch(openingRow("1", "ADJUSTMENT", "DEBIT", "75.00", "75.00")))
	if err != nil || first.Created != 1 {
		t.Fatalf("load: %s (%v)", outcomes(first), err)
	}
	res, err := s.ImportOpeningItems(ctx, openingBatch(openingRow("1", "ADJUSTMENT", "CREDIT", "75.00", "75.00")))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Results[0]; r.Code != domain.CodeOpeningIdentity || !strings.Contains(r.Detail, "side") {
		t.Fatalf("side change: %+v", r)
	}
	if got := itemByID(t, s, ctx, *first.Results[0].OpenItemID); got.Side != "DEBIT" || !got.Amount.Equal(amt("75")) {
		t.Fatalf("side flipped: %+v", got)
	}
}

// A legacy key already loaded into another legal entity is an identity change,
// whether or not the caller can see that legal entity.
func TestOpeningImport_LegacyKeyInAnotherLegalEntity(t *testing.T) {
	s := setup(t)
	const otherEntity = "00000000-0000-0000-0000-0000000000b2"
	if _, err := s.ImportOpeningItems(tenantCtx(1), openingBatch(openingRow("1", "INVOICE", "DEBIT", "100.00", "100.00"))); err != nil {
		t.Fatal(err)
	}
	moved := openingRow("1", "INVOICE", "DEBIT", "100.00", "100.00")
	moved.Row.DocumentNumber = "OTHER-1"
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: otherEntity})
	for name, ctx := range map[string]context.Context{"all-scope caller": tenantCtx(1), "caller scoped to the other entity": scoped} {
		batch := openingBatch(moved)
		batch.LegalEntityID = otherEntity
		res, err := s.ImportOpeningItems(ctx, batch)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r := res.Results[0]; r.Code != domain.CodeOpeningIdentity {
			t.Errorf("%s: %+v, want %s", name, r, domain.CodeOpeningIdentity)
		}
	}
}

func TestOpeningImport_PerRowErrorsAndAtomicMode(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	if _, err := s.ImportOpeningItems(ctx, openingBatch(openingRow("1", "INVOICE", "DEBIT", "100.00", "100.00"))); err != nil {
		t.Fatal(err)
	}
	clash := openingRow("2", "INVOICE", "DEBIT", "10.00", "10.00")
	clash.Row.DocumentNumber = "L-1" // taken by legacy key 1
	identity := openingRow("1", "INVOICE", "DEBIT", "100.00", "100.00")
	identity.Row.Currency = "EUR"
	rejected := OpeningItemInput{Row: openingRow("5", "INVOICE", "DEBIT", "1.00", "1.00").Row, RejectCode: domain.CodeOpeningInvalid, RejectDetail: "currency must be an ISO code"}
	good := openingRow("3", "CREDIT_NOTE", "CREDIT", "30.00", "30.00")

	res, err := s.ImportOpeningItems(ctx, openingBatch(clash, identity, rejected, good))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.Rejected != 3 || !res.Committed {
		t.Fatalf("per-row: %s", outcomes(res))
	}
	want := []string{domain.CodeOpeningDocumentExists, domain.CodeOpeningIdentity, domain.CodeOpeningInvalid, ""}
	for i, w := range want {
		if res.Results[i].Code != w {
			t.Errorf("row %d code = %q, want %q", i, res.Results[i].Code, w)
		}
	}

	// The clash left the batch's good row stored, and the duplicate document was not created.
	items, _ := s.ListOpenItems(ctx, OpenItemFilter{ImportSource: "SAP-PRD"}, 100)
	if len(items) != 2 {
		t.Fatalf("stored %d items, want 2", len(items))
	}
	// GetOpenItem reads one back by id (the migration loader's reconciliation read); an unknown id is not found.
	if got, err := s.GetOpenItem(ctx, items[0].ID); err != nil || got.ID != items[0].ID || got.DocumentNumber != items[0].DocumentNumber || !got.OpenAmount.Equal(items[0].OpenAmount) {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetOpenItem(ctx, "00000000-0000-4000-8000-000000000999"); err == nil || !strings.Contains(err.Error(), "open item not found") {
		t.Fatalf("unknown id: %v", err)
	}

	before := countOutbox(t, s, ctx, rcvevents.OpenItemsImported)
	atomic := openingBatch(openingRow("7", "INVOICE", "DEBIT", "9.00", "9.00"), clash)
	atomic.Atomic = true
	out, err := s.ImportOpeningItems(ctx, atomic)
	if err != nil {
		t.Fatal(err)
	}
	if out.Committed || out.Rejected != 2 || out.Created != 0 {
		t.Fatalf("atomic: %s", outcomes(out))
	}
	if out.Results[0].Code != domain.CodeOpeningRejected || out.Results[1].Code != domain.CodeOpeningDocumentExists {
		t.Fatalf("atomic codes: %s", outcomes(out))
	}
	items, _ = s.ListOpenItems(ctx, OpenItemFilter{ImportSource: "SAP-PRD"}, 100)
	if len(items) != 2 || countOutbox(t, s, ctx, rcvevents.OpenItemsImported) != before {
		t.Fatalf("an atomic refusal stored something: %d items", len(items))
	}
}

func TestOpeningImport_CreditItemsStayOutOfMatchingDunningAndSettlement(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	res, err := s.ImportOpeningItems(ctx, openingBatch(openingRow("1", "INVOICE", "DEBIT", "100.00", "100.00"), openingRow("2", "CREDIT_NOTE", "CREDIT", "40.00", "40.00")))
	if err != nil {
		t.Fatal(err)
	}
	cust := importCustomer
	pay := newPayment(t, s, ctx, &cust, "100.00", "USD", "imp-credit")
	match, err := s.MatchPayment(ctx, pay.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range match.Top {
		for _, id := range c.ItemIDs {
			if id == *res.Results[1].OpenItemID {
				t.Fatal("a credit note was offered as a match candidate")
			}
		}
	}
	_, err = s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: *res.Results[1].OpenItemID, Amount: amt("40.00")}}}, "")
	if problemCode(err) != domain.CodeCreditItem {
		t.Fatalf("applying a payment to a credit: %v", err)
	}
	items, err := loadDunnableForTest(t, s, ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("dunnable items = %d (%v), want only the invoice", len(items), err)
	}
	rows, err := s.AgingRows(ctx)
	if err != nil || len(rows) != 1 || !rows[0].OpenAmount.Equal(amt("60")) {
		t.Fatalf("aging nets credits: %+v %v", rows, err)
	}
}

func TestOpeningImport_OtherTenantSeesNothing(t *testing.T) {
	s := setup(t)
	if _, err := s.ImportOpeningItems(tenantCtx(1), openingBatch(openingRow("1", "INVOICE", "DEBIT", "100.00", "100.00"))); err != nil {
		t.Fatal(err)
	}
	// Tenant 2 loads the same legacy key: a separate item, not a collision.
	res, err := s.ImportOpeningItems(tenantCtx(2), openingBatch(openingRow("1", "INVOICE", "DEBIT", "100.00", "100.00")))
	if err != nil || res.Created != 1 {
		t.Fatalf("tenant 2: %+v %v", res, err)
	}
	if sums, _ := s.ListOpeningImports(tenantCtx(2), 10); len(sums) != 1 || sums[0].ItemCount != 1 {
		t.Fatalf("tenant 2 summary: %+v", sums)
	}
}

func loadDunnableForTest(t *testing.T, s *Postgres, ctx context.Context) ([]domain.DunnableItem, error) {
	t.Helper()
	var out []domain.DunnableItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadDunnable(ctx, tx, legalEntity)
		return err
	})
	return out, err
}

// Credits net against debits (FR-30.1): the worklist lists the customer with a
// debit, net of its credits, and the balance snapshot also stores a customer that
// holds only credits, net in credit, and reports it in customerbalance.changed.v1.
func TestOpeningImport_CreditsNetInWorklistAndSnapshot(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	const creditOnly = "00000000-0000-0000-0000-0000000000d2"
	onlyCredit := openingRow("3", "PAYMENT", "CREDIT", "40.00", "40.00")
	onlyCredit.Row.CustomerID = creditOnly
	if _, err := s.ImportOpeningItems(ctx, openingBatch(openingRow("1", "INVOICE", "DEBIT", "100.00", "100.00"),
		openingRow("2", "CREDIT_NOTE", "CREDIT", "150.00", "150.00"), onlyCredit)); err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) // a month after the rows' 2026-10-01 due date

	rows, err := s.Worklist(ctx, legalEntity, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CustomerID != importCustomer || !rows[0].TotalOpen.Equal(amt("-50")) || !rows[0].Overdue.IsZero() || rows[0].RiskScore != 0 {
		t.Fatalf("worklist = %+v, want only %s at -50 with nothing overdue", rows, importCustomer)
	}

	run, err := s.RunAgingSnapshot(ctx, legalEntity, asOf)
	if err != nil || run.Rows != 2 || run.Changed != 2 {
		t.Fatalf("snapshot = %+v (%v), want 2 rows and 2 events", run, err)
	}
	snaps, err := s.ListBalanceSnapshots(ctx, SnapshotFilter{LegalEntityID: legalEntity}, 10)
	if err != nil || len(snaps) != 2 {
		t.Fatalf("snapshots = %+v (%v)", snaps, err)
	}
	for _, b := range snaps {
		want := map[string]string{importCustomer: "-50", creditOnly: "-40"}[b.CustomerID]
		if !b.TotalOpen.Equal(amt(want)) || !b.Overdue.IsZero() {
			t.Errorf("snapshot %s = %s open, %s overdue; want %s and 0", b.CustomerID, b.TotalOpen, b.Overdue, want)
		}
	}
	if data := latestBalanceEvent(t, s, ctx, creditOnly); !data.Balance.Equal(amt("-40")) || !data.Overdue.IsZero() {
		t.Fatalf("event for the customer in credit = %+v, want balance -40", data)
	}
}
