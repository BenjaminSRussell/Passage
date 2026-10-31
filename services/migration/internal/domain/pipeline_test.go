package domain_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const system = "ax2012"

func newPipeline(rows []domain.Record) (*domain.Pipeline, *domain.MemXref, *domain.MemLoader) {
	xref, loader := domain.NewMemXref(), domain.NewMemLoader()
	return &domain.Pipeline{Extractor: sliceExtractor{rows: rows}, Loader: loader, Reader: loader, Xref: xref, IDGen: counterIDs()}, xref, loader
}

func spec(t testing.TB) domain.RunSpec {
	return domain.RunSpec{SourceSystem: system, Entity: "customer", Mapping: customerMapping(t)}
}

func TestPipeline_LoadsAndReconcilesAXDataset(t *testing.T) {
	rows := axDataset(1000, 0, 0, 0).Customers
	p, _, loader := newPipeline(rows)
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != domain.StatusSucceeded || rep.Created != 1000 || len(loader.Rows) != 1000 || loader.Calls != 1 {
		t.Fatalf("status=%s created=%d loaded=%d calls=%d", rep.Status, rep.Created, len(loader.Rows), loader.Calls)
	}
	if rep.Reconciliation == nil || !rep.Reconciliation.OK() {
		t.Fatalf("reconciliation = %+v", rep.Reconciliation)
	}
}

func TestPipeline_WarnedRowsAreLoadedAndListed(t *testing.T) {
	m := parseMapping(t, fieldDoc("", `{"source":"st","target":"status","type":"string","enum":{"A":"active"},"enumDefault":"unknown"}`))
	rows := []domain.Record{
		{Key: "C1", Fields: map[string]any{"st": "A"}},
		{Key: "C2", Fields: map[string]any{"st": "Blocked"}},
	}
	p, _, loader := newPipeline(rows)
	rep, err := p.Run(context.Background(), domain.RunSpec{SourceSystem: system, Entity: "customer", Mapping: m})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 1 || rep.Warnings[0].SourceKey != "C2" || rep.Warnings[0].Warnings[0].Code != domain.CodeMapEnumDefault {
		t.Fatalf("warnings %+v", rep.Warnings)
	}
	if rep.Created != 2 || len(loader.Rows) != 2 || len(rep.Rejects) != 0 {
		t.Fatalf("created=%d loaded=%d rejects=%+v", rep.Created, len(loader.Rows), rep.Rejects)
	}
}

func TestPipeline_BatchesAtMostThousand(t *testing.T) {
	p, _, loader := newPipeline(axDataset(2500, 0, 0, 0).Customers)
	if _, err := p.Run(context.Background(), spec(t)); err != nil {
		t.Fatal(err)
	}
	if loader.Calls != 3 {
		t.Fatalf("calls = %d, want 3 batches for 2500 rows", loader.Calls)
	}
}

func TestPipeline_RerunZeroChanges(t *testing.T) {
	p, xref, loader := newPipeline(axDataset(300, 0, 0, 0).Customers)
	if _, err := p.Run(context.Background(), spec(t)); err != nil {
		t.Fatal(err)
	}
	calls, puts := loader.Calls, xref.Puts
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if loader.Calls != calls || xref.Puts != puts {
		t.Fatalf("second run changed state: loader calls %d->%d, xref puts %d->%d", calls, loader.Calls, puts, xref.Puts)
	}
	if rep.Unchanged != 300 || rep.Created != 0 || rep.Updated != 0 || rep.Status != domain.StatusSucceeded {
		t.Fatalf("report = %+v", rep)
	}
}

func TestPipeline_ChangedRowReloadsWithSameTargetID(t *testing.T) {
	rows := axDataset(3, 0, 0, 0).Customers
	p, xref, loader := newPipeline(rows)
	if _, err := p.Run(context.Background(), spec(t)); err != nil {
		t.Fatal(err)
	}
	before := xref.Entries[domain.XrefKey{SourceSystem: system, Entity: "customer", SourceKey: "C000001"}]
	rows[1].Fields["Name"] = "Renamed"
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	after := xref.Entries[domain.XrefKey{SourceSystem: system, Entity: "customer", SourceKey: "C000001"}]
	if rep.Updated != 1 || rep.Unchanged != 2 || before.TargetID != after.TargetID || before.ContentHash == after.ContentHash {
		t.Fatalf("updated=%d unchanged=%d before=%+v after=%+v", rep.Updated, rep.Unchanged, before, after)
	}
	if loader.Rows[after.TargetID].Fields["name"] != "Renamed" {
		t.Fatalf("target not updated: %+v", loader.Rows[after.TargetID])
	}
}

type xrefCheckingLoader struct {
	xref  *domain.MemXref
	inner *domain.MemLoader
	t     *testing.T
}

func (l xrefCheckingLoader) LoadBatch(ctx context.Context, entity string, items []domain.LoadItem) ([]domain.LoadResult, error) {
	for _, it := range items {
		e, ok, _ := l.xref.Get(ctx, domain.XrefKey{SourceSystem: system, Entity: entity, SourceKey: it.SourceKey})
		if !ok || e.TargetID != it.TargetID || it.TargetID == it.SourceKey {
			l.t.Errorf("xref not stored before load for %s: %+v", it.SourceKey, e)
		}
	}
	return l.inner.LoadBatch(ctx, entity, items)
}

func TestPipeline_TargetIDStoredInXrefBeforeLoad(t *testing.T) {
	p, xref, loader := newPipeline(axDataset(5, 0, 0, 0).Customers)
	p.Loader = xrefCheckingLoader{xref, loader, t}
	if _, err := p.Run(context.Background(), spec(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPipeline_IdempotencyKeyIsRunAgnostic(t *testing.T) {
	a := domain.IdempotencyKey(system, "customer", "C1", "h")
	if a != domain.IdempotencyKey(system, "customer", "C1", "h") {
		t.Fatal("key must be deterministic")
	}
	for _, other := range []string{
		domain.IdempotencyKey("sap_ecc", "customer", "C1", "h"),
		domain.IdempotencyKey(system, "vendor", "C1", "h"),
		domain.IdempotencyKey(system, "customer", "C2", "h"),
		domain.IdempotencyKey(system, "customer", "C1", "h2"),
	} {
		if other == a {
			t.Fatal("every component must change the key")
		}
	}
}

func TestPipeline_DryRunWritesNothing(t *testing.T) {
	p, xref, loader := newPipeline(axDataset(50, 0, 0, 0).Customers)
	s := spec(t)
	s.DryRun = true
	rep, err := p.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if xref.Puts != 0 || loader.Calls != 0 || len(xref.Entries) != 0 {
		t.Fatalf("dry run wrote: puts=%d calls=%d", xref.Puts, loader.Calls)
	}
	if !rep.DryRun || rep.Created != 50 || len(rep.Ledger) != 50 || rep.Status != domain.StatusSucceeded {
		t.Fatalf("report = %+v", rep)
	}
}

func TestPipeline_MappingRejectsGoToExceptionQueueAndRerunReloadsOnlyThem(t *testing.T) {
	rows := axDataset(10, 0, 0, 0).Customers
	rows[4].Fields["CustGroup"] = "99"
	p, _, loader := newPipeline(rows)
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != domain.StatusPartial || len(rep.Rejects) != 1 || rep.Rejects[0].SourceKey != "C000004" || rep.Rejects[0].Rejects[0].Code != domain.CodeMapEnum {
		t.Fatalf("report = %+v", rep)
	}
	if len(loader.Rows) != 9 {
		t.Fatalf("loaded %d, want 9", len(loader.Rows))
	}
	rows[4].Fields["CustGroup"] = "20"
	callsBefore := loader.Calls
	rerun, err := p.Rerun(context.Background(), spec(t), rep)
	if err != nil {
		t.Fatal(err)
	}
	if rerun.Created != 1 || rerun.Unchanged != 0 || rerun.Status != domain.StatusSucceeded || loader.Calls != callsBefore+1 || len(loader.Rows) != 10 {
		t.Fatalf("rerun = %+v loaded=%d", rerun, len(loader.Rows))
	}
}

func TestPipeline_LoaderErrorFailsRunKeepsXrefAndRetryReusesTargetID(t *testing.T) {
	p, xref, loader := newPipeline(axDataset(4, 0, 0, 0).Customers)
	loader.Err = errors.New("target down")
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != domain.StatusFailed || len(rep.Rejects) != 4 || rep.Rejects[0].Rejects[0].Code != domain.CodeLoadFailed || len(loader.Rows) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	reserved := map[string]string{}
	for k, e := range xref.Entries {
		if e.ContentHash != "" {
			t.Fatalf("hash recorded for unloaded row %s", k.SourceKey)
		}
		reserved[k.SourceKey] = e.TargetID
	}
	if len(reserved) != 4 {
		t.Fatalf("xref reserved %d ids, want 4", len(reserved))
	}
	loader.Err = nil
	retry, err := p.Rerun(context.Background(), spec(t), rep)
	if err != nil || retry.Status != domain.StatusSucceeded || retry.Created != 4 {
		t.Fatalf("retry = %+v err=%v", retry, err)
	}
	for k, e := range xref.Entries {
		if e.TargetID != reserved[k.SourceKey] {
			t.Fatalf("target id for %s changed on retry", k.SourceKey)
		}
	}
}

func TestPipeline_PerRowLoaderRejectKeepsRowInExceptionQueue(t *testing.T) {
	p, xref, loader := newPipeline(axDataset(3, 0, 0, 0).Customers)
	loader.RejectKeys = map[string]string{"C000001": "TGT-DUPLICATE"}
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != domain.StatusFailed || rep.Created != 2 || len(rep.Rejects) != 1 || rep.Rejects[0].Rejects[0].Code != "TGT-DUPLICATE" {
		t.Fatalf("report = %+v", rep)
	}
	if xref.Entries[domain.XrefKey{SourceSystem: system, Entity: "customer", SourceKey: "C000001"}].ContentHash != "" {
		t.Fatal("rejected row must not record a content hash")
	}
}

func TestPipeline_ExtractorErrorFailsClosed(t *testing.T) {
	p, xref, loader := newPipeline(nil)
	p.Extractor = sliceExtractor{err: errors.New("odbc down")}
	rep, err := p.Run(context.Background(), spec(t))
	if err == nil || rep.Status != domain.StatusFailed || loader.Calls != 0 || xref.Puts != 0 {
		t.Fatalf("rep=%+v err=%v calls=%d puts=%d", rep, err, loader.Calls, xref.Puts)
	}
}

func TestPipeline_DuplicateSourceKeyRejected(t *testing.T) {
	rows := axDataset(2, 0, 0, 0).Customers
	rows = append(rows, rows[0])
	p, _, loader := newPipeline(rows)
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rejects) != 1 || rep.Rejects[0].Rejects[0].Code != domain.CodeDuplicate || len(loader.Rows) != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestPipeline_SourceRowsMissingSinceLastRunAreReportedNotDeleted(t *testing.T) {
	rows := axDataset(3, 0, 0, 0).Customers
	p, _, loader := newPipeline(rows)
	if _, err := p.Run(context.Background(), spec(t)); err != nil {
		t.Fatal(err)
	}
	p.Extractor = sliceExtractor{rows: rows[:2]}
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Absent) != 1 || rep.Absent[0] != "C000002" || len(loader.Rows) != 3 || rep.Status != domain.StatusSucceeded || !rep.Reconciliation.OK() {
		t.Fatalf("absent=%v loaded=%d status=%s reconciliation=%+v", rep.Absent, len(loader.Rows), rep.Status, rep.Reconciliation)
	}
}

func TestPipeline_SecondSourceSystemReconcilesOnItsOwnKeys(t *testing.T) {
	p, _, loader := newPipeline(axDataset(3, 0, 0, 0).Customers)
	if _, err := p.Run(context.Background(), spec(t)); err != nil {
		t.Fatal(err)
	}
	sap := axDataset(2, 0, 0, 0).Customers
	for i := range sap {
		sap[i].Key = "S" + sap[i].Key
	}
	p.Extractor = sliceExtractor{rows: sap}
	second := spec(t)
	second.SourceSystem = "sap_ecc"
	rep, err := p.Run(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != domain.StatusSucceeded || rep.Created != 2 || len(loader.Rows) != 5 || rep.Reconciliation.Source != 2 || rep.Reconciliation.Target != 2 {
		t.Fatalf("status=%s created=%d loaded=%d reconciliation=%+v", rep.Status, rep.Created, len(loader.Rows), rep.Reconciliation)
	}
}

func TestPipeline_EmptyExtractSucceedsWithZeroCounts(t *testing.T) {
	p, _, _ := newPipeline(nil)
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil || rep.Status != domain.StatusSucceeded || rep.Extracted != 0 || rep.Reconciliation == nil || !rep.Reconciliation.OK() {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
}

func TestPipeline_CancelledContextStopsBeforeLoading(t *testing.T) {
	p, _, loader := newPipeline(axDataset(5, 0, 0, 0).Customers)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Run(ctx, spec(t)); !errors.Is(err, context.Canceled) || loader.Calls != 0 {
		t.Fatalf("err=%v calls=%d", err, loader.Calls)
	}
}

func TestPipeline_AXDatasetAllEntitiesReconcileFully(t *testing.T) {
	set := axDataset(1000, 400, 2000, 200)
	for entity, rep := range loadAll(t, newPipelines(set)) {
		if rep.Status != domain.StatusSucceeded || !rep.Reconciliation.OK() {
			t.Fatalf("%s: rep=%+v", entity, rep)
		}
	}
}

type racingXref struct{ *domain.MemXref }

func (r racingXref) Reserve(ctx context.Context, sourceSystem, entity string, items []domain.XrefItem) (map[string]string, error) {
	for _, it := range items {
		r.Entries[domain.XrefKey{SourceSystem: sourceSystem, Entity: entity, SourceKey: it.SourceKey}] = domain.XrefEntry{TargetID: "won-" + it.SourceKey}
	}
	return r.MemXref.Reserve(ctx, sourceSystem, entity, items)
}

func TestPipeline_LosingReservationLoadsUnderWinningTargetID(t *testing.T) {
	p, xref, loader := newPipeline(axDataset(2, 0, 0, 0).Customers)
	p.Xref = racingXref{xref}
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil || rep.Created != 2 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	for id, item := range loader.Rows {
		if id != "won-"+item.SourceKey {
			t.Fatalf("row %s loaded under %s, not the reserved id", item.SourceKey, id)
		}
	}
	for _, e := range rep.Ledger {
		if e.TargetID != "won-"+e.SourceKey {
			t.Fatalf("ledger %s carries %s", e.SourceKey, e.TargetID)
		}
	}
}

func TestPipeline_LoaderFailureIsLoggedWithCause(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	p, _, loader := newPipeline(axDataset(2, 0, 0, 0).Customers)
	loader.Err = errors.New("target unreachable")
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil || rep.Status != domain.StatusFailed {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	for _, want := range []string{"target load batch failed", "target unreachable", `"batchSize":2`, `"entity":"customer"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestPipeline_RejectWhenQueuesRowWithReasonCodeAndLoadsTheRest(t *testing.T) {
	m := parseMapping(t, `{"entity":"customer","version":1,"fields":[{"source":"id","target":"id","type":"string"}],
	"rejectWhen":[{"field":"id","op":"match","value":"^TMP","code":"CUST-TEMP"}]}`)
	rows := []domain.Record{{Key: "C1", Fields: map[string]any{"id": "C1"}}, {Key: "T1", Fields: map[string]any{"id": "TMP-1"}}}
	p, _, loader := newPipeline(rows)
	rep, err := p.Run(context.Background(), domain.RunSpec{SourceSystem: system, Entity: "customer", Mapping: m})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Created != 1 || len(loader.Rows) != 1 || len(rep.Rejects) != 1 || rep.Rejects[0].SourceKey != "T1" || rep.Rejects[0].Rejects[0].Code != "CUST-TEMP" {
		t.Fatalf("created=%d rejects=%+v", rep.Created, rep.Rejects)
	}
}
