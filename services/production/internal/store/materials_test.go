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

// Material requirements (4.21.14): sampleOrder needs 200 and 100 of one raw material on lines 10 and
// 20.1. A posted picking list lowers what is outstanding, a draft does not, a reversal gives it back,
// cancelled orders drop out, and the item roll-up sums the lines of all open orders.
func TestMaterialRequirements(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(82))
	le := ids.New()
	first := sampleOrder(t, "mat-1", le)
	st := startedOrder(t, s, ctx, first)
	second := sampleOrder(t, "mat-2", le)
	for i := range second.Lines {
		second.Lines[i].ItemID = first.Lines[0].ItemID // both orders need the same raw material
	}
	created, _, err := s.CreateOrder(ctx, second)
	if err != nil {
		t.Fatal(err)
	}

	all, err := s.MaterialRequirements(ctx, MaterialFilter{})
	if err != nil || len(all.Items) != 4 || all.NextCursor != "" || all.Totals.Lines != 4 || all.Totals.Orders != 2 || all.Totals.Items != 1 {
		t.Fatalf("before issuing: %+v %v", all, err)
	}
	for _, r := range all.Items {
		if !r.QtyIssued.IsZero() || !r.QtyOutstanding.Equal(r.QtyRequired) || r.DueDate == nil || *r.DueDate != "2026-11-30" {
			t.Fatalf("row before issuing: %+v", r)
		}
	}

	// A draft picking list issues nothing yet; the posted one issues 50 of line 10 and all of 20.1.
	pick := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("50")}, {LineNo: "20.1", Qty: d("100")}}})
	draft, err := s.MaterialRequirements(ctx, MaterialFilter{OrderID: st.orderID})
	if err != nil || len(draft.Items) != 2 {
		t.Fatalf("draft: %+v %v", draft, err)
	}
	st.mustPost(pick.ID)
	got, err := s.MaterialRequirements(ctx, MaterialFilter{OrderID: st.orderID})
	if err != nil || len(got.Items) != 1 || got.Items[0].LineNumber != "10" || !got.Items[0].QtyIssued.Equal(d("50")) ||
		!got.Items[0].QtyOutstanding.Equal(d("150")) || got.Items[0].OrderStatus != domain.Started {
		t.Fatalf("after issuing: %+v %v", got, err)
	}
	full, err := s.MaterialRequirements(ctx, MaterialFilter{OrderID: st.orderID, All: true})
	if err != nil || len(full.Items) != 2 || !full.Items[1].QtyOutstanding.IsZero() {
		t.Fatalf("all lines: %+v %v", full, err)
	}

	items, err := s.MaterialItems(ctx, MaterialFilter{})
	if err != nil || len(items.Items) != 1 {
		t.Fatalf("items: %+v %v", items, err)
	}
	it := items.Items[0]
	// The fully issued line 20.1 is left out: 200 + 300 required, 50 issued, 450 outstanding over 2 orders and 3 lines.
	if it.Orders != 2 || it.Lines != 3 || !it.QtyRequired.Equal(d("500")) || !it.QtyIssued.Equal(d("50")) || !it.QtyOutstanding.Equal(d("450")) ||
		it.EarliestDue == nil || *it.EarliestDue != "2026-11-30" {
		t.Fatalf("item = %+v", it)
	}

	// Pages and filters.
	page, err := s.MaterialRequirements(ctx, MaterialFilter{Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" || page.Totals.Lines != 3 {
		t.Fatalf("page 1: %+v %v", page, err)
	}
	last := page.Items[1]
	rest, err := s.MaterialRequirements(ctx, MaterialFilter{Limit: 2, CursorOrder: last.OrderID, CursorSeq: last.LineSeq})
	if err != nil || len(rest.Items) != 1 || rest.NextCursor != "" {
		t.Fatalf("page 2: %+v %v", rest, err)
	}
	for name, tc := range map[string]struct {
		f    MaterialFilter
		want int
	}{
		"item":          {MaterialFilter{ItemID: all.Items[0].ItemID}, 3},
		"other item":    {MaterialFilter{ItemID: ids.New()}, 0},
		"legal entity":  {MaterialFilter{LegalEntityID: le}, 3},
		"other entity":  {MaterialFilter{LegalEntityID: ids.New()}, 0},
		"created":       {MaterialFilter{OrderStatus: domain.Created}, 2},
		"started":       {MaterialFilter{OrderStatus: domain.Started}, 1},
		"due in Nov":    {MaterialFilter{DueFrom: "2026-11-01", DueTo: "2026-11-30"}, 3},
		"due later":     {MaterialFilter{DueFrom: "2026-12-01"}, 0},
		"order created": {MaterialFilter{OrderID: created.ID}, 2},
	} {
		got, err := s.MaterialRequirements(ctx, tc.f)
		if err != nil || len(got.Items) != tc.want || got.Totals.Lines != tc.want {
			t.Errorf("%s: want %d, got %d totals %d (%v)", name, tc.want, len(got.Items), got.Totals.Lines, err)
		}
	}
	if withAll, err := s.MaterialItems(ctx, MaterialFilter{All: true}); err != nil || len(withAll.Items) != 1 || withAll.Items[0].Lines != 4 || !withAll.Items[0].QtyRequired.Equal(d("600")) {
		t.Fatalf("items with all: %+v %v", withAll, err)
	}
	if byItem, err := s.MaterialItems(ctx, MaterialFilter{CursorItem: it.ItemID}); err != nil || len(byItem.Items) != 0 {
		t.Fatalf("items after the cursor: %+v %v", byItem, err)
	}

	// Cancelled orders are not open demand; asking for the status lists them.
	if res := s.BulkTransition(ctx, Bulk{OrderIDs: []string{created.ID}, Command: domain.CmdCancel, Actor: "planner-1", TraceID: "t", IdempotencyKey: "mat-cancel", RequestHash: "h"}); !res[0].OK {
		t.Fatalf("cancel: %+v", res)
	}
	if got, err := s.MaterialRequirements(ctx, MaterialFilter{}); err != nil || len(got.Items) != 1 || got.Items[0].OrderID != st.orderID {
		t.Fatalf("after cancelling: %+v %v", got, err)
	}
	if got, err := s.MaterialRequirements(ctx, MaterialFilter{OrderStatus: domain.Cancelled}); err != nil || len(got.Items) != 2 {
		t.Fatalf("cancelled orders: %+v %v", got, err)
	}
	if foreign, err := s.MaterialRequirements(tenantCtx(testkit.TenantID(83)), MaterialFilter{All: true}); err != nil || len(foreign.Items) != 0 || foreign.Totals.Lines != 0 {
		t.Fatalf("another tenant: %+v %v", foreign, err)
	}

	// A reset to Released reverses the posted picking list: nothing is issued and the demand is back.
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Released })
	back, err := s.MaterialRequirements(ctx, MaterialFilter{OrderID: st.orderID})
	if err != nil || len(back.Items) != 2 || back.Items[0].OrderStatus != domain.Released {
		t.Fatalf("after the reversal: %+v %v", back, err)
	}
	for _, r := range back.Items {
		if !r.QtyIssued.IsZero() || !r.QtyOutstanding.Equal(r.QtyRequired) {
			t.Fatalf("line after the reversal: %+v", r)
		}
	}
}

func TestMaterialRequirements_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tenantA := testkit.TenantID(84)
	leA, leB := ids.New(), ids.New()
	ctxA := tenantCtx(tenantA)
	for key, le := range map[string]string{"mat-rls-a": leA, "mat-rls-b": leB} {
		if _, _, err := s.CreateOrder(ctxA, sampleOrder(t, key, le)); err != nil {
			t.Fatal(err)
		}
	}
	scoped := scopedCtx(tenantA, leA)
	lines, err := s.MaterialRequirements(scoped, MaterialFilter{})
	if err != nil || len(lines.Items) != 2 || lines.Items[0].LegalEntityID != leA || lines.Totals.Orders != 1 {
		t.Fatalf("lines under a legal-entity scope: %+v %v", lines, err)
	}
	if leak, err := s.MaterialRequirements(scoped, MaterialFilter{LegalEntityID: leB}); err != nil || len(leak.Items) != 0 || leak.Totals.Lines != 0 {
		t.Fatalf("lines of another legal entity: %+v %v", leak, err)
	}
	items, err := s.MaterialItems(scoped, MaterialFilter{})
	if err != nil || len(items.Items) != 1 || !items.Items[0].QtyRequired.Equal(d("300")) || items.Items[0].Orders != 1 {
		t.Fatalf("items under a legal-entity scope: %+v %v", items, err)
	}
	if rep, err := s.MaterialRequirements(tenantCtx(testkit.TenantID(85)), MaterialFilter{}); err != nil || len(rep.Items) != 0 {
		t.Fatalf("another tenant sees lines: %+v %v", rep, err)
	}
}
