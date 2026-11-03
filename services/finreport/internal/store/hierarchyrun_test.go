package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

func runHierarchyFixture(t *testing.T, st *Postgres, ctx context.Context) (ledger string) {
	t.Helper()
	h := domain.Hierarchy{Code: "ACCT-GRP", Name: "Account groups", Subject: domain.SubjectAccount, Nodes: []domain.HierarchyNode{
		{Code: "ASSETS", Label: "Assets"},
		{Code: "CASH", Label: "Cash", Parent: "ASSETS", Rules: []domain.Rule{{Values: []string{"10*"}}}},
		{Code: "RECV", Label: "Receivables", Parent: "ASSETS", Rules: []domain.Rule{{Values: []string{"12*"}}}},
		{Code: "EQUITY", Label: "Equity", Rules: []domain.Rule{{Values: []string{"30*"}}}},
	}}
	if _, _, err := st.SaveHierarchy(ctx, h, nil, "owner"); err != nil {
		t.Fatal(err)
	}
	members := []domain.Member{{ID: "1000", Key: "1000"}, {ID: "1200", Key: "1200"}, {ID: "3000", Key: "3000"}}
	if _, _, err := st.PublishHierarchy(ctx, "ACCT-GRP", "publisher", members); err != nil {
		t.Fatal(err)
	}
	def := storeFixtureDefinition("TB-HIER")
	def.Rows = def.Rows[:1]
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "TB-HIER", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger = ids.New()
	for _, b := range []struct{ account, net string }{{"1000", "400"}, {"1200", "250"}, {"3000", "-600"}, {"9999", "-50"}} {
		seedBalance(t, ctx, st.pool, ledger, 2026, 3, b.account, "acct", b.net, 77)
	}
	return ledger
}

// A run over a hierarchy evaluates one row per node over the ledger's accounts,
// keeps that expanded definition with the run, and every cell still drills.
func TestCreateRun_OverHierarchy(t *testing.T) {
	st, ctx := migrated(t)
	ledger := runHierarchyFixture(t, st, ctx)
	spec := RunSpec{Code: "TB-HIER", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: "h-1", RequestHash: "h1",
		Hierarchy: &HierarchyRun{Code: "ACCT-GRP"}}
	run, err := st.CreateRun(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if run.Hierarchy == nil || run.Hierarchy.Code != "ACCT-GRP" || run.Hierarchy.Version != 1 {
		t.Fatalf("run hierarchy = %+v, want ACCT-GRP v1", run.Hierarchy)
	}
	if len(run.Checks) != 1 || run.Checks[0].Code != domain.CodeHierarchyRunUnassigned || !strings.Contains(run.Checks[0].Detail, "9999") {
		t.Fatalf("checks = %+v, want REPORT-0034 naming 9999", run.Checks)
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil || got.Hierarchy == nil || got.Hierarchy.Version != 1 {
		t.Fatalf("stored run = %+v err %v", got, err)
	}
	def, err := st.RunDefinition(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, r := range def.Rows {
		codes = append(codes, r.Code)
	}
	if strings.Join(codes, ",") != "CASH,RECV,ASSETS,EQUITY,UNASSIGNED,TOTAL" {
		t.Fatalf("expanded rows = %v", codes)
	}
	cells, err := st.ListCells(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CASH": "400", "RECV": "250", "ASSETS": "650", "EQUITY": "-600", "UNASSIGNED": "-50", "TOTAL": "0"}
	for _, c := range cells {
		if err := c.CheckDrill(); err != nil {
			t.Fatalf("drill %s: %v", c.Row, err)
		}
		if !c.Value.Equal(decimal.RequireFromString(want[c.Row])) {
			t.Errorf("%s = %s, want %s", c.Row, c.Value, want[c.Row])
		}
	}
	// Output and drill level 2 read the expanded definition, not the published one.
	_, outDef, _, err := st.RunOutput(ctx, run.ID)
	if err != nil || len(outDef.Rows) != len(def.Rows) {
		t.Fatalf("run output definition has %d rows, err %v; want %d", len(outDef.Rows), err, len(def.Rows))
	}
	page, err := st.DrillLines(ctx, run.ID, "CASH", "C01", LineQuery{Key: -1, Limit: 10})
	if err != nil || !page.Value.Equal(decimal.NewFromInt(400)) {
		t.Fatalf("drill lines of CASH = %+v err %v", page, err)
	}
	// The key replays; the event and the audit entry name the hierarchy.
	again, err := st.CreateRun(ctx, spec)
	if err != nil || again.ID != run.ID || again.Hierarchy == nil {
		t.Fatalf("replay = %+v err %v", again, err)
	}
	if n := countType(outboxTypes(t, ctx, st.pool), events.StatementGenerated); n != 1 {
		t.Fatalf("statement.generated events = %d, want 1", n)
	}
	var param, audited string
	if err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select payload->'data'->'parameters'->>'hierarchy' from outbox where type=$1`, events.StatementGenerated).Scan(&param); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select details->>'hierarchy' from audit_trail_entry where object_ref=$1`, "run:"+run.ID).Scan(&audited)
	}); err != nil {
		t.Fatal(err)
	}
	if param != "ACCT-GRP@1" || audited != "ACCT-GRP@1" {
		t.Fatalf("event parameter %q, audit entry %q; want ACCT-GRP@1 in both", param, audited)
	}
}

func TestCreateRun_OverHierarchyRefusals(t *testing.T) {
	st, ctx := migrated(t)
	ledger := runHierarchyFixture(t, st, ctx)
	base := RunSpec{Code: "TB-HIER", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), RequestedBy: "tester", RequestHash: "h"}
	for name, c := range map[string]struct {
		h    HierarchyRun
		code string
	}{
		"unknown":       {HierarchyRun{Code: "NOPE"}, CodeHierarchyNotFound},
		"old version":   {HierarchyRun{Code: "ACCT-GRP", Version: 9}, CodeHierarchyNotFound},
		"bad balance":   {HierarchyRun{Code: "ACCT-GRP", NormalBalance: "left"}, domain.CodeHierarchyRun},
		"account range": {HierarchyRun{Code: "ACCT-GRP", AccountFrom: "1", AccountTo: "2"}, domain.CodeHierarchyRun},
	} {
		spec := base
		spec.Hierarchy = &c.h
		if _, err := st.CreateRun(ctx, spec); problemCode(err) != c.code {
			t.Errorf("%s: err = %v, want %s", name, err, c.code)
		}
	}
	if runs, _ := st.ListRuns(ctx, "TB-HIER", 10); len(runs) != 0 {
		t.Fatalf("a refused run stored %d runs", len(runs))
	}
	// A plain run keeps serving its published definition.
	plain := base
	plain.IdempotencyKey = "plain"
	run, err := st.CreateRun(ctx, plain)
	if err != nil || run.Hierarchy != nil {
		t.Fatalf("plain run = %+v err %v", run, err)
	}
	if def, err := st.RunDefinition(ctx, run.ID); err != nil || def.Code != "TB-HIER" || len(def.Rows) != 1 {
		t.Fatalf("plain run definition = %+v err %v", def, err)
	}
}
