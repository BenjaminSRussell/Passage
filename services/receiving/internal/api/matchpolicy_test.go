package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/api"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/store/memstore"
)

const (
	policiesPath   = "/v1/receiving/matching-policies"
	exceptionsPath = "/v1/receiving/invoice-exceptions"
	strictPolicy   = `"policy":{"priceTolPct":1,"priceTolAbs":0}`
	loosePolicy    = `"policy":{"priceTolPct":100,"priceTolAbs":100}`
)

type exceptionListJSON struct {
	Items []struct {
		InvoiceID      string `json:"invoiceId"`
		Assignee       string `json:"assignee"`
		Reasons        string `json:"reasons"`
		Status         string `json:"status"`
		DecidedBy      string `json:"decidedBy"`
		DecisionReason string `json:"decisionReason"`
	} `json:"items"`
}

func (rg *rig) putMatchingPolicy(t *testing.T, scope, owner, policy string, want int) {
	t.Helper()
	body := fmt.Sprintf(`{"legalEntityId":%q,%s"ownerUserId":%q,%s}`, legalEntityID, scope, owner, policy)
	if rec := rg.call(http.MethodPut, policiesPath, body, rg.policyMaker()); rec.Code != want {
		t.Fatalf("put policy %s: %d %s, want %d", body, rec.Code, rec.Body.String(), want)
	}
}

func (rg *rig) overPriceInvoice(t *testing.T, number, receiptID string) *httpResult {
	t.Helper()
	body := strings.Replace(invoiceBody(number, receiptID, 4, ""), `"unitPrice":2.5`, `"unitPrice":4`, 1)
	rec := rg.call(http.MethodPost, invoicesPath, body, userDuty("receiving.vendor-invoice.post", rg.key(), "alice"))
	return &httpResult{code: rec.Code, body: rec.Body.String()}
}

func (rg *rig) exceptions(t *testing.T, status string) exceptionListJSON {
	t.Helper()
	return decode[exceptionListJSON](t, rg.call(http.MethodGet, exceptionsPath+"?status="+status, "", rg.inquirer()), http.StatusOK)
}

func (rg *rig) approve(id, user, reason string) *httpResult {
	body := fmt.Sprintf(`{"legalEntityId":%q,"reason":%q}`, legalEntityID, reason)
	rec := rg.call(http.MethodPost, exceptionsPath+"/"+id+":approve", body, userDuty("receiving.invoice-audit.release", rg.key(), user))
	return &httpResult{code: rec.Code, body: rec.Body.String()}
}

func TestMatchingPolicy_OverToleranceInvoiceRaisesAnExceptionForTheOwnerAndApprovalReleasesIt(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rg.putMatchingPolicy(t, "", "owner-1", strictPolicy, http.StatusOK)
	r := rg.posted(t, "PS-1", 10, 4)
	receiptEntries := len(rg.ledger.Entries)

	held := rg.overPriceInvoice(t, "INV-1", r.ID).invoice(t, http.StatusCreated)

	if held.Status != "AUDIT_BLOCKED" || held.LedgerEntryID != "" || len(rg.ledger.Entries) != receiptEntries {
		t.Fatalf("held %+v, ledger entries %d (was %d)", held, len(rg.ledger.Entries), receiptEntries)
	}
	open := rg.exceptions(t, "OPEN")
	if len(open.Items) != 1 || open.Items[0].InvoiceID != held.ID || open.Items[0].Assignee != "owner-1" || open.Items[0].Reasons != "line 1: PRICE_VARIANCE" {
		t.Fatalf("open exceptions %+v", open)
	}
	if !strings.Contains(metricsText(rg), "receiving_invoice_exceptions_opened_total 1") {
		t.Fatal("the opened exception is not counted")
	}

	problemCode(t, rg.call(http.MethodPost, exceptionsPath+"/"+held.ID+":approve", fmt.Sprintf(`{"legalEntityId":%q,"reason":"  "}`, legalEntityID), userDuty("receiving.invoice-audit.release", rg.key(), "owner-1")), http.StatusBadRequest, "COM-0001")
	if got := rg.approve(held.ID, "bob", "looks fine"); got.code != http.StatusForbidden || !strings.Contains(got.body, "AP-MPX-002") {
		t.Fatalf("approval by a user who is not the owner: %d %s", got.code, got.body)
	}
	if len(rg.ledger.Entries) != receiptEntries || len(rg.exceptions(t, "OPEN").Items) != 1 {
		t.Fatal("a refused approval changed state")
	}

	released := rg.approve(held.ID, "owner-1", "price agreed with the vendor").invoice(t, http.StatusOK)

	if released.Status != "POSTED" || released.ReleasedBy != "owner-1" || released.LedgerEntryID == "" || len(rg.ledger.Entries) != receiptEntries+1 {
		t.Fatalf("released %+v, ledger entries %d", released, len(rg.ledger.Entries))
	}
	decided := rg.exceptions(t, "APPROVED")
	if len(rg.exceptions(t, "OPEN").Items) != 0 || len(decided.Items) != 1 || decided.Items[0].DecidedBy != "owner-1" || decided.Items[0].DecisionReason != "price agreed with the vendor" {
		t.Fatalf("approved exceptions %+v", decided)
	}
	if got := rg.approve(held.ID, "owner-1", "again"); got.code != http.StatusConflict || !strings.Contains(got.body, "AP-MPX-001") || len(rg.ledger.Entries) != receiptEntries+1 {
		t.Fatalf("second approval: %d %s", got.code, got.body)
	}
	if !strings.Contains(metricsText(rg), "receiving_invoice_exceptions_approved_total 1") {
		t.Fatal("the approved exception is not counted")
	}
}

func TestMatchingPolicy_PosterWhoOwnsThePolicyCannotApproveTheirOwnInvoice(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rg.putMatchingPolicy(t, "", "alice", strictPolicy, http.StatusOK)
	r := rg.posted(t, "PS-1", 10, 4)
	held := rg.overPriceInvoice(t, "INV-1", r.ID).invoice(t, http.StatusCreated)
	entries := len(rg.ledger.Entries)

	if got := rg.approve(held.ID, "alice", "mine"); got.code != http.StatusForbidden || !strings.Contains(got.body, "AP-AUD-003") || len(rg.ledger.Entries) != entries {
		t.Fatalf("self approval: %d %s", got.code, got.body)
	}
}

func TestMatchingPolicy_ReleaseAuditBlockCannotBypassTheOwnerApproval(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rg.putMatchingPolicy(t, "", "owner-1", strictPolicy, http.StatusOK)
	r := rg.posted(t, "PS-1", 10, 4)
	held := rg.overPriceInvoice(t, "INV-1", r.ID).invoice(t, http.StatusCreated)
	entries := len(rg.ledger.Entries)

	if code, body := rg.release(held.ID, "bob"); code != http.StatusForbidden || !strings.Contains(body, "AP-MPX-002") || len(rg.ledger.Entries) != entries {
		t.Fatalf("release bypass: %d %s", code, body)
	}
}

func TestMatchingPolicy_NoPolicyOrWithinToleranceLeavesPostingUnchanged(t *testing.T) {
	for name, scope := range map[string]string{"no stored policy": "", "a stored policy the price is within": loosePolicy} {
		t.Run(name, func(t *testing.T) {
			rg := newRig(domain.PeriodOpen)
			if scope != "" {
				rg.putMatchingPolicy(t, "", "owner-1", scope, http.StatusOK)
			}
			r := rg.posted(t, "PS-1", 10, 4)
			receiptEntries := len(rg.ledger.Entries)

			posted := rg.overPriceInvoice(t, "INV-1", r.ID).invoice(t, http.StatusCreated)

			if posted.Status != "POSTED" || len(rg.ledger.Entries) != receiptEntries+1 || len(rg.exceptions(t, "OPEN").Items) != 0 {
				t.Fatalf("posted %+v, ledger entries %d", posted, len(rg.ledger.Entries))
			}
		})
	}
}

func TestMatchingPolicy_MostSpecificScopeDecidesWhichPolicyApplies(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rg.putMatchingPolicy(t, "", "entity-owner", strictPolicy, http.StatusOK)
	rg.putMatchingPolicy(t, fmt.Sprintf(`"vendorId":%q,"itemId":%q,`, vendorID, productID), "vendor-item-owner", loosePolicy, http.StatusOK)
	r := rg.posted(t, "PS-1", 10, 4)

	if posted := rg.overPriceInvoice(t, "INV-1", r.ID).invoice(t, http.StatusCreated); posted.Status != "POSTED" {
		t.Fatalf("the vendor x item policy is looser than the legal entity policy, invoice is %s", posted.Status)
	}

	rg.putMatchingPolicy(t, fmt.Sprintf(`"vendorId":%q,`, vendorID), "vendor-owner", strictPolicy, http.StatusOK)
	rg.putMatchingPolicy(t, fmt.Sprintf(`"vendorId":%q,"itemId":%q,`, vendorID, productID), "vendor-item-owner", strictPolicy, http.StatusOK)
	r2 := rg.posted(t, "PS-2", 10, 4)
	if held := rg.overPriceInvoice(t, "INV-2", r2.ID).invoice(t, http.StatusCreated); held.Status != "AUDIT_BLOCKED" {
		t.Fatalf("replacing the vendor x item scope must apply, invoice is %s", held.Status)
	}
	if open := rg.exceptions(t, "OPEN"); len(open.Items) != 1 || open.Items[0].Assignee != "vendor-item-owner" {
		t.Fatalf("the exception goes to the owner of the most specific policy: %+v", open)
	}
}

func TestMatchingPolicy_PutValidatesAndAuthorises(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	valid := fmt.Sprintf(`{"legalEntityId":%q,"ownerUserId":"owner-1"}`, legalEntityID)
	for _, tc := range []struct {
		name, body string
		hdr        headers
		status     int
		code       string
	}{
		{"missing owner", fmt.Sprintf(`{"legalEntityId":%q}`, legalEntityID), rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"vendor not a uuid", fmt.Sprintf(`{"legalEntityId":%q,"ownerUserId":"o","vendorId":"x"}`, legalEntityID), rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"item not a uuid", fmt.Sprintf(`{"legalEntityId":%q,"ownerUserId":"o","itemId":"x"}`, legalEntityID), rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"negative tolerance", fmt.Sprintf(`{"legalEntityId":%q,"ownerUserId":"o","policy":{"priceTolPct":-1}}`, legalEntityID), rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"unknown match level", fmt.Sprintf(`{"legalEntityId":%q,"ownerUserId":"o","policy":{"matchLevel":"FOUR_WAY"}}`, legalEntityID), rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"tolerance of the wrong type", fmt.Sprintf(`{"legalEntityId":%q,"ownerUserId":"o","policy":{"priceTolPct":{"a":1}}}`, legalEntityID), rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"legal entity not a uuid", `{"legalEntityId":"x","ownerUserId":"o"}`, rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"legal entity outside the caller's scope", strings.Replace(valid, legalEntityID, ledgerID, 1), userDuty("receiving.matching-policy.maintain", rg.key(), "policy-admin", entityHeader, legalEntityID), http.StatusForbidden, "AUTH-0003"},
		{"another duty", valid, userDuty("receiving.vendor-invoice.post", rg.key(), "policy-admin"), http.StatusForbidden, "AUTH-0003"},
		{"no user", valid, userDuty("receiving.matching-policy.maintain", rg.key(), ""), http.StatusUnauthorized, "AUTH-0001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problemCode(t, rg.call(http.MethodPut, policiesPath, tc.body, tc.hdr), tc.status, tc.code)
		})
	}
	rec := rg.call(http.MethodPut, policiesPath, valid, rg.policyMaker())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ownerUserId":"owner-1"`) || !strings.Contains(rec.Body.String(), `"matchLevel":"THREE_WAY"`) {
		t.Fatalf("valid policy: %d %s", rec.Code, rec.Body.String())
	}
}

func TestInvoiceExceptions_ListAndApproveValidateAuthoriseAndScope(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rg.putMatchingPolicy(t, "", "owner-1", strictPolicy, http.StatusOK)
	r := rg.posted(t, "PS-1", 10, 4)
	held := rg.overPriceInvoice(t, "INV-1", r.ID).invoice(t, http.StatusCreated)
	approvePath := exceptionsPath + "/" + held.ID + ":approve"
	body := fmt.Sprintf(`{"legalEntityId":%q,"reason":"ok"}`, legalEntityID)
	releaser := func(user string, extra ...string) headers {
		return userDuty("receiving.invoice-audit.release", rg.key(), user, extra...)
	}

	for _, tc := range []struct {
		name, method, path, body string
		hdr                      headers
		status                   int
		code                     string
	}{
		{"unknown status", http.MethodGet, exceptionsPath + "?status=DONE", "", rg.inquirer(), http.StatusBadRequest, "COM-0001"},
		{"legal entity not a uuid", http.MethodGet, exceptionsPath + "?legalEntityId=x", "", rg.inquirer(), http.StatusBadRequest, "COM-0001"},
		{"legal entity outside the caller's scope", http.MethodGet, exceptionsPath + "?legalEntityId=" + ledgerID, "", rg.inquirer(entityHeader, legalEntityID), http.StatusForbidden, "AUTH-0003"},
		{"list needs the inquiry duty", http.MethodGet, exceptionsPath, "", rg.maintain(), http.StatusForbidden, "AUTH-0003"},
		{"approve needs the release duty", http.MethodPost, approvePath, body, userDuty("receiving.matching-policy.maintain", rg.key(), "owner-1"), http.StatusForbidden, "AUTH-0003"},
		{"approve needs a user", http.MethodPost, approvePath, body, releaser(""), http.StatusUnauthorized, "AUTH-0001"},
		{"approve id not a uuid", http.MethodPost, exceptionsPath + "/x:approve", body, releaser("owner-1"), http.StatusBadRequest, "COM-0001"},
		{"approve without a legal entity", http.MethodPost, approvePath, `{"reason":"ok"}`, releaser("owner-1"), http.StatusBadRequest, "COM-0001"},
		{"approve legal entity outside the caller's scope", http.MethodPost, approvePath, strings.Replace(body, legalEntityID, ledgerID, 1), releaser("owner-1", entityHeader, legalEntityID), http.StatusForbidden, "AUTH-0003"},
		{"approve an invoice that is not held", http.MethodPost, exceptionsPath + "/" + poID + ":approve", body, releaser("owner-1"), http.StatusNotFound, "RCV-NF-005"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problemCode(t, rg.call(tc.method, tc.path, tc.body, tc.hdr), tc.status, tc.code)
		})
	}
	if open := rg.exceptions(t, "OPEN"); len(open.Items) != 1 {
		t.Fatalf("refused calls must leave the exception open: %+v", open)
	}
}

func TestMatchingPolicy_ReceiptReadFailureStopsThePostingInsteadOfSkippingTheMatch(t *testing.T) {
	mem := memstore.New()
	flaky := &receiptReadFails{Store: mem}
	rg := &rig{st: mem, ledger: &domain.FakeLedger{}}
	a := api.New(flaky, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: rg.ledger,
		Profile: domain.PostingProfile{LedgerID: ledgerID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"},
	})
	router := httpx.NewRouter()
	a.Register(router)
	rg.h, rg.api = router, a
	rg.putMatchingPolicy(t, "", "owner-1", strictPolicy, http.StatusOK)
	r := rg.posted(t, "PS-1", 10, 4)
	entries := len(rg.ledger.Entries)
	flaky.failing = true

	rec := rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 4, ""), userDuty("receiving.vendor-invoice.post", rg.key(), "alice"))

	flaky.failing = false
	if rec.Code != http.StatusInternalServerError || len(rg.ledger.Entries) != entries || len(rg.exceptions(t, "OPEN").Items) != 0 {
		t.Fatalf("got %d %s, ledger entries %d (was %d)", rec.Code, rec.Body.String(), len(rg.ledger.Entries), entries)
	}
	if after := rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 4, ""), userDuty("receiving.vendor-invoice.post", rg.key(), "alice")); after.Code != http.StatusCreated {
		t.Fatalf("the invoice must post once the receipt can be read: %d %s", after.Code, after.Body.String())
	}
}
