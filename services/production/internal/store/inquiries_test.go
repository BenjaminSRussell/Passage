package store

import (
	"context"
	"os"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

// The operations overview (4.21.14): planned hours are setup + run × process
// quantity, reported hours and quantities come from posted route cards, the
// filters and keyset pages work, totals cover the whole filter, and another
// tenant sees nothing.
func TestOperationsOverview(t *testing.T) {
	_, s := migrated(t)
	tid := testkit.TenantID(75)
	ctx := tenantCtx(tid)
	le := ids.New()
	vendor := ids.New()

	st := startedOrder(t, s, ctx, subcontractOrder(t, "ops-1", le, vendor))
	route := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("20"), QtyGood: d("60"), QtyError: d("2")}}})
	st.mustPost(route.ID)
	if _, _, err := s.CreateOrder(ctx, sampleOrder(t, "ops-2", ids.New())); err != nil {
		t.Fatal(err)
	}

	all, err := s.OperationsOverview(ctx, OperationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 3 || all.NextCursor != "" {
		t.Fatalf("want 3 operations, got %+v", all.Items)
	}
	var started, sub *OperationRow
	for i := range all.Items {
		r := &all.Items[i]
		if r.OrderID == st.orderID && r.OperNumber == 10 {
			started = r
		}
		if r.OrderID == st.orderID && r.OperNumber == 20 {
			sub = r
		}
	}
	if started == nil || sub == nil {
		t.Fatalf("rows: %+v", all.Items)
	}
	// 0.5 h per unit × 100 units = 50 h planned; the route card booked 20 h, 60 good, 2 error.
	if !started.PlannedHours.Equal(d("50")) || !started.HoursReported.Equal(d("20")) || !started.QtyGood.Equal(d("60")) ||
		!started.QtyError.Equal(d("2")) || started.OperStatus != domain.OperStarted || started.OrderStatus != domain.Started || started.Subcontract {
		t.Fatalf("started row = %+v", started)
	}
	if !sub.Subcontract || !sub.PlannedHours.IsZero() || sub.OperStatus != domain.OperOpen || sub.DueDate == nil || *sub.DueDate != "2026-11-30" {
		t.Fatalf("subcontract row = %+v", sub)
	}
	tot := all.Totals
	if tot.Operations != 3 || tot.Open != 2 || tot.Started != 1 || tot.Finished != 0 || !tot.PlannedHours.Equal(d("100")) ||
		!tot.HoursReported.Equal(d("20")) || !tot.QtyGood.Equal(d("60")) || !tot.QtyError.Equal(d("2")) {
		t.Fatalf("totals = %+v", tot)
	}

	page, err := s.OperationsOverview(ctx, OperationFilter{Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" || page.Totals.Operations != 3 {
		t.Fatalf("page 1: %+v %v", page, err)
	}
	last := page.Items[1]
	rest, err := s.OperationsOverview(ctx, OperationFilter{Limit: 2, CursorOrder: last.OrderID, CursorOper: last.OperNumber})
	if err != nil || len(rest.Items) != 1 || rest.NextCursor != "" {
		t.Fatalf("page 2: %+v %v", rest, err)
	}

	for name, tc := range map[string]struct {
		f    OperationFilter
		want int
	}{
		"order":           {OperationFilter{OrderID: st.orderID}, 2},
		"legal entity":    {OperationFilter{LegalEntityID: le}, 2},
		"vendor resource": {OperationFilter{ResourceID: vendor}, 1},
		"started":         {OperationFilter{OperStatus: domain.OperStarted}, 1},
		"order started":   {OperationFilter{OrderStatus: domain.Started}, 2},
		"order created":   {OperationFilter{OrderStatus: domain.Created}, 1},
		"due in Nov":      {OperationFilter{DueFrom: "2026-11-01", DueTo: "2026-11-30"}, 3},
		"due later":       {OperationFilter{DueFrom: "2026-12-01"}, 0},
	} {
		got, err := s.OperationsOverview(ctx, tc.f)
		if err != nil || len(got.Items) != tc.want || got.Totals.Operations != tc.want {
			t.Errorf("%s: want %d, got %d totals %d (%v)", name, tc.want, len(got.Items), got.Totals.Operations, err)
		}
	}

	foreign, err := s.OperationsOverview(tenantCtx(testkit.TenantID(76)), OperationFilter{})
	if err != nil || len(foreign.Items) != 0 || foreign.Totals.Operations != 0 {
		t.Fatalf("another tenant: %+v %v", foreign, err)
	}
}

// The journal inquiry across orders (4.21.14): drafts and posted journals of
// several orders with their values, filters by type, posted state, order and
// date, keyset pages, the count over the whole filter, and tenant isolation.
func TestJournalInquiry(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(77))
	le := ids.New()
	st := startedOrder(t, s, ctx, sampleOrder(t, "jinq-1", le))

	pick := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{
		{LineNo: "10", Qty: d("203"), UnitCost: qty("10.20")}, {LineNo: "20.1", Qty: d("102"), UnitCost: qty("10.20")}}})
	st.mustPost(pick.ID)
	route := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("46"), QtyGood: d("100")}}})
	st.mustPost(route.ID)
	draft := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("1"), UnitCost: qty("10")}}})

	other := startedOrder(t, s, ctx, sampleOrder(t, "jinq-2", ids.New()))
	other.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("1"), UnitCost: qty("10")}}})

	all, err := s.JournalInquiry(ctx, JournalFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 4 || all.Total != 4 || all.NextCursor != "" {
		t.Fatalf("want 4 journals, got %+v", all.Items)
	}
	by := map[string]JournalRow{}
	for _, r := range all.Items {
		by[r.ID] = r
	}
	p := by[pick.ID]
	// 305 kg at 10.20 = 3,111.00 consumed, two lines, posted.
	if !p.Posted || p.PostedAt == nil || p.Type != domain.JournalPickingList || p.Lines != 2 || !p.Consumed.Equal(d("3111")) || !p.Received.IsZero() ||
		p.Currency != "USD" || p.OrderID != st.orderID || p.JournalNumber != pick.JournalNumber {
		t.Fatalf("picking list row = %+v", p)
	}
	// 46 h at 30.00 labor and 10.00 overhead = 1,840.00.
	if r := by[route.ID]; !r.Posted || !r.Consumed.Equal(d("1840")) || r.Lines != 1 {
		t.Fatalf("route card row = %+v", r)
	}
	if r := by[draft.ID]; r.Posted || r.PostedAt != nil || r.Lines != 1 || r.OrderNumber == "" {
		t.Fatalf("draft row = %+v", r)
	}

	page, err := s.JournalInquiry(ctx, JournalFilter{Limit: 3})
	if err != nil || len(page.Items) != 3 || page.NextCursor != page.Items[2].ID || page.Total != 4 {
		t.Fatalf("page 1: %+v %v", page, err)
	}
	rest, err := s.JournalInquiry(ctx, JournalFilter{Limit: 3, Cursor: page.NextCursor})
	if err != nil || len(rest.Items) != 1 || rest.NextCursor != "" {
		t.Fatalf("page 2: %+v %v", rest, err)
	}

	today := p.PostingDate
	for name, tc := range map[string]struct {
		f    JournalFilter
		want int
	}{
		"picking lists": {JournalFilter{Type: domain.JournalPickingList}, 3},
		"route cards":   {JournalFilter{Type: domain.JournalRouteCard}, 1},
		"posted":        {JournalFilter{Posted: "true"}, 2},
		"drafts":        {JournalFilter{Posted: "false"}, 2},
		"order":         {JournalFilter{OrderID: other.orderID}, 1},
		"order number":  {JournalFilter{OrderNumber: st.refresh().OrderNumber, LegalEntityID: le}, 3},
		"no such order": {JournalFilter{OrderNumber: "PO-999999"}, 0},
		"legal entity":  {JournalFilter{LegalEntityID: le}, 3},
		"other entity":  {JournalFilter{LegalEntityID: ids.New()}, 0},
		"today":         {JournalFilter{From: today, To: today}, 4},
		"before":        {JournalFilter{To: "2000-01-01"}, 0},
	} {
		got, err := s.JournalInquiry(ctx, tc.f)
		if err != nil || len(got.Items) != tc.want || got.Total != tc.want {
			t.Errorf("%s: want %d, got %d total %d (%v)", name, tc.want, len(got.Items), got.Total, err)
		}
	}

	foreign, err := s.JournalInquiry(tenantCtx(testkit.TenantID(78)), JournalFilter{})
	if err != nil || len(foreign.Items) != 0 || foreign.Total != 0 {
		t.Fatalf("another tenant: %+v %v", foreign, err)
	}
}

// A report as finished shows its receipt value as received, and the reversal
// that a reset posts shows the same value negated and points at the original.
func TestJournalInquiry_ReceiptAndReversal(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(79))
	st := startedOrder(t, s, ctx, sampleOrder(t, "jinq-raf", ids.New()))
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("60") })
	st.must(domain.CmdResetStatus, nil)

	got, err := s.JournalInquiry(ctx, JournalFilter{Type: domain.JournalReportFinished})
	if err != nil || len(got.Items) != 2 || got.Total != 2 {
		t.Fatalf("report-as-finished journals: %+v %v", got, err)
	}
	orig, rev := got.Items[0], got.Items[1]
	if orig.ReversalOf != nil {
		orig, rev = rev, orig
	}
	// 60 units at the planned unit cost of 50.00 = 3,000.00 received; nothing consumed.
	if !orig.Posted || !orig.Received.Equal(d("3000")) || !orig.Consumed.IsZero() || !orig.QtyGood.Equal(d("60")) {
		t.Fatalf("receipt row = %+v", orig)
	}
	if rev.ReversalOf == nil || *rev.ReversalOf != orig.ID || !rev.Posted || !rev.Received.Equal(d("-3000")) {
		t.Fatalf("reversal row = %+v", rev)
	}
}

// Both inquiries under the non-superuser application role: a principal scoped
// to one legal entity sees only that entity's operations and journals, and
// another tenant sees nothing, totals included.
func TestInquiries_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tenantA := testkit.TenantID(80)
	leA, leB := ids.New(), ids.New()
	ctxA := tenantCtx(tenantA)
	for key, le := range map[string]string{"inq-rls-a": leA, "inq-rls-b": leB} {
		st := startedOrder(t, s, ctxA, sampleOrder(t, key, le))
		st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("4"), QtyGood: d("10")}}}).ID)
	}

	scoped := scopedCtx(tenantA, leA)
	ops, err := s.OperationsOverview(scoped, OperationFilter{})
	if err != nil || len(ops.Items) != 1 || ops.Items[0].LegalEntityID != leA || ops.Totals.Operations != 1 || !ops.Totals.HoursReported.Equal(d("4")) {
		t.Fatalf("operations under a legal-entity scope: %+v %v", ops, err)
	}
	// An explicit filter on another legal entity does not widen the scope.
	if leak, err := s.OperationsOverview(scoped, OperationFilter{LegalEntityID: leB}); err != nil || len(leak.Items) != 0 || leak.Totals.Operations != 0 {
		t.Fatalf("operations of another legal entity: %+v %v", leak, err)
	}
	js, err := s.JournalInquiry(scoped, JournalFilter{})
	if err != nil || len(js.Items) != 1 || js.Items[0].LegalEntityID != leA || js.Total != 1 || !js.Items[0].Consumed.Equal(d("160")) {
		t.Fatalf("journals under a legal-entity scope: %+v %v", js, err)
	}
	if leak, err := s.JournalInquiry(scoped, JournalFilter{LegalEntityID: leB}); err != nil || len(leak.Items) != 0 || leak.Total != 0 {
		t.Fatalf("journals of another legal entity: %+v %v", leak, err)
	}

	other := tenantCtx(testkit.TenantID(81))
	if rep, err := s.OperationsOverview(other, OperationFilter{}); err != nil || len(rep.Items) != 0 || rep.Totals.Operations != 0 {
		t.Fatalf("another tenant sees operations: %+v %v", rep, err)
	}
	if rep, err := s.JournalInquiry(other, JournalFilter{}); err != nil || len(rep.Items) != 0 || rep.Total != 0 {
		t.Fatalf("another tenant sees journals: %+v %v", rep, err)
	}
}
