package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// ListSSCCs answers from canned rows sorted by SSCC, honouring the cursor and
// the limit the handler asks for, and records the filter.
func (f *fakeStore) ListSSCCs(_ context.Context, filter store.SSCCFilter) ([]store.SSCCRow, error) {
	f.ssccFilter, f.ssccCalls = filter, f.ssccCalls+1
	out := []store.SSCCRow{}
	for _, r := range f.ssccRows {
		if filter.AfterSSCC != "" && r.SSCC <= filter.AfterSSCC {
			continue
		}
		if len(out) == filter.Limit {
			break
		}
		out = append(out, r)
	}
	return out, nil
}

func ssccRows(n int) []store.SSCCRow {
	out := make([]store.SSCCRow, n)
	for i := range out {
		out[i] = store.SSCCRow{SSCC: fmt.Sprintf("00614141012345678%d", i), ContainerID: ids.New(), ContainerType: "CARTON-C12",
			Status: "CLOSED", ShipmentID: ids.New(), ShipmentNumber: "SH-0000001", OrderID: ids.New(), CustomerID: ids.New(),
			IssuedAt: time.Date(2026, 10, 21, 23, 30, 0, 0, time.UTC)}
	}
	return out
}

func decodeSSCCPage(t *testing.T, body []byte) ssccPage {
	t.Helper()
	var page ssccPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestSSCCRegister_PagesByCursorWithNoReuseDate(t *testing.T) {
	rows := ssccRows(3)
	fs := &fakeStore{ssccRows: rows}
	h := newServer(t, fs)

	rec := do(t, h, http.MethodGet, "/v1/fulfillment/sscc-register?limit=2", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fs.ssccFilter.Limit != 3 {
		t.Fatalf("store limit = %d, want limit+1 = 3", fs.ssccFilter.Limit)
	}
	page := decodeSSCCPage(t, rec.Body.Bytes())
	if len(page.Items) != 2 || page.Items[0].SSCC != rows[0].SSCC || page.NextCursor != rows[1].SSCC {
		t.Fatalf("page = %+v, want the first 2 rows and cursor %s", page, rows[1].SSCC)
	}
	if page.Items[0].NoReuseUntil != "2027-10-21" {
		t.Fatalf("noReuseUntil = %q, want 2027-10-21 (12 months after issue, BR-28.5)", page.Items[0].NoReuseUntil)
	}

	rec = do(t, h, http.MethodGet, "/v1/fulfillment/sscc-register?limit=2&cursor="+page.NextCursor, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second page status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fs.ssccFilter.AfterSSCC != rows[1].SSCC {
		t.Fatalf("store cursor = %q, want %q", fs.ssccFilter.AfterSSCC, rows[1].SSCC)
	}
	second := decodeSSCCPage(t, rec.Body.Bytes())
	if len(second.Items) != 1 || second.Items[0].SSCC != rows[2].SSCC || second.NextCursor != "" {
		t.Fatalf("second page = %+v, want only the last row and no cursor", second)
	}
}

func TestSSCCRegister_EmptyIsAnEmptyList(t *testing.T) {
	rec := do(t, newServer(t, &fakeStore{}), http.MethodGet, "/v1/fulfillment/sscc-register", "", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"items\":[]}\n" {
		t.Fatalf("status = %d body = %q, want 200 with an empty items array", rec.Code, rec.Body.String())
	}
}

func TestSSCCRegister_PassesFilters(t *testing.T) {
	customer := ids.New()
	fs := &fakeStore{}
	path := "/v1/fulfillment/sscc-register?companyPrefix=0614141&customerId=" + customer + "&from=2026-10-01&to=2026-10-31"
	if rec := do(t, newServer(t, fs), http.MethodGet, path, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	f := fs.ssccFilter
	if f.CompanyPrefix != "0614141" || f.CustomerID != customer || f.From.Format(dateLayout) != "2026-10-01" ||
		f.To.Format(dateLayout) != "2026-10-31" || f.Limit != 101 {
		t.Fatalf("filter = %+v, want the query values and the default limit 100 (+1)", f)
	}
}

func TestSSCCRegister_RejectsBadInput(t *testing.T) {
	for _, tc := range []struct{ name, query, field string }{
		{"prefix too short", "companyPrefix=12345", "companyPrefix"},
		{"prefix not digits", "companyPrefix=06141A", "companyPrefix"},
		{"customer not a uuid", "customerId=nope", "customerId"},
		{"impossible date", "from=2026-02-30", "from"},
		{"to before from", "from=2026-10-31&to=2026-10-01", "to"},
		{"limit zero", "limit=0", "limit"},
		{"limit too high", "limit=501", "limit"},
		{"cursor not an sscc", "cursor=abc", "cursor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := do(t, newServer(t, fs), http.MethodGet, "/v1/fulfillment/sscc-register?"+tc.query, "", nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s, want 400", rec.Code, rec.Body.String())
			}
			var problem struct {
				Code   string            `json:"code"`
				Errors map[string]string `json:"errors"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil || problem.Code != CodeValidation || problem.Errors[tc.field] == "" {
				t.Fatalf("body = %s, want %s on %s", rec.Body.String(), CodeValidation, tc.field)
			}
			if fs.ssccCalls != 0 {
				t.Fatal("the store must not be read for invalid input")
			}
		})
	}
}

func TestSSCCRegister_RequiresWarehouseView(t *testing.T) {
	fs := &fakeStore{}
	rec := do(t, newServer(t, fs), http.MethodGet, "/v1/fulfillment/sscc-register", "", map[string]string{"X-Passage-Duties": "WarehousePick"})
	if rec.Code != http.StatusForbidden || fs.ssccCalls != 0 {
		t.Fatalf("status = %d calls = %d, want 403 and no read", rec.Code, fs.ssccCalls)
	}
}
