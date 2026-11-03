package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const (
	ruleID     = "77777777-0000-4000-8000-0000000000e1"
	senderOne  = "77777777-0000-4000-8000-0000000000a1"
	senderTwo  = "77777777-0000-4000-8000-0000000000a2"
	receiverID = "77777777-0000-4000-8000-0000000000b1"
	rulesPath  = "/v1/production/inheritance-rules"
)

func (f *fakeStore) CreateInheritanceRule(_ context.Context, in store.InheritanceRule, idem store.Idem) (store.InheritanceRule, bool, error) {
	f.gotRule, f.gotIdem = in, idem
	in.ID, in.Version = ruleID, 1
	return in, !f.replay, f.err
}

func (f *fakeStore) ListInheritanceRules(_ context.Context, filter store.RuleFilter) ([]store.InheritanceRule, error) {
	f.gotRuleFilt = filter
	return nil, f.err
}

func (f *fakeStore) GetInheritanceRule(context.Context, string) (store.InheritanceRule, error) {
	return store.InheritanceRule{ID: ruleID, Version: 1}, f.err
}

func (f *fakeStore) RetireInheritanceRule(_ context.Context, id string, version int, idem store.Idem) (store.InheritanceRule, bool, error) {
	f.gotVersion, f.gotIdem = version, idem
	return store.InheritanceRule{ID: id, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) DeriveBatch(_ context.Context, in store.DeriveInput, idem store.Idem) (store.DeriveResult, bool, error) {
	f.gotDerive, f.gotIdem = in, idem
	return store.DeriveResult{Derived: []store.DerivedValue{}, Warnings: []string{}}, !f.replay, f.err
}

func (f *fakeStore) ListDerivations(_ context.Context, filter store.DerivationFilter) ([]store.Derivation, error) {
	f.gotDerivFilt = filter
	return nil, f.err
}

func TestCreateRule_PassesRuleAndKey(t *testing.T) {
	f := &fakeStore{}
	body := `{"itemId":"` + pickItem + `","attribute":"potency","aggregation":"weighted-average","decimals":2}`
	rec := call{method: "POST", path: rulesPath, body: body, duties: DutyInheritanceRuleWrite, headers: key("i-1")}.do(newRouter(f))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := f.gotRule
	if g.ItemID != pickItem || g.Attribute != "potency" || g.Aggregation != "weighted-average" || g.Decimals == nil || *g.Decimals != 2 || g.LegalEntityID == "" || f.gotIdem.Key != "i-1" {
		t.Fatalf("stored %+v idem %+v", g, f.gotIdem)
	}
	fixed := `{"itemId":"` + pickItem + `","attribute":"grade","aggregation":"fixed","fixed":{"text":"A"}}`
	if rec := (call{method: "POST", path: rulesPath, body: fixed, duties: DutyInheritanceRuleWrite, headers: key("i-2")}).do(newRouter(f)); rec.Code != http.StatusCreated || f.gotRule.FixedText == nil {
		t.Fatalf("fixed %d %s %+v", rec.Code, rec.Body, f.gotRule)
	}
	date := `{"itemId":"` + pickItem + `","attribute":"expiry","aggregation":"fixed","fixed":{"date":"2027-05-01"}}`
	if rec := (call{method: "POST", path: rulesPath, body: date, duties: DutyInheritanceRuleWrite, headers: key("i-3")}).do(newRouter(f)); rec.Code != http.StatusCreated || f.gotRule.FixedDate == nil || *f.gotRule.FixedDate != "2027-05-01" {
		t.Fatalf("fixed date %d %s %+v", rec.Code, rec.Body, f.gotRule)
	}
	f.replay = true
	rec = call{method: "POST", path: rulesPath, body: body, duties: DutyInheritanceRuleWrite, headers: key("i-1")}.do(newRouter(f))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %d %v", rec.Code, rec.Header())
	}
}

func TestCreateRule_Rejects(t *testing.T) {
	base := `"itemId":"` + pickItem + `","attribute":"potency"`
	tests := map[string]string{
		"bad item":       `{"itemId":"x","attribute":"potency","aggregation":"min"}`,
		"no attribute":   `{"itemId":"` + pickItem + `","aggregation":"min"}`,
		"long attribute": `{"itemId":"` + pickItem + `","attribute":"` + strings.Repeat("x", 41) + `","aggregation":"min"}`,
		"no aggregation": `{` + base + `}`,
		"unknown agg":    `{` + base + `,"aggregation":"median"}`,
		"decimals high":  `{` + base + `,"aggregation":"weighted-average","decimals":7}`,
		"decimals low":   `{` + base + `,"aggregation":"weighted-average","decimals":-1}`,
		"two fixed":      `{` + base + `,"aggregation":"fixed","fixed":{"num":"1","text":"a"}}`,
		"unknown field":  `{` + base + `,"aggregation":"min","extra":1}`,
	}
	for name, body := range tests {
		f := &fakeStore{}
		rec := call{method: "POST", path: rulesPath, body: body, duties: DutyInheritanceRuleWrite, headers: key("k")}.do(newRouter(f))
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" || f.gotRule.ItemID != "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestCreateRule_DutyKeyAndScope(t *testing.T) {
	body := `{"itemId":"` + pickItem + `","attribute":"potency","aggregation":"min","legalEntityId":"` + entityID + `"}`
	f := &fakeStore{}
	if rec := post(rulesPath, body, DutyInheritanceRuleWrite).do(newRouter(f)); rec.Code != http.StatusBadRequest {
		t.Errorf("missing key: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: rulesPath, body: body, duties: DutyRead, headers: key("k")}).do(newRouter(f)); rec.Code != http.StatusForbidden {
		t.Errorf("read duty wrote: %d", rec.Code)
	}
	h := key("k")
	h["X-Passage-Legal-Entity"] = ""
	rec := call{method: "POST", path: rulesPath, body: body, duties: DutyInheritanceRuleWrite, headers: h}.do(newRouter(f))
	if rec.Code != http.StatusForbidden || f.gotRule.ItemID != "" {
		t.Errorf("no scope: %d %s", rec.Code, rec.Body)
	}
}

func TestRules_ListGetRetire(t *testing.T) {
	f := &fakeStore{}
	rec := call{method: "GET", path: rulesPath + "?itemId=" + strings.ToUpper(pickItem) + "&active=true&limit=5", duties: DutyRead}.do(newRouter(f))
	if rec.Code != http.StatusOK || f.gotRuleFilt.ItemID != pickItem || !f.gotRuleFilt.Active || f.gotRuleFilt.Limit != 5 {
		t.Fatalf("list %d %s filter %+v", rec.Code, rec.Body, f.gotRuleFilt)
	}
	for _, q := range []string{"?itemId=x", "?legalEntityId=x", "?active=maybe", "?limit=0"} {
		if rec := (call{method: "GET", path: rulesPath + q, duties: DutyRead}).do(newRouter(f)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := (call{method: "GET", path: rulesPath + "/" + ruleID, duties: DutyRead}).do(newRouter(f)); rec.Code != http.StatusOK {
		t.Errorf("get %d", rec.Code)
	}
	if rec := (call{method: "GET", path: rulesPath + "/nope", duties: DutyRead}).do(newRouter(f)); rec.Code != http.StatusBadRequest {
		t.Errorf("get bad id %d", rec.Code)
	}
	rec = call{method: "POST", path: rulesPath + "/" + ruleID + ":retire", body: `{"version":3}`, duties: DutyInheritanceRuleWrite, headers: key("r")}.do(newRouter(f))
	if rec.Code != http.StatusOK || f.gotVersion != 3 {
		t.Errorf("retire %d %s version %d", rec.Code, rec.Body, f.gotVersion)
	}
	rec = call{method: "POST", path: rulesPath + "/" + ruleID + ":retire", body: `{"version":0}`, duties: DutyInheritanceRuleWrite, headers: key("r")}.do(newRouter(f))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("retire version 0: %d", rec.Code)
	}
}

func TestDeriveStored_PassesSendersAndKey(t *testing.T) {
	f := &fakeStore{}
	path := "/v1/production/batches/" + receiverID + ":derive"
	body := `{"version":2,"senders":[{"batchId":"` + strings.ToUpper(senderOne) + `","qty":"60"},{"batchId":"` + senderTwo + `","qty":"40.5"}]}`
	rec := call{method: "POST", path: path, body: body, duties: DutyBatchAttributeWrite, headers: key("d-1")}.do(newRouter(f))
	g := f.gotDerive
	if rec.Code != http.StatusOK || g.BatchID != receiverID || g.Version != 2 || len(g.Senders) != 2 || g.Senders[0].BatchID != senderOne || g.Senders[1].Qty.String() != "40.5" || f.gotIdem.Key != "d-1" {
		t.Fatalf("derive %d %s got %+v", rec.Code, rec.Body, g)
	}
	f.replay = true
	rec = call{method: "POST", path: path, body: body, duties: DutyBatchAttributeWrite, headers: key("d-1")}.do(newRouter(f))
	if rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("no replay header: %v", rec.Header())
	}
	if rec := (call{method: "POST", path: path, body: body, duties: DutyRead, headers: key("d-1")}).do(newRouter(f)); rec.Code != http.StatusForbidden {
		t.Errorf("read duty derived: %d", rec.Code)
	}
}

func TestDeriveStored_Rejects(t *testing.T) {
	path := "/v1/production/batches/" + receiverID + ":derive"
	tests := map[string]string{
		"no version":    `{"senders":[{"batchId":"` + senderOne + `","qty":"1"}]}`,
		"no senders":    `{"version":1,"senders":[]}`,
		"bad sender":    `{"version":1,"senders":[{"batchId":"x","qty":"1"}]}`,
		"duplicate":     `{"version":1,"senders":[{"batchId":"` + senderOne + `","qty":"1"},{"batchId":"` + strings.ToUpper(senderOne) + `","qty":"1"}]}`,
		"negative qty":  `{"version":1,"senders":[{"batchId":"` + senderOne + `","qty":"-1"}]}`,
		"too many dec.": `{"version":1,"senders":[{"batchId":"` + senderOne + `","qty":"1.0000001"}]}`,
	}
	for name, body := range tests {
		f := &fakeStore{}
		rec := call{method: "POST", path: path, body: body, duties: DutyBatchAttributeWrite, headers: key("k")}.do(newRouter(f))
		if rec.Code != http.StatusBadRequest || f.gotDerive.BatchID != "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestListDerivations_Filters(t *testing.T) {
	f := &fakeStore{}
	rec := call{method: "GET", path: derivationsPath + "?batchId=" + strings.ToUpper(receiverID) + "&itemId=" + pickItem + "&withMissing=true&limit=7", duties: DutyRead}.do(newRouter(f))
	g := f.gotDerivFilt
	if rec.Code != http.StatusOK || g.BatchID != receiverID || g.ItemID != pickItem || !g.WithMissing || g.Limit != 7 {
		t.Fatalf("list %d %s filter %+v", rec.Code, rec.Body, g)
	}
	for _, q := range []string{"?batchId=x", "?itemId=x", "?withMissing=yes", "?limit=999"} {
		if rec := (call{method: "GET", path: derivationsPath + q, duties: DutyRead}).do(newRouter(f)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}
