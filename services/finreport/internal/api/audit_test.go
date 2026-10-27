package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// auditFake answers the audit trail routes and records what they were asked.
type auditFake struct {
	fakeStore
	page      store.AuditPage
	export    store.AuditExport
	exportErr error
	filter    *domain.AuditFilter
	after     *domain.AuditCursor
	limit     int
}

func (f *auditFake) AuditTrail(_ context.Context, flt domain.AuditFilter, after *domain.AuditCursor, limit int) (store.AuditPage, error) {
	f.filter, f.after, f.limit = &flt, after, limit
	return f.page, nil
}

func (f *auditFake) ExportAuditTrail(_ context.Context, flt domain.AuditFilter) (store.AuditExport, error) {
	f.filter = &flt
	return f.export, f.exportErr
}

func auditRouter(f *auditFake, cfg config.Reporting) http.Handler {
	r := chi.NewRouter()
	New(f).WithConfig(cfg).Register(r)
	return r
}

const auditPath = "/api/v1/reporting/audit-trail"

func TestAuditTrail_NeedsTheAuditDuty(t *testing.T) {
	f := &auditFake{}
	for _, path := range []string{auditPath + "?period=2026-03", auditPath + ":export?period=2026-03"} {
		w := call(auditRouter(f, config.Reporting{}), http.MethodGet, path, "", nil, DutyView, DutyMaintain)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s without %s: status %d, want 403", path, DutyAudit, w.Code)
		}
	}
	if f.filter != nil {
		t.Fatal("the store must not be reached without the duty")
	}
}

func TestAuditTrail_PassesTheFilterAndPagesWithTraceLinks(t *testing.T) {
	at := time.Date(2026, 3, 31, 9, 0, 0, 0, time.UTC)
	f := &auditFake{page: store.AuditPage{
		Items: []domain.AuditEntry{{ID: "0190f5a2-7c1e-7d00-8000-0000000000a1", OccurredAt: at, LedgerID: ledgerUUID, Period: "2026-03",
			Kind: domain.AuditReopenApproved, ActorID: "u2", ObjectRef: "period:2026-03", TraceID: "trace-1",
			Details: map[string]any{"approvers": []any{"u2", "u3"}}}},
		Next: &domain.AuditCursor{OccurredAt: at, ID: "0190f5a2-7c1e-7d00-8000-0000000000a1"},
	}}
	cfg := config.Reporting{TraceURL: "https://grafana.example/explore?traceId={traceId}"}
	q := url.Values{"ledger": {ledgerUUID}, "period": {"2026-03"}, "kind": {"reopen_approved"}, "actor": {"u2"}, "limit": {"25"},
		"from": {"2026-03-01T00:00:00Z"}, "to": {"2026-04-01T00:00:00Z"}}
	w := call(auditRouter(f, cfg), http.MethodGet, auditPath+"?"+q.Encode(), "", nil, DutyAudit)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	want := domain.AuditFilter{LedgerID: ledgerUUID, Period: "2026-03", Kind: "reopen_approved", ActorID: "u2",
		From: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)}
	if *f.filter != want || f.limit != 25 || f.after != nil {
		t.Fatalf("store got %+v limit %d after %v", *f.filter, f.limit, f.after)
	}
	var body struct {
		Items []struct {
			Kind     string         `json:"kind"`
			ActorID  string         `json:"actorId"`
			TraceID  string         `json:"traceId"`
			TraceURL string         `json:"traceUrl"`
			Details  map[string]any `json:"details"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].TraceURL != "https://grafana.example/explore?traceId=trace-1" || body.Items[0].ActorID != "u2" {
		t.Fatalf("items = %+v", body.Items)
	}
	if body.NextCursor == "" {
		t.Fatal("a full page must carry the next cursor")
	}
	// The cursor comes back to the store decoded.
	w = call(auditRouter(f, cfg), http.MethodGet, auditPath+"?period=2026-03&cursor="+body.NextCursor, "", nil, DutyAudit)
	if w.Code != http.StatusOK || f.after == nil || f.after.ID != "0190f5a2-7c1e-7d00-8000-0000000000a1" || f.limit != domain.AuditDefaultPage {
		t.Fatalf("second page: status %d after %+v limit %d", w.Code, f.after, f.limit)
	}
}

func TestAuditTrail_RefusesUnboundedOrMalformedFilters(t *testing.T) {
	for _, q := range []string{
		"",                           // no period and no window
		"?ledger=" + ledgerUUID,      // a ledger alone is not a narrowed range
		"?from=2026-01-01T00:00:00Z", // half a window
		"?from=2025-01-01T00:00:00Z&to=2026-03-01T00:00:00Z", // over 366 days
		"?period=2026-3",
		"?period=2026-03&kind=deleted",
		"?period=2026-03&ledger=GL-1",
		"?from=yesterday&to=2026-03-01T00:00:00Z",
		"?period=2026-03&limit=0",
		"?period=2026-03&limit=1001",
		"?period=2026-03&cursor=not-a-cursor",
	} {
		f := &auditFake{}
		w := call(auditRouter(f, config.Reporting{}), http.MethodGet, auditPath+q, "", nil, DutyAudit)
		if w.Code != http.StatusBadRequest || problemCode(t, w) != "COM-0002" {
			t.Fatalf("%q: status %d %s, want 400 COM-0002", q, w.Code, w.Body.String())
		}
		if f.filter != nil {
			t.Fatalf("%q reached the store", q)
		}
	}
}

func TestAuditExport_SendsTheFileWithItsDigestAndExportID(t *testing.T) {
	body := []byte("occurred_at,kind,ledger_id,period,actor_id,object_ref,details,trace_id\n")
	f := &auditFake{export: store.AuditExport{ID: "0190f5a2-7c1e-7d00-8000-0000000000e1", Body: body, SHA256: domain.AuditDigest(body), Entries: 0}}
	w := call(auditRouter(f, config.Reporting{}), http.MethodGet, auditPath+":export?period=2026-03&ledger="+ledgerUUID, "", nil, DutyAudit)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content type %q", ct)
	}
	if got := w.Header().Get("X-Passage-Export-Sha256"); got != domain.AuditDigest(w.Body.Bytes()) {
		t.Fatalf("digest header %q does not match the body", got)
	}
	if w.Header().Get("X-Passage-Export-Id") != f.export.ID || w.Header().Get("X-Passage-Export-Entries") != "0" {
		t.Fatalf("export headers %v", w.Header())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), f.export.ID) {
		t.Fatalf("file name should carry the export id: %q", w.Header().Get("Content-Disposition"))
	}
	if f.filter.LedgerID != ledgerUUID || f.filter.Period != "2026-03" {
		t.Fatalf("filter %+v", *f.filter)
	}
}

func TestAuditExport_TooManyEntriesIsRefusedNotTruncated(t *testing.T) {
	f := &auditFake{exportErr: httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeAuditExportTooLarge, "narrow the filter")}
	w := call(auditRouter(f, config.Reporting{}), http.MethodGet, auditPath+":export?period=2026-03", "", nil, DutyAudit)
	if w.Code != http.StatusUnprocessableEntity || problemCode(t, w) != domain.CodeAuditExportTooLarge {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Passage-Export-Sha256") != "" {
		t.Fatal("a refused export must not carry a digest")
	}
}
