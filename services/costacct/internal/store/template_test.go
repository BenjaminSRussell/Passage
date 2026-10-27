package store

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	ccevents "github.com/BenjaminSRussell/passage/services/costacct/internal/events"
)

type templateFixture struct {
	ledgerFixture
	tenantID      string
	sender, order string
	lots          string
}

// newTemplateLedger adds a MATERIAL primary element, a quality cost center (the
// sender), an internal order with 20,000.00 material cost in USD (and 1,000.00 in
// EUR, which a USD template must ignore) and 4 inspection lots in 2026-09.
func newTemplateLedger(t *testing.T, tenant int) templateFixture {
	t.Helper()
	f := templateFixture{ledgerFixture: newLedger(t, tenant), tenantID: testkit.TenantID(tenant)}
	if _, _, err := f.st.CreateCostElement(f.ctx, ids.New(), "h", CostElement{LegalEntityID: f.le, Kind: "Primary", Code: "MATERIAL", Name: "Material",
		MainAccountFrom: "500000", MainAccountTo: "509999"}); err != nil {
		t.Fatal(err)
	}
	f.sender = f.object(t, "CostCenter", "QUALITY")
	f.order = f.object(t, "InternalOrder", "ORDER-1")
	if _, _, err := f.st.MirrorJournalLines(f.ctx, ids.New(), "h", MirrorRequest{LegalEntityID: f.le, Lines: []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "500100", CostObjectID: f.order, Period: "2026-09", Currency: "USD", Amount: dec("20000.00")},
		{JournalLineID: ids.New(), MainAccount: "500100", CostObjectID: f.order, Period: "2026-09", Currency: "EUR", Amount: dec("1000.00")},
	}}); err != nil {
		t.Fatal(err)
	}
	f.lots = f.measure(t, "inspectionLots")
	if _, _, err := f.st.LoadMeasureValues(f.ctx, ids.New(), "h", MeasureValuesRequest{MeasureID: f.lots, LegalEntityID: f.le, Period: "2026-09",
		Values: []MeasureValue{{CostObjectID: f.order, Value: dec("4")}}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f templateFixture) measure(t *testing.T, code string) string {
	t.Helper()
	status, raw, err := f.st.CreateMeasure(f.ctx, ids.New(), "h", StatisticalMeasure{LegalEntityID: f.le, Code: code, Name: code, Unit: "lot"})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create measure %s: %v", code, err)
	}
	return idOf(t, raw)
}

func (f templateFixture) template(code, formula string, allowNegative bool) AllocationTemplate {
	return AllocationTemplate{LegalEntityID: f.le, Code: code, Name: code, Currency: "USD", SenderCostObjectID: f.sender, AllowNegative: allowNegative,
		Lines: []TemplateLine{{ReceiverCostObjectID: f.order, SecondaryElementID: f.secondary, Formula: formula}}}
}

func (f templateFixture) save(t *testing.T, tpl AllocationTemplate, expected int) AllocationTemplate {
	t.Helper()
	_, raw, err := f.st.SaveTemplate(f.ctx, ids.New(), "h", tpl, expected)
	if err != nil {
		t.Fatalf("save %s: %v", tpl.Code, err)
	}
	var saved AllocationTemplate
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

// AC-38.14: 25.00 × 4 inspection lots + 0.005 × 20,000.00 material gives the
// order 200.00 and credits the sender 200.00; the run nets to zero.
func TestTemplateRun_AC38_14_WorkedExample(t *testing.T) {
	f := newTemplateLedger(t, 4)
	tpl := f.save(t, f.template("TPL-INSP", "25.00 × m.inspectionLots + 0.005 × b.MATERIAL", false), 0)
	if tpl.Version != 1 {
		t.Fatalf("version %d", tpl.Version)
	}

	dry, err := f.st.DryRunTemplate(f.ctx, TemplateRunRequest{TemplateID: tpl.ID, Period: "2026-09"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Lines) != 1 || !dry.Lines[0].Amount.Equal(dec("200.00")) || dry.TemplateVersion != 1 {
		t.Fatalf("dry run: %+v", dry)
	}
	settle := "tenant_id = $1 and cost_element_id = $2"
	if got := sumEntries(t, f.ctx, f.st.pool, settle+" and cost_object_id = $3", f.tenantID, f.secondary, f.order); !got.IsZero() {
		t.Fatalf("a dry run posted %s", got)
	}

	status, raw, err := f.st.RunTemplate(f.ctx, "run-1", "h", TemplateRunRequest{TemplateID: tpl.ID, Period: "2026-09"})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("run: %d %v", status, err)
	}
	var run TemplateRun
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if !run.Total.Equal(dec("200")) || run.SenderCostObjectID != f.sender || run.TemplateVersion != 1 {
		t.Fatalf("run: %+v", run)
	}
	if got := sumEntries(t, f.ctx, f.st.pool, settle+" and cost_object_id = $3", f.tenantID, f.secondary, f.order); !got.Equal(dec("200")) {
		t.Errorf("order received %s, want 200.00", got)
	}
	if got := sumEntries(t, f.ctx, f.st.pool, settle+" and cost_object_id = $3", f.tenantID, f.secondary, f.sender); !got.Equal(dec("-200")) {
		t.Errorf("sender credited %s, want -200.00", got)
	}
	if got := sumEntries(t, f.ctx, f.st.pool, "tenant_id = $1 and template_run_id = $2", f.tenantID, run.ID); !got.IsZero() {
		t.Errorf("run entries net to %s, want 0", got)
	}
	var method, templateID string
	if err := f.st.pool.QueryRow(f.ctx, `select payload->'data'->>'method', payload->'data'->>'template_id' from outbox where type = $1 and aggregate_id = $2`,
		ccevents.AllocationCompleted, run.ID).Scan(&method, &templateID); err != nil || method != "Template" || templateID != tpl.ID {
		t.Fatalf("allocation.completed: %q %q %v", method, templateID, err)
	}
	entries, err := f.st.EntriesFor(f.ctx, f.order, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	linked := 0
	for _, e := range entries {
		if e.TplRunID == run.ID {
			linked++
		}
	}
	if linked != 1 {
		t.Errorf("order entries linked to the run: %d", linked)
	}

	if _, _, err := f.st.RunTemplate(f.ctx, "run-2", "h", TemplateRunRequest{TemplateID: tpl.ID, Period: "2026-09"}); !hasCode(err, CodePeriodAllocated) {
		t.Fatalf("second run: %v", err)
	}
	runs, err := f.st.ListTemplateRuns(f.ctx, f.le, tpl.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("list runs: %v %d", err, len(runs))
	}
}

func TestSaveTemplate_ReferencesAndVersions(t *testing.T) {
	f := newTemplateLedger(t, 5)
	for name, formula := range map[string]string{
		"unknown measure": "25 × m.lots",
		"unknown element": "0.005 × b.MAT",
		"syntax":          "25 ×",
	} {
		if _, _, err := f.st.SaveTemplate(f.ctx, ids.New(), "h", f.template("TPL-BAD", formula, false), 0); !hasCode(err, CodeFormulaInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	tpl := f.save(t, f.template("TPL-INSP", "25 × m.inspectionLots", false), 0)
	if _, _, err := f.st.SaveTemplate(f.ctx, ids.New(), "h", f.template("TPL-INSP", "30 × m.inspectionLots", false), 0); !hasCode(err, CodeTemplateVersion) {
		t.Fatalf("stale save: %v", err)
	}
	v2 := f.save(t, f.template("TPL-INSP", "30 × m.inspectionLots", false), 1)
	if v2.ID != tpl.ID || v2.Version != 2 || v2.Lines[0].Formula != "30 × m.inspectionLots" {
		t.Fatalf("second version: %+v", v2)
	}
	if _, _, err := f.st.SaveTemplate(f.ctx, ids.New(), "h", f.template("TPL-NEW", "1", false), 3); !hasCode(err, CodeTemplateVersion) {
		t.Fatalf("new code with a version: %v", err)
	}
	bad := f.template("TPL-X", "1", false)
	bad.Lines[0].SecondaryElementID = f.primary
	if _, _, err := f.st.SaveTemplate(f.ctx, ids.New(), "h", bad, 0); !hasCode(err, CodeSecondaryNotFound) {
		t.Fatalf("primary element on a line: %v", err)
	}
	if _, err := f.st.GetTemplate(f.ctx, ids.New()); !hasCode(err, CodeTemplateNotFound) {
		t.Fatalf("missing template: %v", err)
	}
}

// BR-38.12: a negative receiver amount fails the run unless allow-negative is
// set; dividing by a zero measure gives 0 for that receiver and a CO-0622 warning.
func TestTemplateRun_NegativeAndZeroDivisor(t *testing.T) {
	f := newTemplateLedger(t, 6)
	f.measure(t, "zeroLots")
	neg := f.save(t, f.template("TPL-NEG", "0 - m.inspectionLots", false), 0)
	if _, _, err := f.st.RunTemplate(f.ctx, ids.New(), "h", TemplateRunRequest{TemplateID: neg.ID, Period: "2026-09"}); !hasCode(err, CodeNegativeReceiver) {
		t.Fatalf("negative without allow-negative: %v", err)
	}
	allowed := f.save(t, f.template("TPL-NEG", "0 - m.inspectionLots", true), 1)
	if _, raw, err := f.st.RunTemplate(f.ctx, ids.New(), "h", TemplateRunRequest{TemplateID: allowed.ID, Period: "2026-09"}); err != nil {
		t.Fatalf("negative with allow-negative: %v", err)
	} else {
		var run TemplateRun
		_ = json.Unmarshal(raw, &run)
		if !run.Total.Equal(dec("-4")) || run.TemplateVersion != 2 {
			t.Fatalf("run: %+v", run)
		}
	}
	div := f.save(t, f.template("TPL-DIV", "100 / m.zeroLots", false), 0)
	dry, err := f.st.DryRunTemplate(f.ctx, TemplateRunRequest{TemplateID: div.ID, Period: "2026-09"})
	if err != nil {
		t.Fatal(err)
	}
	if !dry.Lines[0].Amount.IsZero() || !dry.Lines[0].DivByZero || len(dry.Warnings) != 1 || dry.Warnings[0][:7] != domain.CodeMeasureZero {
		t.Fatalf("zero divisor: %+v", dry)
	}
}
