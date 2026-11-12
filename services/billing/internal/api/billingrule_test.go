package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

func (f *fakeStore) PutBillingRule(_ context.Context, r store.BillingRule, ifMatch int) (store.BillingRule, error) {
	if f.rules == nil {
		f.rules = map[string]store.BillingRule{}
	}
	k := r.LegalEntityID + "/" + r.CustomerID
	prev, found := f.rules[k]
	if (found && prev.Version != ifMatch) || (!found && ifMatch != 0) {
		return store.BillingRule{}, store.ErrRuleVersion
	}
	r.Version = prev.Version + 1
	f.rules[k] = r
	return r, nil
}

func (f *fakeStore) GetBillingRule(_ context.Context, le, customer string) (store.BillingRule, bool, error) {
	r, ok := f.rules[le+"/"+customer]
	if !ok {
		return store.BillingRule{LegalEntityID: le, CustomerID: customer, Rule: store.RuleStandard}, false, nil
	}
	return r, true, nil
}

// #35 (35-5): a customer that self-bills under an agreement gets no invoice proposal of ours for its shipments while the agreement
// is valid; before it starts, or once it is set back to STANDARD, billing proposes as usual.
func TestBillingRule_SelfBilledCustomerGetsNoShipmentInvoice(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	path := "/v1/billing/customers/" + custID + "/billing-rule"
	rule := `{"legalEntityId":"` + leID + `","rule":"SELF_BILLED","agreementRef":"SBA-2027-01","validFrom":"2027-03-01"}`
	rec := do(h, http.MethodPut, path, "", rule)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	refused := do(h, http.MethodPost, "/v1/billing/proposals", "ship-sb", shipmentBody(false, slipA))
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), CodeSelfBilled) || !strings.Contains(refused.Body.String(), "SBA-2027-01") {
		t.Fatalf("self-billed customer: %d %s", refused.Code, refused.Body)
	}
	if s.batchCreates != 0 {
		t.Fatal("nothing is stored for a self-billed customer")
	}
	// A stale version is refused; the current one moves the customer back to STANDARD and proposals flow again.
	if rec := doAs(h, http.MethodPut, path, "", `{"legalEntityId":"`+leID+`","rule":"STANDARD"}`, allDuties, map[string]string{"If-Match": `"7"`}); rec.Code != http.StatusConflict {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(h, http.MethodPut, path, "", `{"legalEntityId":"`+leID+`","rule":"STANDARD"}`, allDuties, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK {
		t.Fatalf("standard: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/v1/billing/proposals", "ship-std", shipmentBody(false, slipA)); rec.Code != http.StatusCreated {
		t.Fatalf("standard customer: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, path+"?legalEntityId="+leID, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"STANDARD"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
}

func TestBillingRule_AgreementNotYetValidStillInvoices(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	path := "/v1/billing/customers/" + custID + "/billing-rule"
	if rec := do(h, http.MethodPut, path, "", `{"legalEntityId":"`+leID+`","rule":"SELF_BILLED","agreementRef":"SBA-1","validFrom":"2027-04-01"}`); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/v1/billing/proposals", "ship-early", shipmentBody(false, slipA)); rec.Code != http.StatusCreated {
		t.Fatalf("invoice dated before the agreement: %d %s", rec.Code, rec.Body)
	}
}

func TestBillingRule_Validation(t *testing.T) {
	h := setup(t, newFakeStore(), &fakeTax{perLine: "0"})
	path := "/v1/billing/customers/" + custID + "/billing-rule"
	for name, body := range map[string]string{
		"rule":         `{"legalEntityId":"` + leID + `","rule":"WEEKLY"}`,
		"agreementRef": `{"legalEntityId":"` + leID + `","rule":"SELF_BILLED","validFrom":"2027-03-01"}`,
		"validFrom":    `{"legalEntityId":"` + leID + `","rule":"SELF_BILLED","agreementRef":"A"}`,
		"validTo":      `{"legalEntityId":"` + leID + `","rule":"SELF_BILLED","agreementRef":"A","validFrom":"2027-03-01","validTo":"2027-02-01"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(h, http.MethodPut, path, "", body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+name+`"`) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	if rec := doAs(h, http.MethodPut, path, "", `{"legalEntityId":"`+leID+`","rule":"STANDARD"}`, "billing.invoice.view", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("duty: %d", rec.Code)
	}
}
