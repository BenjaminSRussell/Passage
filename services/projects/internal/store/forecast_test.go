package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

func forecastLine(period string, typ domain.TxnType, wbs *string, qty, cost, sales string) domain.ForecastLine {
	return domain.ForecastLine{Period: period, TxnType: typ, WBSCode: wbs, Quantity: qty, CostAmount: cost, SalesAmount: sales}
}

// TestForecasts_CreateReplaceAndCompareWithActuals covers FR-41.20: a model
// with lines on WORKING nodes, replay and key reuse, duplicate names, If-Match
// replace, and forecast vs actual over recorded transactions.
func TestForecasts_CreateReplaceAndCompareWithActuals(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(3)
	billingProject(t, s, ctx, "ES-FC", domain.TypeTimeMaterial)
	working, _, err := s.CreatePlanVersion(ctx, "ES-FC", NewPlan{Kind: domain.KindWorking}, "fc-w")
	if err != nil {
		t.Fatal(err)
	}
	nodes, deps := wbsInput()
	if _, err := s.ReplaceWBS(ctx, "ES-FC", working.ID, 1, nodes, deps); err != nil {
		t.Fatal(err)
	}

	in := ForecastInput{Name: "Q4 plan", Lines: []domain.ForecastLine{
		forecastLine("2026-10", domain.TxnHour, strPtr("1.2"), "10", "500.005", "900"),
		forecastLine("2026-11", domain.TxnHour, nil, "20", "1000", "1800"),
	}}
	m, created, err := s.CreateForecastModel(ctx, "ES-FC", in, "fc-1")
	if err != nil || !created || m.Version != 1 || m.Currency != "USD" || m.LineCount != 2 || m.CreatedBy != "user-test" {
		t.Fatalf("create: %+v created=%v %v", m, created, err)
	}
	if m.Lines[0].CostAmount != "500.00" || m.TotalCost != "1500.00" || m.TotalSales != "2700.00" {
		t.Fatalf("rounding and totals: %+v", m)
	}
	again, created, err := s.CreateForecastModel(ctx, "ES-FC", in, "fc-1")
	if err != nil || created || again.ID != m.ID {
		t.Fatalf("replay: %+v created=%v %v", again, created, err)
	}
	if _, _, err := s.CreateForecastModel(ctx, "ES-FC", ForecastInput{Name: "Other", Lines: in.Lines}, "fc-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := s.CreateForecastModel(ctx, "ES-FC", in, "fc-2"); ruleCode(err) != domain.CodeForecastDuplicate {
		t.Fatalf("duplicate name: %v", err)
	}
	bad := ForecastInput{Name: "Bad", Lines: []domain.ForecastLine{forecastLine("2026-10", domain.TxnHour, strPtr("7"), "1", "1", "1")}}
	if _, _, err := s.CreateForecastModel(ctx, "ES-FC", bad, "fc-3"); problemCode(err) != CodeValidation {
		t.Fatalf("node outside WORKING: %v", err)
	}

	// Actuals: 8 h in October, 4 h in November at cost 50, sales 90.005 → 90.01 per line.
	mustTxn(t, s, ctx, hours("ES-FC", "CHARGEABLE", "2026-10-05", "8"))
	mustTxn(t, s, ctx, hours("ES-FC", "NON_CHARGEABLE", "2026-11-02", "4"))
	cmp, err := s.GetForecast(ctx, "ES-FC", m.ID)
	if err != nil || len(cmp.Model.Lines) != 2 || len(cmp.Rows) != 2 {
		t.Fatalf("compare: %+v %v", cmp, err)
	}
	oct := cmp.Rows[0]
	if oct.Period != "2026-10" || oct.ForecastQuantity != "10.000" || oct.ActualQuantity != "8.000" || oct.ActualCost != "400.00" ||
		oct.CostVariance != "-100.00" || oct.ActualSales != "720.04" || oct.SalesVariance != "-179.96" {
		t.Fatalf("October: %+v", oct)
	}
	if nov := cmp.Rows[1]; nov.ActualCost != "200.00" || nov.CostVariance != "-800.00" {
		t.Fatalf("November: %+v", nov)
	}

	if _, err := s.ReplaceForecast(ctx, "ES-FC", m.ID, 2, in); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale replace: %v", err)
	}
	other, _, err := s.CreateForecastModel(ctx, "ES-FC", ForecastInput{Name: "Stretch", Lines: nil}, "fc-4")
	if err != nil || other.LineCount != 0 || other.TotalCost != "0.00" {
		t.Fatalf("empty model: %+v %v", other, err)
	}
	if _, err := s.ReplaceForecast(ctx, "ES-FC", other.ID, 1, ForecastInput{Name: "Q4 plan"}); ruleCode(err) != domain.CodeForecastDuplicate {
		t.Fatalf("rename onto another model's name: %v", err)
	}
	upd, err := s.ReplaceForecast(ctx, "ES-FC", m.ID, 1, ForecastInput{Name: "Q4 plan v2", Lines: in.Lines[1:]})
	if err != nil || upd.Version != 2 || upd.Name != "Q4 plan v2" || len(upd.Lines) != 1 || upd.Lines[0].LineNo != 1 || upd.TotalCost != "1000.00" {
		t.Fatalf("replace: %+v %v", upd, err)
	}
	list, err := s.ListForecastModels(ctx, "ES-FC")
	if err != nil || len(list) != 2 || list[0].Name != "Q4 plan v2" || list[0].LineCount != 1 || list[0].Lines != nil {
		t.Fatalf("list: %+v %v", list, err)
	}

	// Tenant and legal-entity scope.
	if _, err := s.GetForecast(tenantCtx(4), "ES-FC", m.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("other tenant reads the model: %v", err)
	}
	if _, err := s.ListForecastModels(leCtx(3, ids.New()), "ES-FC"); problemCode(err) != CodeNotFound {
		t.Fatalf("other legal entity lists models: %v", err)
	}
	if _, _, err := s.CreateForecastModel(tenantCtx(4), "ES-FC", ForecastInput{Name: "X"}, "fc-5"); problemCode(err) != CodeNotFound {
		t.Fatalf("other tenant creates a model: %v", err)
	}
	foreign := leCtx(3, ids.New())
	if _, err := s.GetForecast(foreign, "ES-FC", m.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("other legal entity reads the model: %v", err)
	}
	if _, _, err := s.CreateForecastModel(foreign, "ES-FC", ForecastInput{Name: "X"}, "fc-6"); problemCode(err) != CodeNotFound {
		t.Fatalf("other legal entity creates a model: %v", err)
	}
	if _, err := s.ReplaceForecast(foreign, "ES-FC", m.ID, upd.Version, in); problemCode(err) != CodeNotFound {
		t.Fatalf("other legal entity replaces the model: %v", err)
	}
	if _, err := s.ActivateForecastModel(foreign, "ES-FC", m.ID, upd.Version); problemCode(err) != CodeNotFound {
		t.Fatalf("other legal entity activates the model: %v", err)
	}
	if _, err := s.GetForecast(ctx, "ES-FC", ids.New()); problemCode(err) != CodeNotFound {
		t.Fatalf("unknown model: %v", err)
	}
}

// TestForecastGrants_AppRoleCannotDeleteModels checks the slice-5 grants: the
// app role deletes forecast lines (replace) but never models.
func TestForecastGrants_AppRoleCannotDeleteModels(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from project_forecast_model`)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("delete from project_forecast_model = %v; want insufficient_privilege 42501", err)
	}
}

// TestTransactionsAndProposals_CursorPages walks both lists page by page (D3e).
func TestTransactionsAndProposals_CursorPages(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(5)
	billingProject(t, s, ctx, "ES-PAGE", domain.TypeTimeMaterial)
	want := []string{}
	for _, date := range []string{"2026-10-01", "2026-10-03", "2026-10-03", "2026-10-02", "2026-10-05"} {
		want = append(want, mustTxn(t, s, ctx, hours("ES-PAGE", "CHARGEABLE", date, "1")).ID)
	}
	var got []domain.Transaction
	after, pages := "", 0
	for {
		page, err := s.ListTransactions(ctx, "ES-PAGE", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		got = append(got, page.Items...)
		if page.NextID == "" {
			break
		}
		after = page.NextID
	}
	if pages != 3 || len(got) != len(want) {
		t.Fatalf("%d pages, %d rows", pages, len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].TxnDate < got[i].TxnDate || (got[i-1].TxnDate == got[i].TxnDate && got[i-1].CreatedAt.Before(got[i].CreatedAt)) {
			t.Fatalf("not newest first at %d: %s %s", i, got[i-1].TxnDate, got[i].TxnDate)
		}
	}
	seen := map[string]bool{}
	for _, tx := range got {
		if seen[tx.ID] {
			t.Fatalf("transaction %s on two pages", tx.ID)
		}
		seen[tx.ID] = true
	}
	if page, err := s.ListTransactions(ctx, "ES-PAGE", ids.New(), 2); err != nil || len(page.Items) != 0 {
		t.Fatalf("unknown cursor: %+v %v", page, err)
	}

	for i := 0; i < 3; i++ {
		day := fmt.Sprintf("2026-10-%02d", i+1)
		if _, _, err := s.CreateProposal(ctx, domain.ProposalRequest{ProjectID: "ES-PAGE", From: day, To: day}, ids.New()); err != nil {
			t.Fatalf("proposal %d: %v", i, err)
		}
	}
	first, err := s.ListProposals(ctx, ProposalFilter{ProjectID: "ES-PAGE", Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextID == "" {
		t.Fatalf("first proposal page: %+v %v", first, err)
	}
	second, err := s.ListProposals(ctx, ProposalFilter{ProjectID: "ES-PAGE", Limit: 2, AfterID: first.NextID})
	if err != nil || len(second.Items) != 1 || second.NextID != "" || second.Items[0].ID == first.Items[0].ID || second.Items[0].ID == first.Items[1].ID {
		t.Fatalf("second proposal page: %+v %v", second, err)
	}
}

// TestForecasts_ConcurrentSameKeyReplays sends one create five times at once:
// exactly one creates the model and the others replay it (the project row lock
// serialises them, so none sees the unique key as a reuse).
func TestForecasts_ConcurrentSameKeyReplays(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(4)
	billingProject(t, s, ctx, "ES-FCK", domain.TypeTimeMaterial)
	in := ForecastInput{Name: "Q4", Lines: []domain.ForecastLine{forecastLine("2026-10", domain.TxnFee, nil, "1", "0", "100")}}
	var wg sync.WaitGroup
	results := make([]domain.ForecastModel, 5)
	created := make([]bool, 5)
	errs := make([]error, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], created[i], errs[i] = s.CreateForecastModel(ctx, "ES-FCK", in, "fc-same")
		}(i)
	}
	wg.Wait()
	n := 0
	for i := range results {
		if errs[i] != nil || results[i].ID != results[0].ID || results[i].LineCount != 1 {
			t.Fatalf("request %d: %+v %v", i, results[i], errs[i])
		}
		if created[i] {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d requests created a model, want 1", n)
	}
}
