package ledger

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	tenantID   = "00000000-0000-0000-0000-000000000001"
	ledgerID   = "00000000-0000-4000-8000-0000000000a1"
	documentID = "00000000-0000-4000-8000-0000000000d1"
)

// ledgerPostEntryRequest mirrors postEntryRequest, sourceDTO, lineDTO and amountDTO
// in services/ledger/internal/api/api.go, which is internal to another module.
type ledgerPostEntryRequest struct {
	LedgerID     string  `json:"ledgerId"`
	DocumentID   string  `json:"documentId"`
	PostingDate  string  `json:"postingDate"`
	DocumentDate *string `json:"documentDate"`
	Description  string  `json:"description"`
	Source       struct {
		System string `json:"system"`
		Type   string `json:"type"`
		ID     string `json:"id"`
		URI    string `json:"uri"`
	} `json:"source"`
	Lines []struct {
		Account       string            `json:"account"`
		Dimensions    map[string]string `json:"dimensions"`
		DebitCredit   string            `json:"debitCredit"`
		Amount        struct{ Currency, Value string }
		Text          string `json:"text"`
		SourceLineRef string `json:"sourceLineRef"`
	} `json:"lines"`
	OptionalDimensions []string `json:"optionalDimensions"`
}

// ledgerReverseRequest mirrors reverseRequest in services/ledger/internal/api/api.go.
type ledgerReverseRequest struct {
	PostingDate   *string `json:"postingDate"`
	Description   string  `json:"description"`
	ReasonCode    string  `json:"reasonCode"`
	ReasonComment string  `json:"reasonComment"`
}

func strictDecode(t *testing.T, raw []byte, into any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("the ledger would reject this body (%v): %s", err, raw)
	}
}

func TestPostBodyDecodesIntoTheLedgersRequestShape(t *testing.T) {
	client, _, raw := serve(t, http.StatusCreated, `{"entries":[{"id":"E1"}]}`)
	if _, err := client.Post(testCtx(), testEntry()); err != nil {
		t.Fatal(err)
	}
	var got ledgerPostEntryRequest
	strictDecode(t, *raw, &got)
	if got.LedgerID != ledgerID || got.DocumentID != documentID || got.PostingDate != "2026-09-01" || got.DocumentDate == nil || *got.DocumentDate != "2026-09-01" {
		t.Fatalf("header fields %+v", got)
	}
	if got.Source.System != "receiving" || got.Source.Type != "productreceipt" || got.Source.ID != documentID {
		t.Fatalf("source %+v", got.Source)
	}
	if len(got.Lines) != 2 || got.Lines[0].DebitCredit != "D" || got.Lines[0].Amount.Value != "12.50" || got.Lines[0].Amount.Currency != "USD" ||
		got.Lines[1].DebitCredit != "C" || got.Lines[1].Amount.Value != "12.50" || got.Lines[1].Account != "2100" {
		t.Fatalf("lines %+v", got.Lines)
	}
}

func TestPostSendsLineDimensions(t *testing.T) {
	client, _, raw := serve(t, http.StatusCreated, `{"entries":[{"id":"E1"}]}`)
	entry := testEntry()
	entry.Lines[1].Dimensions = map[string]string{"VENDGRP": "DOM"}
	entry.OptionalDimensions = []string{"VENDGRP"}
	if _, err := client.Post(testCtx(), entry); err != nil {
		t.Fatal(err)
	}
	var got ledgerPostEntryRequest
	strictDecode(t, *raw, &got)
	if len(got.Lines[0].Dimensions) != 0 || got.Lines[1].Dimensions["VENDGRP"] != "DOM" {
		t.Fatalf("line dimensions %v and %v, want none and VENDGRP=DOM", got.Lines[0].Dimensions, got.Lines[1].Dimensions)
	}
	// The ledger drops an optional dimension it does not define, so a ledger
	// without VENDGRP keeps accepting the posting.
	if len(got.OptionalDimensions) != 1 || got.OptionalDimensions[0] != "VENDGRP" {
		t.Fatalf("optionalDimensions %v, want [VENDGRP]", got.OptionalDimensions)
	}
}

func TestReverseBodyDecodesIntoTheLedgersRequestShape(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	if err := New(srv.URL, nil).Reverse(testCtx(), "E1", "k:reverse", "", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var got ledgerReverseRequest
	strictDecode(t, raw, &got)
	if got.PostingDate == nil || *got.PostingDate != "2026-09-01" || got.Description == "" {
		t.Fatalf("reverse body %+v", got)
	}
}

func testEntry() domain.JournalEntry {
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return domain.JournalEntry{
		IdempotencyKey: "post:receiving:productreceipt:r-1:receipt",
		LegalEntityID:  "le-1",
		LedgerID:       ledgerID,
		PostingDate:    day,
		DocumentDate:   day,
		Source:         domain.DocumentSource{Context: "receiving", DocumentType: "productreceipt", DocumentID: documentID, Purpose: "receipt"},
		RateType:       "spot",
		Lines: []domain.JournalLine{
			{Account: "1400", Amount: money.Must("12.5", "USD")},
			{Account: "2100", Amount: money.Must("-12.5", "USD")},
		},
	}
}

func testCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
}

func serve(t *testing.T, status int, body string) (*Client, *http.Request, *[]byte) {
	t.Helper()
	var got http.Request
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(context.Background())
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, nil), &got, &raw
}

func TestPost_SendsHeadersAndBodyAndReturnsID(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		client, got, raw := serve(t, status, `{"journalEntryId":"je-9"}`)
		id, err := client.Post(testCtx(), testEntry())
		if err != nil || id != "je-9" {
			t.Fatalf("status %d: id %q err %v", status, id, err)
		}
		if got.URL.Path != domain.LedgerPostPath || got.Method != http.MethodPost {
			t.Fatalf("request %s %s", got.Method, got.URL.Path)
		}
		if got.Header.Get("Idempotency-Key") != "post:receiving:productreceipt:r-1:receipt" || got.Header.Get("X-Passage-Tenant") != tenantID {
			t.Fatalf("headers %v", got.Header)
		}
		if strings.Contains(string(*raw), "post:receiving") {
			t.Fatal("idempotency key must travel in the header only")
		}
	}
}

func TestPostForwardsTheCallersDutiesAndUser(t *testing.T) {
	client, got, _ := serve(t, http.StatusCreated, `{"journalEntryId":"je-1"}`)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: "le-1", UserID: "u-1", Duties: map[string]struct{}{"ReceivingPost": {}, "ledger.journal.post": {}}})
	if _, err := client.Post(ctx, testEntry()); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("X-Passage-Duties") != "ReceivingPost,ledger.journal.post" || got.Header.Get("X-Passage-User") != "u-1" || got.Header.Get("X-Passage-Legal-Entity") != "le-1" {
		t.Fatalf("headers %v", got.Header)
	}
}

func TestPost_Failures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantPeriod bool
	}{
		{"period closed", http.StatusUnprocessableEntity, `{"code":"period_closed"}`, true},
		{"ledger period closed code", http.StatusUnprocessableEntity, `{"code":"LEDGER_PERIOD_CLOSED"}`, true},
		{"period closed among violations", http.StatusUnprocessableEntity, `{"code":"LEDGER_UNBALANCED","violations":[{"code":"LEDGER_UNBALANCED"},{"code":"LEDGER_PERIOD_CLOSED"}]}`, true},
		{"other 422", http.StatusUnprocessableEntity, `{"code":"unbalanced_entry"}`, false},
		{"422 not json", http.StatusUnprocessableEntity, `nope`, false},
		{"server error", http.StatusInternalServerError, `{"code":"period_closed"}`, false},
		{"unavailable", http.StatusServiceUnavailable, ``, false},
		{"empty 2xx", http.StatusCreated, ``, false},
		{"non-json 2xx", http.StatusCreated, `<html>`, false},
		{"2xx without id", http.StatusCreated, `{"journalEntryId":""}`, false},
		{"2xx wrong type", http.StatusCreated, `{"journalEntryId":{"a":1}}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := serve(t, tc.status, tc.body)
			id, err := client.Post(testCtx(), testEntry())
			if err == nil || id != "" {
				t.Fatalf("want error and no id, got %q %v", id, err)
			}
			if got := errors.Is(err, domain.ErrPeriodClosed); got != tc.wantPeriod {
				t.Fatalf("period error = %v, want %v (%v)", got, tc.wantPeriod, err)
			}
		})
	}
}

func TestPost_TimeoutIsNotPeriodClosed(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	client := New(srv.URL, &http.Client{Timeout: 50 * time.Millisecond})
	id, err := client.Post(testCtx(), testEntry())
	if err == nil || id != "" || errors.Is(err, domain.ErrPeriodClosed) {
		t.Fatalf("got %q %v", id, err)
	}
}

func TestNew_DefaultsToThreeSecondTimeout(t *testing.T) {
	if got := New("http://ledger", nil).http.Timeout; got != 3*time.Second {
		t.Fatalf("timeout %v", got)
	}
}

func TestPost_RejectsBeforeSending(t *testing.T) {
	mixed := testEntry()
	mixed.Lines[1].Amount = money.Must("-12.5", "EUR")
	noAccount := testEntry()
	noAccount.Lines[0].Account = ""
	tests := []struct {
		name  string
		ctx   context.Context
		entry domain.JournalEntry
	}{
		{"no principal", context.Background(), testEntry()},
		{"no lines", testCtx(), domain.JournalEntry{IdempotencyKey: "k"}},
		{"mixed currency", testCtx(), mixed},
		{"empty account", testCtx(), noAccount},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer srv.Close()
			if _, err := New(srv.URL, nil).Post(tc.ctx, tc.entry); err == nil || called {
				t.Fatalf("err %v called %v", err, called)
			}
		})
	}
}

func TestPathsMatchTheLedgerServiceRouteTable(t *testing.T) {
	src, err := os.ReadFile("../../../ledger/internal/api/api.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`r.Route("/v1/ledger"`, `Post("/journal-entries", a.postEntry)`, `Post("/journal-entries/{id}/reverse", a.reverseEntry)`} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("ledger route table no longer contains %s; update domain.LedgerPostPath and LedgerReversePathTmpl", want)
		}
	}
	if domain.LedgerPostPath != "/v1/ledger/journal-entries" || fmt.Sprintf(domain.LedgerReversePathTmpl, "E1") != "/v1/ledger/journal-entries/E1/reverse" {
		t.Fatalf("paths %s %s do not match the ledger routes", domain.LedgerPostPath, domain.LedgerReversePathTmpl)
	}
}

func TestPostReadsTheLedgerEntriesResponseAndClassifiesRejections(t *testing.T) {
	entry := domain.JournalEntry{IdempotencyKey: "k", LegalEntityID: "le", Lines: []domain.JournalLine{{Account: "1400", Amount: money.Must("5.00", "USD")}, {Account: "2100", Amount: money.Must("-5.00", "USD")}}}
	tests := []struct {
		name     string
		status   int
		body     string
		wantID   string
		rejected bool
		conflict bool
	}{
		{"entries array", http.StatusCreated, `{"entries":[{"id":"E7"}]}`, "E7", false, false},
		{"validation rejection is definite", http.StatusBadRequest, `{"code":"COM-0001"}`, "", true, false},
		{"forbidden is definite", http.StatusForbidden, `{}`, "", true, false},
		{"server error is unknown", http.StatusInternalServerError, `{}`, "", false, false},
		{"gateway timeout is unknown", http.StatusGatewayTimeout, `{}`, "", false, false},
		{"request timeout is unknown", http.StatusRequestTimeout, `{}`, "", false, false},
		{"idempotency conflict", http.StatusConflict, `{"code":"LEDGER_IDEMPOTENCY_CONFLICT"}`, "", false, true},
		{"in progress is unknown", http.StatusConflict, `{"code":"LEDGER_IDEMPOTENCY_IN_PROGRESS"}`, "", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := serve(t, tc.status, tc.body)
			id, err := client.Post(testCtx(), entry)
			if errors.Is(err, domain.ErrLedgerRejected) != tc.rejected || errors.Is(err, domain.ErrLedgerConflict) != tc.conflict || id != tc.wantID {
				t.Fatalf("id=%q err=%v, want id %q rejected=%v conflict=%v", id, err, tc.wantID, tc.rejected, tc.conflict)
			}
		})
	}
}

func TestReverseCallsTheReverseRouteWithItsOwnKey(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := New(srv.URL, nil).Reverse(testCtx(), "E1", "k:reverse", "", day); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/v1/ledger/journal-entries/E1/reverse" || got.Header.Get("Idempotency-Key") != "k:reverse" {
		t.Fatalf("reverse sent %s with key %q", got.URL.Path, got.Header.Get("Idempotency-Key"))
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(failing.Close)
	if err := New(failing.URL, nil).Reverse(testCtx(), "E1", "k", "", day); err == nil {
		t.Fatal("a failed reversal must surface")
	}
}

func TestReverseClassifiesLedgerRejectionsApartFromOutages(t *testing.T) {
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		status   int
		body     string
		closed   bool
		rejected bool
		conflict bool
	}{
		{"period closed", http.StatusUnprocessableEntity, `{"code":"LEDGER_PERIOD_CLOSED"}`, true, false, false},
		{"other 422 is definite", http.StatusUnprocessableEntity, `{"code":"LEDGER_REASON_REQUIRED"}`, false, true, false},
		{"not found is definite", http.StatusNotFound, `{}`, false, true, false},
		{"server error is unknown", http.StatusInternalServerError, `{}`, false, false, false},
		{"timeout status is unknown", http.StatusRequestTimeout, `{}`, false, false, false},
		{"idempotency conflict", http.StatusConflict, `{"code":"LEDGER_IDEMPOTENCY_CONFLICT"}`, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := serve(t, tc.status, tc.body)
			err := client.Reverse(testCtx(), "E1", "k", "", day)
			if err == nil || errors.Is(err, domain.ErrPeriodClosed) != tc.closed || errors.Is(err, domain.ErrLedgerRejected) != tc.rejected || errors.Is(err, domain.ErrLedgerConflict) != tc.conflict {
				t.Fatalf("err=%v, want closed=%v rejected=%v conflict=%v", err, tc.closed, tc.rejected, tc.conflict)
			}
		})
	}
}

func TestPost_DoesNotFollowRedirectsWithTheCallersIdentity(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	_, err := New(redirector.URL, nil).Post(testCtx(), testEntry())

	if err == nil || followed {
		t.Fatalf("redirect followed %v, err %v", followed, err)
	}
}

func testSigner(t *testing.T) (*tenant.ServiceSigner, *tenant.ServiceVerifier) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("receiving", priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"receiving": pub})
}

func TestSignedPostSendsServiceTokenInsteadOfUserDuties(t *testing.T) {
	const entity = "00000000-0000-4000-8000-0000000000e1"
	signer, verifier := testSigner(t)
	client, got, _ := serve(t, http.StatusCreated, `{"journalEntryId":"je-1"}`)
	client.WithSigner(signer)
	entry := testEntry()
	entry.LegalEntityID = entity
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: "00000000-0000-4000-8000-0000000000e2," + entity, UserID: "u-1", Duties: map[string]struct{}{"ReceivingPost": {}}})
	if _, err := client.Post(ctx, entry); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"X-Passage-Duties", "X-Passage-User", "X-Passage-Tenant", "X-Passage-Legal-Entity"} {
		if got.Header.Get(h) != "" {
			t.Fatalf("%s must not be sent with a service token: %v", h, got.Header)
		}
	}
	claims, err := verifier.Verify(got.Header.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Le != entity || claims.Act != "u-1" || claims.Tid != tenantID || claims.Aud != "ledger" || !slices.Equal(claims.Duties, []string{"ledger.entry.post"}) {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func TestSignedReverseUsesPrincipalLegalEntity(t *testing.T) {
	const entity = "00000000-0000-4000-8000-0000000000e1"
	signer, verifier := testSigner(t)
	client, got, _ := serve(t, http.StatusCreated, `{}`)
	client.WithSigner(signer)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entity, UserID: "u-1"})
	if err := client.Reverse(ctx, "je-1", "rev-key", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if claims, err := verifier.Verify(got.Header.Get(tenant.HeaderServiceToken)); err != nil || claims.Le != entity || !slices.Equal(claims.Duties, []string{"ledger.journal.reverse"}) {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func TestSignedPostWithoutExplicitSingleLegalEntityFailsBeforeSending(t *testing.T) {
	signer, _ := testSigner(t)
	client, got, _ := serve(t, http.StatusCreated, `{"journalEntryId":"je-1"}`)
	client.WithSigner(signer)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "u-1"})
	if _, err := client.Post(ctx, testEntry()); err == nil || got.Method != "" {
		t.Fatalf("err %v, request sent: %q", err, got.Method)
	}
}

func TestSignedPostForAnotherEntityThanTheUsersScopeIsRefusedAndNothingIsSent(t *testing.T) {
	signer, _ := testSigner(t)
	client, got, _ := serve(t, http.StatusCreated, `{"journalEntryId":"je-1"}`)
	client.WithSigner(signer)
	entry := testEntry()
	entry.LegalEntityID = "00000000-0000-4000-8000-0000000000e1"
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: "00000000-0000-4000-8000-0000000000e2", UserID: "admin-a"})
	if _, err := client.Post(ctx, entry); err == nil || got.Method != "" {
		t.Fatalf("err %v, request sent: %q", err, got.Method)
	}
	if err := client.Reverse(ctx, "je-1", "k", entry.LegalEntityID, time.Now()); err == nil || got.Method != "" {
		t.Fatalf("reverse err %v, request sent: %q", err, got.Method)
	}
}

func TestSignedReverseForAMultiEntityUserSignsTheExplicitEntity(t *testing.T) {
	const entity = "00000000-0000-4000-8000-0000000000e1"
	signer, verifier := testSigner(t)
	client, got, _ := serve(t, http.StatusCreated, `{}`)
	client.WithSigner(signer)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entity + ",00000000-0000-4000-8000-0000000000e2", UserID: "u-1"})
	if err := client.Reverse(ctx, "je-1", "rev-key", entity, time.Now()); err != nil {
		t.Fatal(err)
	}
	if claims, err := verifier.Verify(got.Header.Get(tenant.HeaderServiceToken)); err != nil || claims.Le != entity {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}
