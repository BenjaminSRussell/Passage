package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
	"github.com/BenjaminSRussell/passage/services/purchasing/migrations"
)

const legalEntity = "00000000-0000-7000-8000-0000000001e1"

func principalContext(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: ids.New(), Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

var adminPools sync.Map

func migrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	adminPools.Store(app, admin)
	return app
}

func TestChildRowsRespectLegalEntityScope(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	ctxA := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(91), LegalEntityID: entityA, UserID: ids.New()})
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(91), LegalEntityID: entityB, UserID: ids.New()})

	rfqInput := draftRFQ(t)
	rfqInput.LegalEntityID = entityA
	rfq, err := s.CreateRFQ(ctxA, rfqInput, store.Idem{})
	if err != nil {
		t.Fatalf("create RFQ: %v", err)
	}
	if _, err := s.SendRFQ(ctxA, rfq.ID, 1, []string{ids.New()}, store.Idem{}); err != nil {
		t.Fatalf("send RFQ: %v", err)
	}

	saInput := weeklyAgreement(t)
	saInput.LegalEntityID = entityA
	sa, err := s.CreateSchedulingAgreement(ctxA, saInput, store.Idem{})
	if err != nil {
		t.Fatalf("create scheduling agreement: %v", err)
	}
	rows := []struct {
		table, key, id string
	}{
		{"purchase_rfq_line", "rfq_id", rfq.ID},
		{"purchase_rfq_invitation", "rfq_id", rfq.ID},
		{"purchase_sa_schedule_line", "agreement_id", sa.Agreement.ID},
	}
	for _, row := range rows {
		t.Run(row.table, func(t *testing.T) {
			var count int
			err := dbx.WithTenantTx(ctxB, pool, func(tx pgx.Tx) error {
				return tx.QueryRow(ctxB, "SELECT count(*) FROM "+row.table+" WHERE tenant_id = $1 AND "+row.key+" = $2", testkit.TenantID(91), row.id).Scan(&count)
			})
			if err != nil {
				t.Fatalf("query child rows: %v", err)
			}
			if count != 0 {
				t.Errorf("entity %s can see %d %s rows owned by entity %s", entityB, count, row.table, entityA)
			}
		})
	}
}

func TestChildInsertUnderOtherEntityParentIsRefused(t *testing.T) {
	pool := migrated(t)
	entityA, entityB := ids.New(), ids.New()
	ctxA := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(92), LegalEntityID: entityA, UserID: ids.New()})
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(92), LegalEntityID: entityB, UserID: ids.New()})
	rfqInput := draftRFQ(t)
	rfqInput.LegalEntityID = entityA
	rfq, err := store.New(pool).CreateRFQ(ctxA, rfqInput, store.Idem{})
	if err != nil {
		t.Fatalf("create RFQ: %v", err)
	}
	err = dbx.WithTenantTx(ctxB, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctxB, `INSERT INTO purchase_rfq_line (tenant_id, rfq_id, line_no, id, description, qty, unit)
			VALUES ($1, $2, 99, $3, 'smuggled', 1, 'EA')`, testkit.TenantID(92), rfq.ID, ids.New())
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err = %v, want a pgconn error", err)
	}
	if pgErr.Code != "42501" || !strings.Contains(pgErr.Message, "row-level security") {
		t.Fatalf("pg error = %s %q, want 42501 row-level security", pgErr.Code, pgErr.Message)
	}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func day(t *testing.T, s string) domain.Day {
	t.Helper()
	d, err := domain.ParseDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// problemCode is the stable code of a store problem or of a domain rule
// error (which the API turns into a 422).
func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func requisition(t *testing.T, requester string, amounts ...string) store.Requisition {
	r := store.Requisition{LegalEntityID: legalEntity, RequesterID: requester, Currency: "USD", Total: decimal.Zero}
	for i, a := range amounts {
		r.Lines = append(r.Lines, store.RequisitionLine{LineNo: i + 1, Description: "Item", Category: "MRO", Qty: dec("1"), Unit: "EA",
			EstimatedUnitPrice: dec(a), Amount: dec(a), RequestedDate: day(t, "2026-11-02")})
		r.Total = r.Total.Add(dec(a))
	}
	return r
}

type outboxRow struct {
	Type string
	Data map[string]any
}

func outbox(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []outboxRow {
	t.Helper()
	var out []outboxRow
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select type, payload from outbox order by created_at, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			var payload []byte
			if err := rows.Scan(&typ, &payload); err != nil {
				return err
			}
			var env struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(payload, &env); err != nil {
				return err
			}
			out = append(out, outboxRow{Type: typ, Data: env.Data})
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRequisition_CreateReplaySubmitAndEvents(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(1)
	requester := ids.New()

	first, err := s.CreateRequisition(ctx, requisition(t, requester, "400", "100"), store.Idem{Key: "k1", Hash: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Number == "" || first.Status != store.RequisitionDraft || first.Version != 1 || len(first.Lines) != 2 || first.Lines[0].ID == "" {
		t.Fatalf("created %+v", first)
	}
	replay, err := s.CreateRequisition(ctx, requisition(t, requester, "400", "100"), store.Idem{Key: "k1", Hash: "h1"})
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %v, %v; want the original %s", replay.ID, err, first.ID)
	}
	if _, err := s.CreateRequisition(ctx, requisition(t, requester, "1"), store.Idem{Key: "k1", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key = %v", err)
	}

	if _, err := s.SubmitRequisition(ctx, first.ID, 7, nil, "builtin-1", store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	submitted, err := s.SubmitRequisition(ctx, first.ID, 1, nil, "builtin-1", store.Idem{Key: "s1", Hash: "sh"})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != store.RequisitionApproved || submitted.Version != 2 || submitted.PolicyVersion != "builtin-1" {
		t.Fatalf("auto-approved submit = %+v", submitted)
	}
	again, err := s.SubmitRequisition(ctx, first.ID, 1, nil, "builtin-1", store.Idem{Key: "s1", Hash: "sh"})
	if err != nil || again.Version != 2 {
		t.Fatalf("replayed submit = %+v, %v; want the current requisition, not a conflict", again, err)
	}
	if _, err := s.SubmitRequisition(ctx, first.ID, 2, nil, "builtin-1", store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("second submit = %v", err)
	}

	events := outbox(t, ctx, pool)
	if len(events) != 2 || events[0].Type != purchasingevents.RequisitionSubmitted || events[1].Type != purchasingevents.RequisitionApproved {
		t.Fatalf("outbox = %+v; want submitted then approved exactly once", events)
	}
	if events[0].Data["total"] != "500" || events[0].Data["requisition_id"] != first.ID || len(events[0].Data["lines"].([]any)) != 2 {
		t.Fatalf("submitted payload = %v", events[0].Data)
	}

	routed, err := s.CreateRequisition(ctx, requisition(t, requester, "5000"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	inReview, err := s.SubmitRequisition(ctx, routed.ID, 1, []string{domain.RoleManager}, "builtin-1", store.Idem{})
	if err != nil || inReview.Status != store.RequisitionInReview || len(inReview.ApproverChain) != 1 {
		t.Fatalf("routed submit = %+v, %v", inReview, err)
	}
	if got := len(outbox(t, ctx, pool)); got != 3 {
		t.Fatalf("outbox rows = %d; IN_REVIEW must add only requisition.submitted", got)
	}

	if _, err := s.GetRequisition(principalContext(2), first.ID); problemCode(err) != store.CodeRequisitionNotFound {
		t.Fatalf("other tenant read = %v; want not found", err)
	}
}

func TestRequisition_ListPagesNewestFirstAndFilters(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(3)
	me, other := ids.New(), ids.New()
	var created []string
	for _, who := range []string{me, me, other} {
		r, err := s.CreateRequisition(ctx, requisition(t, who, "10"), store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, r.ID)
	}
	first, err := s.ListRequisitions(ctx, store.RequisitionFilter{}, "", 2)
	if err != nil || len(first.Items) != 2 || first.Items[0].ID != created[2] || first.Next != created[1] || first.Items[0].LineCount != 1 {
		t.Fatalf("page 1 = %+v, %v", first, err)
	}
	second, err := s.ListRequisitions(ctx, store.RequisitionFilter{}, first.Next, 2)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != created[0] || second.Next != "" {
		t.Fatalf("page 2 = %+v, %v", second, err)
	}
	mine, err := s.ListRequisitions(ctx, store.RequisitionFilter{RequesterID: me, Status: store.RequisitionDraft}, "", 0)
	if err != nil || len(mine.Items) != 2 {
		t.Fatalf("mine = %+v, %v", mine, err)
	}
	none, err := s.ListRequisitions(principalContext(4), store.RequisitionFilter{}, "", 0)
	if err != nil || len(none.Items) != 0 {
		t.Fatalf("other tenant = %+v, %v", none, err)
	}
}

func purchaseOrder(t *testing.T) store.PurchaseOrder {
	doc, err := domain.Price([]domain.LineInput{{Qty: dec("10"), UnitPrice: dec("12.5"), PriceUnit: dec("1"), DiscountPct: dec("10"), Tax: dec("9")}},
		[]domain.ChargeInput{{Calc: domain.CalcFixed, Value: dec("5")}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	l := doc.Lines[0]
	return store.PurchaseOrder{LegalEntityID: legalEntity, VendorID: ids.New(), Currency: "USD", ChangeManagement: true,
		Subtotal: doc.Totals.Subtotal, DiscountTotal: doc.Totals.DiscountTotal, ChargesTotal: doc.Totals.ChargesTotal, TaxTotal: doc.Totals.TaxTotal,
		Total: doc.Totals.Total,
		Lines: []store.PurchaseOrderLine{{LineNo: 1, Description: "Widget", Qty: dec("10"), Unit: "EA", UnitPrice: dec("12.5"), PriceUnit: dec("1"),
			DiscountPct: dec("10"), GrossAmount: l.Gross, DiscountAmount: l.Discount, LineAmount: l.Amount, TaxAmount: l.Tax,
			DeliveryDate: day(t, "2026-12-01"), PriceSource: "MANUAL"}},
		Charges: []store.PurchaseOrderCharge{{Seq: 1, Calc: domain.CalcFixed, Value: dec("5"), Amount: doc.Charges[0]}},
	}
}

func TestPurchaseOrder_CreateReadListAndTotalIdentityTrigger(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(5)
	po, err := s.CreatePurchaseOrder(ctx, purchaseOrder(t), store.Idem{Key: "po", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil || got.ApprovalStatus != "DRAFT" || got.DocumentStatus != "NONE" || got.VersionNo != 0 || !got.Total.Equal(dec("126.5")) ||
		len(got.Lines) != 1 || len(got.Charges) != 1 {
		t.Fatalf("read back %+v, %v", got, err)
	}
	page, err := s.ListPurchaseOrders(ctx, store.PurchaseOrderFilter{ApprovalStatus: "DRAFT", VendorID: po.VendorID}, "", 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].LineCount != 1 || !page.Items[0].Total.Equal(dec("126.5")) {
		t.Fatalf("list = %+v, %v", page, err)
	}

	broken := purchaseOrder(t)
	broken.Total = broken.Total.Add(dec("0.01"))
	if _, err := s.CreatePurchaseOrder(ctx, broken, store.Idem{}); err == nil {
		t.Fatal("a header whose total is off by a cent must be refused by the deferred trigger")
	}
	if _, err := s.GetPurchaseOrder(principalContext(6), po.ID); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("other tenant read = %v", err)
	}
}

func agreement(t *testing.T, code, account, item, itemID, price, from string) domain.TradeAgreement {
	return domain.TradeAgreement{Relation: domain.RelationPrice, AccountCode: code, AccountID: account, ItemCode: item, ItemID: itemID,
		Currency: "USD", Unit: "EA", FromQty: decimal.Zero, Price: dec(price), PriceUnit: dec("1"), ValidFrom: day(t, from), Status: domain.StatusActive}
}

func TestTradeAgreement_CandidatesOverlapAndList(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(7)
	vendor, otherVendor, group, product := ids.New(), ids.New(), ids.New(), ids.New()
	rows := []domain.TradeAgreement{
		agreement(t, domain.AccountVendor, vendor, domain.ItemProduct, product, "10", "2026-01-01"),
		agreement(t, domain.AccountGroup, group, domain.ItemAll, "", "9", "2026-01-01"),
		agreement(t, domain.AccountAll, "", domain.ItemAll, "", "8", "2026-01-01"),
		agreement(t, domain.AccountVendor, otherVendor, domain.ItemAll, "", "1", "2026-01-01"),
		agreement(t, domain.AccountAll, "", domain.ItemAll, "", "7", "2027-01-01"),
	}
	consign := agreement(t, domain.AccountVendor, vendor, domain.ItemAll, "", "2", "2026-01-01")
	consign.Consignment = true
	rows = append(rows, consign)
	for i, r := range rows {
		if _, err := s.CreateTradeAgreement(ctx, r, store.Idem{}); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	got, err := s.PriceCandidates(ctx, store.PriceFilter{Relation: domain.RelationPrice, Currency: "USD", Unit: "EA", Date: day(t, "2026-06-01"),
		VendorID: vendor, VendorGroupIDs: []string{group, "not-a-uuid"}, ProductID: product})
	if err != nil {
		t.Fatal(err)
	}
	prices := map[string]bool{}
	for _, r := range got {
		prices[r.Price.String()] = true
	}
	if len(got) != 3 || !prices["10"] || !prices["9"] || !prices["8"] {
		t.Fatalf("candidates = %v; want the vendor, group and all rows only (no other vendor, future or consignment)", prices)
	}
	if res := domain.Resolve(got, domain.Query{Relation: domain.RelationPrice, VendorID: vendor, VendorGroupIDs: []string{group}, ProductID: product,
		Qty: dec("5"), Currency: "USD", Unit: "EA", Date: day(t, "2026-06-01")}); res.Winner == nil || !res.Winner.Price.Equal(dec("10")) {
		t.Fatalf("winner = %+v; want the vendor/product row (AC-17.4)", res.Winner)
	}

	dup := agreement(t, domain.AccountGroup, group, domain.ItemAll, "", "8.5", "2026-01-01")
	if _, err := s.CreateTradeAgreement(ctx, dup, store.Idem{}); problemCode(err) != store.CodeTradeAgreementOverlap {
		t.Fatalf("equal specificity and date = %v; want %s", err, store.CodeTradeAgreementOverlap)
	}
	broken := dup
	broken.FromQty = dec("100")
	if _, err := s.CreateTradeAgreement(ctx, broken, store.Idem{}); problemCode(err) != store.CodeTradeAgreementOverlap {
		t.Fatalf("open-ended break overlapping an open-ended row = %v", err)
	}
	lower := agreement(t, domain.AccountAll, "", domain.ItemProduct, product, "6", "2026-01-01")
	upper := lower
	to := dec("100")
	lower.ToQty = &to
	upper.FromQty = dec("100")
	for _, r := range []domain.TradeAgreement{lower, upper} {
		if _, err := s.CreateTradeAgreement(ctx, r, store.Idem{}); err != nil {
			t.Fatalf("adjacent quantity breaks [0,100) and [100,∞) must both be accepted: %v", err)
		}
	}

	page, err := s.ListTradeAgreements(ctx, store.TradeAgreementFilter{AccountID: vendor}, "", 0)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("vendor rows = %+v, %v", page, err)
	}
	one, err := s.GetTradeAgreement(ctx, page.Items[0].ID)
	if err != nil || one.ID != page.Items[0].ID {
		t.Fatalf("get = %+v, %v", one, err)
	}
	if _, err := s.GetTradeAgreement(principalContext(8), one.ID); problemCode(err) != store.CodeTradeAgreementNotFound {
		t.Fatalf("other tenant = %v", err)
	}
}

// TestRowLevelSecurity_AppRoleSeesOnlyItsTenant runs the store as the
// non-owner application role, where FORCE ROW LEVEL SECURITY applies.
func TestRowLevelSecurity_AppRoleSeesOnlyItsTenant(t *testing.T) {
	if os.Getenv(testkit.EnvURL) != "" && os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s not set; skipping the application-role RLS test", testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"grant usage on schema " + schema + " to passage_app",
		"grant select, insert, update, delete on all tables in schema " + schema + " to passage_app",
		"grant usage, select on all sequences in schema " + schema + " to passage_app",
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	s := store.New(app)
	a, b := principalContext(11), principalContext(12)
	r, err := s.CreateRequisition(a, requisition(t, ids.New(), "10"), store.Idem{Key: "k", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitRequisition(a, r.ID, 1, nil, "builtin-1", store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRequisition(b, r.ID); problemCode(err) != store.CodeRequisitionNotFound {
		t.Fatalf("tenant B read tenant A's requisition: %v", err)
	}
	if got := len(outbox(t, b, app)); got != 0 {
		t.Fatalf("tenant B sees %d outbox rows", got)
	}
	if got := len(outbox(t, a, app)); got != 2 {
		t.Fatalf("tenant A outbox rows = %d", got)
	}
	// Tenant B reusing tenant A's key is a fresh claim, not a replay.
	other, err := s.CreateRequisition(b, requisition(t, ids.New(), "10"), store.Idem{Key: "k", Hash: "h"})
	if err != nil || other.ID == r.ID {
		t.Fatalf("tenant B with the same key = %v, %v", other.ID, err)
	}
	// A legal-entity-scoped caller cannot see another entity's requisition.
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(11), LegalEntityID: ids.New()})
	if _, err := s.GetRequisition(scoped, r.ID); problemCode(err) != store.CodeRequisitionNotFound {
		t.Fatalf("out-of-scope read = %v", err)
	}
	// Purchase agreements (0002) are isolated the same way, by tenant and by
	// legal entity scope.
	pa, err := s.CreateAgreement(a, blanket(t, ids.New(), ids.New()), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgreement(b, pa.Agreement.ID); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("tenant B read tenant A's agreement: %v", err)
	}
	if _, err := s.GetAgreementLine(b, pa.Agreement.Lines[0].ID); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("tenant B read tenant A's agreement line: %v", err)
	}
	if _, err := s.GetAgreement(scoped, pa.Agreement.ID); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("out-of-scope agreement read = %v", err)
	}
	// PO versions (0003) carry no legal entity; they are read through the PO.
	po, err := s.CreatePurchaseOrder(a, lifecyclePO(t, ids.New(), "1", "2027-01-05"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{domain.POActionSubmit, domain.POActionApprove, domain.POActionConfirm} {
		if po, err = s.ActOnPurchaseOrder(a, po.ID, po.RowVersion, action, store.Idem{}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if v, err := s.GetPurchaseOrderVersion(a, po.ID, 1); err != nil || v.ConfirmedAt == nil {
		t.Fatalf("tenant A version 1 = %+v, %v", v, err)
	}
	for name, ctx := range map[string]context.Context{"tenant B": b, "out of scope": scoped} {
		if _, err := s.GetPurchaseOrderVersion(ctx, po.ID, 1); problemCode(err) != store.CodePurchaseOrderNotFound {
			t.Fatalf("%s read of a PO version = %v", name, err)
		}
		if _, err := s.ListPurchaseOrderVersions(ctx, po.ID); problemCode(err) != store.CodePurchaseOrderNotFound {
			t.Fatalf("%s list of PO versions = %v", name, err)
		}
	}
	// Invoicing plans (0006) carry no legal entity either; they are read through the PO.
	plan, err := s.PutInvoicingPlan(a, po.ID, 1, po.RowVersion, store.InvoicingPlanInput{Kind: domain.PlanPartial,
		Milestones: []domain.Milestone{{Date: day(t, "2027-02-01"), Amount: &po.Lines[0].LineAmount}}})
	if err != nil || len(plan.Rows) != 1 {
		t.Fatalf("tenant A plan = %+v, %v", plan, err)
	}
	for name, ctx := range map[string]context.Context{"tenant B": b, "out of scope": scoped} {
		if _, err := s.GetInvoicingPlan(ctx, po.ID, 1); problemCode(err) != store.CodePurchaseOrderNotFound {
			t.Fatalf("%s read of an invoicing plan = %v", name, err)
		}
	}
	// Without a tenant the policy refuses: no rows, or an error when the
	// pooled connection's tenant setting is reset to an empty string.
	var leaked int
	if err := app.QueryRow(ctx, `select count(*) from purchase_order_version`).Scan(&leaked); err == nil && leaked != 0 {
		t.Fatalf("app role without a tenant sees %d versions", leaked)
	}
	for _, table := range []string{"purchase_invoicing_plan", "purchase_invoicing_plan_row", "purchase_rfq_criterion", "purchase_rfq_score"} {
		if err := app.QueryRow(ctx, `select count(*) from `+table).Scan(&leaked); err == nil && leaked != 0 {
			t.Fatalf("app role without a tenant sees %d rows of %s", leaked, table)
		}
	}
	// Writes are checked too: the app role cannot insert a row for another tenant.
	_, err = app.Exec(ctx, `insert into trade_agreement (tenant_id, id, relation, account_code, item_code, currency, unit, price, valid_from)
		values ($1, $2, 'PRICE', 'ALL', 'ALL', 'USD', 'EA', 1, '2026-01-01')`, testkit.TenantID(12), ids.New())
	if err == nil {
		t.Fatal("insert without a tenant context must violate the RLS policy")
	}
}
