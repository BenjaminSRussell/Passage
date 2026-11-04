package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const (
	oiItemA = "0190a000-0000-7000-8000-0000000000e1"
	oiItemB = "0190a000-0000-7000-8000-0000000000e2"
	oiDoc   = "0190a000-0000-7000-8000-0000000000f1"
)

// openItemFake records the open item calls of fakeStore.
type openItemFake struct {
	accounts   []store.OpenItemAccount
	putAccount []bool
	page       store.OpenItemPage
	filters    []store.OpenItemFilter
	clearCalls []store.ClearingInput
	simCalls   []store.ClearingInput
	doc        store.ClearingDocument
	docNew     bool
	resets     []store.ResetInput
	groups     []store.ToleranceGroup
	putGroups  []store.ToleranceGroup
	putVers    []int
	groupNew   bool
	activate   []store.ActivationInput
	activated  bool
}

func (f *fakeStore) ListOpenItemAccounts(context.Context, string) ([]store.OpenItemAccount, error) {
	return f.oi.accounts, f.err
}

func (f *fakeStore) PutOpenItemAccount(_ context.Context, ledgerID, number string, enabled bool) (store.OpenItemAccount, bool, error) {
	f.oi.putAccount = append(f.oi.putAccount, enabled)
	return store.OpenItemAccount{LedgerID: ledgerID, Account: number, Enabled: enabled}, true, f.err
}

func (f *fakeStore) ActivateOpenItemAccount(_ context.Context, in store.ActivationInput) (store.ActivationRun, bool, error) {
	f.oi.activate = append(f.oi.activate, in)
	return store.ActivationRun{ID: oiDoc, LedgerID: in.LedgerID, Account: in.Account, ActivationDate: in.Date.Format("2006-01-02")}, f.oi.activated, f.err
}

func (f *fakeStore) ListActivationRuns(context.Context, string) ([]store.ActivationRun, error) {
	return []store.ActivationRun{{ID: oiDoc}}, f.err
}

func (f *fakeStore) ListToleranceGroups(context.Context, string) ([]store.ToleranceGroup, error) {
	return f.oi.groups, f.err
}

func (f *fakeStore) GetToleranceGroup(_ context.Context, _, code string) (store.ToleranceGroup, error) {
	return store.ToleranceGroup{Code: code, Version: 4}, f.err
}

func (f *fakeStore) PutToleranceGroup(_ context.Context, _, code string, version int, in store.ToleranceGroup) (store.ToleranceGroup, bool, error) {
	f.oi.putGroups = append(f.oi.putGroups, in)
	f.oi.putVers = append(f.oi.putVers, version)
	in.Code, in.Version = code, 2
	return in, f.oi.groupNew, f.err
}

func (f *fakeStore) ListOpenItems(_ context.Context, fl store.OpenItemFilter) (store.OpenItemPage, error) {
	f.oi.filters = append(f.oi.filters, fl)
	return f.oi.page, f.err
}

func (f *fakeStore) SimulateClearing(_ context.Context, in store.ClearingInput) (store.ClearingPreview, error) {
	f.oi.simCalls = append(f.oi.simCalls, in)
	return store.ClearingPreview{Outcome: "difference", Net: "0.02"}, f.err
}

func (f *fakeStore) ClearItems(_ context.Context, in store.ClearingInput) (store.ClearingDocument, bool, error) {
	f.oi.clearCalls = append(f.oi.clearCalls, in)
	return f.oi.doc, f.oi.docNew, f.err
}

func (f *fakeStore) ListClearings(_ context.Context, _, _ string, page store.Page) ([]store.ClearingDocument, error) {
	f.listPage = page
	if f.clearingPage != nil {
		return f.clearingPage, f.err
	}
	return []store.ClearingDocument{f.oi.doc}, f.err
}

func (f *fakeStore) GetClearing(context.Context, string) (store.ClearingDocument, error) {
	return f.oi.doc, f.err
}

func (f *fakeStore) ResetClearing(_ context.Context, in store.ResetInput) (store.ClearingDocument, error) {
	f.oi.resets = append(f.oi.resets, in)
	return f.oi.doc, f.err
}

const clearBody = `{"ledgerId":"` + testLedgerID + `","itemIds":["` + oiItemB + `","` + oiItemA + `"],"clearingDate":"2026-03-31","toleranceGroup":"STD","residualItem":true}`

func TestClearing_CreateMapsRequestAndAnswers201Then200(t *testing.T) {
	fs := &fakeStore{}
	fs.oi.docNew = true
	fs.oi.doc = store.ClearingDocument{ID: oiDoc, Status: "cleared"}
	h := server(fs)
	hdr := map[string]string{"Idempotency-Key": "k1"}
	rec := call(h, http.MethodPost, "/v1/ledger/clearings", clearBody, dutyClearingWrite, hdr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	in := fs.oi.clearCalls[0]
	if in.Key != "k1" || in.ToleranceGroup != "STD" || !in.ResidualItem || len(in.ItemIDs) != 2 || in.ClearingDate.Format("2006-01-02") != "2026-03-31" || len(in.Hash) != 32 {
		t.Fatalf("request not mapped: %+v", in)
	}
	fs.oi.docNew = false
	rec = call(h, http.MethodPost, "/v1/ledger/clearings", clearBody, dutyClearingWrite, hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay: %d", rec.Code)
	}
	if string(fs.oi.clearCalls[0].Hash) != string(fs.oi.clearCalls[1].Hash) {
		t.Fatal("the hash must be stable")
	}
	// The same selection in another order has the same hash.
	swapped := strings.Replace(clearBody, `"`+oiItemB+`","`+oiItemA+`"`, `"`+oiItemA+`","`+oiItemB+`"`, 1)
	call(h, http.MethodPost, "/v1/ledger/clearings", swapped, dutyClearingWrite, hdr)
	if string(fs.oi.clearCalls[0].Hash) != string(fs.oi.clearCalls[2].Hash) {
		t.Fatal("item order must not change the hash")
	}
}

func TestClearing_ValidationAndDuties(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	hdr := map[string]string{"Idempotency-Key": "k1"}
	if rec := call(h, http.MethodPost, "/v1/ledger/clearings", clearBody, dutyClearingWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d", rec.Code)
	}
	one := `{"ledgerId":"` + testLedgerID + `","itemIds":["` + oiItemA + `"],"clearingDate":"2026-03-31"}`
	if rec := call(h, http.MethodPost, "/v1/ledger/clearings", one, dutyClearingWrite, hdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("one item: %d", rec.Code)
	}
	dup := `{"ledgerId":"` + testLedgerID + `","itemIds":["` + oiItemA + `","` + oiItemA + `"],"clearingDate":"2026-03-31"}`
	if rec := call(h, http.MethodPost, "/v1/ledger/clearings", dup, dutyClearingWrite, hdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate items: %d", rec.Code)
	}
	bad := strings.Replace(clearBody, "2026-03-31", "31/03/2026", 1)
	if rec := call(h, http.MethodPost, "/v1/ledger/clearings", bad, dutyClearingWrite, hdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad date: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/ledger/clearings", clearBody, dutyRead, hdr); rec.Code != http.StatusForbidden {
		t.Fatalf("a reader must not clear: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/ledger/clearings:simulate", clearBody, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a reader must not simulate: %d", rec.Code)
	}
	if len(fs.oi.clearCalls) != 0 {
		t.Fatal("nothing may reach the store")
	}
	rec := call(h, http.MethodPost, "/v1/ledger/clearings:simulate", clearBody, dutyClearingWrite, nil)
	if rec.Code != http.StatusOK || len(fs.oi.simCalls) != 1 {
		t.Fatalf("simulate needs no key: %d %s", rec.Code, rec.Body.String())
	}
}

func TestClearing_RuleViolationIs422WithCode(t *testing.T) {
	fs := &fakeStore{err: &domain.RejectedError{Violations: []domain.Violation{{Code: store.CodeClearingUnbalanced, Field: "itemIds", Message: "residual 0.02"}}}}
	rec := call(server(fs), http.MethodPost, "/v1/ledger/clearings", clearBody, dutyClearingWrite, map[string]string{"Idempotency-Key": "k"})
	body := decode(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || body["code"] != store.CodeClearingUnbalanced {
		t.Fatalf("%d %v", rec.Code, body)
	}
}

func TestResetClearing_NeedsKeyAndReason(t *testing.T) {
	fs := &fakeStore{}
	fs.oi.doc = store.ClearingDocument{ID: oiDoc, Status: "reset"}
	h := server(fs)
	path := "/v1/ledger/clearings/" + oiDoc + "/reset"
	if rec := call(h, http.MethodPost, path, `{}`, dutyClearingReset, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, `{"reasonCode":"bad code"}`, dutyClearingReset, map[string]string{"Idempotency-Key": "r1"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad reason: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, `{"comment":"no reason"}`, dutyClearingReset, map[string]string{"Idempotency-Key": "r1"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a reset needs a reason code: %d", rec.Code)
	}
	if len(fs.oi.resets) != 0 {
		t.Fatal("an invalid reset must not reach the store")
	}
	if rec := call(h, http.MethodPost, path, `{"reasonCode":"CORR","comment":"wrong items"}`, dutyClearingWrite, map[string]string{"Idempotency-Key": "r1"}); rec.Code != http.StatusForbidden {
		t.Fatalf("the clear duty must not reset: %d", rec.Code)
	}
	rec := call(h, http.MethodPost, path, `{"reasonCode":"CORR","comment":"wrong items"}`, dutyClearingReset, map[string]string{"Idempotency-Key": "r1"})
	if rec.Code != http.StatusOK || len(fs.oi.resets) != 1 || fs.oi.resets[0].ReasonCode != "CORR" || fs.oi.resets[0].Key != "r1" || fs.oi.resets[0].ID != oiDoc {
		t.Fatalf("reset: %d %s %+v", rec.Code, rec.Body.String(), fs.oi.resets)
	}
	if rec := call(h, http.MethodPost, "/v1/ledger/clearings/not-a-uuid/reset", `{}`, dutyClearingReset, map[string]string{"Idempotency-Key": "r1"}); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestListOpenItems_FiltersAndPaging(t *testing.T) {
	fs := &fakeStore{}
	fs.oi.page = store.OpenItemPage{Items: []store.OpenItem{{ID: oiItemA, Amount: "100.00", DebitCredit: "D"}}, NextCursor: "42"}
	h := server(fs)
	rec := call(h, http.MethodGet, "/v1/ledger/open-items?ledger="+testLedgerID+"&account=1999&status=open&assignment=PO-7&dim.DEPT=D100&limit=50&cursor=10", "", dutyRead, nil)
	body := decode(t, rec)
	if rec.Code != http.StatusOK || body["nextCursor"] != "42" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	f := fs.oi.filters[0]
	if f.Account != "1999" || f.Status != "open" || f.Assignment != "PO-7" || f.Dimensions["DEPT"] != "D100" || f.Limit != 50 || f.Cursor != "10" {
		t.Fatalf("filter not mapped: %+v", f)
	}
	for _, q := range []string{"", "?ledger=" + testLedgerID + "&status=bogus", "?ledger=" + testLedgerID + "&cursor=x", "?ledger=" + testLedgerID + "&dim.bad=1", "?ledger=" + testLedgerID + "&limit=0"} {
		if rec := call(h, http.MethodGet, "/v1/ledger/open-items"+q, "", dutyRead, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: %d", q, rec.Code)
		}
	}
}

func TestPutToleranceGroup_CreateReplaceAndValidation(t *testing.T) {
	fs := &fakeStore{}
	fs.oi.groupNew = true
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedgerID + "/tolerance-groups/STD"
	body := `{"name":"Standard","debitLimit":"0.05","creditLimit":"0.01","percentLimit":"0.5","differenceAccount":"6990"}`
	rec := call(h, http.MethodPut, path, body, dutyClearingRules, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("create: %d etag %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	g := fs.oi.putGroups[0]
	if g.DebitLimit != "0.05" || g.CreditLimit != "0.01" || g.PercentLimit != "0.5" || g.DifferenceAccount != "6990" || !g.Active {
		t.Fatalf("not mapped: %+v", g)
	}
	fs.oi.groupNew = false
	rec = call(h, http.MethodPut, path, body, dutyClearingRules, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || fs.oi.putVers[1] != 2 {
		t.Fatalf("replace: %d %v", rec.Code, fs.oi.putVers)
	}
	for _, bad := range []string{
		`{"name":"","debitLimit":"0.05","creditLimit":"0.05","differenceAccount":"6990"}`,
		`{"name":"X","debitLimit":"-1","creditLimit":"0.05","differenceAccount":"6990"}`,
		`{"name":"X","debitLimit":"0.05","creditLimit":"0.0000001","differenceAccount":"6990"}`,
		`{"name":"X","debitLimit":"0.05","differenceAccount":"6990"}`,
		`{"name":"X","debitLimit":"1","creditLimit":"1","percentLimit":"101","differenceAccount":"6990"}`,
		`{"name":"X","debitLimit":"1","creditLimit":"1","differenceAccount":"bad account"}`,
	} {
		if rec := call(h, http.MethodPut, path, bad, dutyClearingRules, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := call(h, http.MethodPut, path, body, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("needs the rules duty: %d", rec.Code)
	}
}

func TestPutOpenItemAccount_RequiresEnabledFlag(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedgerID + "/open-item-accounts/1999"
	if rec := call(h, http.MethodPut, path, `{}`, dutyAccountWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing flag: %d", rec.Code)
	}
	rec := call(h, http.MethodPut, path, `{"enabled":true}`, dutyAccountWrite, nil)
	if rec.Code != http.StatusCreated || len(fs.oi.putAccount) != 1 || !fs.oi.putAccount[0] {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, http.MethodPut, path, `{"enabled":true}`, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("needs the account duty: %d", rec.Code)
	}
}

func TestActivateOpenItemAccount_KeyDateDutyAndReplay(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedgerID + "/open-item-accounts/2999:activate"
	hdr := map[string]string{"Idempotency-Key": "act-1"}
	body := `{"activationDate":"2026-04-01"}`
	if rec := call(h, http.MethodPost, path, body, dutyRebuild, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, `{"activationDate":"1/4/2026"}`, dutyRebuild, hdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad date: %d", rec.Code)
	}
	// The PRD gives the run to "Administer ledger" (ledger.balance.rebuild here), not to the
	// chart maintainer who may switch an account without history.
	for _, duty := range []string{dutyRead, dutyAccountWrite} {
		if rec := call(h, http.MethodPost, path, body, duty, hdr); rec.Code != http.StatusForbidden {
			t.Fatalf("%s must not activate: %d", duty, rec.Code)
		}
	}
	if len(fs.oi.activate) != 0 {
		t.Fatal("nothing may reach the store")
	}
	rec := call(h, http.MethodPost, path, body, dutyRebuild, hdr)
	if rec.Code != http.StatusCreated || len(fs.oi.activate) != 1 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	in := fs.oi.activate[0]
	if in.Account != "2999" || in.Key != "act-1" || in.Date.Format("2006-01-02") != "2026-04-01" || len(in.Hash) != 32 {
		t.Fatalf("request not mapped: %+v", in)
	}
	fs.oi.activated = true
	if rec := call(h, http.MethodPost, path, body, dutyRebuild, hdr); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d", rec.Code)
	}
	if string(fs.oi.activate[0].Hash) != string(fs.oi.activate[1].Hash) {
		t.Fatal("the hash must be stable")
	}
	list := call(h, http.MethodGet, "/v1/ledger/ledgers/"+testLedgerID+"/open-item-activations", "", dutyRead, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d", list.Code)
	}
}
