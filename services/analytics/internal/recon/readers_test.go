package recon

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const tenantID = "00000000-0000-0000-0000-000000000001"

func jobContext() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "job", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

func TestCubeReader_ReadsNullStringAndNumberValues(t *testing.T) {
	var gotAuth, gotPath string
	var gotQuery map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotQuery)
		_, _ = w.Write([]byte(`{"data":[
			{"fin_journal_line.legal_entity_id":"` + entityA + `","fin_journal_line.revenue":null},
			{"fin_journal_line.legal_entity_id":"` + entityB + `","fin_journal_line.revenue":"12.345"}]}`))
	}))
	defer srv.Close()
	rows, err := NewCubeReader(srv.URL, "tok", nil).Revenue(jobContext(), "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := rows[entityA]; !ok || v != nil {
		t.Fatalf("a NULL revenue must come back as a nil value, got %v present=%v", v, ok)
	}
	if v := rows[entityB]; v == nil || !v.Equal(decimal.RequireFromString("12.345")) {
		t.Fatalf("entity B: %v", v)
	}
	if gotAuth != "tok" || gotPath != "/cubejs-api/v1/load" {
		t.Fatalf("auth %q path %q", gotAuth, gotPath)
	}
	q, _ := json.Marshal(gotQuery)
	want := `{"query":{"dimensions":["fin_journal_line.legal_entity_id"],"measures":["fin_journal_line.revenue"],"timeDimensions":[{"dateRange":["2026-03-10","2026-03-10"],"dimension":"fin_journal_line.posting_date"}]}}`
	if string(q) != want {
		t.Fatalf("query %s", q)
	}
}

func TestCubeReader_FailsOnErrorsAndBadRows(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"http error":   {http.StatusInternalServerError, `{}`},
		"cube error":   {http.StatusOK, `{"error":"Continue wait"}`},
		"not json":     {http.StatusOK, `nope`},
		"no entity":    {http.StatusOK, `{"data":[{"fin_journal_line.revenue":"1"}]}`},
		"bad number":   {http.StatusOK, `{"data":[{"fin_journal_line.legal_entity_id":"x","fin_journal_line.revenue":"abc"}]}`},
		"unauthorised": {http.StatusForbidden, `{}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			if _, err := NewCubeReader(srv.URL, "tok", nil).Revenue(jobContext(), "2026-03-10"); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func ledgerServer(t *testing.T, pages map[string]string, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(tenant.HeaderServiceToken) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/ledger/journal-lines" {
			*seen = append(*seen, r.URL.RawQuery)
			if body, ok := pages[r.URL.Query().Get("cursor")]; ok {
				_, _ = w.Write([]byte(body))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"currency":"USD"}`))
	}))
}

func ledgerReader(t *testing.T, url string) *LedgerReader {
	t.Helper()
	signer, err := tenant.NewServiceSigner("analytics", ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return NewLedgerReader(url, signer, nil, []LedgerRef{{LegalEntityID: entityA, LedgerID: entityB}}, "4000", "4999")
}

func TestLedgerReader_SumsCreditsMinusDebitsAcrossPages(t *testing.T) {
	var seen []string
	srv := ledgerServer(t, map[string]string{
		"":    `{"items":[{"debitCredit":"C","amountAcct":"100.10"},{"debitCredit":"D","amountAcct":"0.05"}],"nextCursor":"7.2"}`,
		"7.2": `{"items":[{"debitCredit":"C","amountAcct":"0.01"}]}`,
	}, &seen)
	defer srv.Close()
	got, err := ledgerReader(t, srv.URL).Revenue(jobContext(), "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LegalEntityID != entityA || got[0].Currency != "USD" || !got[0].Amount.Equal(decimal.RequireFromString("100.06")) {
		t.Fatalf("got %+v", got)
	}
	if len(seen) != 2 {
		t.Fatalf("queries %v", seen)
	}
}

func TestLedgerReader_EmptyDayIsZeroAndErrorsPropagate(t *testing.T) {
	var seen []string
	srv := ledgerServer(t, map[string]string{"": `{"items":[]}`}, &seen)
	defer srv.Close()
	got, err := ledgerReader(t, srv.URL).Revenue(jobContext(), "2026-03-10")
	if err != nil || len(got) != 1 || !got[0].Amount.IsZero() || got[0].Currency != "USD" {
		t.Fatalf("got %+v err %v", got, err)
	}
	down := ledgerServer(t, map[string]string{}, &seen)
	defer down.Close()
	if _, err := ledgerReader(t, down.URL).Revenue(jobContext(), "2026-03-10"); err == nil {
		t.Fatal("a ledger error must fail the read")
	}
	r := ledgerReader(t, srv.URL)
	r.signer = nil
	if _, err := r.Revenue(jobContext(), "2026-03-10"); err == nil {
		t.Fatal("a missing signing key must fail the read")
	}
}
