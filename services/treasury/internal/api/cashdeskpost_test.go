package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const (
	testDeskAcct   = "0190b7a0-0000-7000-8000-000000000020"
	testOtherAcct  = "0190b7a0-0000-7000-8000-000000000021"
	deskCashLedger = "1000"
)

func deskProfile() map[string]GLProfile {
	return map[string]GLProfile{testLE: {LegalEntityID: testLE, LedgerID: testLedger, BankAccounts: map[string]string{testBankAcct: "1100"},
		FeeAccount: "6800", InterestAccount: "7100", SuspenseAccount: "1900", CashAccounts: map[string]string{testDeskAcct: deskCashLedger}, CashDifferenceAccount: "6990"}}
}

type deskPost struct {
	h      http.Handler
	f      *fakeDesks
	ledger *ledgerclient.Fake
	st     *fakeStore
	path   string
}

func newDeskPost(t *testing.T, profiles map[string]GLProfile, dual bool) *deskPost {
	t.Helper()
	f := &fakeDesks{desk: store.CashDesk{ID: ids.New(), LegalEntityID: testLE, Code: "FRONT-1", Currency: "EUR", Status: "active", MainAccountID: testDeskAcct, DualControl: dual}}
	ledger, st := ledgerclient.NewFake(), newFakeStore()
	st.accounts[testBankAcct] = store.BankAccount{ID: testBankAcct, LegalEntityID: testLE, Currency: "EUR", Status: "active"}
	r := httpx.NewRouter()
	New(st).WithCashDesks(f).WithLedger(ledger, profiles).Register(r)
	return &deskPost{h: r, f: f, ledger: ledger, st: st, path: "/v1/treasury/cash-desks/" + f.desk.ID}
}

func (d *deskPost) do(method, path, body string, headers map[string]string) (int, string, string) {
	rec := call(d.h, method, d.path+path, rawBody(body), "application/json", headers)
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return rec.Code, p.Code, rec.Body.String()
}

func key() map[string]string { return map[string]string{"Idempotency-Key": ids.New()} }

func TestCashDeskEntry_PostsToTheLedgerWithTheDeskCashAccount(t *testing.T) {
	d := newDeskPost(t, deskProfile(), false)
	if code, _, body := d.do(http.MethodPost, "/entries", `{"kind":"receipt","amount":"40.00","description":"sale","counterAccount":"8000"}`, key()); code != http.StatusCreated || !strings.Contains(body, `"postingStatus":"posted"`) {
		t.Fatalf("receipt = %d %s; want 201 posted", code, body)
	}
	if code, _, body := d.do(http.MethodPost, "/entries", `{"kind":"payment","amount":"12.50","description":"taxi","counterAccount":"6400"}`, key()); code != http.StatusCreated {
		t.Fatalf("payment = %d %s", code, body)
	}
	got := d.ledger.Entries()
	if len(got) != 2 {
		t.Fatalf("ledger entries = %d; want 2", len(got))
	}
	recv, pay := got[0].Lines, got[1].Lines
	if recv[0].Account != deskCashLedger || recv[0].Credit || recv[1].Account != "8000" || !recv[1].Credit {
		t.Errorf("receipt lines = %+v; want debit cash 1000, credit 8000", recv)
	}
	if pay[0].Account != deskCashLedger || !pay[0].Credit || pay[1].Account != "6400" || pay[1].Credit || !pay[0].Amount.Equal(decimal.RequireFromString("12.50")) {
		t.Errorf("payment lines = %+v; want credit cash 1000, debit 6400", pay)
	}
	if got[0].SourceType != "cash_desk_entry" || !strings.HasPrefix(got[0].IdempotencyKey, "treasury-desk-") || got[0].DocumentID != d.f.desk.ID {
		t.Errorf("entry = %+v", got[0])
	}
}

func TestCashDeskEntry_BankTransferUsesTheBankAccountsLedgerAccount(t *testing.T) {
	d := newDeskPost(t, deskProfile(), false)
	if code, _, body := d.do(http.MethodPost, "/entries", `{"kind":"payment","amount":"500","description":"deposit","bankAccountId":"`+testBankAcct+`"}`, key()); code != http.StatusCreated {
		t.Fatalf("deposit = %d %s", code, body)
	}
	lines := d.ledger.Entries()[0].Lines
	if lines[0].Account != deskCashLedger || !lines[0].Credit || lines[1].Account != "1100" || lines[1].Credit {
		t.Fatalf("lines = %+v; want credit cash, debit bank 1100", lines)
	}
	for name, body := range map[string]string{
		"both":           `{"kind":"payment","amount":"5","description":"x","counterAccount":"6400","bankAccountId":"` + testBankAcct + `"}`,
		"neither":        `{"kind":"payment","amount":"5","description":"x"}`,
		"bad account":    `{"kind":"payment","amount":"5","description":"x","counterAccount":"has space"}`,
		"not a uuid":     `{"kind":"payment","amount":"5","description":"x","bankAccountId":"x"}`,
		"unknown (404)":  `{"kind":"payment","amount":"5","description":"x","bankAccountId":"` + ids.New() + `"}`,
		"other currency": "",
	} {
		if body == "" {
			continue
		}
		code, _, _ := d.do(http.MethodPost, "/entries", body, key())
		want := http.StatusBadRequest
		if name == "unknown (404)" {
			want = http.StatusNotFound
		}
		if code != want {
			t.Errorf("%s: %d, want %d", name, code, want)
		}
	}
	d.st.accounts[testBankAcct] = store.BankAccount{ID: testBankAcct, LegalEntityID: testLE, Currency: "USD", Status: "active"}
	if code, _, _ := d.do(http.MethodPost, "/entries", `{"kind":"payment","amount":"5","description":"x","bankAccountId":"`+testBankAcct+`"}`, key()); code != http.StatusBadRequest {
		t.Errorf("bank account in another currency: %d, want 400", code)
	}
}

func TestCashDeskEntry_ALedgerFailureKeepsTheLineWaitingAndPostDayRetriesIt(t *testing.T) {
	d := newDeskPost(t, deskProfile(), false)
	d.ledger.LoseAnswers(errors.New("connection reset"))
	code, problem, body := d.do(http.MethodPost, "/entries", `{"kind":"receipt","amount":"40","description":"sale","counterAccount":"8000"}`, key())
	if code != http.StatusServiceUnavailable || problem != CodeLedgerDown || !strings.Contains(body, "will not post twice") {
		t.Fatalf("lost answer = %d %s %s; want 503 %s", code, problem, body, CodeLedgerDown)
	}
	if len(d.f.journal) != 1 || d.f.journal[0].PostingStatus != "pending" {
		t.Fatalf("journal = %+v; want the line saved as pending", d.f.journal)
	}
	d.ledger.LoseAnswers(nil)
	if code, _, body := d.do(http.MethodPost, "/days/2026-11-02:post", "", nil); code != http.StatusOK {
		t.Fatalf("post day = %d %s", code, body)
	}
	if d.f.journal[0].PostingStatus != "posted" || len(d.ledger.Entries()) != 1 {
		t.Fatalf("after the retry: status %q, %d ledger entries; want posted and exactly one entry", d.f.journal[0].PostingStatus, len(d.ledger.Entries()))
	}
	d.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	d.f.journal[0].PostingStatus = "pending"
	code, problem, body = d.do(http.MethodPost, "/days/2026-11-02:post", "", nil)
	if code != http.StatusUnprocessableEntity || problem != CodeLedgerRefused || !strings.Contains(body, ledgerclient.CodePeriodClosed) {
		t.Fatalf("refusal = %d %s %s; want 422 %s naming the ledger code", code, problem, body, CodeLedgerRefused)
	}
}

func TestCashDeskEntry_ADeskWithoutALedgerAccountIsRefusedAndOneWithoutCashAccountsIsNotPosted(t *testing.T) {
	profiles := deskProfile()
	p := profiles[testLE]
	p.CashAccounts = map[string]string{testOtherAcct: "1010"}
	profiles[testLE] = p
	d := newDeskPost(t, profiles, false)
	if code, problem, _ := d.do(http.MethodPost, "/entries", `{"kind":"receipt","amount":"1","description":"x","counterAccount":"8000"}`, key()); code != http.StatusUnprocessableEntity || problem != CodeLedgerNoProfile {
		t.Fatalf("unmapped desk = %d %s; want 422 %s", code, problem, CodeLedgerNoProfile)
	}
	if len(d.f.journal) != 0 {
		t.Fatalf("an unmapped desk saved %d lines; want none", len(d.f.journal))
	}
	plain := newDeskPost(t, map[string]GLProfile{}, false)
	if code, _, body := plain.do(http.MethodPost, "/entries", `{"kind":"receipt","amount":"1","description":"x"}`, key()); code != http.StatusCreated || !strings.Contains(body, `"postingStatus":"not_posted"`) {
		t.Fatalf("no ledger profile = %d %s; want 201 not_posted", code, body)
	}
}

func TestCashDeskClose_DenominationsMustAddUpAndADifferencePostsToTheDifferenceAccount(t *testing.T) {
	d := newDeskPost(t, deskProfile(), false)
	if code, problem, _ := d.do(http.MethodPost, "/days/2026-11-02:close", `{"countedBalance":"50","denominations":[{"value":"20","count":2}]}`, nil); code != http.StatusUnprocessableEntity || problem != "TRS-0556" {
		t.Fatalf("mismatch = %d %s; want 422 TRS-0556", code, problem)
	}
	for name, body := range map[string]string{
		"dup value":  `{"denominations":[{"value":"5","count":1},{"value":"5.00","count":1}]}`,
		"zero value": `{"denominations":[{"value":"0","count":1}]}`,
		"cents":      `{"denominations":[{"value":"0.001","count":1}]}`,
		"negative":   `{"denominations":[{"value":"5","count":-1}]}`,
	} {
		if code, _, _ := d.do(http.MethodPost, "/days/2026-11-02:close", body, nil); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
			t.Errorf("%s: %d, want a refusal", name, code)
		}
	}
	d.f.day = store.CashDeskDay{}
	code, _, body := d.do(http.MethodPost, "/days/2026-11-02:close", `{"reason":"short","denominations":[{"value":"20","count":2},{"value":"0.50","count":1}]}`, nil)
	if code != http.StatusOK || !d.f.closed.Equal(decimal.RequireFromString("40.5")) || !d.f.closeIn.PostDifference || len(d.f.closeIn.Denominations) != 2 {
		t.Fatalf("close = %d %s (counted %s); want the denomination total 40.50 and the difference posted", code, body, d.f.closed)
	}
	got := d.ledger.Entries()
	if len(got) != 1 || got[0].SourceType != "cash_desk_difference" || got[0].IdempotencyKey != "treasury-deskdiff-"+d.f.desk.ID+"-2026-11-02" {
		t.Fatalf("ledger entries = %+v; want the one difference entry", got)
	}
	if l := got[0].Lines; l[0].Account != deskCashLedger || !l[0].Credit || l[1].Account != "6990" || l[1].Credit || !l[0].Amount.Equal(decimal.RequireFromString("0.50")) {
		t.Fatalf("difference lines = %+v; a shortage credits cash and debits the difference account", l)
	}
	if !strings.Contains(body, `"differencePosting":"posted"`) {
		t.Fatalf("close body = %s; want the difference posted", body)
	}
}

func TestCashDesk_SecondSignatureEditAndDeactivateRoutes(t *testing.T) {
	d := newDeskPost(t, map[string]GLProfile{}, true)
	if code, _, body := d.do(http.MethodPost, "/days/2026-11-02:close", `{"countedBalance":"10"}`, nil); code != http.StatusOK || !strings.Contains(body, `"status":"closing"`) {
		t.Fatalf("close = %d %s; want closing", code, body)
	}
	if code, _, body := d.do(http.MethodPost, "/days/2026-11-02:approve", "", nil); code != http.StatusOK || !strings.Contains(body, `"status":"closed"`) {
		t.Fatalf("approve = %d %s", code, body)
	}
	if code, _, _ := d.do(http.MethodPost, "/days/2026-11-02:reject", "", nil); code != http.StatusOK {
		t.Fatalf("reject = %d", code)
	}
	if code, problem, _ := d.do(http.MethodPatch, "", `{"name":"Back desk"}`, nil); code != http.StatusPreconditionRequired {
		t.Fatalf("patch without If-Match = %d %s; want 428", code, problem)
	}
	ifMatch := map[string]string{"If-Match": `"1"`}
	if code, _, body := d.do(http.MethodPatch, "", `{"name":" Back desk ","maxBalance":"2500","dualControl":false}`, ifMatch); code != http.StatusOK || len(d.f.updates) != 1 || *d.f.updates[0].Name != "Back desk" || *d.f.updates[0].MaxBalance != "2500" {
		t.Fatalf("patch = %d %s %+v", code, body, d.f.updates)
	}
	for name, body := range map[string]string{"empty": `{}`, "bad max": `{"maxBalance":"0"}`, "bad name": `{"name":" "}`, "bad account": `{"mainAccountId":"x"}`, "unknown": `{"owner":"x"}`} {
		if code, _, _ := d.do(http.MethodPatch, "", body, ifMatch); code != http.StatusBadRequest {
			t.Errorf("patch %s: %d, want 400", name, code)
		}
	}
	if code, _, body := d.do(http.MethodPost, ":deactivate", "", ifMatch); code != http.StatusOK || d.f.status != "inactive" || !strings.Contains(body, "inactive") {
		t.Fatalf("deactivate = %d %s", code, body)
	}
	if code, _, _ := d.do(http.MethodPost, ":activate", "", nil); code != http.StatusPreconditionRequired {
		t.Fatalf("activate without If-Match = %d; want 428", code)
	}
}

func TestParseGLProfiles_CashAccountsNeedADifferenceAccount(t *testing.T) {
	base := `[{"legalEntityId":"` + testLE + `","ledgerId":"` + testLedger + `","bankAccounts":{"` + testBankAcct + `":"1100"},"feeAccount":"6800","interestAccount":"7100","suspenseAccount":"1900"`
	got, err := ParseGLProfiles(base + `,"cashAccounts":{"` + strings.ToUpper(testDeskAcct) + `":"1000"},"cashDifferenceAccount":"6990"}]`)
	if err != nil || got[testLE].CashAccounts[testDeskAcct] != "1000" || got[testLE].CashDifferenceAccount != "6990" {
		t.Fatalf("profile = %+v, %v; want the desk cash account keyed by its canonical id", got, err)
	}
	if _, err := ParseGLProfiles(base + `}]`); err != nil {
		t.Fatalf("a profile without cash accounts must stay valid: %v", err)
	}
	for name, raw := range map[string]string{
		"no difference account": base + `,"cashAccounts":{"` + testDeskAcct + `":"1000"}}]`,
		"bad desk account":      base + `,"cashAccounts":{"x":"1000"},"cashDifferenceAccount":"6990"}]`,
		"bad ledger account":    base + `,"cashAccounts":{"` + testDeskAcct + `":"has space"},"cashDifferenceAccount":"6990"}]`,
	} {
		if _, err := ParseGLProfiles(raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
