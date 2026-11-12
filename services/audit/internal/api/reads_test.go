package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
)

// readStore is the read-log half of the fake store: it appends to a real in-memory hash chain.
type readStore struct {
	rows    []store.ReadRecord
	reviews map[string]*store.ReadReview
}

func (f *fakeStore) RecordRead(_ context.Context, read domain.Read) (store.ReadRecord, error) {
	if f.err != nil {
		return store.ReadRecord{}, f.err
	}
	prev := domain.Genesis
	if n := len(f.rows); n > 0 {
		prev = f.rows[n-1].Hash
	}
	entry, err := domain.LinkRead(prev, read, time.Now())
	if err != nil {
		return store.ReadRecord{}, err
	}
	rec := store.ReadRecord{
		ID: "r" + string(rune('0'+len(f.rows))), Seq: int64(len(f.rows) + 1), Entity: entry.Entity, RecordID: entry.RecordID,
		Fields: entry.Fields, Purpose: entry.Purpose, UserID: entry.UserID, SourceService: entry.Source, TraceID: entry.TraceID,
		OccurredAt: entry.OccurredAt, PrevHash: entry.PrevHash, Hash: entry.Hash,
	}
	f.rows = append(f.rows, rec)
	return rec, nil
}

func (f *fakeStore) ListReads(_ context.Context, flt store.ReadFilter) ([]store.ReadRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastFilter = flt
	out := []store.ReadRecord{}
	for _, r := range f.rows {
		if r.Seq <= flt.AfterSeq || (flt.UserID != "" && r.UserID != flt.UserID) || (flt.Entity != "" && r.Entity != flt.Entity) {
			continue
		}
		out = append(out, r)
		if flt.Limit > 0 && len(out) == flt.Limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) chainStatus() store.ReadChain {
	entries := make([]domain.ReadEntry, len(f.rows))
	for i, r := range f.rows {
		entries[i] = domain.ReadEntry{Read: domain.Read{Entity: r.Entity, RecordID: r.RecordID, Fields: r.Fields, Purpose: r.Purpose,
			UserID: r.UserID, Source: r.SourceService, TraceID: r.TraceID, OccurredAt: r.OccurredAt}, PrevHash: r.PrevHash, Hash: r.Hash}
	}
	s := domain.VerifyReads(entries)
	c := store.ReadChain{Verified: s.Verified, Entries: s.Entries, Head: s.Head}
	if !s.Verified {
		c.BrokenAt = &s.BrokenAt
	}
	return c
}

func (f *fakeStore) VerifyReads(context.Context) (store.ReadChain, error) {
	if f.err != nil {
		return store.ReadChain{}, f.err
	}
	return f.chainStatus(), nil
}

func (f *fakeStore) ExportReads(_ context.Context, flt store.ReadFilter, begin func(store.ReadChain) error, each func(store.ReadRecord) error) error {
	if f.err != nil {
		return f.err
	}
	f.lastFilter = flt
	if err := begin(f.chainStatus()); err != nil {
		return err
	}
	for _, r := range f.rows {
		if err := each(r); err != nil {
			return err
		}
	}
	return nil
}

const (
	readsPath  = "/v1/audit/reads"
	validRead  = `{"entity":"hr.worker.benefits","recordId":"w-1","fields":["enrollments.optionCode","lifeEvents.kind"],"purpose":"benefits enquiry","traceId":"trace-1"}`
	dutyWriter = "audit.read-log.write"
	dutyViewer = "audit.read-log.view"
	dutyExport = "audit.read-log.export"
)

func TestRecordReadTakesTheReaderFromTheCaller(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	rec := do(r, http.MethodPost, readsPath, validRead, dutyWriter, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(f.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(f.rows))
	}
	row := f.rows[0]
	if row.UserID != "user-1" || row.Entity != "hr.worker.benefits" || row.RecordID != "w-1" || row.Purpose != "benefits enquiry" || row.TraceID != "trace-1" || row.OccurredAt.IsZero() {
		t.Fatalf("row %+v", row)
	}
	if strings.Join(row.Fields, ",") != "enrollments.optionCode,lifeEvents.kind" {
		t.Fatalf("fields %v", row.Fields)
	}

	named := strings.Replace(validRead, `"purpose"`, `"userId":"someone-else","purpose"`, 1)
	rec = do(r, http.MethodPost, readsPath, named, dutyWriter, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "AUD-0002") {
		t.Fatalf("a body that names the reader must be refused with AUD-0002, got %d %s", rec.Code, rec.Body.String())
	}
	if len(f.rows) != 1 {
		t.Fatal("a refused request must not append")
	}
}

func TestRecordReadValidation(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"purpose missing":  {`{"entity":"e","recordId":"r","fields":["f"]}`, `"purpose"`},
		"purpose blank":    {`{"entity":"e","recordId":"r","fields":["f"],"purpose":"   "}`, `"purpose"`},
		"purpose too long": {`{"entity":"e","recordId":"r","fields":["f"],"purpose":"` + strings.Repeat("x", 501) + `"}`, `"purpose"`},
		"no fields":        {`{"entity":"e","recordId":"r","fields":[],"purpose":"p"}`, `"fields"`},
		"bad field name":   {`{"entity":"e","recordId":"r","fields":["a b"],"purpose":"p"}`, `"fields"`},
		"no entity":        {`{"recordId":"r","fields":["f"],"purpose":"p"}`, `"entity"`},
		"future time":      {`{"entity":"e","recordId":"r","fields":["f"],"purpose":"p","occurredAt":"2999-01-01T00:00:00Z"}`, `"occurredAt"`},
		"unknown key":      {`{"entity":"e","recordId":"r","fields":["f"],"purpose":"p","bogus":1}`, "COM-0001"},
	}
	for name, c := range cases {
		f := &fakeStore{}
		r, _ := newRouter(f)
		rec := do(r, http.MethodPost, readsPath, c.body, dutyWriter, true)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: got %d %s, want 400 naming %s", name, rec.Code, rec.Body.String(), c.want)
		}
		if len(f.rows) != 0 {
			t.Errorf("%s: an invalid read must not append", name)
		}
	}
}

func TestReadLogDuties(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	do(r, http.MethodPost, readsPath, validRead, dutyWriter, true)
	for _, c := range []struct {
		name, method, path, duties string
		withTenant                 bool
		want                       int
	}{
		{"record needs the write duty", http.MethodPost, readsPath, "audit.read-log.view,audit.read-log.export,audit.write", true, http.StatusForbidden},
		{"audit.write alone cannot forge reads", http.MethodPost, readsPath, "audit.write,audit.read", true, http.StatusForbidden},
		{"record without tenant", http.MethodPost, readsPath, dutyWriter, false, http.StatusUnauthorized},
		{"list needs the view duty", http.MethodGet, readsPath, "audit.read,audit.read-log.write", true, http.StatusForbidden},
		{"list ok", http.MethodGet, readsPath, dutyViewer, true, http.StatusOK},
		{"export needs the export duty", http.MethodGet, readsPath + ":export", dutyViewer, true, http.StatusForbidden},
		{"verify needs the export duty", http.MethodGet, readsPath + ":verify", dutyViewer, true, http.StatusForbidden},
		{"export ok", http.MethodGet, readsPath + ":export", dutyExport, true, http.StatusOK},
		{"verify ok", http.MethodGet, readsPath + ":verify", dutyExport, true, http.StatusOK},
	} {
		body := ""
		if c.method == http.MethodPost {
			body = validRead
		}
		if rec := do(r, c.method, c.path, body, c.duties, c.withTenant); rec.Code != c.want {
			t.Errorf("%s: got %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
}

func seeded(t *testing.T, n int) (http.Handler, *fakeStore) {
	t.Helper()
	f := &fakeStore{}
	r, _ := newRouter(f)
	for i := 0; i < n; i++ {
		body := strings.Replace(validRead, "w-1", "w-"+string(rune('1'+i)), 1)
		if rec := do(r, http.MethodPost, readsPath, body, dutyWriter, true); rec.Code != http.StatusCreated {
			t.Fatalf("seed %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	return r, f
}

func TestListReadsPagesAndFilters(t *testing.T) {
	r, f := seeded(t, 3)
	rec := do(r, http.MethodGet, readsPath+"?limit=2&userId=user-1&entity=hr.worker.benefits&from=2020-01-01T00:00:00Z&to=2999-01-01T00:00:00Z", "", dutyViewer, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items      []store.ReadRecord `json:"items"`
		NextCursor string             `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == "" || page.Items[0].Seq != 1 {
		t.Fatalf("page 1 = %+v", page)
	}
	if f.lastFilter.UserID != "user-1" || f.lastFilter.Entity != "hr.worker.benefits" || f.lastFilter.From == nil || f.lastFilter.To == nil {
		t.Fatalf("filter not passed through: %+v", f.lastFilter)
	}
	rec = do(r, http.MethodGet, readsPath+"?limit=2&cursor="+page.NextCursor, "", dutyViewer, true)
	page.Items, page.NextCursor = nil, ""
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Seq != 3 || page.NextCursor != "" {
		t.Fatalf("page 2 = %+v", page)
	}
	for _, q := range []string{"limit=0", "limit=501", "limit=x", "cursor=%25%25", "cursor=" + base64.RawURLEncoding.EncodeToString([]byte("-4")), "from=yesterday", "from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z"} {
		if rec := do(r, http.MethodGet, readsPath+"?"+q, "", dutyViewer, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "AUD-0003") {
			t.Errorf("%s: got %d %s, want 400 AUD-0003", q, rec.Code, rec.Body.String())
		}
	}
}

func TestExportReadsCSVAndNDJSONCarryTheChainVerdict(t *testing.T) {
	r, f := seeded(t, 2)
	rec := do(r, http.MethodGet, readsPath+":export?format=csv", "", dutyExport, true)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("X-Passage-Read-Chain-Verified") != "true" || rec.Header().Get("X-Passage-Read-Chain-Entries") != "2" || rec.Header().Get("X-Passage-Read-Chain-Head") != f.rows[1].Hash {
		t.Fatalf("headers %v", rec.Header())
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "seq,id,occurredAt,userId,entity,recordId,fields,purpose") || !strings.Contains(lines[1], "benefits enquiry") || !strings.Contains(lines[1], "enrollments.optionCode;lifeEvents.kind") {
		t.Fatalf("csv body:\n%s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("an export of PHI access must not be cached")
	}

	rec = do(r, http.MethodGet, readsPath+":export?format=ndjson", "", dutyExport, true)
	lines = strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if rec.Code != http.StatusOK || len(lines) != 2 {
		t.Fatalf("ndjson: %d %s", rec.Code, rec.Body.String())
	}
	var first store.ReadRecord
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first.Hash != f.rows[0].Hash || first.UserID != "user-1" {
		t.Fatalf("ndjson row %q: %+v %v", lines[0], first, err)
	}

	if rec := do(r, http.MethodGet, readsPath+":export?format=xml", "", dutyExport, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "AUD-0003") {
		t.Fatalf("unknown format: %d %s", rec.Code, rec.Body.String())
	}

	f.rows[0].Purpose = "tampered"
	rec = do(r, http.MethodGet, readsPath+":export?format=ndjson", "", dutyExport, true)
	if rec.Header().Get("X-Passage-Read-Chain-Verified") != "false" || rec.Header().Get("X-Passage-Read-Chain-Broken-At") != "0" {
		t.Fatalf("a tampered row must show in the export headers: %v", rec.Header())
	}
	rec = do(r, http.MethodGet, readsPath+":verify", "", dutyExport, true)
	if !strings.Contains(rec.Body.String(), `"verified":false`) || !strings.Contains(rec.Body.String(), `"brokenAt":0`) {
		t.Fatalf("verify: %s", rec.Body.String())
	}
}

func TestExportReadsNeutralisesSpreadsheetFormulas(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	body := strings.Replace(validRead, "benefits enquiry", "=HYPERLINK(\\\"http://x\\\")", 1)
	if rec := do(r, http.MethodPost, readsPath, body, dutyWriter, true); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec := do(r, http.MethodGet, readsPath+":export", "", dutyExport, true)
	if strings.Contains(rec.Body.String(), ",=HYPERLINK") || strings.Contains(rec.Body.String(), ",\"=HYPERLINK") {
		t.Fatalf("a purpose that starts with = must not reach the CSV as a formula:\n%s", rec.Body.String())
	}
}

func TestReadLogStoreErrorsAreHidden(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: context.DeadlineExceeded})
	rec := do(r, http.MethodPost, readsPath, validRead, dutyWriter, true)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "COM-0500") || strings.Contains(rec.Body.String(), "deadline") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
