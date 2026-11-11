package forecastsrc

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func signer(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	s, err := tenant.NewServiceSigner("treasury", priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ctxFor(le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "cfo", Duties: map[string]struct{}{}})
}

var (
	from = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to   = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
)

func TestPayables_ReadsOpenAndPartiallySettledPagesAsOutflows(t *testing.T) {
	le := ids.New()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(tenant.HeaderServiceToken) == "" || r.URL.Path != "/v1/payables/open-items" {
			t.Errorf("path %q token %q", r.URL.Path, r.Header.Get(tenant.HeaderServiceToken))
		}
		queries = append(queries, r.URL.RawQuery)
		switch {
		case r.URL.Query().Get("status") == "OPEN" && r.URL.Query().Get("cursor") == "":
			_, _ = w.Write([]byte(`{"items":[{"id":"a","dueDate":"2026-11-05","currency":"USD","openAmount":"100.50","status":"OPEN"},{"id":"bad","dueDate":"nope","currency":"USD","openAmount":"1"}],"nextCursor":"c2"}`))
		case r.URL.Query().Get("status") == "OPEN":
			_, _ = w.Write([]byte(`{"items":[{"id":"b","dueDate":"2026-11-20","currency":"EUR","openAmount":"40"}]}`))
		default:
			_, _ = w.Write([]byte(`{"items":[{"id":"c","dueDate":"2026-12-01","currency":"USD","openAmount":"25"},{"id":"zero","dueDate":"2026-12-01","currency":"USD","openAmount":"0"}]}`))
		}
	}))
	defer srv.Close()
	res, err := NewPayables(srv.URL, signer(t), nil).Contribute(ctxFor(le), le, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contributions) != 3 || res.Truncated {
		t.Fatalf("contributions = %+v truncated %v, want 3 (the malformed and zero items skipped)", res.Contributions, res.Truncated)
	}
	got := map[string]string{}
	for _, c := range res.Contributions {
		if c.SourceType != TypeAP {
			t.Errorf("source type %q", c.SourceType)
		}
		got[c.SourceID] = c.Amount.String() + " " + c.Currency
	}
	if got["a"] != "-100.5 USD" || got["b"] != "-40 EUR" || got["c"] != "-25 USD" {
		t.Fatalf("amounts = %v, want outflows in the item currency", got)
	}
	if len(queries) != 3 {
		t.Fatalf("%d requests, want 3 (two OPEN pages and one PARTIALLY_SETTLED)", len(queries))
	}
}

func TestReceivables_AppliesTheWindowAndKeepsCreditsNegative(t *testing.T) {
	le := ids.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[
			{"id":"early","dueDate":"2026-09-01","currency":"USD","openAmount":"10"},
			{"id":"in","dueDate":"2026-11-15","currency":"USD","openAmount":"300.25"},
			{"id":"credit","dueDate":"2026-11-16","currency":"USD","openAmount":"-50"},
			{"id":"late","dueDate":"2027-02-01","currency":"USD","openAmount":"10"}]}`))
	}))
	defer srv.Close()
	res, err := NewReceivables(srv.URL, signer(t), nil).Contribute(ctxFor(le), le, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contributions) != 2 || res.Contributions[0].SourceID != "in" || res.Contributions[0].Amount.String() != "300.25" || res.Contributions[1].Amount.String() != "-50" {
		t.Fatalf("contributions = %+v", res.Contributions)
	}
}

func TestSources_FailLoudlyWithoutAKeyOrOnAnErrorStatus(t *testing.T) {
	le := ids.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	if _, err := NewPayables(srv.URL, nil, nil).Contribute(ctxFor(le), le, from, to); err == nil {
		t.Fatal("no signing key must fail, not call unsigned")
	}
	if _, err := NewReceivables(srv.URL, signer(t), nil).Contribute(ctxFor(le), le, from, to); err == nil {
		t.Fatal("a 403 must be an error")
	}
}

func TestReceivables_ReadsEveryPageOfTheWindowAndMarksARunawayListTruncated(t *testing.T) {
	le := ids.New()
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = w.Write([]byte(`{"items":[{"id":"p1","dueDate":"2026-11-01","currency":"USD","openAmount":"10"}],"nextCursor":"c2"}`))
		case "c2":
			_, _ = w.Write([]byte(`{"items":[{"id":"p2","dueDate":"2026-11-02","currency":"USD","openAmount":"20"}],"nextCursor":"c3"}`))
		default:
			_, _ = w.Write([]byte(`{"items":[{"id":"p3","dueDate":"2026-11-03","currency":"USD","openAmount":"30"}]}`))
		}
	}))
	defer srv.Close()
	res, err := NewReceivables(srv.URL, signer(t), nil).Contribute(ctxFor(le), le, from, to)
	if err != nil || len(res.Contributions) != 3 || res.Truncated {
		t.Fatalf("contributions = %+v, truncated %v, %v; want all three pages and a complete result", res.Contributions, res.Truncated, err)
	}
	if q := queries[0]; q.Get("due_from") != "2026-10-01" || q.Get("due_to") != "2026-12-31" || q.Get("status") != "open" || queries[2].Get("cursor") != "c3" {
		t.Fatalf("queries = %v; want the window on every request and the cursor passed on", queries)
	}

	endless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"x","dueDate":"2026-11-01","currency":"USD","openAmount":"1"}],"nextCursor":"more"}`))
	}))
	defer endless.Close()
	res, err = NewReceivables(endless.URL, signer(t), nil).Contribute(ctxFor(le), le, from, to)
	if err != nil || !res.Truncated || len(res.Contributions) != maxPages {
		t.Fatalf("endless = %d contributions, truncated %v, %v; want %d and truncated", len(res.Contributions), res.Truncated, err, maxPages)
	}
}
