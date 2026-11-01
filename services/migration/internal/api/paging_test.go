package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

// walk follows nextCursor with limit=1 and returns the id (or key) of each item in page order.
func walk(t *testing.T, r http.Handler, path, idField string) []string {
	t.Helper()
	var got []string
	cursor := ""
	for page := 0; page < 10; page++ {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		u := path + sep + "limit=1"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := do(r, http.MethodGet, u, "", duties, true)
		var out struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"nextCursor"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Items) != 1 {
			t.Fatalf("page %d of %s: %d %s", page, path, rec.Code, rec.Body)
		}
		got = append(got, out.Items[0][idField].(string))
		if out.NextCursor == "" {
			return got
		}
		cursor = out.NextCursor
	}
	t.Fatalf("%s never ended", path)
	return nil
}

func assertCursorRejected(t *testing.T, r http.Handler, path string) {
	t.Helper()
	for _, bad := range []string{"!!!", "e30", "bm90LWpzb24"} {
		if rec := do(r, http.MethodGet, path+"?cursor="+bad, "", duties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cursor") {
			t.Errorf("%s cursor %q: %d %s", path, bad, rec.Code, rec.Body)
		}
	}
}

func TestListRuns_CursorPagesNewestFirstWithoutGapsOrRepeats(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	var created []string
	for range 3 {
		created = append([]string{decode(t, do(r, http.MethodPost, runsPath, body, duties, true)).Run.ID}, created...)
	}
	got := walk(t, r, runsPath, "id")
	if strings.Join(got, ",") != strings.Join(created, ",") {
		t.Fatalf("pages = %v, want newest first %v", got, created)
	}
	if rec := do(r, http.MethodGet, runsPath+"?limit=200", "", duties, true); strings.Contains(rec.Body.String(), "nextCursor") {
		t.Fatalf("last page must not carry nextCursor: %s", rec.Body)
	}
	assertCursorRejected(t, r, runsPath)
}

func TestListSpecsAndTemplates_CursorPageInDocumentedOrder(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	var specs []string
	for _, v := range []string{"1", "2", "3"} {
		_, s := publish(t, r, strings.Replace(specMapping, `"version":1`, `"version":`+v, 1))
		specs = append([]string{s.ID}, specs...)
	}
	if got := walk(t, r, specsPath+"?entity=customer", "id"); strings.Join(got, ",") != strings.Join(specs, ",") {
		t.Fatalf("specs = %v, want %v", got, specs)
	}
	assertCursorRejected(t, r, specsPath)

	var templates []string
	for _, v := range []string{"1", "2"} {
		_, tb := publishTemplate(t, r, `{"name":"Retail","entity":"customer","version":`+v+`,"defaults":{"segment":"`+v+`"}}`)
		templates = append([]string{tb.ID}, templates...)
	}
	_, other := publishTemplate(t, r, `{"name":"Wholesale","entity":"customer","version":1,"defaults":{"segment":"w"}}`)
	templates = append(templates, other.ID)
	if got := walk(t, r, templatesPath, "id"); strings.Join(got, ",") != strings.Join(templates, ",") {
		t.Fatalf("templates = %v, want %v", got, templates)
	}
	assertCursorRejected(t, r, templatesPath)
}

func TestRunLedger_CursorPagesInSourceKeyOrder(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	run := decode(t, do(r, http.MethodPost, runsPath, body, duties, true))
	got := walk(t, r, runsPath+"/"+run.Run.ID+"/ledger", "sourceKey")
	if len(got) < 2 || got[0] != "C1" || got[1] != "C2" {
		t.Fatalf("ledger pages = %v", got)
	}
	assertCursorRejected(t, r, runsPath+"/"+run.Run.ID+"/ledger")
}

// firstCursor returns the nextCursor of a limit=1 page of path.
func firstCursor(t *testing.T, r http.Handler, path string) string {
	t.Helper()
	var out struct {
		NextCursor string `json:"nextCursor"`
	}
	rec := do(r, http.MethodGet, path+"?limit=1", "", duties, true)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.NextCursor == "" {
		t.Fatalf("%s: want a nextCursor, got %d %s", path, rec.Code, rec.Body)
	}
	return out.NextCursor
}

func TestListCursors_ACursorFromAnotherListIsRejected(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	var runID string
	for range 2 {
		runID = decode(t, do(r, http.MethodPost, runsPath, body, duties, true)).Run.ID
	}
	for _, v := range []string{"1", "2"} {
		publish(t, r, strings.Replace(specMapping, `"version":1`, `"version":`+v, 1))
		publishTemplate(t, r, `{"name":"Retail","entity":"customer","version":`+v+`,"defaults":{"segment":"`+v+`"}}`)
	}
	ledgerPath := runsPath + "/" + runID + "/ledger"
	cursors := map[string]string{
		runsPath:      firstCursor(t, r, runsPath),
		specsPath:     firstCursor(t, r, specsPath),
		templatesPath: firstCursor(t, r, templatesPath),
		ledgerPath:    firstCursor(t, r, ledgerPath),
	}
	for issuedBy, cursor := range cursors {
		for path := range cursors {
			rec := do(r, http.MethodGet, path+"?cursor="+url.QueryEscape(cursor), "", duties, true)
			if path == issuedBy && rec.Code != http.StatusOK {
				t.Errorf("%s with its own cursor: %d %s", path, rec.Code, rec.Body)
			}
			if path != issuedBy && (rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cursor")) {
				t.Errorf("%s with a cursor from %s: %d %s", path, issuedBy, rec.Code, rec.Body)
			}
		}
	}
}

func TestListRuns_AFullPageOf200HasANextCursorAndLimit201IsRejected(t *testing.T) {
	s := store.NewMem()
	for range 201 {
		if _, err := s.SaveRun(context.Background(), store.Run{SourceSystem: "ax2012", Mode: "rehearsal", Entity: "customer", Report: domain.Report{Status: domain.StatusSucceeded}}, ""); err != nil {
			t.Fatal(err)
		}
	}
	r := router(s, rowsExtractor{}, domain.NewMemLoader())
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	rec := do(r, http.MethodGet, runsPath+"?limit=200", "", duties, true)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Items) != 200 || page.NextCursor == "" {
		t.Fatalf("limit=200 of 201 runs: %d, %d items, cursor %q", rec.Code, len(page.Items), page.NextCursor)
	}
	rec = do(r, http.MethodGet, runsPath+"?limit=200&cursor="+url.QueryEscape(page.NextCursor), "", duties, true)
	page.NextCursor = ""
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatalf("second page: %d, %d items, cursor %q", rec.Code, len(page.Items), page.NextCursor)
	}
	if rec := do(r, http.MethodGet, runsPath+"?limit=201", "", duties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit=201: %d %s", rec.Code, rec.Body)
	}
}

func TestListRuns_CursorKeepsTheStatusFilter(t *testing.T) {
	s := store.NewMem()
	var partial []string
	for i := range 4 {
		status := domain.StatusSucceeded
		if i%2 == 0 {
			status = domain.StatusPartial
		}
		run, err := s.SaveRun(context.Background(), store.Run{SourceSystem: "ax2012", Mode: "rehearsal", Entity: "customer", Report: domain.Report{Status: status}}, "")
		if err != nil {
			t.Fatal(err)
		}
		if status == domain.StatusPartial {
			partial = append([]string{run.ID}, partial...)
		}
	}
	r := router(s, rowsExtractor{}, domain.NewMemLoader())
	if got := walk(t, r, runsPath+"?status=partial", "id"); strings.Join(got, ",") != strings.Join(partial, ",") {
		t.Fatalf("partial runs paged = %v, want %v", got, partial)
	}
}
