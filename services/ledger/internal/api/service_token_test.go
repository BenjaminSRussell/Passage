package api

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const testLegalEntity = "0190a000-0000-7000-8000-0000000000a1"

type tokenRig struct {
	handler   http.Handler
	store     *fakeStore
	receiving *tenant.ServiceSigner
	payables  *tenant.ServiceSigner
}

func newTokenRig(t *testing.T) *tokenRig {
	t.Helper()
	rpub, rpriv, _ := ed25519.GenerateKey(nil)
	ppub, ppriv, _ := ed25519.GenerateKey(nil)
	rig := &tokenRig{store: &fakeStore{}}
	rig.receiving, _ = tenant.NewServiceSigner("receiving", rpriv)
	rig.payables, _ = tenant.NewServiceSigner("payables", ppriv)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"receiving": rpub, "payables": ppub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	rig.handler = server(rig.store)
	return rig
}

func serviceCall(h http.Handler, method, path, body, token string, extra map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "receiving-key-1")
	req.Header.Set(tenant.HeaderServiceToken, token)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func endUser() tenant.Principal {
	return tenant.Principal{TenantID: testTenant, LegalEntityID: testLegalEntity, UserID: "warehouse-user", Duties: map[string]struct{}{"receiving.post": {}}}
}

func TestServiceTokenPostsAndReversesWithEndUserAsActor(t *testing.T) {
	rig := newTokenRig(t)
	token, err := rig.receiving.Mint("ledger", endUser(), "")
	if err != nil {
		t.Fatal(err)
	}
	rec := serviceCall(rig.handler, http.MethodPost, "/v1/ledger/journal-entries", entryBody, token, nil)
	if rec.Code != http.StatusCreated || len(rig.store.posted) != 1 {
		t.Fatalf("post status %d: %s", rec.Code, rec.Body.String())
	}
	if rig.store.posted[0].Actor != "warehouse-user" {
		t.Fatalf("ledger entry actor = %q, want the end user", rig.store.posted[0].Actor)
	}

	token, _ = rig.receiving.Mint("ledger", endUser(), "")
	rec = serviceCall(rig.handler, http.MethodPost, "/v1/ledger/journal-entries/"+testEntry+"/reverse", `{"postingDate":"2026-03-31","description":"undo"}`, token, nil)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("reverse status %d: %s", rec.Code, rec.Body.String())
	}
	if len(rig.store.reversed) != 1 || rig.store.reversed[0].Actor != "warehouse-user" {
		t.Fatalf("reversed %+v", rig.store.reversed)
	}
}

func TestServiceTokenCarriesOnlyTheServiceGrantNotForwardedUserDuties(t *testing.T) {
	rig := newTokenRig(t)
	token, _ := rig.receiving.Mint("ledger", endUser(), "")
	rec := serviceCall(rig.handler, http.MethodPost, "/v1/ledger/ledgers", `{}`, token, map[string]string{
		tenant.HeaderDuties: dutyLedgerWrite, tenant.HeaderTenant: testTenant, tenant.HeaderUser: "admin",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("receiving must not create ledgers by smuggling user headers: %d %s", rec.Code, rec.Body.String())
	}

}

func TestPayablesGrantPostsOnlyWithThePostingDutyAndNeverReversesOrCreatesLedgers(t *testing.T) {
	rig := newTokenRig(t)
	reads, _ := rig.payables.MintDuties("ledger", endUser(), "", []string{"ledger.balance.read", "ledger.ledger.lookup", "ledger.entry.lookup"})
	rec := serviceCall(rig.handler, http.MethodPost, "/v1/ledger/journal-entries", entryBody, reads, nil)
	if rec.Code != http.StatusForbidden || len(rig.store.posted) != 0 {
		t.Fatalf("a payables read token must not post: %d %s", rec.Code, rec.Body.String())
	}
	post, _ := rig.payables.MintDuties("ledger", endUser(), "", []string{"ledger.entry.post"})
	if rec = serviceCall(rig.handler, http.MethodPost, "/v1/ledger/journal-entries", entryBody, post, nil); rec.Code != http.StatusCreated || len(rig.store.posted) != 1 {
		t.Fatalf("payables posting token: %d %s", rec.Code, rec.Body.String())
	}
	if rig.store.posted[0].Actor != "warehouse-user" {
		t.Fatalf("actor = %q, want the end user", rig.store.posted[0].Actor)
	}
	mints := map[string][]string{"reads": {"ledger.balance.read", "ledger.ledger.lookup", "ledger.entry.lookup"}, "posting": {"ledger.entry.post"}, "whole grant": nil}
	for name, duties := range mints {
		token, _ := rig.payables.MintDuties("ledger", endUser(), "", duties)
		rec = serviceCall(rig.handler, http.MethodPost, "/v1/ledger/journal-entries/"+testEntry+"/reverse", `{"postingDate":"2026-03-31","description":"undo"}`, token, nil)
		if rec.Code != http.StatusForbidden || len(rig.store.reversed) != 0 {
			t.Errorf("%s: payables must not reverse: %d %s", name, rec.Code, rec.Body.String())
		}
		token, _ = rig.payables.MintDuties("ledger", endUser(), "", duties)
		rec = serviceCall(rig.handler, http.MethodPost, "/v1/ledger/ledgers", `{}`, token, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: payables must not create ledgers: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestServiceTokenRejectedWhenForgedOrMisaddressed(t *testing.T) {
	rig := newTokenRig(t)
	good, _ := rig.receiving.Mint("ledger", endUser(), "")
	closeToken, _ := rig.receiving.Mint("close", endUser(), "")
	_, strangerKey, _ := ed25519.GenerateKey(nil)
	stranger, _ := tenant.NewServiceSigner("receiving", strangerKey)
	forged, _ := stranger.Mint("ledger", endUser(), "")
	for name, token := range map[string]string{
		"tampered":     good[:len(good)-3] + "AAA",
		"wrong key":    forged,
		"other target": closeToken,
		"garbage":      "x.y",
	} {
		rec := serviceCall(rig.handler, http.MethodPost, "/v1/ledger/journal-entries", entryBody, token, map[string]string{tenant.HeaderDuties: dutyPost, tenant.HeaderTenant: testTenant})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if len(rig.store.posted) != 0 {
		t.Fatal("a rejected token must never reach the store")
	}
}

func TestNarrowServiceDutiesOpenOnlyTheirOwnRoute(t *testing.T) {
	rig := newTokenRig(t)
	routes := []struct {
		method, path, body, duty string
		wantOwn                  int
	}{
		{http.MethodPost, "/v1/ledger/journal-entries", entryBody, dutyEntryPost, http.StatusCreated},
		{http.MethodGet, "/v1/ledger/journal-lines?ledger=" + testLedger + "&documentId=" + testDoc, "", dutyEntryLookup, http.StatusOK},
		{http.MethodGet, "/v1/ledger/ledgers/" + testLedger, "", dutyLedgerLookup, http.StatusOK},
	}
	for _, own := range routes {
		for _, other := range routes {
			token, err := rig.payables.MintDuties("ledger", endUser(), "", []string{own.duty})
			if err != nil {
				t.Fatalf("%s: %v", own.duty, err)
			}
			rec := serviceCall(rig.handler, other.method, other.path, other.body, token, nil)
			want := http.StatusForbidden
			if own.duty == other.duty {
				want = other.wantOwn
			}
			if rec.Code != want {
				t.Errorf("%s on %s %s: status %d, want %d: %s", own.duty, other.method, other.path, rec.Code, want, rec.Body.String())
			}
		}
	}
	for _, duty := range []string{dutyEntryValidate, dutyBatchPost, dutySetPost} {
		if _, err := rig.payables.MintDuties("ledger", endUser(), "", []string{duty}); err == nil {
			t.Errorf("%s must not be in any service grant", duty)
		}
	}
}

func TestNarrowHumanPostDutiesEachOpenOneRoute(t *testing.T) {
	keyed := map[string]string{"Idempotency-Key": "post-1", "If-Match": `"5"`}
	cases := []struct {
		duty, path, body string
		headers          map[string]string
		wantOwn          int
	}{
		{dutyEntryValidate, "/v1/ledger/validate", entryBody, nil, http.StatusOK},
		{dutyBatchPost, "/v1/ledger/journal-batches/" + testEntry + "/post", "", keyed, http.StatusCreated},
		{dutySetPost, "/v1/ledger/ledger-sets/" + testEntry + "/journal-entries", setEntryBody, keyed, http.StatusCreated},
	}
	for _, c := range cases {
		for _, other := range cases {
			want := http.StatusForbidden
			if c.duty == other.duty {
				want = c.wantOwn
			}
			rec := call(server(&fakeStore{}), http.MethodPost, other.path, other.body, c.duty, other.headers)
			if rec.Code != want {
				t.Errorf("%s on %s: status %d, want %d: %s", c.duty, other.path, rec.Code, want, rec.Body.String())
			}
		}
		rec := call(server(&fakeStore{}), http.MethodPost, c.path, c.body, dutyPost, c.headers)
		if rec.Code != c.wantOwn {
			t.Errorf("umbrella %s on %s: status %d, want %d", dutyPost, c.path, rec.Code, c.wantOwn)
		}
	}
}

// The store writes posted_by_service from the principal's verified service; the issuer of the token must reach it, and
// a forwarded header or an end user must never set it (#469).
func TestPostedByServiceIsTheVerifiedTokenIssuer(t *testing.T) {
	rig := newTokenRig(t)
	token, _ := rig.receiving.Mint("ledger", endUser(), "")
	if rec := serviceCall(rig.handler, http.MethodPost, "/v1/ledger/journal-entries", entryBody, token, map[string]string{"X-Passage-Service": "close"}); rec.Code != http.StatusCreated {
		t.Fatalf("post status %d: %s", rec.Code, rec.Body.String())
	}
	rec := call(rig.handler, http.MethodPost, "/v1/ledger/journal-entries", entryBody, dutyPost, map[string]string{"X-Passage-Service": "close", "Idempotency-Key": "end-user-key-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("end-user post status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rig.store.postedServices; len(got) != 2 || got[0] != "receiving" || got[1] != "" {
		t.Fatalf("posted_by_service = %q, want [receiving, \"\"]", got)
	}
}
