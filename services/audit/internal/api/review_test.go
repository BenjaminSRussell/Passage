package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
)

func (f *fakeStore) ListBreakGlass(_ context.Context, status string, limit int, afterSeq int64) ([]store.BreakGlassRead, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []store.BreakGlassRead{}
	for _, r := range f.rows {
		review := f.reviews[r.ID]
		if r.Seq <= afterSeq || !domain.IsBreakGlass(r.Purpose) ||
			(status == store.ReviewPending && review != nil) || (status == store.ReviewReviewed && review == nil) {
			continue
		}
		out = append(out, store.BreakGlassRead{ReadRecord: r, Review: review})
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) ReviewRead(_ context.Context, readID, reviewerID, outcome, note string) (store.BreakGlassRead, error) {
	for _, r := range f.rows {
		if r.ID != readID {
			continue
		}
		if why := domain.ReviewRefusal(r.Purpose, r.UserID, reviewerID, f.reviews[r.ID] != nil); why != "" {
			return store.BreakGlassRead{}, httpx.NewProblem(http.StatusConflict, domain.CodeReviewRefused, why)
		}
		if f.reviews == nil {
			f.reviews = map[string]*store.ReadReview{}
		}
		f.reviews[r.ID] = &store.ReadReview{Outcome: outcome, Note: note, ReviewerID: reviewerID, ReviewedAt: time.Now().UTC()}
		return store.BreakGlassRead{ReadRecord: r, Review: f.reviews[r.ID]}, nil
	}
	return store.BreakGlassRead{}, httpx.NewProblem(http.StatusNotFound, domain.CodeReadNotFound, "no such read")
}

const dutyReviewer = "audit.read-log.review"

// doAs is do with another user, so a reviewer can differ from the reader (user-1).
func doAs(r http.Handler, user, method, path, body, duties string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", user)
	req.Header.Set("X-Passage-Duties", duties)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestBreakGlassReadsAreListedAndReviewedByAnotherPerson(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f)
	for _, purpose := range []string{"benefits enquiry", "BREAK-GLASS: member in ER", "break-glass"} {
		body := strings.Replace(validRead, `"benefits enquiry"`, `"`+purpose+`"`, 1)
		if rec := do(r, http.MethodPost, readsPath, body, dutyWriter, true); rec.Code != http.StatusCreated {
			t.Fatalf("seed %q: %d %s", purpose, rec.Code, rec.Body.String())
		}
	}
	list := func(query string) []store.BreakGlassRead {
		t.Helper()
		rec := doAs(r, "auditor", http.MethodGet, readsPath+":break-glass"+query, "", dutyReviewer)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body.String())
		}
		var out struct {
			Items      []store.BreakGlassRead `json:"items"`
			NextCursor string                 `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Items
	}
	pending := list("")
	if len(pending) != 2 || pending[0].Purpose != "BREAK-GLASS: member in ER" {
		t.Fatalf("pending by default: %+v", pending)
	}
	if page := list("?limit=1"); len(page) != 1 {
		t.Fatalf("limit: %+v", page)
	}
	id := pending[0].ID
	review := readsPath + "/" + id + ":review"

	if rec := doAs(r, "user-1", http.MethodPost, review, `{"outcome":"APPROVED"}`, dutyReviewer); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), domain.CodeReviewRefused) {
		t.Fatalf("the reader reviewing their own access: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAs(r, "auditor", http.MethodPost, review, `{"outcome":"FLAGGED"}`, dutyReviewer); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"note"`) {
		t.Fatalf("flagged without a note: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAs(r, "auditor", http.MethodPost, review, `{"outcome":"FINE"}`, dutyReviewer); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown outcome: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAs(r, "auditor", http.MethodPost, review, `{"outcome":"APPROVED"}`, dutyViewer); rec.Code != http.StatusForbidden {
		t.Fatalf("review needs the review duty: %d", rec.Code)
	}
	if rec := doAs(r, "auditor", http.MethodGet, readsPath+":break-glass", "", dutyViewer+","+dutyExport); rec.Code != http.StatusForbidden {
		t.Fatalf("the queue needs the review duty: %d", rec.Code)
	}
	rec := doAs(r, "auditor", http.MethodPost, review, `{"outcome":"FLAGGED","note":"no ticket"}`, dutyReviewer)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reviewerId":"auditor"`) {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAs(r, "auditor-2", http.MethodPost, review, `{"outcome":"APPROVED"}`, dutyReviewer); rec.Code != http.StatusConflict {
		t.Fatalf("second review: %d %s", rec.Code, rec.Body.String())
	}
	if reviewed := list("?status=reviewed"); len(reviewed) != 1 || reviewed[0].Review.Outcome != domain.ReviewFlagged {
		t.Fatalf("reviewed: %+v", reviewed)
	}
	if all := list("?status=all"); len(all) != 2 {
		t.Fatalf("all: %+v", all)
	}
	for _, q := range []string{"?status=open", "?limit=0", "?cursor=!!"} {
		if rec := doAs(r, "auditor", http.MethodGet, readsPath+":break-glass"+q, "", dutyReviewer); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), domain.CodeReadQuery) {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	if rec := doAs(r, "auditor", http.MethodPost, readsPath+"/"+testkit.TenantID(9)+":review", `{"outcome":"APPROVED"}`, dutyReviewer); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown read: %d %s", rec.Code, rec.Body.String())
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "audit_phi_break_glass_reviews_total 1") {
		t.Fatalf("metrics: %s", metrics.Body.String())
	}
}
