package domain_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func products(n int) []domain.Record {
	out := make([]domain.Record, n)
	for i := range out {
		k := fmt.Sprintf("USMF-%04d", i)
		out[i] = domain.Record{Key: k, Fields: map[string]any{"itemNumber": k, "dataAreaId": "USMF", "name": fmt.Sprintf("Product %d", i), "weight": int64(i)}}
	}
	return out
}

var usmfToCamf = []domain.KeyRewrite{
	{Field: domain.KeyField, From: "USMF", To: "CAMF"},
	{Field: "itemNumber", From: "USMF", To: "CAMF"},
	{Field: "dataAreaId", From: "USMF", To: "CAMF"},
}

func TestRewriteKeys_USMFToCAMF(t *testing.T) {
	in := append(products(500), domain.Record{Key: "OTHER-1", Fields: map[string]any{"itemNumber": "OTHER-1", "dataAreaId": "DEMF"}})
	out := domain.RewriteKeys(in, usmfToCamf)
	for i := 0; i < 500; i++ {
		if !strings.HasPrefix(out[i].Key, "CAMF-") || out[i].Fields["itemNumber"] != out[i].Key || out[i].Fields["dataAreaId"] != "CAMF" || out[i].Fields["weight"] != int64(i) {
			t.Fatalf("row %d = %+v", i, out[i])
		}
	}
	if out[500].Key != "OTHER-1" || out[500].Fields["dataAreaId"] != "DEMF" {
		t.Fatalf("unmatched values must pass through: %+v", out[500])
	}
	if in[0].Key != "USMF-0000" || in[0].Fields["dataAreaId"] != "USMF" {
		t.Fatal("input rows must not be modified")
	}
}

func TestRewriteKeys_DeterministicAndIdempotentProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.StringMatching(`S[A-Z]{2}`).Draw(t, "from")
		dst := rapid.StringMatching(`T[a-z]{2}`).Draw(t, "to") // never starts with a source value
		val := rapid.OneOf(rapid.StringMatching(src+`[0-9]{0,4}`), rapid.StringMatching(`[A-Z0-9-]{0,8}`)).Draw(t, "value")
		rules := []domain.KeyRewrite{{Field: "f", From: src, To: dst}}
		rows := []domain.Record{{Key: "k", Fields: map[string]any{"f": val}}}
		once := domain.RewriteKeys(rows, rules)
		if twice := domain.RewriteKeys(once, rules); twice[0].Fields["f"] != once[0].Fields["f"] {
			t.Fatalf("not idempotent: %v -> %v", once[0].Fields["f"], twice[0].Fields["f"])
		}
		if again := domain.RewriteKeys(rows, rules); again[0].Fields["f"] != once[0].Fields["f"] {
			t.Fatal("not deterministic")
		}
	})
}

type fixedExtractor struct {
	rows []domain.Record
	seen string
}

func (f *fixedExtractor) Extract(ctx context.Context, _ string) ([]domain.Record, error) {
	f.seen = domain.LegalEntityFrom(ctx)
	return f.rows, nil
}

// entityLoader records the legal entity each load and read was tagged with.
type entityLoader struct {
	*domain.MemLoader
	loadedInto, readFrom map[string]bool
}

func (e *entityLoader) LoadBatch(ctx context.Context, entity string, items []domain.LoadItem) ([]domain.LoadResult, error) {
	e.loadedInto[domain.LegalEntityFrom(ctx)] = true
	return e.MemLoader.LoadBatch(ctx, entity, items)
}

func (e *entityLoader) Read(ctx context.Context, entity string) ([]domain.Record, error) {
	e.readFrom[domain.LegalEntityFrom(ctx)] = true
	return e.MemLoader.Read(ctx, entity)
}

func copySetup(rows []domain.Record) (*domain.CopyRun, *entityLoader, *fixedExtractor) {
	loader := &entityLoader{domain.NewMemLoader(), map[string]bool{}, map[string]bool{}}
	ex := &fixedExtractor{rows: rows}
	n := 0
	return &domain.CopyRun{Extractor: ex, Loader: loader, Reader: loader, Xref: domain.NewMemXref(), IDGen: func() string { n++; return fmt.Sprintf("id-%d", n) }}, loader, ex
}

func copySpec() domain.CopySpec {
	return domain.CopySpec{Project: "copy-1", Entity: "product", Source: domain.LegalEntityRef{TenantID: "t1", ID: "usmf"}, Target: domain.LegalEntityRef{TenantID: "t1", ID: "camf"}, Rules: usmfToCamf}
}

func TestCopyRun_LoadsThenSkipsEveryRow(t *testing.T) {
	c, loader, ex := copySetup(products(500))
	rep, err := c.Run(context.Background(), copySpec())
	if err != nil || rep.Created != 500 || rep.Status != domain.StatusSucceeded || ex.seen != "usmf" {
		t.Fatalf("first run = %+v, %v (extracted from %q)", rep, err, ex.seen)
	}
	if rep.Reconciliation == nil || !rep.Reconciliation.OK() {
		t.Fatalf("first run must reconcile against the target: %+v", rep.Reconciliation)
	}
	if fmt.Sprint(loader.loadedInto) != "map[camf:true]" || fmt.Sprint(loader.readFrom) != "map[camf:true]" {
		t.Fatalf("loads and reads must target camf only: loaded %v, read %v", loader.loadedInto, loader.readFrom)
	}
	for _, it := range loader.Rows {
		if !strings.HasPrefix(it.SourceKey, "CAMF-") || it.Fields["dataAreaId"] != "CAMF" {
			t.Fatalf("loaded row not rewritten: %+v", it)
		}
	}
	rep, err = c.Run(context.Background(), copySpec())
	if err != nil || rep.Unchanged != 500 || rep.Created+rep.Updated != 0 {
		t.Fatalf("second run = %+v, %v", rep, err)
	}
}

func TestCopyRun_ForeignTargetKeyRejectedUnlessUpsert(t *testing.T) {
	c, loader, _ := copySetup(products(3))
	loader.Rows["foreign"] = domain.LoadItem{TargetID: "foreign", SourceKey: "CAMF-0001", Fields: map[string]any{"name": "Made by hand"}}
	rep, err := c.Run(context.Background(), copySpec())
	if err != nil || rep.Created != 2 || rep.Status != domain.StatusPartial || len(rep.Rejects) != 1 ||
		rep.Rejects[0].SourceKey != "CAMF-0001" || rep.Rejects[0].Rejects[0].Code != domain.CodeCopyKeyExists {
		t.Fatalf("run = %+v, %v", rep, err)
	}
	if loader.Rows["foreign"].Fields["name"] != "Made by hand" {
		t.Fatal("foreign row must not be overwritten")
	}
	up := copySpec()
	up.Upsert = true
	rep, err = c.Run(context.Background(), up)
	if err != nil || rep.Created != 1 || rep.Unchanged != 2 || len(rep.Rejects) != 0 {
		t.Fatalf("upsert run = %+v, %v", rep, err)
	}
}

func TestCopyRun_RefusesSameOrForeignTenantEntity(t *testing.T) {
	c, loader, ex := copySetup(products(1))
	bad := map[string]func(*domain.CopySpec){
		"same entity":    func(s *domain.CopySpec) { s.Target.ID = s.Source.ID },
		"other tenant":   func(s *domain.CopySpec) { s.Target.TenantID = "t2" },
		"missing target": func(s *domain.CopySpec) { s.Target.ID = "" },
		"no tenants":     func(s *domain.CopySpec) { s.Source.TenantID, s.Target.TenantID = "", "" },
	}
	for name, f := range bad {
		s := copySpec()
		f(&s)
		if _, err := c.Run(context.Background(), s); code(err) != domain.CodeCopyInvalid {
			t.Errorf("%s: want %s, got %v", name, domain.CodeCopyInvalid, err)
		}
	}
	c.Reader = nil // existing target keys could not be detected
	if _, err := c.Run(context.Background(), copySpec()); code(err) != domain.CodeCopyInvalid {
		t.Errorf("no target reader: want %s, got %v", domain.CodeCopyInvalid, err)
	}
	if ex.seen != "" || len(loader.Rows) != 0 {
		t.Fatal("refused copies must not extract or load")
	}
}

func TestCopyRun_DryRunLoadsNothing(t *testing.T) {
	c, loader, _ := copySetup(products(5))
	s := copySpec()
	s.DryRun = true
	rep, err := c.Run(context.Background(), s)
	if err != nil || rep.Created != 5 || len(loader.Rows) != 0 {
		t.Fatalf("dry run = %+v, %v", rep, err)
	}
}
