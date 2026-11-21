package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/services/ledger/internal/store"
)

const testPostingKey = "post:ap:vendor_invoice:7F3A:post"

func postingRequestFake() *fakeStore {
	entry := testEntry
	return &fakeStore{postingRequests: map[string]store.PostingRequest{
		testPostingKey: {Key: testPostingKey, Status: store.PostingRequestPosted, LedgerID: testLedger, EntryID: &entry},
	}}
}

func TestGetPostingRequest_ReturnsWhatTheRegistryHolds(t *testing.T) {
	for _, duty := range []string{dutyRead, dutyEntryPost} {
		rec := call(server(postingRequestFake()), http.MethodGet, "/v1/ledger/posting-requests/"+testPostingKey, "", duty, nil)
		body := decode(t, rec)
		if rec.Code != http.StatusOK || body["status"] != "posted" || body["entryId"] != testEntry || body["key"] != testPostingKey {
			t.Fatalf("duty %s: %d %s", duty, rec.Code, rec.Body.String())
		}
	}
}

func TestGetPostingRequest_UnknownKeyIs404WithTheCatalogCode(t *testing.T) {
	rec := call(server(postingRequestFake()), http.MethodGet, "/v1/ledger/posting-requests/post:ap:other:1:post", "", dutyRead, nil)
	if rec.Code != http.StatusNotFound || decode(t, rec)["code"] != store.CodePostingRequestNotFound {
		t.Fatalf("want 404 %s, got %d %s", store.CodePostingRequestNotFound, rec.Code, rec.Body.String())
	}
}

func TestGetPostingRequest_KeyOverTheBoundIs404WithoutAStoreCall(t *testing.T) {
	fs := &fakeStore{err: errors.New("store must not be called")}
	rec := call(server(fs), http.MethodGet, "/v1/ledger/posting-requests/"+strings.Repeat("k", maxIdempotencyKey+1), "", dutyRead, nil)
	if rec.Code != http.StatusNotFound || decode(t, rec)["code"] != store.CodePostingRequestNotFound {
		t.Fatalf("want 404, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestGetPostingRequest_NeedsAReadOrPostDuty(t *testing.T) {
	rec := call(server(postingRequestFake()), http.MethodGet, "/v1/ledger/posting-requests/"+testPostingKey, "", dutyBalanceRead, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestGetPostingRequest_NoTenantIs401(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/ledger/posting-requests/"+testPostingKey, nil)
	req.Header.Set("X-Depot-Duties", dutyRead)
	rec := httptest.NewRecorder()
	server(postingRequestFake()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestGetPostingRequest_StoreFailureIs500AndFailsClosed(t *testing.T) {
	rec := call(server(&fakeStore{err: errors.New("db down")}), http.MethodGet, "/v1/ledger/posting-requests/"+testPostingKey, "", dutyRead, nil)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "db down") {
		t.Fatalf("want a 500 without the cause, got %d %s", rec.Code, rec.Body.String())
	}
}
