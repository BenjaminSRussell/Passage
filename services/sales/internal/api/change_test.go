package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// changeStore records the change, cancel and history calls.
type changeStore struct {
	fakeStore
	changeVersion int
	change        *store.ChangeRequest
	cancelReason  *string
	cancelVersion int
	actor         string
	historyFor    string
}

func (c *changeStore) ChangeOrder(_ context.Context, _ string, version int, req store.ChangeRequest, actor string, _ time.Time) (store.Order, error) {
	c.changeVersion, c.change, c.actor = version, &req, actor
	return store.Order{Version: version + 1, Lines: []store.Line{}, Holds: []store.Hold{}}, nil
}

func (c *changeStore) CancelOrder(_ context.Context, _ string, version int, reason, actor string, _ time.Time) (store.Order, error) {
	c.cancelVersion, c.cancelReason, c.actor = version, &reason, actor
	return store.Order{Version: version + 1, Status: "CANCELLED", Lines: []store.Line{}, Holds: []store.Hold{}}, nil
}

func (c *changeStore) ListOrderVersions(_ context.Context, id string) ([]store.OrderVersion, error) {
	c.historyFor = id
	return []store.OrderVersion{{Version: 4, Actor: "u1", Reason: "call", Diff: []domain.FieldChange{{LineNo: 10, Field: "quantity", Old: "5", New: "8"}}}}, nil
}

func serveBody(t *testing.T, s store.Store, method, path, body string, p tenant.Principal, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	New(s).Register(r)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestPatchOrder_PassesTheChangeAndETag covers the PATCH contract (FR-27.9):
// If-Match is the version changed, line fields arrive as decimals, the actor is
// the caller, and the response ETag is the new version.
func TestPatchOrder_PassesTheChangeAndETag(t *testing.T) {
	c := &changeStore{}
	body := `{"reason":"customer call","lines":[{"lineNo":10,"quantity":"8"}]}`
	rec := serveBody(t, c, http.MethodPatch, orders+"/"+ids.New(), body, principal(dutyWrite), map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if c.changeVersion != 3 || c.actor != "u1" || c.change.Reason != "customer call" || len(c.change.Lines) != 1 ||
		c.change.Lines[0].LineNo != 10 || !c.change.Lines[0].Quantity.Equal(decimal.NewFromInt(8)) || c.change.Lines[0].UnitPrice != nil {
		t.Fatalf("store got version=%d actor=%s change=%+v", c.changeVersion, c.actor, c.change)
	}
	if rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("ETag %q", rec.Header().Get("ETag"))
	}
}

func TestPatchOrder_Refusals(t *testing.T) {
	id := ids.New()
	cases := []struct {
		name    string
		body    string
		duty    string
		headers map[string]string
		want    int
		field   string
	}{
		{"missing If-Match", `{"lines":[{"lineNo":1,"quantity":"2"}]}`, dutyWrite, nil, http.StatusPreconditionRequired, ""},
		{"without the write duty", `{"lines":[{"lineNo":1,"quantity":"2"}]}`, dutyRead, map[string]string{"If-Match": `"1"`}, http.StatusForbidden, ""},
		{"nothing to change", `{"reason":"x"}`, dutyWrite, map[string]string{"If-Match": `"1"`}, http.StatusBadRequest, "lines"},
		{"line 0", `{"lines":[{"lineNo":0,"quantity":"2"}]}`, dutyWrite, map[string]string{"If-Match": `"1"`}, http.StatusBadRequest, "lines[0].lineNo"},
		{"quantity not a decimal", `{"lines":[{"lineNo":1,"quantity":"two"}]}`, dutyWrite, map[string]string{"If-Match": `"1"`}, http.StatusBadRequest, "lines[0].quantity"},
		{"bad date", `{"requestedShipDate":"03/04/2027"}`, dutyWrite, map[string]string{"If-Match": `"1"`}, http.StatusBadRequest, "requestedShipDate"},
		{"reason too long", `{"reason":"` + strings.Repeat("x", 501) + `","requestedShipDate":"2027-03-04"}`, dutyWrite, map[string]string{"If-Match": `"1"`}, http.StatusBadRequest, "reason"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &changeStore{}
			rec := serveBody(t, c, http.MethodPatch, orders+"/"+id, tc.body, principal(tc.duty), tc.headers)
			if rec.Code != tc.want || c.change != nil {
				t.Fatalf("status %d (want %d), store called=%v: %s", rec.Code, tc.want, c.change != nil, rec.Body.String())
			}
			if tc.field != "" && !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
				t.Fatalf("field error %s missing: %s", tc.field, rec.Body.String())
			}
		})
	}
}

// TestCancelOrder_DutyAndBody checks that :cancel needs sales.order.confirm
// (PRD §5.27.6), takes an optional reason and answers with the new ETag.
func TestCancelOrder_DutyAndBody(t *testing.T) {
	id := ids.New()
	c := &changeStore{}
	rec := serveBody(t, c, http.MethodPost, orders+"/"+id+":cancel", `{"reason":"duplicate"}`, principal(dutyConfirm), map[string]string{"If-Match": `"5"`})
	if rec.Code != http.StatusOK || c.cancelVersion != 5 || c.cancelReason == nil || *c.cancelReason != "duplicate" || rec.Header().Get("ETag") != `"6"` {
		t.Fatalf("status %d version %d reason %v etag %s: %s", rec.Code, c.cancelVersion, c.cancelReason, rec.Header().Get("ETag"), rec.Body.String())
	}
	c = &changeStore{}
	if rec := serveBody(t, c, http.MethodPost, orders+"/"+id+":cancel", "", principal(dutyConfirm), map[string]string{"If-Match": `"5"`}); rec.Code != http.StatusOK || *c.cancelReason != "" {
		t.Fatalf("cancel without a body: %d", rec.Code)
	}
	c = &changeStore{}
	if rec := serveBody(t, c, http.MethodPost, orders+"/"+id+":cancel", "", principal(dutyWrite), map[string]string{"If-Match": `"5"`}); rec.Code != http.StatusForbidden || c.cancelReason != nil {
		t.Fatalf("cancel with only the write duty: %d", rec.Code)
	}
	if rec := serveBody(t, c, http.MethodPost, orders+"/"+id+":cancel", "", principal(dutyConfirm), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("cancel without If-Match: %d", rec.Code)
	}
}

func TestListOrderVersions(t *testing.T) {
	id := ids.New()
	c := &changeStore{}
	rec := serveBody(t, c, http.MethodGet, orders+"/"+id+"/versions", "", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || c.historyFor != id {
		t.Fatalf("status %d for %q: %s", rec.Code, c.historyFor, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"items":[{"version":4`) || !strings.Contains(rec.Body.String(), `"field":"quantity"`) {
		t.Fatalf("body %s", rec.Body.String())
	}
	if rec := serveBody(t, &changeStore{}, http.MethodGet, orders+"/not-a-uuid/versions", "", principal(dutyRead), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id: %d", rec.Code)
	}
}
