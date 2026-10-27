package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

// postedInvoice creates and posts a 2-line EUR invoice: 3 × 33.33 and
// 1 × 100.00, each taxed 19.00 by the fake engine, plus 10.00 charges
// (net 199.99, tax 38.00, gross 247.99).
func postedInvoice(t *testing.T, h http.Handler) invoiceDTO {
	t.Helper()
	body := fmt.Sprintf(`{"legalEntityId":%q,"customerId":%q,"invoiceAccountId":%q,"currency":"EUR","invoiceDate":"2027-03-10","paymentTermDays":30,"chargeAmount":"10",`+
		`"lines":[{"description":"Consulting","qty":"3","unitPrice":"33.33","revenueAccount":"4900","taxCode":"DE19"},`+
		`{"description":"Workshop","qty":"1","unitPrice":"100","revenueAccount":"4910","taxCode":"DE19"}]}`, leID, custID, acctID)
	rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", "inv-"+t.Name(), body)
	var created invoiceDTO
	decode(t, rec, &created)
	post := do(h, http.MethodPost, "/v1/billing/invoices/"+created.ID+":post", "", "")
	var posted invoiceDTO
	decode(t, post, &posted)
	if post.Code != http.StatusOK || posted.Totals.Gross != "247.99" {
		t.Fatalf("posting the original: status %d %+v", post.Code, posted)
	}
	return posted
}

func creditBody(original, date, extra string) string {
	return fmt.Sprintf(`{"originalInvoiceId":%q,"creditDate":%q,"reason":"Service not delivered"%s}`, original, date, extra)
}

// AC-29.6 (billing side): a full credit note reverses the original's amounts
// exactly, tax included, without re-quoting tax; it posts with its own CN-
// number and leaves nothing creditable.
func TestCreditNote_FullCreditReversesTheOriginal(t *testing.T) {
	s := newFakeStore()
	tax := &fakeTax{perLine: "19.00"}
	h := setup(t, s, tax)
	orig := postedInvoice(t, h)
	if orig.Credited == nil || orig.Credited.Remaining != "247.99" || orig.Lines[0].CreditedQty != "0" {
		t.Fatalf("posted invoice shows credited %+v lines %+v", orig.Credited, orig.Lines)
	}
	tax.perLine = "99.00" // a fresh quote would differ; the credit must carry the original tax

	rec := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-1", creditBody(orig.ID, "2027-03-20", ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d %s", rec.Code, rec.Body)
	}
	var cn invoiceDTO
	decode(t, rec, &cn)
	if cn.Type != "CREDIT_NOTE" || cn.Status != "PROPOSAL" || cn.OriginalInvoiceID != orig.ID || cn.OriginalNumber != orig.Number || cn.Reason != "Service not delivered" {
		t.Fatalf("credit note header %+v", cn.summaryDTO)
	}
	if cn.Totals != orig.Totals {
		t.Fatalf("credit totals %+v, want the original's %+v", cn.Totals, orig.Totals)
	}
	if len(cn.Lines) != 2 || cn.Lines[0].OriginalLineNo != 1 || cn.Lines[0].Tax != "19.00" || cn.Lines[1].RevenueAccount != "4910" || cn.DueDate != "2027-03-20" {
		t.Fatalf("credit lines %+v due %s", cn.Lines, cn.DueDate)
	}
	if cn.Credited != nil {
		t.Errorf("a credit note has no creditable remainder of its own, got %+v", cn.Credited)
	}

	replay := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-1", creditBody(orig.ID, "2027-03-20", ""))
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || s.creditCreates != 1 {
		t.Fatalf("replay: status %d creates %d", replay.Code, s.creditCreates)
	}

	wrong := do(h, http.MethodPost, "/v1/billing/invoices/"+cn.ID+":post", "", "")
	var p httpx.Problem
	decode(t, wrong, &p)
	if wrong.Code != http.StatusConflict || p.Code != "BIL-0101" || s.posts != 1 {
		t.Fatalf("credit note through the invoice post: status %d code %s posts %d, want 409 BIL-0101", wrong.Code, p.Code, s.posts)
	}
	notCN := do(h, http.MethodPost, "/v1/billing/credit-notes/"+orig.ID+":post", "", "")
	decode(t, notCN, &p)
	if notCN.Code != http.StatusConflict || p.Code != "BIL-0101" {
		t.Fatalf("invoice through the credit note post: status %d code %s, want 409 BIL-0101", notCN.Code, p.Code)
	}

	post := doAs(h, http.MethodPost, "/v1/billing/credit-notes/"+cn.ID+":post", "", "", allDuties, map[string]string{"If-Match": `"1"`})
	var posted invoiceDTO
	decode(t, post, &posted)
	if post.Code != http.StatusOK || posted.Number != "CN-2027-0000001" || posted.Status != "POSTED" || posted.PostingDate != "2027-03-12" {
		t.Fatalf("post: status %d %+v", post.Code, posted.summaryDTO)
	}
	again := do(h, http.MethodPost, "/v1/billing/credit-notes/"+cn.ID+":post", "", "")
	if again.Code != http.StatusOK || s.creditPosts != 1 {
		t.Fatalf("repeat post: status %d posts %d, want the posted note unchanged", again.Code, s.creditPosts)
	}

	var after invoiceDTO
	decode(t, do(h, http.MethodGet, "/v1/billing/invoices/"+orig.ID, "", ""), &after)
	if after.Credited.Gross != "247.99" || after.Credited.Remaining != "0.00" || after.Lines[0].CreditedQty != "3" {
		t.Fatalf("original after the credit: %+v lines %+v", after.Credited, after.Lines)
	}
	more := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-2", creditBody(orig.ID, "2027-03-20", ""))
	decode(t, more, &p)
	if more.Code != http.StatusUnprocessableEntity || p.Code != "BIL-0005" {
		t.Fatalf("crediting a fully credited invoice: status %d code %s, want 422 BIL-0005", more.Code, p.Code)
	}
}

func TestCreditNote_PartialLinesAndRemainingAmount(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	orig := postedInvoice(t, h)

	rec := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-p1", creditBody(orig.ID, "2027-03-20", `,"lines":[{"lineNo":1,"qty":"1"}]`))
	var cn invoiceDTO
	decode(t, rec, &cn)
	// 1 of 3 units: net 33.33, tax 19.00 / 3 = 6.33, no charges.
	if rec.Code != http.StatusCreated || len(cn.Lines) != 1 || cn.Totals.Net != "33.33" || cn.Totals.Tax != "6.33" || cn.Totals.Charges != "0.00" || cn.Totals.Gross != "39.66" {
		t.Fatalf("partial credit: status %d %+v", rec.Code, cn)
	}
	// A second proposal of the same units fits alone; once the first posts it does not.
	var second invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-p2", creditBody(orig.ID, "2027-03-20", `,"lines":[{"lineNo":1,"qty":"3"}]`)), &second)
	if rec := do(h, http.MethodPost, "/v1/billing/credit-notes/"+cn.ID+":post", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("post first: %d %s", rec.Code, rec.Body)
	}
	late := do(h, http.MethodPost, "/v1/billing/credit-notes/"+second.ID+":post", "", "")
	var p httpx.Problem
	decode(t, late, &p)
	if late.Code != http.StatusUnprocessableEntity || p.Code != "BIL-0005" {
		t.Fatalf("overtaken proposal: status %d code %s, want 422 BIL-0005", late.Code, p.Code)
	}

	over := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-p3", creditBody(orig.ID, "2027-03-20", `,"lines":[{"lineNo":1,"qty":"2.5"}]`))
	decode(t, over, &p)
	if over.Code != http.StatusUnprocessableEntity || p.Code != "BIL-0005" || !strings.Contains(p.Detail, "line 1 has 2 left") {
		t.Fatalf("over-credit: status %d %+v", over.Code, p)
	}
	charges := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-p4", creditBody(orig.ID, "2027-03-20", `,"lines":[{"lineNo":2,"qty":"1"}],"chargeAmount":"10.01"`))
	decode(t, charges, &p)
	if charges.Code != http.StatusUnprocessableEntity || p.Code != "BIL-0005" {
		t.Fatalf("charges beyond the original: status %d code %s", charges.Code, p.Code)
	}
	unknown := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-p5", creditBody(orig.ID, "2027-03-20", `,"lines":[{"lineNo":7,"qty":"1"}]`))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown line: status %d, want 400", unknown.Code)
	}
	var list listDTO
	decode(t, do(h, http.MethodGet, "/v1/billing/invoices?type=CREDIT_NOTE&originalInvoiceId="+orig.ID, "", ""), &list)
	if len(list.Items) != 2 || s.lastFilter.Type != "CREDIT_NOTE" || s.lastFilter.OriginalInvoiceID != orig.ID {
		t.Fatalf("credit notes of the original: %d items, filter %+v", len(list.Items), s.lastFilter)
	}
}

func TestCreditNote_Refusals(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	var prop invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/free-text-invoices", key, proposal("1", "100", "4900")), &prop)
	var p httpx.Problem
	rec := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-r1", creditBody(prop.ID, "2027-03-20", ""))
	decode(t, rec, &p)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != "BIL-0005" {
		t.Fatalf("crediting a proposal: status %d code %s, want 422 BIL-0005", rec.Code, p.Code)
	}

	orig := postedInvoice(t, h)
	early := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-r2", creditBody(orig.ID, "2027-03-01", ""))
	decode(t, early, &p)
	if early.Code != http.StatusBadRequest || p.Errors["creditDate"] == "" {
		t.Fatalf("credit before the original date: status %d %+v", early.Code, p)
	}
	bad := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-r3", `{"originalInvoiceId":"x","creditDate":"20.03.2027","reason":" ","chargeAmount":"-1","lines":[{"lineNo":0,"qty":"0"}]}`)
	decode(t, bad, &p)
	for _, f := range []string{"originalInvoiceId", "creditDate", "reason", "chargeAmount", "lines[0].lineNo", "lines[0].qty"} {
		if p.Errors[f] == "" {
			t.Errorf("no field error for %s: %+v", f, p.Errors)
		}
	}
	if missing := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-r4", creditBody(leID, "2027-03-20", "")); missing.Code != http.StatusNotFound {
		t.Errorf("unknown original: status %d, want 404", missing.Code)
	}
	if noKey := do(h, http.MethodPost, "/v1/billing/credit-notes", "", creditBody(orig.ID, "2027-03-20", "")); noKey.Code != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key: status %d, want 400", noKey.Code)
	}
	if s.creditCreates != 0 {
		t.Errorf("%d credit notes stored by refused requests", s.creditCreates)
	}

	var cn invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-r5", creditBody(orig.ID, "2027-03-20", "")), &cn)
	stale := doAs(h, http.MethodPost, "/v1/billing/credit-notes/"+cn.ID+":post", "", "", allDuties, map[string]string{"If-Match": "abc"})
	if stale.Code != http.StatusBadRequest {
		t.Errorf("malformed If-Match: status %d, want 400", stale.Code)
	}
}

func TestCreditNote_Duties(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	orig := postedInvoice(t, h)
	if rec := doAs(h, http.MethodPost, "/v1/billing/credit-notes", "cn-d1", creditBody(orig.ID, "2027-03-20", ""), "billing.invoice.view,billing.creditnote.post", nil); rec.Code != http.StatusForbidden {
		t.Errorf("create without InvoiceMaintain: status %d, want 403", rec.Code)
	}
	var cn invoiceDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-d2", creditBody(orig.ID, "2027-03-20", "")), &cn)
	// InvoicePost does not grant CreditNotePost (§5.29.17: billing supervisor).
	if rec := doAs(h, http.MethodPost, "/v1/billing/credit-notes/"+cn.ID+":post", "", "", "billing.invoice.view,billing.invoice.maintain,billing.invoice.post", nil); rec.Code != http.StatusForbidden || s.creditPosts != 0 {
		t.Errorf("post without CreditNotePost: status %d posts %d, want 403", rec.Code, s.creditPosts)
	}
}

// AC-29.10: terms 30/30/40 at +0/+30/+60 days on a gross of 1,000.01 USD
// invoiced 2027-03-10 give open items of 300.00, 300.00 and 400.01.
func TestCreateProposal_InstallmentsAC29_10(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "0"})
	body := fmt.Sprintf(`{"legalEntityId":%q,"customerId":%q,"invoiceAccountId":%q,"currency":"USD","invoiceDate":"2027-03-10","paymentTermDays":60,`+
		`"installmentTerms":[{"percent":"30","offsetDays":0},{"percent":"30","offsetDays":30},{"percent":"40","offsetDays":60}],`+
		`"lines":[{"description":"Machine","qty":"1","unitPrice":"1000.01","revenueAccount":"4000","taxCode":"US0"}]}`, leID, custID, acctID)
	rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", "inst-1", body)
	var got invoiceDTO
	decode(t, rec, &got)
	if rec.Code != http.StatusCreated || got.Totals.Gross != "1000.01" || len(got.Installments) != 3 {
		t.Fatalf("status %d %+v", rec.Code, got)
	}
	want := []installmentOutDTO{
		{No: 1, Percent: "30", OffsetDays: 0, Amount: "300.00", DueDate: "2027-03-10"},
		{No: 2, Percent: "30", OffsetDays: 30, Amount: "300.00", DueDate: "2027-04-09"},
		{No: 3, Percent: "40", OffsetDays: 60, Amount: "400.01", DueDate: "2027-05-09"},
	}
	for i, w := range want {
		if got.Installments[i] != w {
			t.Errorf("installment %d = %+v, want %+v", i+1, got.Installments[i], w)
		}
	}
	if got.DueDate != "2027-05-09" {
		t.Errorf("due date %s, want the last installment's 2027-05-09", got.DueDate)
	}
	post := do(h, http.MethodPost, "/v1/billing/invoices/"+got.ID+":post", "", "")
	var posted invoiceDTO
	decode(t, post, &posted)
	if post.Code != http.StatusOK || len(posted.Installments) != 3 || posted.Number == "" {
		t.Fatalf("posted with one number and its installments: %d %+v", post.Code, posted)
	}

	plain := do(h, http.MethodPost, "/v1/billing/free-text-invoices", "inst-plain", proposal("1", "100", "4900"))
	var none invoiceDTO
	decode(t, plain, &none)
	if none.Installments == nil || len(none.Installments) != 0 {
		t.Errorf("an invoice without a plan lists %v installments, want an empty list", none.Installments)
	}
}

func TestCreateProposal_InstallmentRefusals(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "0"})
	withTerms := func(terms string) string {
		return fmt.Sprintf(`{"legalEntityId":%q,"customerId":%q,"invoiceAccountId":%q,"currency":"USD","invoiceDate":"2027-03-10","paymentTermDays":30,`+
			`"installmentTerms":%s,"lines":[{"description":"x","qty":"1","unitPrice":"10","revenueAccount":"4000","taxCode":"US0"}]}`, leID, custID, acctID, terms)
	}
	var p httpx.Problem
	rec := do(h, http.MethodPost, "/v1/billing/free-text-invoices", "inst-r1", withTerms(`[{"percent":"30","offsetDays":0},{"percent":"60","offsetDays":30}]`))
	decode(t, rec, &p)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != "BIL-0011" || !strings.Contains(p.Detail, "90%") {
		t.Fatalf("percentages total 90: status %d %+v, want 422 BIL-0011", rec.Code, p)
	}
	rec = do(h, http.MethodPost, "/v1/billing/free-text-invoices", "inst-r2", withTerms(`[{"percent":"0","offsetDays":30},{"percent":"100","offsetDays":10}]`))
	decode(t, rec, &p)
	if rec.Code != http.StatusBadRequest || p.Errors["installmentTerms[0].percent"] == "" || p.Errors["installmentTerms[1].offsetDays"] == "" {
		t.Fatalf("bad terms: status %d %+v", rec.Code, p.Errors)
	}
	if s.creates != 0 {
		t.Errorf("%d proposals stored by refused requests", s.creates)
	}
}
