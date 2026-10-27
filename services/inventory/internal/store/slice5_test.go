package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const consignmentAccount = "2190"

func consignmentParams() store.PostParams {
	p := postParams()
	p.ConsignmentAccount = consignmentAccount
	return p
}

func replenishment(ctx context.Context, product, qty, price string) domain.ConsignmentOrder {
	return domain.ConsignmentOrder{LegalEntityID: legalEntity(ctx), VendorID: "V1", Site: "S1", Warehouse: "W1", Currency: "USD",
		RequestedDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Lines:         []domain.ConsignmentLine{{LineNo: 1, ProductID: product, Unit: "EA", Qty: dec(qty), Price: dec(price)}}}
}

// receiveConsignment creates a replenishment order and receives all of it.
func receiveConsignment(t *testing.T, s *store.Postgres, ctx context.Context, product, qty, price string) domain.ConsignmentOrder {
	t.Helper()
	o, _, err := s.CreateConsignment(ctx, ids.New(), replenishment(ctx, product, qty, price))
	if err != nil {
		t.Fatal(err)
	}
	o, err = s.ReceiveConsignment(ctx, o.ID, o.Version, []domain.ConsignmentReceipt{{LineID: o.Lines[0].ID, Qty: dec(qty)}},
		time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func vendorDims() domain.Dimensions { return dims(domain.DimOwner, "V1") }

// ownerStock returns physical quantity and posted value of the product at one owner ("" = legal entity).
func ownerStock(t *testing.T, s *store.Postgres, ctx context.Context, product, owner string) (decimal.Decimal, decimal.Decimal) {
	t.Helper()
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, GroupBy: []string{"owner"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Rows {
		if r.Group["owner"] == owner {
			return r.PhysicalInvent, r.PostedValue
		}
	}
	return decimal.Zero, decimal.Zero
}

func eventData(t *testing.T, admin *pgxpool.Pool, typ, field, value string, into any) {
	t.Helper()
	var raw []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = $1 and payload->'data'->>`+field+` = $2`, typ, value).
		Scan(&raw); err != nil {
		t.Fatalf("%s with %s=%s: %v", typ, field, value, err)
	}
	var evt struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil || json.Unmarshal(evt.Data, into) != nil {
		t.Fatalf("decode %s: %v %s", typ, err, raw)
	}
}

// AC-16.12: 500 EA vendor-owned stock and an OWNERSHIP_CHANGE of 120 EA at
// 4.00 leave 380 EA owned by V1 at value 0 and 120 EA owned by the legal
// entity at 480.00, with Dr inventory / Cr consignment accrual 480.00 and
// ownership.changed carrying 480.00.
func TestConsignmentReceiptAndOwnershipChange(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	o := receiveConsignment(t, s, ctx, product, "500", "4.00")
	if o.Status != domain.ConsignmentReceived || !o.Lines[0].ReceivedQty.Equal(dec("500")) || o.Version != 2 {
		t.Fatalf("order after receipt: %s received %s version %d", o.Status, o.Lines[0].ReceivedQty, o.Version)
	}
	if qty, value := ownerStock(t, s, ctx, product, "V1"); !qty.Equal(dec("500")) || !value.IsZero() {
		t.Fatalf("vendor-owned stock %s / %s, want 500 at value 0", qty, value)
	}
	var financial *time.Time
	var posted *string
	var status, source string
	if err := admin.QueryRow(context.Background(), `select t.receipt_status, o.source_type, t.financial_date, t.cost_amount_posted::text
		from inventory_transaction t join inventory_transaction_origin o on o.tenant_id = t.tenant_id and o.id = t.origin_id
		where t.product_id = $1`, product).Scan(&status, &source, &financial, &posted); err != nil {
		t.Fatal(err)
	}
	if status != "RECEIVED" || source != "CONSIGNMENT_LINE" || financial != nil || posted != nil {
		t.Fatalf("consignment receipt %s %s financial %v posted %v; want RECEIVED, CONSIGNMENT_LINE, no financial date or value", status, source, financial, posted)
	}

	// Without the accrual account the ownership change cannot post.
	l := line(product, "120", "4.00", vendorDims())
	l.LineNo = 1
	j, err := newJournal(ctx, s, domain.JournalOwnershipChange, l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, postParams()); problemCode(err) != store.CodeAccountMissing {
		t.Fatalf("no consignment account: %v, want %s", err, store.CodeAccountMissing)
	}
	posted2, err := s.PostJournal(ctx, j.ID, j.Version, consignmentParams())
	if err != nil {
		t.Fatal(err)
	}
	if posted2.GLEntry == nil || !posted2.GLEntry.Balanced() || !posted2.GLEntry.Net(inventoryAccount).Equal(dec("480")) ||
		!posted2.GLEntry.Net(consignmentAccount).Equal(dec("-480")) {
		t.Fatalf("GL entry %+v, want Dr %s 480 / Cr %s 480", posted2.GLEntry, inventoryAccount, consignmentAccount)
	}
	if qty, value := ownerStock(t, s, ctx, product, "V1"); !qty.Equal(dec("380")) || !value.IsZero() {
		t.Fatalf("vendor-owned after the change %s / %s, want 380 at 0", qty, value)
	}
	if qty, value := ownerStock(t, s, ctx, product, ""); !qty.Equal(dec("120")) || !value.Equal(dec("480")) {
		t.Fatalf("legal-entity stock %s / %s, want 120 at 480", qty, value)
	}
	vendor, _ := ownerStock(t, s, ctx, product, "V1")
	company, _ := ownerStock(t, s, ctx, product, "")
	if total := vendor.Add(company); !total.Equal(dec("500")) {
		t.Fatalf("physical stock across owners %s, want 500 unchanged (BR-16.14)", total)
	}
	var evt struct {
		VendorID    string `json:"vendorId"`
		TotalAmount string `json:"totalAmount"`
		Lines       []struct {
			Qty          string            `json:"qty"`
			AmountAcct   string            `json:"amountAcct"`
			DimensionSet map[string]string `json:"dimensionSet"`
		} `json:"lines"`
	}
	eventData(t, admin, "dev.passage.inventory.ownership.changed.v1", "'journalId'", j.ID, &evt)
	if evt.VendorID != "V1" || !dec(evt.TotalAmount).Equal(dec("480")) || len(evt.Lines) != 1 || !dec(evt.Lines[0].AmountAcct).Equal(dec("480")) ||
		evt.Lines[0].DimensionSet["owner"] != "" {
		t.Fatalf("ownership.changed %+v, want V1, 480.00, one line without owner", evt)
	}
	// Both legs drill to the journal; the receipt leg has its own origin type.
	rows, err := s.ListTransactions(ctx, store.TransactionFilter{ProductID: product, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	legs := map[string]string{}
	for _, r := range rows {
		if r.JournalID == j.ID {
			legs[r.SourceType] = r.IssueStatus + r.ReceiptStatus
		}
	}
	if legs["INVENT_JOURNAL_LINE"] != "DEDUCTED" || legs["OWNERSHIP_CHANGE"] != "PURCHASED" {
		t.Fatalf("legs %v, want a DEDUCTED journal-line issue and a PURCHASED OWNERSHIP_CHANGE receipt", legs)
	}

	// More than the vendor-owned quantity is INV-OWN-002, whatever the negative policy.
	over := line(product, "381", "4", vendorDims())
	j2, err := newJournal(ctx, s, domain.JournalOwnershipChange, over)
	if err != nil {
		t.Fatal(err)
	}
	allow := consignmentParams()
	allow.Negative = domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	if _, err := s.PostJournal(ctx, j2.ID, j2.Version, allow); problemCode(err) != domain.CodeOwnershipExceeds {
		t.Fatalf("over the vendor-owned quantity: %v, want %s", err, domain.CodeOwnershipExceeds)
	}
	if qty, _ := ownerStock(t, s, ctx, product, "V1"); !qty.Equal(dec("380")) {
		t.Fatalf("a refused change moved stock: %s", qty)
	}
	if rep, err := s.RebuildOnHand(tenantWideCtx(1), false); err != nil || rep.DiffCount != 0 {
		t.Fatalf("projection must rebuild from the ledger: %v %+v", err, rep)
	}
	if _, _, err := s.ReverseJournal(ctx, j.ID, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)); problemCode(err) != domain.CodeJournalNotReady {
		t.Fatalf("reversing an ownership change: %v, want %s", err, domain.CodeJournalNotReady)
	}
}

// BR-16.14: vendor-owned stock is never valued, reserved, blocked or counted
// before an ownership change.
func TestVendorOwnedStockStaysOutOfValuedOperations(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	receiveConsignment(t, s, ctx, product, "50", "2")

	if _, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-1", "0", vendorDims())); problemCode(err) != domain.CodeOwnershipValue {
		t.Fatalf("adjustment of vendor-owned stock: %v, want %s", err, domain.CodeOwnershipValue)
	}
	if _, err := reserve(ctx, s, product, "1", domain.PolicyManual, vendorDims(), "m1"); problemCode(err) != domain.CodeOwnershipValue {
		t.Fatalf("manual reservation of vendor-owned stock: %v, want %s", err, domain.CodeOwnershipValue)
	}
	if _, err := reserve(ctx, s, product, "1", domain.PolicyFIFO, nil, "f1"); problemCode(err) != domain.CodeReserveShortfall {
		t.Fatalf("policy reservation must skip vendor-owned stock: %v, want %s", err, domain.CodeReserveShortfall)
	}
	if _, _, err := s.CreateBlocking(ctx, store.BlockingInput{LegalEntityID: legalEntity(ctx), ProductID: product, Dims: vendorDims(), Qty: dec("1"),
		Target: domain.StockBlocked, Reason: "QUALITY", Unit: "EA", IdempotencyKey: "b1", PhysicalDate: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Fatal("blocking vendor-owned stock must be refused")
	}
	plan := countPlan(t, s, ctx, false)
	sheet, _, err := s.GenerateCountSheet(ctx, plan.ID, "g1")
	if err == nil && len(sheet.Lines) != 0 {
		t.Fatalf("vendor-owned stock must not be on a count sheet: %d lines", len(sheet.Lines))
	}
}

func TestConsignmentIdempotencyReceiptsAndScope(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	in := replenishment(ctx, product, "10", "3")
	o, replayed, err := s.CreateConsignment(ctx, "k1", in)
	if err != nil || replayed || o.Status != domain.ConsignmentCreated || len(o.Number) != 16 || o.Lines[0].Dims["owner"] != "V1" {
		t.Fatalf("create: %v replayed %v %+v", err, replayed, o)
	}
	again, replayed, err := s.CreateConsignment(ctx, "k1", in)
	if err != nil || !replayed || again.ID != o.ID {
		t.Fatalf("replay: %v %v %s", err, replayed, again.ID)
	}
	changed := in
	changed.VendorID = "V2"
	if _, _, err := s.CreateConsignment(ctx, "k1", changed); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	date := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	lineID := o.Lines[0].ID
	if _, err := s.ReceiveConsignment(ctx, o.ID, o.Version+1, []domain.ConsignmentReceipt{{LineID: lineID, Qty: dec("1")}}, date); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v", err)
	}
	o, err = s.ReceiveConsignment(ctx, o.ID, o.Version, []domain.ConsignmentReceipt{{LineID: lineID, Qty: dec("4")}}, date)
	if err != nil || o.Status != domain.ConsignmentPartial {
		t.Fatalf("partial receipt: %v %s", err, o.Status)
	}
	if _, err := s.ReceiveConsignment(ctx, o.ID, o.Version, []domain.ConsignmentReceipt{{LineID: lineID, Qty: dec("7")}}, date); problemCode(err) != domain.CodeConsignmentOver {
		t.Fatalf("over-receipt: %v", err)
	}
	if qty, _ := ownerStock(t, s, ctx, product, "V1"); !qty.Equal(dec("4")) {
		t.Fatalf("vendor-owned %s, want 4 (the refused receipt wrote nothing)", qty)
	}
	stock, err := s.ListConsignmentStock(ctx, "V1")
	if err != nil || len(stock) != 1 || !stock[0].Qty.Equal(dec("4")) || stock[0].Price == nil || !stock[0].Value.Equal(dec("12")) ||
		stock[0].OldestReceipt == nil || !stock[0].OldestReceipt.Equal(date) {
		t.Fatalf("consignment stock: %v %+v", err, stock)
	}
	if other, err := s.ListConsignmentStock(ctx, "V9"); err != nil || len(other) != 0 {
		t.Fatalf("another vendor: %v %+v", err, other)
	}
	list, err := s.ListConsignments(ctx, store.ConsignmentFilter{Status: domain.ConsignmentPartial, VendorID: "V1"})
	if err != nil || len(list) != 1 || len(list[0].Lines) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	otherTenant := entityCtx(2, legalEntity(ctx))
	if _, err := s.GetConsignment(otherTenant, o.ID); problemCode(err) != domain.CodeConsignmentNotFound {
		t.Fatalf("another tenant must not see the order: %v", err)
	}
	if rows, err := s.ListConsignmentStock(otherTenant, ""); err != nil || len(rows) != 0 {
		t.Fatalf("another tenant must not see the stock: %v %d", err, len(rows))
	}
}

// BR-16.11: DRAFT -> SUBMITTED -> APPROVED -> POSTED; SUBMITTED -> REJECTED ->
// DRAFT; DRAFT | REJECTED -> CANCELLED. :validate dry-runs the post.
func TestJournalApprovalWorkflowAndValidate(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2", dims()))

	j, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-4", "0", dims()))
	if err != nil {
		t.Fatal(err)
	}
	events := outboxCount(t, admin, "dev.passage.inventory.transaction.posted.v1")
	if v, err := s.ValidateJournal(ctx, j.ID, postParams()); err != nil || v.ID != j.ID || v.Status != domain.JournalDraft {
		t.Fatalf("validate: %v %+v", err, v)
	}
	if qty, _ := onHandFor(t, s, ctx, product); !qty.Equal(dec("10")) || outboxCount(t, admin, "dev.passage.inventory.transaction.posted.v1") != events {
		t.Fatalf("validate wrote something: on-hand %s", qty)
	}
	if got, _ := s.GetJournal(ctx, j.ID); got.Status != domain.JournalDraft || got.Version != j.Version {
		t.Fatalf("validate changed the journal: %s v%d", got.Status, got.Version)
	}

	sub, err := s.TransitionJournal(ctx, j.ID, j.Version, domain.JournalSubmitted)
	if err != nil || sub.Status != domain.JournalSubmitted || sub.Version != j.Version+1 {
		t.Fatalf("submit: %v %s v%d", err, sub.Status, sub.Version)
	}
	if _, err := s.TransitionJournal(ctx, j.ID, j.Version, domain.JournalApproved); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale approve: %v", err)
	}
	if _, err := s.AddJournalLines(ctx, j.ID, []domain.JournalLine{line(product, "1", "1", dims())}); problemCode(err) != store.CodeJournalState {
		t.Fatalf("lines on a submitted journal: %v", err)
	}
	if _, err := s.PostJournal(ctx, j.ID, sub.Version, postParams()); problemCode(err) != store.CodeJournalState {
		t.Fatalf("posting before approval: %v", err)
	}
	if _, err := s.TransitionJournal(ctx, j.ID, sub.Version, domain.JournalPosted); problemCode(err) != store.CodeJournalState {
		t.Fatalf("POSTED is not a workflow step: %v", err)
	}
	app, err := s.TransitionJournal(ctx, j.ID, sub.Version, domain.JournalApproved)
	if err != nil || app.Status != domain.JournalApproved {
		t.Fatalf("approve: %v %s", err, app.Status)
	}
	if v, err := s.ValidateJournal(ctx, j.ID, postParams()); err != nil || v.Status != domain.JournalApproved {
		t.Fatalf("validate an approved journal: %v", err)
	}
	done, err := s.PostJournal(ctx, j.ID, app.Version, postParams())
	if err != nil || done.Status != domain.JournalPosted {
		t.Fatalf("post approved: %v %s", err, done.Status)
	}
	if _, err := s.ValidateJournal(ctx, j.ID, postParams()); problemCode(err) != store.CodeJournalState {
		t.Fatalf("validate a posted journal: %v", err)
	}
	if _, err := s.TransitionJournal(ctx, j.ID, done.Version, domain.JournalCancelled); problemCode(err) != store.CodeJournalState {
		t.Fatalf("cancel a posted journal: %v", err)
	}

	// Validate reports the post's problem: 7 more is beyond the 6 left.
	short, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-7", "0", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateJournal(ctx, short.ID, postParams()); problemCode(err) != domain.CodeNegativePhysical {
		t.Fatalf("validate beyond stock: %v, want %s", err, domain.CodeNegativePhysical)
	}
	rej, err := s.TransitionJournal(ctx, short.ID, short.Version, domain.JournalSubmitted)
	if err == nil {
		rej, err = s.TransitionJournal(ctx, short.ID, rej.Version, domain.JournalRejected)
	}
	if err != nil || rej.Status != domain.JournalRejected {
		t.Fatalf("reject: %v %s", err, rej.Status)
	}
	if _, err := s.PostJournal(ctx, short.ID, rej.Version, postParams()); problemCode(err) != store.CodeJournalState {
		t.Fatalf("posting a rejected journal: %v", err)
	}
	reopened, err := s.TransitionJournal(ctx, short.ID, rej.Version, domain.JournalDraft)
	if err != nil || reopened.Status != domain.JournalDraft {
		t.Fatalf("reopen: %v %s", err, reopened.Status)
	}
	cancelled, err := s.TransitionJournal(ctx, short.ID, reopened.Version, domain.JournalCancelled)
	if err != nil || cancelled.Status != domain.JournalCancelled {
		t.Fatalf("cancel: %v %s", err, cancelled.Status)
	}
	if qty, _ := onHandFor(t, s, ctx, product); !qty.Equal(dec("6")) {
		t.Fatalf("on-hand %s, want 6", qty)
	}

	empty, _, err := s.CreateJournal(ctx, header(ctx, domain.JournalAdjustment, "USD"), ids.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionJournal(ctx, empty.ID, empty.Version, domain.JournalSubmitted); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("submitting a journal without lines: %v", err)
	}
}

// FR-16.20, BR-16.17: a project demand takes its project's stock, then free
// stock with fallback; free demand never takes project stock.
func TestSegmentAwareReservations(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	p1 := dims(domain.DimSegment, "PROJECT:P1")
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "4", "1", p1), line(product, "5", "1", dims()))

	segmented := func(qty, segment string, fallback bool, key string, d domain.Dimensions, policy domain.ReservationPolicy) (store.ReserveResult, error) {
		return s.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec(qty), DemandType: domain.DemandSalesOrder,
			DemandID: "SO-1", Policy: policy, Dims: d, RequiredDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), IdempotencyKey: key,
			Segment: segment, SegmentFallback: fallback})
	}
	if _, err := segmented("6", "", true, "free", nil, domain.PolicyFIFO); problemCode(err) != domain.CodeReserveShortfall {
		t.Fatalf("free demand of 6 with 5 free: %v, want %s", err, domain.CodeReserveShortfall)
	}
	if _, err := segmented("6", "PROJECT:P1", false, "nofallback", nil, domain.PolicyFIFO); problemCode(err) != domain.CodeReserveShortfall {
		t.Fatalf("project demand without fallback: %v", err)
	}
	res, err := segmented("6", "PROJECT:P1", true, "p1", nil, domain.PolicyFIFO)
	if err != nil || len(res.Reservations) != 2 || res.Reservations[0].Dims[domain.DimSegment] != "PROJECT:P1" || !res.Reservations[0].Qty.Equal(dec("4")) ||
		res.Reservations[1].Dims[domain.DimSegment] != "" || !res.Reservations[1].Qty.Equal(dec("2")) || res.Reservations[0].DemandSegment != "PROJECT:P1" {
		t.Fatalf("project demand: %v %+v", err, res.Reservations)
	}
	if _, err := segmented("6", "PROJECT:P2", true, "p1", nil, domain.PolicyFIFO); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other segment: %v", err)
	}
	if _, err := segmented("1", "PROJECT:P2", true, "manual", p1, domain.PolicyManual); problemCode(err) != domain.CodeSegment {
		t.Fatalf("manual P1 stock for a P2 demand: %v, want %s", err, domain.CodeSegment)
	}
	if _, err := segmented("1", "", true, "filter", p1, domain.PolicyFIFO); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("segment as a policy filter: %v", err)
	}
	list, err := s.ListReservations(ctx, store.ReservationFilter{ProductID: product})
	if err != nil || len(list) != 2 || list[0].DemandSegment != "PROJECT:P1" {
		t.Fatalf("inquiry: %v %+v", err, list)
	}
	res2, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, Segment: "PROJECT:P1"})
	if err != nil || len(res2.Rows) != 1 || !res2.Rows[0].PhysicalInvent.Equal(dec("4")) || !res2.Rows[0].AvailPhysical.IsZero() {
		t.Fatalf("on-hand of the project segment: %v %+v", err, res2.Rows)
	}
}
