package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

type deferralRig struct {
	handler  http.Handler
	api      *API
	ledger   *ledgerclient.Fake
	ledgerID string
	seq      int
}

func newDeferralRig(t *testing.T, configured bool) *deferralRig {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(app)
	g := &deferralRig{ledger: ledgerclient.NewFake(), ledgerID: ids.New()}
	g.api = New(st, exemptTax(t), 2000)
	if configured {
		g.api.WithLedger(g.ledger, st, map[string]store.DeferralProfile{leID: {
			LegalEntityID: leID, LedgerID: g.ledgerID, ReceivableAccount: "1200", DeferredAccount: "2400", DeferredRevenueAccounts: []string{"4100"},
		}})
	}
	g.api.now = func() time.Time { return time.Date(2027, 3, 12, 15, 4, 0, 0, time.UTC) }
	r := httpx.NewRouter()
	g.api.Register(r)
	g.handler = r
	return g
}

func (g *deferralRig) invoice(t *testing.T, deferred, plain string) string {
	t.Helper()
	g.seq++
	lines := ""
	for _, l := range []struct{ amount, account string }{{deferred, "4100"}, {plain, "4000"}} {
		if l.amount == "" {
			continue
		}
		if lines != "" {
			lines += ","
		}
		lines += fmt.Sprintf(`{"description":"Line","qty":"1","unitPrice":%q,"revenueAccount":%q,"taxCode":"EXEMPT"}`, l.amount, l.account)
	}
	body := fmt.Sprintf(`{"legalEntityId":%q,"customerId":%q,"invoiceAccountId":%q,"currency":"USD","invoiceDate":"2027-03-10","paymentTermDays":30,"lines":[%s]}`, leID, custID, acctID, lines)
	var created struct {
		ID string `json:"id"`
	}
	rec := do(g.handler, http.MethodPost, "/v1/billing/free-text-invoices", fmt.Sprintf("deferral-%d", g.seq), body)
	decode(t, rec, &created)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invoice: %d %s", rec.Code, rec.Body)
	}
	if rec := do(g.handler, http.MethodPost, "/v1/billing/invoices/"+created.ID+":post", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("post invoice: %d %s", rec.Code, rec.Body)
	}
	return created.ID
}

type deferralResult struct {
	Items  []deferralOut       `json:"items"`
	Failed []deferralFailedOut `json:"failed"`
}

func (g *deferralRig) book(t *testing.T, want int) deferralResult {
	t.Helper()
	rec := do(g.handler, http.MethodPost, "/v1/billing/deferral-postings", "", `{"legalEntityId":"`+leID+`"}`)
	if rec.Code != want {
		t.Fatalf("deferral-postings status %d, want %d: %s", rec.Code, want, rec.Body)
	}
	var out deferralResult
	if want == http.StatusOK {
		decode(t, rec, &out)
	}
	return out
}

func (g *deferralRig) balance2400(t *testing.T) ledgerclient.Balance {
	t.Helper()
	b, err := g.ledger.Balance(context.Background(), ledgerclient.BalanceQuery{LegalEntityID: leID, LedgerID: g.ledgerID, Account: "2400"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDeferralPostings_BookTheDeferredNetOfEachInvoiceOnce(t *testing.T) {
	g := newDeferralRig(t, true)
	first := g.invoice(t, "3000.00", "500.00")
	second := g.invoice(t, "1600.00", "")
	g.invoice(t, "", "900.00")

	out := g.book(t, http.StatusOK)
	if len(out.Items) != 2 || len(out.Failed) != 0 {
		t.Fatalf("booked %+v, want the two invoices with deferred lines", out)
	}
	entries := g.ledger.Entries()
	if len(entries) != 2 {
		t.Fatalf("%d ledger entries, want 2", len(entries))
	}
	want := map[string]string{first: "3000.00", second: "1600.00"}
	for _, e := range entries {
		if e.LedgerID != g.ledgerID || e.LegalEntityID != leID || e.SourceType != "invoice_deferral" || e.PostingDate != "2027-03-12" || len(e.Lines) != 2 ||
			e.Lines[0].Account != "1200" || e.Lines[0].Credit || e.Lines[1].Account != "2400" || !e.Lines[1].Credit ||
			e.Lines[0].Amount.StringFixed(2) != want[e.DocumentID] || !e.Lines[0].Amount.Equal(e.Lines[1].Amount) {
			t.Errorf("entry %+v, want Dr 1200 / Cr 2400 for %s", e, want[e.DocumentID])
		}
	}
	if b := g.balance2400(t); b.Credit.StringFixed(2) != "4600.00" || !b.Debit.IsZero() {
		t.Errorf("2400 holds credits %s and debits %s, want 4600.00 credits", b.Credit, b.Debit)
	}

	if again := g.book(t, http.StatusOK); len(again.Items) != 0 || len(g.ledger.Entries()) != 2 || g.api.deferralPostings.Load() != 2 {
		t.Errorf("second call booked %d, the ledger holds %d entries, counter %d; want nothing new", len(again.Items), len(g.ledger.Entries()), g.api.deferralPostings.Load())
	}
}

func TestDeferralPostings_LostLedgerAnswerNeverBooksAnInvoiceTwice(t *testing.T) {
	g := newDeferralRig(t, true)
	g.invoice(t, "1000.00", "")
	g.invoice(t, "250.00", "")
	g.ledger.LoseAnswers(errors.New("timeout"))
	if g.book(t, http.StatusBadGateway); len(g.ledger.Entries()) != 1 {
		t.Fatalf("%d entries after the lost answer, want the one the ledger accepted", len(g.ledger.Entries()))
	}
	g.ledger.LoseAnswers(nil)
	if out := g.book(t, http.StatusOK); len(out.Items) != 2 {
		t.Fatalf("retry booked %d invoices, want both", len(out.Items))
	}
	if b := g.balance2400(t); len(g.ledger.Entries()) != 2 || b.Credit.StringFixed(2) != "1250.00" {
		t.Errorf("%d entries and %s credited on 2400, want 2 and 1250.00", len(g.ledger.Entries()), b.Credit)
	}
}

func TestDeferralPostings_RefusedEntryIsListedAndOfferedAgain(t *testing.T) {
	g := newDeferralRig(t, true)
	id := g.invoice(t, "700.00", "")
	g.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	if out := g.book(t, http.StatusOK); len(out.Items) != 0 || len(out.Failed) != 1 || out.Failed[0].InvoiceID != id || out.Failed[0].LedgerCode != ledgerclient.CodePeriodClosed {
		t.Fatalf("refused call %+v, want the invoice under failed with %s", out, ledgerclient.CodePeriodClosed)
	}
	g.ledger.FailPosts(nil)
	if out := g.book(t, http.StatusOK); len(out.Items) != 1 || out.Items[0].Amount != "700.00" {
		t.Errorf("retry %+v, want the 700.00 deferral booked", out)
	}
}

func TestDeferralPostings_RequestCannotNameAmountAccountOrLedger(t *testing.T) {
	g := newDeferralRig(t, true)
	g.invoice(t, "100.00", "")
	rec := do(g.handler, http.MethodPost, "/v1/billing/deferral-postings", "", `{"legalEntityId":"`+leID+`","amount":"999999.00","deferredAccount":"9999","ledgerId":"`+ids.New()+`"}`)
	if rec.Code != http.StatusBadRequest || len(g.ledger.Entries()) != 0 {
		t.Errorf("status %d with %d entries, want 400 and nothing booked", rec.Code, len(g.ledger.Entries()))
	}
}

func TestDeferralPostings_Guards(t *testing.T) {
	g := newDeferralRig(t, true)
	body := `{"legalEntityId":"` + leID + `"}`
	if rec := doAs(g.handler, http.MethodPost, "/v1/billing/deferral-postings", "", body, "billing.invoice.view,billing.invoice.maintain", nil); rec.Code != http.StatusForbidden {
		t.Errorf("without billing.invoice.post %d, want 403", rec.Code)
	}
	if rec := doAs(g.handler, http.MethodPost, "/v1/billing/deferral-postings", "", body, allDuties, map[string]string{tenant.HeaderLegalEntity: ids.New()}); rec.Code != http.StatusForbidden {
		t.Errorf("caller scoped to another entity %d, want 403", rec.Code)
	}
	if rec := do(g.handler, http.MethodPost, "/v1/billing/deferral-postings", "", `{"legalEntityId":"x"}`); decodeProblemOf(t, rec).Errors["legalEntityId"] == "" || rec.Code != http.StatusBadRequest {
		t.Errorf("invalid entity %d, want 400 with a field error", rec.Code)
	}
	other := ids.New()
	if rec := doAs(g.handler, http.MethodPost, "/v1/billing/deferral-postings", "", `{"legalEntityId":"`+other+`"}`, allDuties, map[string]string{tenant.HeaderLegalEntity: other}); decodeProblemOf(t, rec).Code != CodeLedgerNotMapped || rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("entity without a profile %d, want 422 %s", rec.Code, CodeLedgerNotMapped)
	}
	if p := decodeProblemOf(t, do(newDeferralRig(t, false).handler, http.MethodPost, "/v1/billing/deferral-postings", "", body)); p.Code != CodeLedgerNotConfigured {
		t.Errorf("without a ledger %s, want %s", p.Code, CodeLedgerNotConfigured)
	}
}

func TestParseGLProfiles(t *testing.T) {
	le, ledger := ids.New(), ids.New()
	good := fmt.Sprintf(`[{"legalEntityId":%q,"ledgerId":%q,"receivableAccount":"1200","deferredAccount":"2400","deferredRevenueAccounts":["4100"]}]`, le, ledger)
	got, err := ParseGLProfiles(good)
	if err != nil || got[le].LedgerID != ledger || got[le].DeferredRevenueAccounts[0] != "4100" {
		t.Fatalf("good profile %+v, err %v", got, err)
	}
	for name, raw := range map[string]string{
		"empty":            "",
		"not an array":     `{}`,
		"bad entity":       `[{"legalEntityId":"x","ledgerId":"` + ledger + `","receivableAccount":"1200","deferredAccount":"2400","deferredRevenueAccounts":["4100"]}]`,
		"no revenue":       fmt.Sprintf(`[{"legalEntityId":%q,"ledgerId":%q,"receivableAccount":"1200","deferredAccount":"2400","deferredRevenueAccounts":[]}]`, le, ledger),
		"bad account text": fmt.Sprintf(`[{"legalEntityId":%q,"ledgerId":%q,"receivableAccount":"1200","deferredAccount":"2400","deferredRevenueAccounts":["4 100"]}]`, le, ledger),
	} {
		if _, err := ParseGLProfiles(raw); err == nil {
			t.Errorf("%s accepted, want an error", name)
		}
	}
}
