package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const exportsURL = "/v1/pricing/commission/settlement-exports"

// exportFake keeps recorded exports in memory and applies the rules the Postgres store does:
// overlap per legal entity and target (PRC-0065), an empty period (PRC-0066), the version
// check and a second void (PRC-0031).
type exportFake struct {
	*fakeStore
	rows    []domain.SettlementRow
	exports []domain.SettlementExport
	byKey   map[string]domain.SettlementExport
}

func newExportFake(rows ...domain.SettlementRow) *exportFake {
	return &exportFake{fakeStore: newFake(), rows: rows, byKey: map[string]domain.SettlementExport{}}
}

func (s *exportFake) CreateSettlementExport(_ context.Context, in store.SettlementExportInput, key string) (domain.SettlementExport, error) {
	if prev, ok := s.byKey[key]; ok && key != "" {
		return prev, nil
	}
	from, to := in.From.Format(time.DateOnly), in.To.Format(time.DateOnly)
	for _, e := range s.exports {
		if e.Status == domain.ExportIssued && e.LegalEntityID == in.LegalEntityID && e.Target == in.Target && e.From <= to && e.To >= from {
			return domain.SettlementExport{}, httpx.NewProblem(http.StatusConflict, domain.CodeExportOverlap, "export "+e.ID+" covers this period")
		}
	}
	if len(s.rows) == 0 {
		return domain.SettlementExport{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeExportEmpty, "no commission")
	}
	csv, _ := domain.SettlementCSV(in.Target, from, to, s.rows)
	e := domain.SettlementExport{ID: s.nextID("0077"), LegalEntityID: in.LegalEntityID, Target: in.Target, From: from, To: to,
		Status: domain.ExportIssued, Lines: s.rows, CSVSHA256: domain.CSVDigest(csv), ExportedBy: "u1", Version: 1}
	s.exports = append(s.exports, e)
	if key != "" {
		s.byKey[key] = e
	}
	return e, nil
}

func (s *exportFake) GetSettlementExport(_ context.Context, id string) (domain.SettlementExport, error) {
	for _, e := range s.exports {
		if e.ID == id {
			return e, nil
		}
	}
	return domain.SettlementExport{}, httpx.NewProblem(http.StatusNotFound, domain.CodeExportNotFound, "no settlement export "+id)
}

func (s *exportFake) ListSettlementExports(_ context.Context, f store.SettlementExportFilter) ([]domain.SettlementExport, error) {
	out := []domain.SettlementExport{}
	for _, e := range s.exports {
		if (f.Status == "" || e.Status == f.Status) && (f.Target == "" || e.Target == f.Target) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *exportFake) VoidSettlementExport(_ context.Context, id string, version int, reason, _ string) (domain.SettlementExport, error) {
	for i, e := range s.exports {
		if e.ID != id {
			continue
		}
		if e.Version != version {
			return domain.SettlementExport{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "version")
		}
		if e.Status != domain.ExportIssued {
			return domain.SettlementExport{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "already voided")
		}
		e.Status, e.VoidReason, e.VoidedBy, e.Version = domain.ExportVoided, reason, "u1", e.Version+1
		s.exports[i] = e
		return e, nil
	}
	return domain.SettlementExport{}, httpx.NewProblem(http.StatusNotFound, domain.CodeExportNotFound, "no settlement export "+id)
}

func exportBody(le, from, to, target string) string {
	return `{"legalEntityId":"` + le + `","from":"` + from + `","to":"` + to + `","target":"` + target + `"}`
}

func TestSettlementExports_RecordDownloadAndVoid(t *testing.T) {
	fs := newExportFake(
		domain.SettlementRow{RepID: "R1", Currency: "USD", Amount: decimal.RequireFromString("100"), Transactions: 3},
		domain.SettlementRow{RepID: "R2", Currency: "USD", Amount: decimal.RequireFromString("40.5"), Transactions: 2})
	h := router(fs)
	const maintain, view = "pricing.commission.maintain", "pricing.commission.view"

	code, out := ce(t, h, http.MethodPost, exportsURL, maintain, exportBody(legalEntity, "2027-03-01", "2027-03-31", "PAYROLL"), "Idempotency-Key", "k1")
	if code != http.StatusCreated || out["status"] != "ISSUED" || out["version"] != float64(1) || out["lineCount"] != float64(2) {
		t.Fatalf("record = %d %v", code, out)
	}
	if totals := out["totals"].([]any); len(totals) != 1 || totals[0].(map[string]any)["amount"] != "140.50" {
		t.Fatalf("totals = %v", out["totals"])
	}
	id, digest := out["id"].(string), out["csvSha256"].(string)

	// The same Idempotency-Key replays; the same period for the same target is PRC-0065.
	if code, again := ce(t, h, http.MethodPost, exportsURL, maintain, exportBody(legalEntity, "2027-03-01", "2027-03-31", "PAYROLL"), "Idempotency-Key", "k1"); code != http.StatusCreated || again["id"] != id {
		t.Fatalf("replay = %d %v", code, again)
	}
	if code, out := ce(t, h, http.MethodPost, exportsURL, maintain, exportBody(legalEntity, "2027-03-15", "2027-04-15", "PAYROLL")); code != http.StatusConflict || out["code"] != "PRC-0065" {
		t.Fatalf("overlap = %d %v; want 409 PRC-0065", code, out)
	}
	// Another target is not a duplicate.
	if code, _ := ce(t, h, http.MethodPost, exportsURL, maintain, exportBody(legalEntity, "2027-03-01", "2027-03-31", "AP")); code != http.StatusCreated {
		t.Fatalf("other target = %d", code)
	}

	code, raw, hdr := callRaw(t, h, http.MethodGet, exportsURL+"/"+id+"/file", view)
	if code != http.StatusOK || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") || hdr.Get("X-Content-SHA256") != digest ||
		domain.CSVDigest([]byte(raw)) != digest ||
		!strings.Contains(raw, "PAYROLL,R1,USD,100.00,3,2027-03-01,2027-03-31") || !strings.Contains(hdr.Get("Content-Disposition"), "commission-settlement-PAYROLL-2027-03-01-2027-03-31.csv") {
		t.Fatalf("file = %d %q %v", code, raw, hdr)
	}
	if code, got := ce(t, h, http.MethodGet, exportsURL+"/"+id, view, ""); code != http.StatusOK || len(got["lines"].([]any)) != 2 {
		t.Fatalf("detail = %d %v", code, got)
	}
	if code, list := ce(t, h, http.MethodGet, exportsURL+"?target=PAYROLL&status=ISSUED", view, ""); code != http.StatusOK || len(list["items"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, list)
	} else if _, has := list["items"].([]any)[0].(map[string]any)["lines"]; has {
		t.Fatal("a list row must not carry the lines")
	}

	// Void needs a reason and the current version; afterwards the period can be sent again.
	for _, reason := range []string{`""`, `"   "`} {
		if code, out := ce(t, h, http.MethodPost, exportsURL+"/"+id+":void", maintain, `{"version":1,"reason":`+reason+`}`); code != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["reason"] == nil {
			t.Fatalf("void with reason %s = %d %v", reason, code, out)
		}
	}
	if code, out := ce(t, h, http.MethodPost, exportsURL+"/"+id+":void", maintain, `{"version":3,"reason":"wrong period"}`); code != http.StatusPreconditionFailed {
		t.Fatalf("stale void = %d %v", code, out)
	}
	if code, out := ce(t, h, http.MethodPost, exportsURL+"/"+id+":void", maintain, `{"version":1,"reason":"wrong period"}`); code != http.StatusOK || out["status"] != "VOIDED" || out["voidReason"] != "wrong period" {
		t.Fatalf("void = %d %v", code, out)
	}
	if code, out := ce(t, h, http.MethodPost, exportsURL+"/"+id+":void", maintain, `{"version":2,"reason":"again"}`); code != http.StatusConflict || out["code"] != "PRC-0031" {
		t.Fatalf("second void = %d %v", code, out)
	}
	if code, _ := ce(t, h, http.MethodPost, exportsURL, maintain, exportBody(legalEntity, "2027-03-01", "2027-03-31", "PAYROLL")); code != http.StatusCreated {
		t.Fatalf("re-export after void = %d", code)
	}
}

func TestSettlementExports_ValidationDutiesScopeAndEmpty(t *testing.T) {
	h := router(newExportFake())
	const maintain = "pricing.commission.maintain"
	for name, body := range map[string]string{
		"reversed period": exportBody(legalEntity, "2027-03-31", "2027-03-01", "AP"),
		"NONE target":     exportBody(legalEntity, "2027-03-01", "2027-03-31", "NONE"),
		"bad entity":      exportBody("x", "2027-03-01", "2027-03-31", "AP"),
	} {
		if code, out := ce(t, h, http.MethodPost, exportsURL, maintain, body); code != http.StatusUnprocessableEntity || out["errors"] == nil {
			t.Errorf("%s = %d %v; want 422", name, code, out)
		}
	}
	if code, _ := ce(t, h, http.MethodPost, exportsURL, "pricing.commission.view", exportBody(legalEntity, "2027-03-01", "2027-03-31", "AP")); code != http.StatusForbidden {
		t.Fatalf("recording needs pricing.commission.maintain, got %d", code)
	}
	if code, _ := ce(t, h, http.MethodGet, exportsURL, "pricing.view", ""); code != http.StatusForbidden {
		t.Fatalf("list needs pricing.commission.view, got %d", code)
	}
	if code, out := ce(t, h, http.MethodPost, exportsURL, maintain, exportBody(otherEntity, "2027-03-01", "2027-03-31", "AP")); code != http.StatusForbidden || out["code"] != "AUTH-0003" {
		t.Fatalf("other legal entity = %d %v; want 403 AUTH-0003", code, out)
	}
	if code, out := ce(t, h, http.MethodPost, exportsURL, maintain, exportBody(legalEntity, "2027-03-01", "2027-03-31", "")); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0066" {
		t.Fatalf("empty period = %d %v; want 422 PRC-0066", code, out)
	}
	if code, out := ce(t, h, http.MethodGet, exportsURL+"/not-a-uuid", "pricing.commission.view", ""); code != http.StatusNotFound || out["code"] != "PRC-0067" {
		t.Fatalf("bad id = %d %v", code, out)
	}
	if code, _ := ce(t, h, http.MethodGet, exportsURL+"?status=BOGUS", "pricing.commission.view", ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad status filter = %d", code)
	}
}

// A record whose rebuilt file no longer matches the digest handed over is refused, not served.
func TestSettlementExports_FileRefusedWhenDigestDiffers(t *testing.T) {
	fs := newExportFake(domain.SettlementRow{RepID: "R1", Currency: "USD", Amount: decimal.RequireFromString("10"), Transactions: 1})
	h := router(fs)
	code, out := ce(t, h, http.MethodPost, exportsURL, "pricing.commission.maintain", exportBody(legalEntity, "2027-03-01", "2027-03-31", "AP"))
	if code != http.StatusCreated {
		t.Fatalf("record = %d %v", code, out)
	}
	fs.exports[0].CSVSHA256 = strings.Repeat("0", 64)
	if code, raw, _ := callRaw(t, h, http.MethodGet, exportsURL+"/"+out["id"].(string)+"/file", "pricing.commission.view"); code != http.StatusInternalServerError || !strings.Contains(raw, "PRC-0068") {
		t.Fatalf("file with a stale digest = %d %q; want 500 PRC-0068", code, raw)
	}
}

// An upper-case legal entity id is the same entity: it is canonicalized before the scope check.
func TestSettlementExports_UpperCaseLegalEntity(t *testing.T) {
	fs := newExportFake(domain.SettlementRow{RepID: "R1", Currency: "USD", Amount: decimal.RequireFromString("10"), Transactions: 1})
	h := router(fs)
	code, out := ce(t, h, http.MethodPost, exportsURL, "pricing.commission.maintain", exportBody(strings.ToUpper(legalEntity), "2027-03-01", "2027-03-31", "AP"))
	if code != http.StatusCreated || out["legalEntityId"] != legalEntity {
		t.Fatalf("upper-case entity = %d %v", code, out)
	}
	if code, out := ce(t, h, http.MethodPost, exportsURL, "pricing.commission.maintain", exportBody(legalEntity, "2027-03-10", "2027-03-20", "AP")); code != http.StatusConflict || out["code"] != "PRC-0065" {
		t.Fatalf("same entity in lower case = %d %v; want 409 PRC-0065", code, out)
	}
}

// ce calls as a caller scoped to the test legal entity.
func ce(t *testing.T, h http.Handler, method, path, duties, body string, headers ...string) (int, map[string]any) {
	t.Helper()
	return call(t, h, method, path, duties, body, append(headers, inEntity...)...)
}
