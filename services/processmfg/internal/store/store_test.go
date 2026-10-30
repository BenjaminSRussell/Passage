package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
	"github.com/BenjaminSRussell/passage/services/processmfg/migrations"
)

const (
	entityA = "11111111-0000-4000-8000-00000000000a"
	entityB = "11111111-0000-4000-8000-00000000000b"
)

// setup migrates a throwaway schema as the owner and returns a store on the
// application role (NOSUPERUSER, NOBYPASSRLS), so every query runs under RLS,
// plus the owner pool for assertions across tenants.
func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(app), admin
}

func ctxFor(tenantN int, legalEntity string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(tenantN), LegalEntityID: legalEntity,
		UserID: "planner-1", Duties: testkit.ScopeDuties(legalEntity)})
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// ac241 is the AC-24.1 formula (1,000 kg, a 50 kg non-scalable line) plus a
// co-product output line.
func ac241(code string) store.Formula {
	return store.Formula{Code: code, Name: "Dough", ItemID: ids.New(), Size: dec("1000"), Unit: "kg", Lines: []store.FormulaLine{
		{LineNumber: 1, ItemID: ids.New(), Type: domain.LineIngredient, Qty: dec("600"), Scalable: true},
		{LineNumber: 2, ItemID: ids.New(), Type: domain.LineIngredient, Qty: dec("350"), Scalable: true},
		{LineNumber: 3, ItemID: ids.New(), Type: domain.LineIngredient, Qty: dec("50"), Scalable: false},
		{LineNumber: 4, ItemID: ids.New(), Type: domain.LineCoProduct, Qty: dec("100"), Scalable: true},
	}}
}

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

func outboxEvents(t *testing.T, admin *pgxpool.Pool, typ string) []events.Event {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select payload from outbox where type = $1 order by created_at, id`, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		var raw []byte
		var evt events.Event
		if err := rows.Scan(&raw); err != nil || json.Unmarshal(raw, &evt) != nil {
			t.Fatalf("scan outbox: %v", err)
		}
		out = append(out, evt)
	}
	return out
}

func TestCreateFormula_PersistsLinesEventAndReplays(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	in := ac241("DOUGH-1")
	f, created, err := s.CreateFormula(ctx, in, store.Idem{Key: "f-1", Fingerprint: "fp-1"})
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	got, err := s.GetFormula(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != "DOUGH-1" || got.LineCount != 4 || len(got.Lines) != 4 || got.Lines[2].Scalable || !got.Lines[2].Qty.Equal(dec("50")) {
		t.Fatalf("stored formula %+v", got)
	}
	again, created, err := s.CreateFormula(ctx, in, store.Idem{Key: "f-1", Fingerprint: "fp-1"})
	if err != nil || created || again.ID != f.ID {
		t.Fatalf("replay: created=%v id=%s err=%v", created, again.ID, err)
	}
	if _, _, err := s.CreateFormula(ctx, ac241("DOUGH-2"), store.Idem{Key: "f-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := s.CreateFormula(ctx, ac241("DOUGH-1"), store.Idem{Key: "f-2", Fingerprint: "fp-2"}); problemCode(err) != store.CodeFormulaDuplicate {
		t.Fatalf("duplicate code: %v", err)
	}
	evts := outboxEvents(t, admin, pmfevents.FormulaCreated)
	if len(evts) != 1 || evts[0].TenantID != testkit.TenantID(1) || evts[0].Subject != f.ID {
		t.Fatalf("events %+v", evts)
	}
	list, err := s.ListFormulas(ctx, 10)
	if err != nil || len(list) != 1 || list[0].LineCount != 4 || list[0].Lines != nil {
		t.Fatalf("list %+v err=%v", list, err)
	}
}

// AC-24.9: a percent-controlled formula summing to 99.5% is refused and nothing is written.
func TestCreateFormula_PercentSumRefusedWithoutWriting(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := store.Formula{Code: "MIX-1", Name: "Mix", ItemID: ids.New(), Size: dec("100"), Unit: "kg", Lines: []store.FormulaLine{
		{LineNumber: 1, ItemID: ids.New(), Type: domain.LineIngredient, Percent: dec("59.5"), Scalable: true, PercentControlled: true},
		{LineNumber: 2, ItemID: ids.New(), Type: domain.LineIngredient, Percent: dec("40"), Scalable: true, PercentControlled: true},
	}}
	if _, _, err := s.CreateFormula(ctx, f, store.Idem{}); problemCode(err) != domain.CodePercentSum {
		t.Fatalf("err = %v, want PMF-0001", err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select (select count(*) from formula) + (select count(*) from outbox)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows written: %d %v", n, err)
	}
}

// AC-24.1 end to end: the order snapshots the formula, scales the scalable
// lines ×2.5, keeps the 50 kg line, numbers per legal entity and emits the
// created event with the co-product line.
func TestCreateBatchOrder_ScalesSnapshotNumbersAndEmits(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f, _, err := s.CreateFormula(ctx, ac241("DOUGH-1"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	o, created, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("2500")}, store.Idem{Key: "o-1", Fingerprint: "fp"})
	if err != nil || !created {
		t.Fatalf("create: %v", err)
	}
	if o.OrderNumber != "BO-000001" || o.Status != "planned" || o.Version != 1 || !o.ScaleFactor.Equal(dec("2.5")) || o.CreatedBy != "planner-1" {
		t.Fatalf("order %+v", o)
	}
	got, err := s.GetBatchOrder(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1500", "875", "50", "250"}
	for i, w := range want {
		if !got.Lines[i].Qty.Equal(dec(w)) {
			t.Fatalf("line %d qty %s, want %s", i+1, got.Lines[i].Qty, w)
		}
	}
	second, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("1000")}, store.Idem{})
	if err != nil || second.OrderNumber != "BO-000002" {
		t.Fatalf("second order %q %v", second.OrderNumber, err)
	}
	other, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityB, FormulaID: f.ID, BatchSize: dec("1000")}, store.Idem{})
	if err != nil || other.OrderNumber != "BO-000001" {
		t.Fatalf("first order of another legal entity %q %v", other.OrderNumber, err)
	}
	replayed, created, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("2500")}, store.Idem{Key: "o-1", Fingerprint: "fp"})
	if err != nil || created || replayed.ID != o.ID || len(replayed.Lines) != 4 {
		t.Fatalf("replay created=%v id=%s err=%v", created, replayed.ID, err)
	}
	evts := outboxEvents(t, admin, pmfevents.BatchOrderCreated)
	if len(evts) != 3 {
		t.Fatalf("created events = %d, want 3", len(evts))
	}
	var data pmfevents.BatchOrderCreatedData
	if err := json.Unmarshal(evts[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.OrderID != o.ID || data.ScaleFactor != "2.5" || len(data.CoProducts) != 1 || data.CoProducts[0].Qty != "250" || evts[0].Actor != "planner-1" {
		t.Fatalf("event data %+v", data)
	}
	if _, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: ids.New(), BatchSize: dec("1")}, store.Idem{}); problemCode(err) != store.CodeFormulaNotFound {
		t.Fatalf("missing formula: %v", err)
	}
}

// Row-level security: another tenant sees nothing; a caller scoped to one
// legal entity sees only that entity's orders.
func TestBatchOrders_TenantAndLegalEntityIsolation(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f, _, err := s.CreateFormula(ctx, ac241("DOUGH-1"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("10")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityB, FormulaID: f.ID, BatchSize: dec("10")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBatchOrder(ctxFor(2, ""), a.ID); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("other tenant read the order: %v", err)
	}
	if list, err := s.ListFormulas(ctxFor(2, ""), 10); err != nil || len(list) != 0 {
		t.Fatalf("other tenant formulas %d %v", len(list), err)
	}
	scoped := ctxFor(1, entityA)
	list, err := s.ListBatchOrders(scoped, store.OrderFilter{Limit: 10})
	if err != nil || len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("scoped list %+v %v", list, err)
	}
	if _, err := s.GetBatchOrder(scoped, b.ID); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("scoped caller read another entity's order: %v", err)
	}
	if list, err := s.ListBatchOrders(ctx, store.OrderFilter{Status: "released", Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("status filter %d %v", len(list), err)
	}
}

func TestRescaleBatchOrder_VersionStatusAndReplay(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f, _, err := s.CreateFormula(ctx, ac241("DOUGH-1"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("1000")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RescaleBatchOrder(ctx, o.ID, dec("2500"), 7, store.Idem{Key: "r-0", Fingerprint: "x"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	r, changed, err := s.RescaleBatchOrder(ctx, o.ID, dec("2500"), 1, store.Idem{Key: "r-1", Fingerprint: "fp"})
	if err != nil || !changed || r.Version != 2 || !r.ScaleFactor.Equal(dec("2.5")) || !r.Lines[0].Qty.Equal(dec("1500")) || !r.Lines[2].Qty.Equal(dec("50")) {
		t.Fatalf("rescale changed=%v order=%+v err=%v", changed, r, err)
	}
	stored, err := s.GetBatchOrder(ctx, o.ID)
	if err != nil || !stored.BatchSize.Equal(dec("2500")) || !stored.Lines[1].Qty.Equal(dec("875")) {
		t.Fatalf("stored after rescale %+v %v", stored, err)
	}
	again, changed, err := s.RescaleBatchOrder(ctx, o.ID, dec("2500"), 1, store.Idem{Key: "r-1", Fingerprint: "fp"})
	if err != nil || changed || again.Version != 2 {
		t.Fatalf("replay changed=%v version=%d err=%v", changed, again.Version, err)
	}
	if n := len(outboxEvents(t, admin, pmfevents.BatchOrderRescaled)); n != 1 {
		t.Fatalf("rescaled events = %d, want 1 (the replay writes none)", n)
	}
	if _, err := admin.Exec(context.Background(), `update batch_order set status = 'started' where id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RescaleBatchOrder(ctx, o.ID, dec("3000"), 2, store.Idem{Key: "r-2", Fingerprint: "fp"}); problemCode(err) != store.CodeOrderNotRescalable {
		t.Fatalf("started order rescaled: %v", err)
	}
	if _, _, err := s.RescaleBatchOrder(ctx, ids.New(), dec("3000"), 1, store.Idem{}); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("missing order: %v", err)
	}
}
