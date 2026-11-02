package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

func subscriptionHandler(t *testing.T, tax TaxEngine) http.Handler {
	t.Helper()
	h, _ := subscriptionStack(t, tax)
	return h
}

func subscriptionStack(t *testing.T, tax TaxEngine) (http.Handler, *store.Postgres) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(app)
	a := New(st, tax, 2000).WithSubscriptions(st)
	r := httpx.NewRouter()
	a.Register(r)
	return r, st
}

func TestSubscription_ClientKeysCannotUseTheReservedPrefix(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	sub := newSubscription(t, h, subscriptionBody("USD", "100.00", 1, "2027-01-01"))
	reserved := "subscription-" + sub.ID + "-2027-01-01"
	for _, path := range []string{"/v1/billing/free-text-invoices", "/v1/billing/proposals", "/v1/billing/credit-notes"} {
		rec := do(h, http.MethodPost, path, reserved, `{}`)
		if p := decodeProblemOf(t, rec); rec.Code != http.StatusBadRequest || p.Code != "COM-0001" {
			t.Errorf("POST %s with a reserved key: %d %s, want 400 COM-0001", path, rec.Code, p.Code)
		}
	}
}

func TestSubscription_PreexistingInvoiceUnderTheOldDerivedKeyCannotBlockBilling(t *testing.T) {
	h, st := subscriptionStack(t, exemptTax(t))
	sub := newSubscription(t, h, subscriptionBody("USD", "100.00", 1, "2027-01-01"))
	otherLE := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: otherLE, UserID: "squatter", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	squat := store.Invoice{ID: ids.New(), LegalEntityID: otherLE, CustomerID: custID, InvoiceAccountID: acctID, Type: domain.TypeFreeText,
		Currency: "USD", InvoiceDate: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), DueDate: time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC),
		PaymentTermDays: 30, IdempotencyKey: "subscription-" + sub.ID + "-2027-01-01", RequestHash: "x"}
	if _, err := st.CreateProposal(ctx, squat); err != nil {
		t.Fatalf("squat: %v", err)
	}
	first := billPeriod(h, sub.ID, "2027-01-01")
	if first.Code != http.StatusCreated {
		t.Fatalf("bill after a squat: %d %s, want 201", first.Code, first.Body)
	}
	var a, b invoiceDTO
	decode(t, first, &a)
	again := billPeriod(h, sub.ID, "2027-01-01")
	decode(t, again, &b)
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" || a.ID != b.ID || a.ID == squat.ID {
		t.Errorf("replay %d id %s, want the subscription invoice %s and not the squatting invoice", again.Code, b.ID, a.ID)
	}
}

func TestSubscription_CancelBeforeAFutureDatedPlanChangeIgnoresThatChange(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	sub := newSubscription(t, h, subscriptionBody("USD", "310.00", 1, "2027-03-01"))
	if rec := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+":change-plan", "", `{"planCode":"PREMIUM","price":"600.00","effectiveFrom":"2027-03-20"}`); rec.Code != http.StatusOK {
		t.Fatalf("change plan: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+":cancel", "", `{"lastServiceDay":"2027-03-10"}`); rec.Code != http.StatusOK {
		t.Fatalf("cancel before the future change: %d %s, want 200", rec.Code, rec.Body)
	}
	var read subscriptionDTO
	decode(t, do(h, http.MethodGet, "/v1/billing/subscriptions/"+sub.ID, "", ""), &read)
	if len(read.Terms) != 1 || read.Terms[0].PlanCode != "STANDARD" {
		t.Errorf("terms %+v, want only the plan that was in force until the last service day", read.Terms)
	}
	var inv invoiceDTO
	rec := billPeriod(h, sub.ID, "2027-03-01")
	decode(t, rec, &inv)
	if rec.Code != http.StatusCreated || len(inv.Lines) != 1 || inv.Lines[0].Net != "100.00" {
		t.Errorf("bill %d %+v, want 10 of 31 days of 310.00 on the first plan only", rec.Code, inv)
	}
}

func exemptTax(t *testing.T) TaxEngine {
	t.Helper()
	tax, err := ParseTaxRates("EXEMPT=0")
	if err != nil {
		t.Fatal(err)
	}
	return tax
}

func subscriptionBody(currency, price string, months int, start string) string {
	return fmt.Sprintf(`{"legalEntityId":%q,"customerId":%q,"invoiceAccountId":%q,"currency":%q,"planCode":"STANDARD","price":%q,`+
		`"periodMonths":%d,"startDate":%q,"revenueAccount":"4100","taxCode":"EXEMPT","paymentTermDays":30}`, leID, custID, acctID, currency, price, months, start)
}

var subscriptionSeq int

func newSubscription(t *testing.T, h http.Handler, body string) subscriptionDTO {
	t.Helper()
	subscriptionSeq++
	rec := do(h, http.MethodPost, "/v1/billing/subscriptions", fmt.Sprintf("sub-%d", subscriptionSeq), body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create subscription: %d %s", rec.Code, rec.Body)
	}
	var sub subscriptionDTO
	decode(t, rec, &sub)
	return sub
}

func billPeriod(h http.Handler, id, periodFrom string) *httptest.ResponseRecorder {
	return do(h, http.MethodPost, "/v1/billing/subscriptions/"+id+":bill", "", fmt.Sprintf(`{"periodFrom":%q}`, periodFrom))
}

func decodeProblemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	decode(t, rec, &p)
	return p
}

func monthsBetween(from, toExclusive string) int {
	f, _ := time.Parse(dateLayout, from)
	e, _ := time.Parse(dateLayout, toExclusive)
	return (e.Year()-f.Year())*12 + int(e.Month()-f.Month())
}

func TestSubscription_SubscriptionEdges25_ThroughInvoices(t *testing.T) {
	raw, err := os.ReadFile("../domain/testdata/subscription-edges-25.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			ID          string `json:"id"`
			Why         string `json:"why"`
			Amount      string `json:"amount"`
			Currency    string `json:"currency"`
			PeriodFrom  string `json:"periodFrom"`
			PeriodTo    string `json:"periodTo"`
			ServiceFrom string `json:"serviceFrom"`
			ServiceTo   string `json:"serviceTo"`
			Expected    string `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) != 25 {
		t.Fatalf("fixture has %d cases, want 25", len(fx.Cases))
	}
	h := subscriptionHandler(t, exemptTax(t))
	for _, c := range fx.Cases {
		t.Run(c.ID+" "+c.Why, func(t *testing.T) {
			periodEnd, _ := time.Parse(dateLayout, c.PeriodTo)
			months := monthsBetween(c.PeriodFrom, periodEnd.AddDate(0, 0, 1).Format(dateLayout))
			sub := newSubscription(t, h, subscriptionBody(c.Currency, c.Amount, months, c.ServiceFrom))
			cancelled := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+":cancel", "", fmt.Sprintf(`{"lastServiceDay":%q}`, c.ServiceTo))
			if cancelled.Code != http.StatusOK {
				t.Fatalf("cancel: %d %s", cancelled.Code, cancelled.Body)
			}
			rec := billPeriod(h, sub.ID, c.PeriodFrom)
			want := decimal.RequireFromString(c.Expected)
			if want.IsZero() {
				var p httpx.Problem
				decode(t, rec, &p)
				if rec.Code != http.StatusUnprocessableEntity || p.Code != domain.CodeNothingToBill {
					t.Fatalf("zero period: %d %s, want 422 %s and no invoice", rec.Code, p.Code, domain.CodeNothingToBill)
				}
				return
			}
			if rec.Code != http.StatusCreated {
				t.Fatalf("bill: %d %s", rec.Code, rec.Body)
			}
			var inv invoiceDTO
			decode(t, rec, &inv)
			gross := decimal.RequireFromString(inv.Totals.Gross)
			if inv.Type != domain.TypeSubscription || inv.Status != domain.StatusProposal || inv.Currency != c.Currency || len(inv.Lines) != 1 || !gross.Equal(want) {
				t.Errorf("invoice %s %s %s lines %d gross %s, want a SUBSCRIPTION proposal in %s of %s", inv.Type, inv.Status, inv.Currency, len(inv.Lines), inv.Totals.Gross, c.Currency, c.Expected)
			}
		})
	}
}

func TestSubscription_PlanChangeProratesBothTerms(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	sub := newSubscription(t, h, subscriptionBody("USD", "300.00", 1, "2027-01-01"))
	rec := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+":change-plan", "", `{"planCode":"PREMIUM","price":"600.00","effectiveFrom":"2027-03-17"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("change plan: %d %s", rec.Code, rec.Body)
	}
	rec = billPeriod(h, sub.ID, "2027-03-01")
	var inv invoiceDTO
	decode(t, rec, &inv)
	if rec.Code != http.StatusCreated || len(inv.Lines) != 2 || inv.Lines[0].Net != "154.84" || inv.Lines[1].Net != "290.32" || inv.Totals.Gross != "445.16" {
		t.Fatalf("invoice %d %+v, want 16 days of 300.00 (154.84) and 15 days of 600.00 (290.32) = 445.16", rec.Code, inv)
	}
	if inv.DueDate != "2027-03-31" {
		t.Errorf("due %s, want the invoice date plus 30 days", inv.DueDate)
	}
}

func TestSubscription_BillingIsIdempotentPerPeriodAndRefusesOverlap(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	sub := newSubscription(t, h, subscriptionBody("EUR", "900.00", 3, "2027-01-01"))
	first := billPeriod(h, sub.ID, "2027-01-01")
	var a invoiceDTO
	decode(t, first, &a)
	if first.Code != http.StatusCreated || a.Totals.Gross != "900.00" {
		t.Fatalf("first bill %d %s", first.Code, first.Body)
	}
	again := billPeriod(h, sub.ID, "2027-01-01")
	var b invoiceDTO
	decode(t, again, &b)
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" || b.ID != a.ID {
		t.Errorf("repeat bill %d replayed=%q id %s, want the first invoice %s", again.Code, again.Header().Get("Idempotent-Replayed"), b.ID, a.ID)
	}
	other := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+":bill", "", `{"periodFrom":"2027-01-01","invoiceDate":"2027-01-15"}`)
	if p := decodeProblemOf(t, other); other.Code != http.StatusUnprocessableEntity || p.Code != "idempotency_key_reuse" {
		t.Errorf("same period, other invoice date: %d %s, want 422 idempotency_key_reuse", other.Code, p.Code)
	}
	sameDate := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+":bill", "", `{"periodFrom":"2027-01-01","invoiceDate":"2027-01-01"}`)
	if sameDate.Code != http.StatusOK || sameDate.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("explicit invoice date equal to the default: %d, want a replay", sameDate.Code)
	}
	overlap := billPeriod(h, sub.ID, "2027-02-01")
	if p := decodeProblemOf(t, overlap); overlap.Code != http.StatusConflict || p.Code != domain.CodePeriodInvoiced {
		t.Errorf("overlapping period: %d %s, want 409 %s", overlap.Code, p.Code, domain.CodePeriodInvoiced)
	}
	skipped := billPeriod(h, sub.ID, "2027-07-01")
	if p := decodeProblemOf(t, skipped); skipped.Code != http.StatusBadRequest || p.Code != "COM-0001" {
		t.Errorf("skipping a period: %d %s, want 400 COM-0001", skipped.Code, p.Code)
	}
	var read subscriptionDTO
	decode(t, do(h, http.MethodGet, "/v1/billing/subscriptions/"+sub.ID, "", ""), &read)
	if read.BilledThrough != "2027-03-31" {
		t.Errorf("billed through %q after refused periods, want 2027-03-31", read.BilledThrough)
	}
	if next := billPeriod(h, sub.ID, "2027-04-01"); next.Code != http.StatusCreated {
		t.Errorf("following period: %d %s", next.Code, next.Body)
	}
}

func TestSubscription_PeriodsMayBeBilledBackwardsButNotAcrossAGap(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	sub := newSubscription(t, h, subscriptionBody("USD", "100.00", 1, "2027-01-01"))
	for _, from := range []string{"2027-03-01", "2027-02-01", "2027-01-01"} {
		if rec := billPeriod(h, sub.ID, from); rec.Code != http.StatusCreated {
			t.Fatalf("bill %s: %d %s, want 201 at either end of the billed range", from, rec.Code, rec.Body)
		}
	}
	var read subscriptionDTO
	decode(t, do(h, http.MethodGet, "/v1/billing/subscriptions/"+sub.ID, "", ""), &read)
	if read.BilledThrough != "2027-03-31" {
		t.Errorf("billed through %q, want 2027-03-31", read.BilledThrough)
	}

	gapped := newSubscription(t, h, subscriptionBody("USD", "100.00", 1, "2027-01-01"))
	if rec := billPeriod(h, gapped.ID, "2027-01-01"); rec.Code != http.StatusCreated {
		t.Fatalf("bill January: %d %s", rec.Code, rec.Body)
	}
	if rec := billPeriod(h, gapped.ID, "2027-03-01"); rec.Code != http.StatusBadRequest {
		t.Errorf("bill March after January: %d, want 400 for the February gap", rec.Code)
	}
	backwards := newSubscription(t, h, subscriptionBody("USD", "100.00", 1, "2027-01-01"))
	if rec := billPeriod(h, backwards.ID, "2027-03-01"); rec.Code != http.StatusCreated {
		t.Fatalf("bill March: %d %s", rec.Code, rec.Body)
	}
	rec := billPeriod(h, backwards.ID, "2027-01-01")
	if p := decodeProblemOf(t, rec); rec.Code != http.StatusBadRequest || p.Code != "COM-0001" {
		t.Errorf("bill January after March: %d %s, want 400 COM-0001 for the February gap", rec.Code, p.Code)
	}
}

func TestSubscription_OtherLegalEntityScopeCannotReadOrChange(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	sub := newSubscription(t, h, subscriptionBody("USD", "100.00", 1, "2027-01-01"))
	other := map[string]string{tenant.HeaderLegalEntity: ids.New()}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "", ""},
		{http.MethodPost, ":bill", `{"periodFrom":"2027-01-01"}`},
		{http.MethodPost, ":change-plan", `{"planCode":"X","price":"1.00","effectiveFrom":"2027-02-01"}`},
		{http.MethodPost, ":cancel", `{"lastServiceDay":"2027-01-31"}`},
	} {
		rec := doAs(h, c.method, "/v1/billing/subscriptions/"+sub.ID+c.path, "", c.body, allDuties, other)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("%s%s from another legal entity: %d, want 403 or 404", c.method, c.path, rec.Code)
		}
	}
	var read subscriptionDTO
	decode(t, do(h, http.MethodGet, "/v1/billing/subscriptions/"+sub.ID, "", ""), &read)
	if read.Status != domain.SubscriptionActive || read.BilledThrough != "" || read.Version != 1 {
		t.Errorf("subscription %+v after refused calls, want it untouched", read)
	}
}

func TestSubscription_InvoicedPeriodsAreProtectedFromChangeAndCancel(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	sub := newSubscription(t, h, subscriptionBody("USD", "310.00", 1, "2027-03-01"))
	if rec := billPeriod(h, sub.ID, "2027-03-01"); rec.Code != http.StatusCreated {
		t.Fatalf("bill: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct{ path, body string }{
		{":change-plan", `{"planCode":"PREMIUM","price":"400.00","effectiveFrom":"2027-03-20"}`},
		{":cancel", `{"lastServiceDay":"2027-03-10"}`},
	} {
		rec := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+c.path, "", c.body)
		if p := decodeProblemOf(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != domain.CodeEffectiveInvoiced {
			t.Errorf("%s inside an invoiced period: %d %s, want 422 %s", c.path, rec.Code, p.Code, domain.CodeEffectiveInvoiced)
		}
	}
	if rec := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+":cancel", "", `{"lastServiceDay":"2027-03-31"}`); rec.Code != http.StatusOK {
		t.Fatalf("cancel on the invoiced period end: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+":cancel", "", `{"lastServiceDay":"2027-04-30"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("second cancel: %d, want 422", rec.Code)
	}
	rec := do(h, http.MethodPost, "/v1/billing/subscriptions/"+sub.ID+":change-plan", "", `{"planCode":"X","price":"1.00","effectiveFrom":"2027-05-01"}`)
	if p := decodeProblemOf(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != domain.CodeSubscriptionClosed {
		t.Errorf("change after cancel: %d %s, want 422 %s", rec.Code, p.Code, domain.CodeSubscriptionClosed)
	}
	got := do(h, http.MethodGet, "/v1/billing/subscriptions/"+sub.ID, "", "")
	var read subscriptionDTO
	decode(t, got, &read)
	if read.Status != domain.SubscriptionCancelled || read.CancelEffective != "2027-03-31" || read.BilledThrough != "2027-03-31" || read.Version != 2 {
		t.Errorf("subscription %+v, want cancelled on 2027-03-31, billed through it, version 2", read)
	}
}

func TestSubscription_CreateReplayValidationAndGuards(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	body := subscriptionBody("USD", "100.00", 1, "2027-03-01")
	first := do(h, http.MethodPost, "/v1/billing/subscriptions", "sub-replay", body)
	again := do(h, http.MethodPost, "/v1/billing/subscriptions", "sub-replay", body)
	var a, b subscriptionDTO
	decode(t, first, &a)
	decode(t, again, &b)
	if first.Code != http.StatusCreated || again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" || a.ID != b.ID {
		t.Errorf("create %d then %d, ids %s %s, want 201 then a 200 replay of the same subscription", first.Code, again.Code, a.ID, b.ID)
	}
	if reuse := do(h, http.MethodPost, "/v1/billing/subscriptions", "sub-replay", subscriptionBody("USD", "101.00", 1, "2027-03-01")); reuse.Code != http.StatusUnprocessableEntity {
		t.Errorf("key reuse with another body: %d, want 422", reuse.Code)
	}
	if missing := do(h, http.MethodPost, "/v1/billing/subscriptions", "", body); missing.Code != http.StatusBadRequest {
		t.Errorf("no Idempotency-Key: %d, want 400", missing.Code)
	}
	bad := do(h, http.MethodPost, "/v1/billing/subscriptions", "sub-bad", `{"legalEntityId":"x","currency":"usd","periodMonths":2,"price":"0","startDate":"March","taxCode":"a b","paymentTermDays":999}`)
	var p httpx.Problem
	decode(t, bad, &p)
	for _, field := range []string{"legalEntityId", "customerId", "currency", "periodMonths", "price", "startDate", "revenueAccount", "taxCode", "paymentTermDays", "planCode"} {
		if bad.Code != http.StatusBadRequest || p.Errors[field] == "" {
			t.Errorf("invalid subscription %d: no error for %s in %+v", bad.Code, field, p.Errors)
		}
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/subscriptions", "sub-scope", body, allDuties, map[string]string{tenant.HeaderLegalEntity: ids.New()}); rec.Code != http.StatusForbidden {
		t.Errorf("other legal-entity scope: %d, want 403", rec.Code)
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/subscriptions", "sub-view", body, "billing.invoice.view", nil); rec.Code != http.StatusForbidden {
		t.Errorf("view duty creating a subscription: %d, want 403", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/billing/subscriptions/"+ids.New(), "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown subscription: %d, want 404", rec.Code)
	}
	if rec := billPeriod(h, a.ID, "2027-03-15"); rec.Code != http.StatusBadRequest {
		t.Errorf("period not opening on the first: %d, want 400", rec.Code)
	}
	stale := doAs(h, http.MethodPost, "/v1/billing/subscriptions/"+a.ID+":change-plan", "", `{"planCode":"P","price":"5.00","effectiveFrom":"2027-04-01"}`, allDuties, map[string]string{"If-Match": `"9"`})
	if stale.Code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: %d, want 412", stale.Code)
	}
}

func TestSubscription_TaxUnavailableStoresNothingAndUnconfiguredIs503(t *testing.T) {
	h := subscriptionHandler(t, UnavailableTax{})
	sub := newSubscription(t, h, subscriptionBody("USD", "100.00", 1, "2027-03-01"))
	rec := billPeriod(h, sub.ID, "2027-03-01")
	if p := decodeProblemOf(t, rec); rec.Code != http.StatusServiceUnavailable || p.Code != CodeTaxUnavailable {
		t.Errorf("bill with the tax engine down: %d %s, want 503 %s", rec.Code, p.Code, CodeTaxUnavailable)
	}
	read := do(h, http.MethodGet, "/v1/billing/subscriptions/"+sub.ID, "", "")
	var got subscriptionDTO
	decode(t, read, &got)
	if got.BilledThrough != "" {
		t.Errorf("billed through %q after a refused bill, want none", got.BilledThrough)
	}

	unconfigured := setup(t, newFakeStore(), &fakeTax{perLine: "0"})
	if rec := do(unconfigured, http.MethodPost, "/v1/billing/subscriptions", "k", subscriptionBody("USD", "1.00", 1, "2027-03-01")); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without subscription storage: %d, want 503", rec.Code)
	}
}
