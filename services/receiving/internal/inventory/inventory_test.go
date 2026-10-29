package inventory

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	tenantID      = "00000000-0000-0000-0000-000000000001"
	legalEntityID = "00000000-0000-0000-0000-0000000000e1"
	otherEntityID = "00000000-0000-0000-0000-0000000000e2"
	journalID     = "00000000-0000-4000-8000-0000000000a1"
)

const journalJSON = `{"id":"` + journalID + `","legalEntityId":"` + legalEntityID + `","journalType":"OWNERSHIP_CHANGE","status":"POSTED","postingDate":"2026-10-01","currency":"USD","version":3,"lineCount":2,
"lines":[{"id":"l1","lineNo":1,"qty":"-10","dimensions":{"site":"S1","owner":"V-1"},"costPrice":"12.50"},{"id":"l2","lineNo":2,"qty":"-5","dimensions":{"site":"S1","owner":"V-1"},"costPrice":"10.00"}],
"glEntry":{"currency":"USD","lines":[{"account":"1400","debit":"175.00","credit":"0"},{"account":"2300","debit":"0","credit":"175.00"}],"ledgerEntryId":"e-1"}}`

func testCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntityID, Duties: map[string]struct{}{"inventory.read": {}}})
}

type recorder struct {
	mu       sync.Mutex
	requests []http.Request
}

func (r *recorder) last() http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[len(r.requests)-1]
}

func serve(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.requests = append(rec.requests, *r.Clone(context.Background()))
		rec.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, nil), rec
}

func respond(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestGet_MapsAnOwnershipChangeFromTheJournalAndItsGLEntry(t *testing.T) {
	client, rec := serve(t, respond(http.StatusOK, journalJSON))

	got, err := client.Get(testCtx(), journalID)

	if err != nil {
		t.Fatal(err)
	}
	if r := rec.last(); r.Method != http.MethodGet || r.URL.Path != "/v1/inventory/journals/"+journalID || r.Header.Get("X-Passage-Tenant") != tenantID || r.Header.Get("X-Passage-Duties") != "inventory.read" {
		t.Fatalf("request %s %s headers %v", r.Method, r.URL.Path, r.Header)
	}
	if got.JournalID != journalID || got.LegalEntityID != legalEntityID || got.VendorID != "V-1" || got.Currency != "USD" || got.AccrualAccount != "2300" ||
		got.Amount.String() != "175" || got.PostingDate.Format("2006-01-02") != "2026-10-01" {
		t.Fatalf("change %+v", got)
	}
}

func TestGet_RefusesJournalsItCannotSettle(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(journalJSON, old, new, 1) }
	tests := []struct {
		name string
		body string
	}{
		{"not an ownership change", replace(`"OWNERSHIP_CHANGE"`, `"MOVEMENT"`)},
		{"not posted", replace(`"POSTED"`, `"DRAFT"`)},
		{"two vendors", replace(`"owner":"V-1"},"costPrice":"10.00"`, `"owner":"V-2"},"costPrice":"10.00"`)},
		{"a line without an owner", replace(`"site":"S1","owner":"V-1"},"costPrice":"10.00"`, `"site":"S1"},"costPrice":"10.00"`)},
		{"no gl entry", replace(`"glEntry":{`, `"glEntryMissing":{`)},
		{"two accrual accounts", replace(`{"account":"1400","debit":"175.00","credit":"0"},{"account":"2300","debit":"0","credit":"175.00"}`, `{"account":"1400","debit":"175.00","credit":"0"},{"account":"2300","debit":"0","credit":"100.00"},{"account":"2301","debit":"0","credit":"75.00"}`)},
		{"unbalanced gl entry", replace(`"debit":"175.00","credit":"0"`, `"debit":"170.00","credit":"0"`)},
		{"bad posting date", replace(`2026-10-01`, `01/10/2026`)},
		{"not json", `<html>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := serve(t, respond(http.StatusOK, tc.body))

			if got, err := client.Get(testCtx(), journalID); err == nil {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestGet_MapsStatuses(t *testing.T) {
	tests := []struct {
		status int
		is     error
	}{{http.StatusNotFound, domain.ErrOwnershipChangeNotFound}, {http.StatusForbidden, domain.ErrInventoryForbidden}, {http.StatusBadGateway, nil}}
	for _, tc := range tests {
		client, _ := serve(t, respond(tc.status, `{}`))

		_, err := client.Get(testCtx(), journalID)

		if err == nil || (tc.is != nil && !errors.Is(err, tc.is)) {
			t.Fatalf("status %d err %v", tc.status, err)
		}
	}
}

func TestList_PagesThroughPostedOwnershipChangesOfTheLegalEntity(t *testing.T) {
	client, rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"items":[{"id":"j1","legalEntityId":"` + legalEntityID + `"},{"id":"j2","legalEntityId":"` + otherEntityID + `"}],"nextCursor":"c2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"j3","legalEntityId":"` + legalEntityID + `"}]}`))
	})

	got, err := client.List(testCtx(), legalEntityID)

	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "j1,j3" || len(rec.requests) != 2 {
		t.Fatalf("ids %v after %d requests", got, len(rec.requests))
	}
	if q := rec.requests[0].URL.Query(); q.Get("type") != "OWNERSHIP_CHANGE" || q.Get("status") != "POSTED" || q.Get("limit") != "200" {
		t.Fatalf("query %v", q)
	}
	if rec.requests[1].URL.Query().Get("cursor") != "c2" {
		t.Fatalf("second page query %v", rec.requests[1].URL.Query())
	}
}

func TestList_StopsAtThePageBoundWithoutFailing(t *testing.T) {
	client, rec := serve(t, respond(http.StatusOK, `{"items":[{"id":"j1","legalEntityId":"`+legalEntityID+`"}],"nextCursor":"again"}`))

	got, err := client.List(testCtx(), legalEntityID)

	if err != nil || len(got) != maxPages || len(rec.requests) != maxPages {
		t.Fatalf("ids %d requests %d err %v", len(got), len(rec.requests), err)
	}
}

func TestList_FailuresSurface(t *testing.T) {
	for name, handler := range map[string]func(http.ResponseWriter, *http.Request){
		"forbidden": respond(http.StatusForbidden, `{}`), "server error": respond(http.StatusInternalServerError, `{}`), "not json": respond(http.StatusOK, `nope`),
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := serve(t, handler)

			if got, err := client.List(testCtx(), legalEntityID); err == nil || got != nil {
				t.Fatalf("ids %v err %v", got, err)
			}
		})
	}
}

func TestClient_NeedsAPrincipal(t *testing.T) {
	client, rec := serve(t, respond(http.StatusOK, `{}`))

	if _, err := client.List(context.Background(), legalEntityID); err == nil || len(rec.requests) != 0 {
		t.Fatalf("err %v after %d requests", err, len(rec.requests))
	}
}

func TestPathsMatchTheInventoryRouteTable(t *testing.T) {
	api, err := os.ReadFile("../../../inventory/internal/api/api.go")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile("../../../inventory/api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`/v1/inventory/journals:`, `/v1/inventory/journals/{id}:`, `x-passage-duty: inventory.read`, `{ name: type, in: query, schema: { $ref: '#/components/schemas/JournalType' } }`} {
		if !strings.Contains(string(spec), want) {
			t.Fatalf("inventory openapi no longer contains %s; update the paths", want)
		}
	}
	if !strings.Contains(string(api), `"inventory.read"`) || domain.InventoryJournalsPath != "/v1/inventory/journals" || domain.InventoryJournalPathTmpl != "/v1/inventory/journals/%s" {
		t.Fatal("journal paths no longer match the inventory service")
	}
}
