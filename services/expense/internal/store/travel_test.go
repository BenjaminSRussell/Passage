package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/expense/internal/events"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
)

// as returns ctx with the same tenant and legal entity scope but another gateway user.
func as(ctx context.Context, user string) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.UserID = user
	return tenant.With(context.Background(), p)
}

var (
	policy  = store.AdvancePolicy{MaxAmount: decimal.RequireFromString("2000.00"), DueDays: 30}
	tripEnd = time.Date(2025, 3, 10, 0, 0, 0, 0, time.UTC)
)

func newRequisition(le, worker string) store.NewRequisition {
	return store.NewRequisition{LegalEntityID: le, WorkerID: worker, Purpose: "Customer workshop", Currency: "USD",
		TripFrom: time.Date(2025, 3, 3, 0, 0, 0, 0, time.UTC), TripTo: tripEnd,
		Destinations: []store.Destination{{Country: "US", City: "Boston"}},
		Estimates: []store.Estimate{{Category: "LODGING", Amount: decimal.RequireFromString("1200.00")},
			{Category: "AIRFARE", Amount: decimal.RequireFromString("800.00")}}}
}

func cmd(key string, version int) store.Command {
	return store.Command{Key: key, Hash: "h-" + key, ExpectedVersion: version}
}

// FR-43.16: a requisition is created DRAFT with the summed estimate, submitted, and
// decided by someone other than its requester; every step replays by key.
func TestPostgres_RequisitionLifecycleWithOwnItemsBlocked(t *testing.T) {
	s, base, le, _ := setup(t, 21)
	requester, approver := as(base, "u-requester"), as(base, "u-approver")
	worker := ids.New()

	rq, replayed, err := s.CreateRequisition(requester, "rq-1", "h1", newRequisition(le, worker))
	if err != nil || replayed || rq.Status != "DRAFT" || !rq.Estimated.Equal(decimal.NewFromInt(2000)) || rq.Version != 1 {
		t.Fatalf("create = %+v replayed=%v err=%v; want DRAFT with estimated 2000 at version 1", rq, replayed, err)
	}
	if !rq.TripTo.Equal(tripEnd) || len(rq.Destinations) != 1 || rq.Estimates[1].Category != "AIRFARE" {
		t.Fatalf("stored requisition = %+v; want dates, destinations and estimates as given", rq)
	}
	if again, replayed, err := s.CreateRequisition(requester, "rq-1", "h1", newRequisition(le, worker)); err != nil || !replayed || again.ID != rq.ID {
		t.Fatalf("replay = %v %v; want the original", replayed, err)
	}
	if _, _, err := s.CreateRequisition(requester, "rq-1", "other", newRequisition(le, worker)); problemOf(t, err).Code != store.CodeIdempotencyReuse {
		t.Fatalf("reused key must be %s", store.CodeIdempotencyReuse)
	}
	if _, _, err := s.CreateRequisition(requester, "rq-2", "h2", newRequisition(ids.New(), worker)); problemOf(t, err).Code != store.CodeOutOfScope {
		t.Fatalf("another legal entity must be %s", store.CodeOutOfScope)
	}

	if _, _, err := s.DecideRequisition(approver, rq.ID, true, cmd("early", 0)); problemOf(t, err).Code != store.CodeRequisitionState {
		t.Fatalf("deciding a DRAFT must be %s", store.CodeRequisitionState)
	}
	if _, _, err := s.SubmitRequisition(requester, rq.ID, cmd("submit-stale", 9)); problemOf(t, err).Code != store.CodeRequisitionVersion {
		t.Fatalf("stale If-Match must be %s", store.CodeRequisitionVersion)
	}
	sub, _, err := s.SubmitRequisition(requester, rq.ID, cmd("submit", 1))
	if err != nil || sub.Status != "SUBMITTED" || sub.Version != 2 || sub.SubmittedAt == nil {
		t.Fatalf("submit = %+v %v; want SUBMITTED at version 2", sub, err)
	}
	if again, replayed, err := s.SubmitRequisition(requester, rq.ID, cmd("submit", 1)); err != nil || !replayed || again.Version != 2 {
		t.Fatalf("submit replay = %v %v %+v; want a replay that changes nothing", replayed, err, again)
	}

	if _, _, err := s.DecideRequisition(requester, rq.ID, true, cmd("self", 2)); problemOf(t, err).Code != store.CodeOwnDecision {
		t.Fatalf("the requester deciding must be %s", store.CodeOwnDecision)
	}
	if p := problemOf(t, func() error { _, _, err := s.DecideRequisition(base, rq.ID, true, cmd("anon", 2)); return err }()); p.Status != http.StatusUnauthorized {
		t.Fatalf("a decision without a user = %d; want 401", p.Status)
	}
	ok, _, err := s.DecideRequisition(approver, rq.ID, true, cmd("approve", 2))
	if err != nil || ok.Status != "APPROVED" || ok.DecidedBy != "u-approver" || ok.DecidedAt == nil {
		t.Fatalf("approve = %+v %v; want APPROVED by u-approver", ok, err)
	}
	if _, _, err := s.DecideRequisition(approver, rq.ID, false, cmd("reject-late", 0)); problemOf(t, err).Code != store.CodeRequisitionState {
		t.Fatalf("deciding an APPROVED requisition again must be %s", store.CodeRequisitionState)
	}

	page, next, err := s.ListRequisitions(base, store.RequisitionFilter{WorkerID: worker, Status: "APPROVED"}, "", 10)
	if err != nil || len(page) != 1 || next != "" {
		t.Fatalf("list = %d next %q %v; want the one approved requisition", len(page), next, err)
	}
}

// FR-43.15 / AC-43.9: request, approve, pay through AP (event in the outbox once),
// return the open balance in two steps to SETTLED, never more than is open.
func TestPostgres_AdvanceLifecyclePaysOnceAndSettlesByReturn(t *testing.T) {
	s, base, le, pool := setup(t, 22)
	requester, approver := as(base, "u-requester"), as(base, "u-approver")
	worker := ids.New()
	asOf := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	in := store.NewAdvance{LegalEntityID: le, WorkerID: worker, Amount: decimal.RequireFromString("1000.00"), Currency: "USD", Purpose: "Trip", TripEnd: tripEnd}

	over := in
	over.Amount = decimal.RequireFromString("2000.01")
	if _, _, err := s.RequestAdvance(requester, "over", "h", over, policy, asOf); problemOf(t, err).Code != store.CodeAdvanceLimit {
		t.Fatalf("2000.01 over a 2000.00 limit must be %s", store.CodeAdvanceLimit)
	}
	adv, _, err := s.RequestAdvance(requester, "adv", "h", in, policy, asOf)
	if err != nil || adv.Status != "SUBMITTED" || !adv.DueDate.Equal(tripEnd.AddDate(0, 0, 30)) || adv.RequestedBy != "u-requester" {
		t.Fatalf("request = %+v %v; want SUBMITTED, due trip end + 30, requested by u-requester", adv, err)
	}
	if _, _, err := s.PayAdvance(base, adv.ID, ids.New(), cmd("pay-early", 0)); problemOf(t, err).Code != store.CodeAdvanceState {
		t.Fatalf("paying an unapproved advance must be %s", store.CodeAdvanceState)
	}
	if _, _, err := s.DecideAdvance(requester, adv.ID, true, cmd("self", 0)); problemOf(t, err).Code != store.CodeOwnDecision {
		t.Fatalf("the requester approving must be %s", store.CodeOwnDecision)
	}
	if _, _, err := s.DecideAdvance(approver, adv.ID, true, cmd("stale", 7)); problemOf(t, err).Code != store.CodeAdvanceVersion {
		t.Fatalf("stale If-Match must be %s", store.CodeAdvanceVersion)
	}
	if adv, _, err = s.DecideAdvance(approver, adv.ID, true, cmd("approve", 1)); err != nil || adv.Status != "APPROVED" {
		t.Fatalf("approve = %+v %v", adv, err)
	}
	prepay := ids.New()
	if adv, _, err = s.PayAdvance(base, adv.ID, prepay, cmd("pay", 0)); err != nil || adv.Status != "PAID" || !adv.Paid.Equal(adv.Amount) || adv.APPrepaymentID != prepay {
		t.Fatalf("pay = %+v %v; want PAID in full with the prepayment", adv, err)
	}
	if _, replayed, err := s.PayAdvance(base, adv.ID, prepay, cmd("pay", 0)); err != nil || !replayed {
		t.Fatalf("pay replay = %v %v; want a replay", replayed, err)
	}
	if got := outboxTypes(t, base, pool); len(got) != 1 || got[0] != events.CashAdvancePaid {
		t.Fatalf("outbox = %v; want one %s", got, events.CashAdvancePaid)
	}

	if _, _, err := s.ReturnAdvance(base, adv.ID, decimal.RequireFromString("1000.01"), cmd("too-much", 0)); problemOf(t, err).Code != store.CodeAdvanceReturn {
		t.Fatalf("returning more than is open must be %s", store.CodeAdvanceReturn)
	}
	adv, _, err = s.ReturnAdvance(base, adv.ID, decimal.RequireFromString("820.00"), cmd("ret-1", 0))
	if err != nil || adv.Status != "PARTIALLY_SETTLED" || !adv.Domain().Open().Equal(decimal.NewFromInt(180)) {
		t.Fatalf("return 820 = %+v %v; want PARTIALLY_SETTLED with 180.00 open", adv, err)
	}
	adv, _, err = s.ReturnAdvance(base, adv.ID, decimal.RequireFromString("180.00"), cmd("ret-2", 0))
	if err != nil || adv.Status != "SETTLED" || !adv.Domain().Open().IsZero() {
		t.Fatalf("return 180 = %+v %v; want SETTLED with nothing open", adv, err)
	}
	if _, _, err := s.ReturnAdvance(base, adv.ID, decimal.RequireFromString("0.01"), cmd("ret-3", 0)); problemOf(t, err).Code != store.CodeAdvanceReturn {
		t.Fatalf("a return on a settled advance must be %s", store.CodeAdvanceReturn)
	}
}

// EXP-ADV-002: a PAID advance still open after its due date blocks the worker's next
// request; the due date itself is still in time. A requisition of another worker
// cannot carry the advance (EXP-ADV-004).
func TestPostgres_OverdueAdvanceBlocksNewRequests(t *testing.T) {
	s, base, le, _ := setup(t, 23)
	approver := as(base, "u-approver")
	worker := ids.New()
	in := store.NewAdvance{LegalEntityID: le, WorkerID: worker, Amount: decimal.RequireFromString("500.00"), Currency: "USD", Purpose: "Trip", TripEnd: tripEnd}
	adv, _, err := s.RequestAdvance(base, "first", "h", in, policy, tripEnd)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DecideAdvance(approver, adv.ID, true, cmd("approve", 0)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PayAdvance(base, adv.ID, ids.New(), cmd("pay", 0)); err != nil {
		t.Fatal(err)
	}
	due := tripEnd.AddDate(0, 0, 30)
	if _, _, err := s.RequestAdvance(base, "on-due-date", "h", in, policy, due); err != nil {
		t.Fatalf("on the due date the worker is not blocked yet: %v", err)
	}
	if _, _, err := s.RequestAdvance(base, "after-due", "h", in, policy, due.AddDate(0, 0, 1)); problemOf(t, err).Code != store.CodeAdvanceBlocked {
		t.Fatalf("after the due date the request must be %s", store.CodeAdvanceBlocked)
	}
	if _, _, err := s.RequestAdvance(base, "other-worker", "h", store.NewAdvance{LegalEntityID: le, WorkerID: ids.New(), Amount: in.Amount,
		Currency: "USD", Purpose: "Trip", TripEnd: tripEnd}, policy, due.AddDate(0, 0, 1)); err != nil {
		t.Fatalf("another worker is not blocked: %v", err)
	}

	rq, _, err := s.CreateRequisition(base, "rq", "h", newRequisition(le, ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	linked := store.NewAdvance{LegalEntityID: le, WorkerID: ids.New(), Amount: in.Amount, Currency: "USD", Purpose: "Trip", TripEnd: tripEnd, TravelRequisitionID: rq.ID}
	if _, _, err := s.RequestAdvance(base, "wrong-worker", "h", linked, policy, tripEnd); problemOf(t, err).Code != store.CodeAdvanceRequisition {
		t.Fatalf("another worker's requisition must be %s", store.CodeAdvanceRequisition)
	}
	linked.WorkerID = rq.WorkerID
	if got, _, err := s.RequestAdvance(base, "linked", "h", linked, policy, tripEnd); err != nil || got.TravelRequisitionID != rq.ID {
		t.Fatalf("linked request = %+v %v; want the requisition linked", got, err)
	}
	linked.TravelRequisitionID = ids.New()
	if _, _, err := s.RequestAdvance(base, "missing", "h", linked, policy, tripEnd); problemOf(t, err).Code != store.CodeRequisitionNotFound {
		t.Fatalf("an unknown requisition must be %s", store.CodeRequisitionNotFound)
	}

	// A caller scoped to two legal entities cannot carry one entity's requisition on
	// the other entity's advance.
	p, _ := tenant.FromContext(base)
	other := ids.New()
	p.LegalEntityID = le + "," + other
	both := tenant.With(context.Background(), p)
	crossed := linked
	crossed.TravelRequisitionID, crossed.LegalEntityID = rq.ID, other
	if _, _, err := s.RequestAdvance(both, "other-entity", "h", crossed, policy, tripEnd); problemOf(t, err).Code != store.CodeAdvanceRequisition {
		t.Fatalf("another legal entity's requisition must be %s", store.CodeAdvanceRequisition)
	}
}

func fileHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func cardRows(worker string) []store.NewCardRow {
	day := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	return []store.NewCardRow{
		{Line: 2, WorkerID: worker, CardLast4: "4242", TxnDate: day, PostDate: day.AddDate(0, 0, 1), Merchant: "Hotel", MCC: "7011", Amount: decimal.RequireFromString("120.50"), Currency: "USD"},
		{Line: 3, WorkerID: worker, CardLast4: "4242", TxnDate: day, PostDate: day.AddDate(0, 0, 1), Merchant: "Refund", Amount: decimal.RequireFromString("-20.00"), Currency: "USD"},
	}
}

// FR-43.13 / EXP-CARD-002: an import stores its rows in the worker's unassigned pool
// and its quarantine; the same file is never imported twice, while the same key
// replays. Another tenant sees none of it.
func TestPostgres_CardImportIsOncePerFileAndFillsThePool(t *testing.T) {
	s, ctx, le, _ := setup(t, 24)
	worker := ids.New()
	hash := fileHash("feed-1")
	in := store.NewImport{LegalEntityID: le, Format: "CSV", FileHash: hash, Rows: cardRows(worker),
		Quarantined: []store.RowError{{Line: 4, Reason: "card ending 5555 has no cardholder"}}}

	imp, replayed, err := s.ImportCardFeed(ctx, "imp-1", "h1", in)
	if err != nil || replayed || imp.Imported != 2 || len(imp.Quarantined) != 1 || imp.Quarantined[0].Line != 4 {
		t.Fatalf("import = %+v replayed=%v err=%v; want 2 imported and line 4 quarantined", imp, replayed, err)
	}
	if again, replayed, err := s.ImportCardFeed(ctx, "imp-1", "h1", in); err != nil || !replayed || again.ID != imp.ID {
		t.Fatalf("replay = %v %v; want the original import", replayed, err)
	}
	if _, _, err := s.ImportCardFeed(ctx, "imp-2", "h2", in); problemOf(t, err).Code != store.CodeDuplicateFile || problemOf(t, err).Status != http.StatusConflict {
		t.Fatalf("the same file under a new key must be 409 %s", store.CodeDuplicateFile)
	}
	in.LegalEntityID = ids.New()
	in.FileHash = fileHash("feed-2")
	if _, _, err := s.ImportCardFeed(ctx, "imp-3", "h3", in); problemOf(t, err).Code != store.CodeOutOfScope {
		t.Fatalf("another legal entity must be %s", store.CodeOutOfScope)
	}

	first, next, err := s.ListCardTransactions(ctx, store.CardFilter{WorkerID: worker, Status: "UNASSIGNED"}, "", 1)
	if err != nil || len(first) != 1 || next == "" {
		t.Fatalf("first page = %d next %q %v; want one row and a cursor", len(first), next, err)
	}
	second, next, err := s.ListCardTransactions(ctx, store.CardFilter{WorkerID: worker, Status: "UNASSIGNED"}, next, 1)
	if err != nil || len(second) != 1 || next != "" || second[0].ID == first[0].ID {
		t.Fatalf("second page = %d next %q %v; want the other row and no cursor", len(second), next, err)
	}
	amounts := map[string]bool{first[0].Amount.String(): true, second[0].Amount.String(): true}
	if !amounts["120.5"] || !amounts["-20"] || first[0].ImportID != imp.ID {
		t.Fatalf("pool = %+v %+v; want the spend and the refund from this import", first[0], second[0])
	}

	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(25), LegalEntityID: le})
	if theirs, _, err := s.ListCardTransactions(other, store.CardFilter{}, "", 10); err != nil || len(theirs) != 0 {
		t.Fatalf("another tenant sees %d card transactions, %v; want none", len(theirs), err)
	}
}

// Row-level security alone hides another legal entity's advances, requisitions and
// card transactions, not only the store's tenant predicates. Skipped when the test
// role bypasses RLS.
func TestPostgres_TravelTablesRowLevelSecurity(t *testing.T) {
	s, ctx, le, pool := setup(t, 26)
	approver := as(ctx, "u-approver")
	worker := ids.New()
	if _, _, err := s.CreateRequisition(ctx, "rq", "h", newRequisition(le, worker)); err != nil {
		t.Fatal(err)
	}
	adv, _, err := s.RequestAdvance(ctx, "adv", "h", store.NewAdvance{LegalEntityID: le, WorkerID: worker, Amount: decimal.NewFromInt(100),
		Currency: "USD", Purpose: "Trip", TripEnd: tripEnd}, policy, tripEnd)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ImportCardFeed(ctx, "imp", "h", store.NewImport{LegalEntityID: le, Format: "OFX",
		FileHash: fileHash("feed-3"), Rows: cardRows(worker)}); err != nil {
		t.Fatal(err)
	}
	var bypass bool
	if err := pool.QueryRow(context.Background(), `select rolsuper or rolbypassrls from pg_roles where rolname = current_user`).Scan(&bypass); err != nil {
		t.Fatal(err)
	}
	if bypass {
		t.Skip("test role bypasses row-level security")
	}
	count := func(c context.Context) (n int) {
		t.Helper()
		if err := dbx.WithTenantTx(c, pool, func(tx pgx.Tx) error {
			return tx.QueryRow(c, `select (select count(*) from cash_advance) + (select count(*) from travel_requisition)
				+ (select count(*) from card_transaction) + (select count(*) from card_import)`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(ctx); n != 5 {
		t.Fatalf("own scope sees %d rows; want 5 (advance, requisition, import and its 2 card rows)", n)
	}
	if n := count(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(27), LegalEntityID: le})); n != 0 {
		t.Fatalf("another tenant sees %d rows; want none", n)
	}
	otherEntity := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(26), LegalEntityID: ids.New(), UserID: "u-approver"})
	if n := count(otherEntity); n != 0 {
		t.Fatalf("another legal entity sees %d rows; want none", n)
	}
	if _, _, err := s.DecideAdvance(otherEntity, adv.ID, true, cmd("approve", 0)); problemOf(t, err).Code != store.CodeAdvanceNotFound {
		t.Fatalf("another legal entity deciding must be %s", store.CodeAdvanceNotFound)
	}
	if _, _, err := s.DecideAdvance(approver, adv.ID, true, cmd("approve", 0)); err != nil {
		t.Fatalf("own scope deciding: %v", err)
	}
}
