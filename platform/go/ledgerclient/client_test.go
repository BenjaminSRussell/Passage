package ledgerclient_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantID = "11111111-1111-4111-8111-111111111111"
	entityID = "22222222-2222-4222-8222-222222222222"
	ledgerID = "33333333-3333-4333-8333-333333333333"
)

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "user-1"})
}

func signer(t *testing.T, service string) (*tenant.ServiceSigner, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tenant.NewServiceSigner(service, priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, pub
}

func newClientAs(t *testing.T, service string, handler http.HandlerFunc) (*ledgerclient.Client, *tenant.ServiceVerifier) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s, pub := signer(t, service)
	c, err := ledgerclient.New(srv.URL+"/", service, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{service: pub})
}

func newClient(t *testing.T, handler http.HandlerFunc) (*ledgerclient.Client, *tenant.ServiceVerifier) {
	t.Helper()
	return newClientAs(t, "receiving", handler)
}

func entry() ledgerclient.Entry {
	return ledgerclient.Entry{LegalEntityID: entityID, LedgerID: ledgerID, DocumentID: "doc-1", PostingDate: "2026-10-01", Currency: "USD",
		IdempotencyKey: "key-1", SourceType: "wip", Lines: []ledgerclient.Line{
			{Account: "1400", Amount: decimal.RequireFromString("12.5")},
			{Account: "2100", Credit: true, Amount: decimal.RequireFromString("12.50")},
		}}
}

func TestPost_SendsASignedNarrowIdempotentJournal(t *testing.T) {
	var verifier *tenant.ServiceVerifier
	var claims tenant.ServiceClaims
	var got map[string]any
	var key, path string
	c, v := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		var err error
		claims, err = verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil {
			t.Errorf("token: %v", err)
		}
		key, path = r.Header.Get("Idempotency-Key"), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"entries":[{"id":"entry-9"}]}`))
	})
	verifier = v
	id, err := c.Post(principalCtx(), entry())
	if err != nil || id != "entry-9" {
		t.Fatalf("id %q err %v", id, err)
	}
	if path != "/v1/ledger/journal-entries" || key != "key-1" {
		t.Fatalf("path %s key %s", path, key)
	}
	if len(claims.Duties) != 1 || claims.Duties[0] != ledgerclient.DutyPost || claims.Le != entityID || claims.Act != "user-1" {
		t.Fatalf("claims %+v", claims)
	}
	src := got["source"].(map[string]any)
	lines := got["lines"].([]any)
	first := lines[0].(map[string]any)
	second := lines[1].(map[string]any)
	if src["system"] != "receiving" || src["type"] != "wip" || src["id"] != "doc-1" || first["debitCredit"] != "D" || second["debitCredit"] != "C" ||
		first["amount"].(map[string]any)["value"] != "12.50" {
		t.Fatalf("body %v", got)
	}
}

func TestPost_PassesTheProblemCodeThrough(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"LEDGER_PERIOD_CLOSED"}`))
	})
	_, err := c.Post(principalCtx(), entry())
	var le *ledgerclient.Error
	if !errors.As(err, &le) || le.Status != 422 || ledgerclient.CodeOf(err) != ledgerclient.CodePeriodClosed || !ledgerclient.Rejected(err) {
		t.Fatalf("err %v", err)
	}
}

func TestPost_ConflictAndServerErrorsAreNotRejections(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusServiceUnavailable} {
		c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		if _, err := c.Post(principalCtx(), entry()); err == nil || ledgerclient.Rejected(err) {
			t.Fatalf("status %d: err %v", status, err)
		}
	}
}

func TestPost_ResponseWithoutAnEntryIdFails(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"entries":[]}`)) })
	if _, err := c.Post(principalCtx(), entry()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestPost_RefusesWithoutCallingTheLedger(t *testing.T) {
	called := false
	c, _ := newClient(t, func(http.ResponseWriter, *http.Request) { called = true })
	unbalanced := entry()
	unbalanced.Lines[1].Amount = decimal.RequireFromString("12.49")
	if _, err := c.Post(principalCtx(), unbalanced); !errors.Is(err, ledgerclient.ErrUnbalanced) {
		t.Fatalf("unbalanced: %v", err)
	}
	if _, err := c.Post(context.Background(), entry()); !errors.Is(err, ledgerclient.ErrNoPrincipal) {
		t.Fatalf("no principal: %v", err)
	}
	if called {
		t.Fatal("the ledger must not be called")
	}
}

func TestNew_RequiresASigner(t *testing.T) {
	if _, err := ledgerclient.New("http://ledger", "receiving", nil, nil); !errors.Is(err, ledgerclient.ErrUnsigned) {
		t.Fatalf("err %v", err)
	}
	s, _ := signer(t, "receiving")
	if _, err := ledgerclient.New("", "receiving", s, nil); err == nil {
		t.Fatal("expected an error for no URL")
	}
}

func TestReverse_PostsToTheEntryWithTheReverseDuty(t *testing.T) {
	var verifier *tenant.ServiceVerifier
	var path string
	var duties []string
	c, v := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		claims, _ := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		path, duties = r.URL.Path, claims.Duties
		_, _ = w.Write([]byte(`{"journalEntryId":"rev-1"}`))
	})
	verifier = v
	id, err := c.Reverse(principalCtx(), ledgerclient.Reversal{LegalEntityID: entityID, EntryID: "entry 9", IdempotencyKey: "rev-key", PostingDate: "2026-10-02"})
	if err != nil || id != "rev-1" {
		t.Fatalf("id %q err %v", id, err)
	}
	if path != "/v1/ledger/journal-entries/entry 9/reverse" || len(duties) != 1 || duties[0] != ledgerclient.DutyReverse {
		t.Fatalf("path %q duties %v", path, duties)
	}
}

func TestPostClosed_OnlyCloseCanSignThePostClosedDuty(t *testing.T) {
	for _, tc := range []struct {
		service string
		want    []string
	}{
		{"close", []string{ledgerclient.DutyPost, ledgerclient.DutyPostClosed}},
		{"receiving", []string{ledgerclient.DutyPost}},
	} {
		t.Run(tc.service, func(t *testing.T) {
			var verifier *tenant.ServiceVerifier
			var posted, reversed []string
			c, v := newClientAs(t, tc.service, func(w http.ResponseWriter, r *http.Request) {
				claims, _ := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
				if strings.HasSuffix(r.URL.Path, "/reverse") {
					reversed = claims.Duties
				} else {
					posted = claims.Duties
				}
				_, _ = w.Write([]byte(`{"journalEntryId":"e-1"}`))
			})
			verifier = v
			in := entry()
			in.PostClosed = true
			if _, err := c.Post(principalCtx(), in); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Reverse(principalCtx(), ledgerclient.Reversal{LegalEntityID: entityID, EntryID: "e-0", IdempotencyKey: "k", PostClosed: true}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(posted, tc.want) {
				t.Fatalf("post duties %v, want %v", posted, tc.want)
			}
			wantReverse := []string{ledgerclient.DutyReverse}
			if tc.service == "close" {
				wantReverse = append(wantReverse, ledgerclient.DutyPostClosed)
			}
			if !slices.Equal(reversed, wantReverse) {
				t.Fatalf("reverse duties %v, want %v", reversed, wantReverse)
			}
		})
	}
}

func TestBalance_SumsTheRequestedAccountOnly(t *testing.T) {
	var query map[string][]string
	c, _ := newClientAs(t, "payables", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"currency":"USD","rows":[{"account":"1400","debit":"100.00","credit":"40.25"},{"account":"1400","debit":"1.00","credit":"0"},{"account":"9999","debit":"5","credit":"0"}]}`))
	})
	b, err := c.Balance(principalCtx(), ledgerclient.BalanceQuery{LegalEntityID: entityID, LedgerID: ledgerID, Account: "1400", PeriodFrom: "2026-09", PeriodTo: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Net().Equal(decimal.RequireFromString("60.75")) || b.Currency != "USD" || query["account_from"][0] != "1400" || query["period_from"][0] != "2026-09" || query["period_to"][0] != "2026-10" || query["ledger"][0] != ledgerID {
		t.Fatalf("balance %+v query %v", b, query)
	}
}

func TestBalance_RejectsAnUnusableBody(t *testing.T) {
	c, _ := newClientAs(t, "payables", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"currency":"USD","rows":[{"account":"1400","debit":"x","credit":"0"}]}`))
	})
	if _, err := c.Balance(principalCtx(), ledgerclient.BalanceQuery{LegalEntityID: entityID, LedgerID: ledgerID, Account: "1400"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestLegalEntity_ReadsTheOwnerWithTheLookupDuty(t *testing.T) {
	var verifier *tenant.ServiceVerifier
	var claims tenant.ServiceClaims
	var path string
	c, v := newClientAs(t, "payroll", func(w http.ResponseWriter, r *http.Request) {
		claims, _ = verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"` + ledgerID + `","legalEntityId":"` + entityID + `"}`))
	})
	verifier = v
	owner, err := c.LegalEntity(principalCtx(), entityID, ledgerID)
	if err != nil || owner != entityID || path != "/v1/ledger/ledgers/"+ledgerID || len(claims.Duties) != 1 || claims.Duties[0] != ledgerclient.DutyLedgerRead {
		t.Fatalf("owner %q err %v path %s claims %+v", owner, err, path, claims)
	}
}

func TestLegalEntity_UnknownLedgerIsRejectedAndUnusableBodyFails(t *testing.T) {
	missing, _ := newClientAs(t, "payroll", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"LEDGER_NOT_FOUND"}`))
	})
	if _, err := missing.LegalEntity(principalCtx(), entityID, ledgerID); !ledgerclient.Rejected(err) || ledgerclient.CodeOf(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("err %v", err)
	}
	empty, _ := newClientAs(t, "payroll", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	if _, err := empty.LegalEntity(principalCtx(), entityID, ledgerID); err == nil {
		t.Fatal("expected an error")
	}
}

func TestFromEnv(t *testing.T) {
	if c, err := ledgerclient.FromEnv(func(string) string { return "" }, "production"); c != nil || err != nil {
		t.Fatalf("unset: %v %v", c, err)
	}
	if c, err := ledgerclient.FromEnv(func(k string) string { return map[string]string{"LEDGER_URL": "http://ledger"}[k] }, "production"); c != nil || !errors.Is(err, ledgerclient.ErrUnsigned) {
		t.Fatalf("no key: %v %v", c, err)
	}
	seed := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	env := map[string]string{"LEDGER_URL": "http://ledger", "LEDGER_TIMEOUT": "750ms", "PASSAGE_SERVICE_NAME": "production", "PASSAGE_SERVICE_SIGNING_KEY": seed}
	if c, err := ledgerclient.FromEnv(func(k string) string { return env[k] }, "production"); c == nil || err != nil {
		t.Fatalf("configured: %v %v", c, err)
	}
}

func TestEntry_RoundsEachLineBeforeCheckingBalance(t *testing.T) {
	e := entry()
	e.Lines[0].Amount, e.Lines[1].Amount = decimal.RequireFromString("10.004"), decimal.RequireFromString("9.996")
	r, err := e.Rounded()
	if err != nil || !r.Lines[0].Amount.Equal(decimal.RequireFromString("10.00")) || !r.Lines[1].Amount.Equal(decimal.RequireFromString("10.00")) {
		t.Fatalf("rounded %+v %v", r, err)
	}
	e.Lines[1].Amount = decimal.RequireFromString("10.006")
	if _, err := e.Rounded(); !errors.Is(err, ledgerclient.ErrUnbalanced) {
		t.Fatalf("lines that differ after rounding must be refused: %v", err)
	}
	e.Lines[0].Amount, e.Lines[1].Amount = decimal.RequireFromString("0.004"), decimal.RequireFromString("0.004")
	if _, err := e.Rounded(); !errors.Is(err, ledgerclient.ErrEmptyEntry) {
		t.Fatalf("a line that rounds to 0.00 must be refused: %v", err)
	}
	e.Currency = "JPY"
	e.Lines[0].Amount, e.Lines[1].Amount = decimal.RequireFromString("100.4"), decimal.RequireFromString("100")
	if r, err := e.Rounded(); err != nil || !r.Lines[0].Amount.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("JPY has no minor unit: %+v %v", r, err)
	}
}
