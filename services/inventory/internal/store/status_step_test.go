package store_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/events"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// register creates a transaction for a fresh external origin at status.
func register(ctx context.Context, s *store.Postgres, sourceType, product, qty, cost string, status domain.Status, d domain.Dimensions) (store.StatusChange, error) {
	q := dec(qty)
	change, _, err := s.CreateTransaction(ctx, store.CreateTransactionInput{LegalEntityID: legalEntity(ctx), SourceType: sourceType,
		SourceID: ids.New(), Reference: "DOC-1", ProductID: product, Dims: d, Qty: q, EnteredQty: q, EnteredUnit: "EA", Status: status,
		Currency: "USD", CostPrice: dec(cost), Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), IdempotencyKey: ids.New()})
	return change, err
}

// step applies a command under a fresh key; STEP needs from and to.
func step(ctx context.Context, s *store.Postgres, txn string, cmd domain.StepCommand, from, to domain.Status) (store.StatusChange, error) {
	c, _, err := s.ApplyStatusStep(ctx, store.StatusStepInput{TransactionID: txn, Command: cmd, From: from, To: to,
		Date: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), IdempotencyKey: ids.New()})
	return c, err
}

// bucket reads one projection column of a product (all rows summed).
func bucket(t *testing.T, admin *pgxpool.Pool, product, column string) decimal.Decimal {
	t.Helper()
	var v string
	if err := admin.QueryRow(context.Background(), `select coalesce(sum(`+column+`), 0)::text from on_hand
		where tenant_id = $1 and product_id = $2`, testkit.TenantID(1), product).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return dec(v)
}

func wantCode(t *testing.T, what string, err error, status int, code string) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != code || p.Status != status {
		t.Fatalf("%s: got %v, want %d %s", what, err, status, code)
	}
}

// cleanRebuild asserts the projection equals a rebuild from the transactions (FR-16.3).
func cleanRebuild(t *testing.T, s *store.Postgres) {
	t.Helper()
	rep, err := s.RebuildOnHand(tenantWideCtx(1), false)
	if err != nil || rep.DiffCount != 0 {
		t.Fatalf("rebuild after status changes: %v %+v", err, rep)
	}
}

// AC-16.8 (FR-16.2): a receipt at REGISTERED is unregistered back to ARRIVED;
// the registered bucket drops by its quantity, arrived rises, and both the
// registration and the move leave an audit row. A replay returns the same row,
// a key reused for another move is refused, and the command no longer applies.
func TestUnregisterReturnsAReceiptToArrived(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	created, err := register(ctx, s, "RECEIPT_LINE", product, "5", "2", domain.StatusRegistered, dims())
	if err != nil {
		t.Fatal(err)
	}
	if created.Command != domain.CommandCreate || created.From != "" || created.To != domain.StatusRegistered {
		t.Fatalf("registration audit %+v", created)
	}
	if got := bucket(t, admin, product, "registered"); !got.Equal(dec("5")) {
		t.Fatalf("registered %s, want 5", got)
	}
	if physical, _ := onHandFor(t, s, ctx, product); !physical.Equal(dec("5")) {
		t.Fatalf("physical %s after registration, want 5 (REGISTERED is physical)", physical)
	}

	in := store.StatusStepInput{TransactionID: created.TransactionID, Command: domain.CommandUnregister, IdempotencyKey: "unreg-1"}
	moved, applied, err := s.ApplyStatusStep(ctx, in)
	if err != nil || !applied || moved.From != domain.StatusRegistered || moved.To != domain.StatusArrived || !moved.Qty.Equal(dec("5")) {
		t.Fatalf("unregister: applied=%v change=%+v err=%v", applied, moved, err)
	}
	if got := bucket(t, admin, product, "registered"); !got.IsZero() {
		t.Fatalf("registered %s after unregister, want 0", got)
	}
	if got := bucket(t, admin, product, "arrived"); !got.Equal(dec("5")) {
		t.Fatalf("arrived %s after unregister, want 5", got)
	}
	if physical, _ := onHandFor(t, s, ctx, product); !physical.IsZero() {
		t.Fatalf("physical %s after unregister, want 0 (ARRIVED is expected stock)", physical)
	}

	again, applied, err := s.ApplyStatusStep(ctx, in)
	if err != nil || applied || again.ID != moved.ID {
		t.Fatalf("replay: applied=%v id=%s want %s err=%v", applied, again.ID, moved.ID, err)
	}
	reused := in
	reused.Command = domain.CommandUnpick
	_, _, err = s.ApplyStatusStep(ctx, reused)
	wantCode(t, "key reused for another command", err, http.StatusUnprocessableEntity, "idempotency_key_reuse")
	_, err = step(ctx, s, created.TransactionID, domain.CommandUnregister, "", "")
	wantCode(t, "unregister at ARRIVED", err, http.StatusUnprocessableEntity, domain.CodeStatusStep)

	txn, history, err := s.GetTransaction(ctx, created.TransactionID)
	if err != nil || txn.ReceiptStatus != "ARRIVED" || txn.SourceType != "RECEIPT_LINE" || txn.Reference != "DOC-1" {
		t.Fatalf("detail: %v %+v", err, txn)
	}
	if len(history) != 2 || history[0].Command != domain.CommandCreate || history[1].Command != domain.CommandUnregister ||
		history[1].From != domain.StatusRegistered || history[1].Date != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("history %+v", history)
	}
	var data events.TransactionPostedData
	eventData(t, admin, events.TransactionPosted, "'statusChangeId'", moved.ID, &data)
	if data.PreviousStatus == nil || *data.PreviousStatus != "REGISTERED" || data.ReceiptStatus == nil || *data.ReceiptStatus != "ARRIVED" ||
		data.SourceType != "RECEIPT_LINE" {
		t.Fatalf("transaction.posted of the move: %+v", data)
	}
	cleanRebuild(t, s)
}

// FR-16.2 / BR-16.1 for receipts through transactions:apply: registration at
// ORDERED, forward steps with skips, the stale-read guard, the financial step
// that sets the posted value and financial date, and no way back.
func TestReceiptStepsForwardToPurchased(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	created, err := register(ctx, s, "PURCH_LINE", product, "4", "3", domain.StatusOrdered, dims())
	if err != nil {
		t.Fatal(err)
	}
	if got := bucket(t, admin, product, "ordered"); !got.Equal(dec("4")) {
		t.Fatalf("ordered %s, want 4", got)
	}
	txn := created.TransactionID

	_, err = step(ctx, s, txn, domain.CommandStep, domain.StatusArrived, domain.StatusReceived)
	wantCode(t, "stale from", err, http.StatusPreconditionFailed, "conflict_version")
	if _, err := step(ctx, s, txn, domain.CommandStep, domain.StatusOrdered, domain.StatusReceived); err != nil {
		t.Fatalf("skip ORDERED to RECEIVED: %v", err)
	}
	if got := bucket(t, admin, product, "received"); !got.Equal(dec("4")) {
		t.Fatalf("received %s, want 4", got)
	}
	_, err = step(ctx, s, txn, domain.CommandStep, domain.StatusReceived, domain.StatusRegistered)
	wantCode(t, "backward under STEP", err, http.StatusUnprocessableEntity, domain.CodeStatusStep)

	cost := dec("13.20")
	if _, _, err := s.ApplyStatusStep(ctx, store.StatusStepInput{TransactionID: txn, Command: domain.CommandStep, From: domain.StatusReceived,
		To: domain.StatusPurchased, Date: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), CostAmount: &cost, IdempotencyKey: ids.New()}); err != nil {
		t.Fatalf("financial step: %v", err)
	}
	physical, value := onHandFor(t, s, ctx, product)
	if !physical.Equal(dec("4")) || !value.Equal(cost) {
		t.Fatalf("after PURCHASED: physical %s value %s, want 4 and 13.20", physical, value)
	}
	rows, err := s.ListTransactions(ctx, store.TransactionFilter{ID: txn})
	if err != nil || len(rows) != 1 || rows[0].FinancialDate != "2026-09-30" || rows[0].CostAmountPosted == nil || !rows[0].CostAmountPosted.Equal(cost) {
		t.Fatalf("transaction after PURCHASED: %v %+v", err, rows)
	}
	_, err = step(ctx, s, txn, domain.CommandUnregister, "", "")
	wantCode(t, "unregister a purchased receipt", err, http.StatusUnprocessableEntity, domain.CodeStatusStep)
	reconcile(t, admin)
	cleanRebuild(t, s)
}

// The issue side: a sales issue reserved, picked, unpicked, picked again and
// sold at the running average; CancelReservation applies only to reserved
// stock; the negative policy and the one-transaction-per-origin rule hold.
func TestIssueUnpickAndSellAtRunningAverage(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2", dims()))

	created, err := register(ctx, s, "SALES_LINE", product, "-4", "0", domain.StatusReservedPhysical, dims())
	if err != nil {
		t.Fatal(err)
	}
	avail, _ := availFor(t, s, ctx, product)
	if !avail.Equal(dec("6")) {
		t.Fatalf("available %s with 4 reserved, want 6", avail)
	}
	txn := created.TransactionID
	if _, err := step(ctx, s, txn, domain.CommandStep, domain.StatusReservedPhysical, domain.StatusPicked); err != nil {
		t.Fatal(err)
	}
	if physical, _ := onHandFor(t, s, ctx, product); !physical.Equal(dec("6")) {
		t.Fatalf("physical %s with 4 picked, want 6", physical)
	}
	_, err = step(ctx, s, txn, domain.CommandCancelReservation, "", "")
	wantCode(t, "cancel reservation of picked stock", err, http.StatusUnprocessableEntity, domain.CodeStatusStep)
	unpicked, err := step(ctx, s, txn, domain.CommandUnpick, "", "")
	if err != nil || unpicked.To != domain.StatusReservedPhysical {
		t.Fatalf("unpick: %v %+v", err, unpicked)
	}
	if got := bucket(t, admin, product, "picked"); !got.IsZero() {
		t.Fatalf("picked %s after unpick", got)
	}
	if got := bucket(t, admin, product, "reserved_physical"); !got.Equal(dec("4")) {
		t.Fatalf("reserved %s after unpick, want 4", got)
	}
	if _, err := step(ctx, s, txn, domain.CommandStep, domain.StatusReservedPhysical, domain.StatusSold); err != nil {
		t.Fatalf("sell: %v", err)
	}
	physical, value := onHandFor(t, s, ctx, product)
	if !physical.Equal(dec("6")) || !value.Equal(dec("12")) {
		t.Fatalf("after SOLD: physical %s value %s, want 6 and 12 (4 issued at the 2.00 average)", physical, value)
	}

	_, err = register(ctx, s, "SALES_LINE", product, "-7", "0", domain.StatusPicked, dims())
	wantCode(t, "issue beyond stock", err, http.StatusUnprocessableEntity, domain.CodeNegativePhysical)

	cancelled, err := register(ctx, s, "PROD_LINE", product, "-1", "0", domain.StatusReservedPhysical, dims())
	if err != nil {
		t.Fatal(err)
	}
	if c, err := step(ctx, s, cancelled.TransactionID, domain.CommandCancelReservation, "", ""); err != nil || c.To != domain.StatusOnOrder {
		t.Fatalf("cancel reservation: %v %+v", err, c)
	}

	source := ids.New()
	first := store.CreateTransactionInput{LegalEntityID: legalEntity(ctx), SourceType: "SALES_LINE", SourceID: source, ProductID: product,
		Dims: dims(), Qty: dec("-1"), EnteredQty: dec("-1"), EnteredUnit: "EA", Status: domain.StatusOnOrder, Currency: "USD", IdempotencyKey: ids.New()}
	if _, _, err := s.CreateTransaction(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.IdempotencyKey = ids.New()
	_, _, err = s.CreateTransaction(ctx, second)
	wantCode(t, "second registration of one origin", err, http.StatusConflict, domain.CodeStatusOrigin)

	// Some transactions stay at expected or physical statuses, so the check is
	// the status-aware rebuild rather than the posted-sum reconcile.
	cleanRebuild(t, s)
	page, err := s.ListStatusChanges(ctx, store.StatusChangeFilter{TransactionID: txn})
	if err != nil || len(page) != 4 || page[0].To != domain.StatusSold || page[3].Command != domain.CommandCreate {
		t.Fatalf("status changes of the issue, newest first: %v %+v", err, page)
	}
}

// BR-16.14: vendor-owned stock is registered without value and never reaches
// a financial status; registrations validate their status and dimensions.
func TestStatusStepGuards(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	owned, err := register(ctx, s, "RECEIPT_LINE", product, "3", "5", domain.StatusReceived, vendorDims())
	if err != nil {
		t.Fatal(err)
	}
	if physical, value := ownerStock(t, s, ctx, product, "V1"); !physical.Equal(dec("3")) || !value.IsZero() {
		t.Fatalf("vendor-owned registration: physical %s value %s", physical, value)
	}
	_, err = step(ctx, s, owned.TransactionID, domain.CommandStep, domain.StatusReceived, domain.StatusPurchased)
	wantCode(t, "vendor-owned to PURCHASED", err, http.StatusUnprocessableEntity, domain.CodeOwnershipValue)

	_, err = register(ctx, s, "PURCH_LINE", product, "3", "1", domain.StatusPurchased, dims())
	wantCode(t, "registration at a financial status", err, http.StatusUnprocessableEntity, domain.CodeStatusStep)
	_, err = register(ctx, s, "PURCH_LINE", product, "3", "1", domain.StatusOnOrder, dims())
	wantCode(t, "receipt at an issue status", err, http.StatusUnprocessableEntity, domain.CodeStatusStep)
	_, err = register(ctx, s, "PURCH_LINE", product, "3", "1", domain.StatusOrdered, domain.Dimensions{domain.DimWarehouse: "W1"})
	wantCode(t, "registration without site", err, http.StatusUnprocessableEntity, domain.CodeDimensionMandatory)
	_, err = register(ctx, s, "INVENT_JOURNAL_LINE", product, "3", "1", domain.StatusOrdered, dims())
	wantCode(t, "the service's own origin type", err, http.StatusBadRequest, domain.CodeInvalid)
	_, err = step(ctx, s, ids.New(), domain.CommandUnpick, "", "")
	wantCode(t, "unknown transaction", err, http.StatusNotFound, domain.CodeTransactionNotFound)
}

// A transaction id that is not a UUID is not found (the lookup compares as uuid).
func TestGetTransactionWithAMalformedIDIsNotFound(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	created, err := register(ctx, s, "PURCH_LINE", ids.New(), "2", "1", domain.StatusOrdered, dims())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.GetTransaction(ctx, "not-a-uuid")
	wantCode(t, "malformed id", err, http.StatusNotFound, domain.CodeTransactionNotFound)
	if txn, _, err := s.GetTransaction(ctx, created.TransactionID); err != nil || txn.ID != created.TransactionID {
		t.Fatalf("lookup by uuid: %v %+v", err, txn)
	}
}

// Status changes are tenant and legal-entity scoped: another tenant or legal
// entity cannot read or move the transaction.
func TestStatusStepsAreTenantIsolated(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	created, err := register(ctx, s, "PURCH_LINE", ids.New(), "2", "1", domain.StatusOrdered, dims())
	if err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]context.Context{"other tenant": entityCtx(2, legalEntity(ctx)), "other legal entity": entityCtx(1, ids.New())} {
		_, _, err := s.GetTransaction(other, created.TransactionID)
		wantCode(t, name+" reads", err, http.StatusNotFound, domain.CodeTransactionNotFound)
		_, err = step(other, s, created.TransactionID, domain.CommandStep, domain.StatusOrdered, domain.StatusArrived)
		wantCode(t, name+" moves", err, http.StatusNotFound, domain.CodeTransactionNotFound)
		if page, err := s.ListStatusChanges(other, store.StatusChangeFilter{}); err != nil || len(page) != 0 {
			t.Fatalf("%s lists %d status changes: %v", name, len(page), err)
		}
	}
}
