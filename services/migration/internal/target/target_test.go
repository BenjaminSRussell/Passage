package target_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target/targettest"
)

const system = "custom_acme"

var (
	le1 = testkit.TenantID(11)
	le2 = testkit.TenantID(12)
)

type harness struct {
	f      *targettest.Fakes
	xref   *domain.MemXref
	sleeps []time.Duration
	mu     sync.Mutex
}

func newHarness(t *testing.T, legalEntity string) (*harness, *target.Loader) {
	t.Helper()
	h := &harness{f: targettest.Start(t), xref: domain.NewMemXref()}
	cfg := target.Config{
		PartyURL: h.f.PartyURL, ProductURL: h.f.ProductURL, ReceivablesURL: h.f.ReceivablesURL, EngineeringURL: h.f.EngineeringURL, Signer: h.f.Signer,
		Concurrency: 1,
		Sleep: func(_ context.Context, d time.Duration) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.sleeps = append(h.sleeps, d)
			return nil
		},
	}
	return h, target.New(cfg, target.Run{SourceSystem: system, LegalEntityID: legalEntity, Xref: h.xref})
}

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "admin", Duties: map[string]struct{}{tenant.DutyTenantAdmin: {}}})
}

func item(key string, existing bool, version int, fields map[string]any) domain.LoadItem {
	return domain.LoadItem{TargetID: ids.New(), SourceKey: key, IdempotencyKey: "idem-" + key, Fields: fields, Existing: existing, TargetVersion: version}
}

func load(t *testing.T, l *target.Loader, entity string, items ...domain.LoadItem) []domain.LoadResult {
	t.Helper()
	res, err := l.LoadBatch(ctx(), entity, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(items) {
		t.Fatalf("%d results for %d items", len(res), len(items))
	}
	return res
}

func TestParty_CreatesUnderTheReservedIDThenPatchesUnderItsVersion(t *testing.T) {
	h, l := newHarness(t, le1)
	it := item("P1", false, 0, map[string]any{"legalName": "Acme GmbH", "name": "ignored", "modifiedAt": "2020-01-01T00:00:00Z"})
	res := load(t, l, "party", it)
	if res[0].Code != "" || res[0].TargetID != it.TargetID || res[0].TargetVersion != 1 {
		t.Fatalf("create = %+v", res[0])
	}
	call := h.f.Calls("party")[0]
	if call.Method != "POST" || call.Body["partyType"] != "organization" || call.Body["id"] != it.TargetID || call.Body["legalName"] != "Acme GmbH" {
		t.Fatalf("create sent %+v", call.Body)
	}
	if _, leaked := call.Body["name"]; leaked {
		t.Fatalf("a field outside the party contract was sent: %+v", call.Body)
	}
	if call.Header.Get("Idempotency-Key") != "idem-P1" {
		t.Fatalf("Idempotency-Key = %q", call.Header.Get("Idempotency-Key"))
	}
	c := call.Claims
	if c.Iss != "migration" || c.Aud != "party" || c.Act != "service:migration-loader" || c.Le != le1 || c.Tid != testkit.TenantID(1) || strings.Join(c.Duties, ",") != "party.import.write,party.import.read" {
		t.Fatalf("token claims = %+v", c)
	}

	upd := it
	upd.Existing, upd.TargetVersion = true, 1
	upd.Fields = map[string]any{"legalName": "Acme Holding GmbH"}
	res = load(t, l, "party", upd)
	if res[0].Code != "" || res[0].TargetVersion != 2 {
		t.Fatalf("update = %+v", res[0])
	}
	patch := h.f.Calls("party")[1]
	if patch.Method != "PATCH" || patch.Header.Get("If-Match") != `"1"` || h.f.Parties[it.TargetID].Body["legalName"] != "Acme Holding GmbH" {
		t.Fatalf("patch = %+v, party = %+v", patch, h.f.Parties[it.TargetID])
	}

	stale := upd
	stale.TargetVersion = 1
	// BR-10.8: a party changed in Passage since the load is drift, reported and not overwritten.
	if res = load(t, l, "party", stale); res[0].Code != target.CodeDrift || !strings.Contains(res[0].Detail, "PTY-VERSION-CONFLICT") {
		t.Fatalf("stale version = %+v", res[0])
	}
	noVersion := upd
	noVersion.TargetVersion = 0
	if res = load(t, l, "party", noVersion); res[0].Code != target.CodeVersionUnknown {
		t.Fatalf("unknown version = %+v", res[0])
	}
}

func TestParty_RefusalIsARejectWithTheTargetCodeAndNoRetry(t *testing.T) {
	h, l := newHarness(t, le1)
	res := load(t, l, "party", item("P1", false, 0, map[string]any{"tradeName": "no type, no name"}))
	if res[0].Code != target.CodeFieldRequired {
		t.Fatalf("untyped party = %+v", res[0])
	}
	if n := len(h.f.Calls("party")); n != 0 {
		t.Fatalf("a row the loader can refuse itself reached the target: %d calls", n)
	}
	res = load(t, l, "party", item("P2", false, 0, map[string]any{"partyType": "organization", "givenName": "x"}))
	if res[0].Code != "" {
		t.Fatalf("typed party = %+v", res[0])
	}
	taken := item("P3", false, 0, map[string]any{"legalName": "Dup"})
	taken.TargetID = h.f.Calls("party")[0].Body["id"].(string)
	res = load(t, l, "party", taken)
	if res[0].Code != "PTY-PARTY-ID-TAKEN" || !strings.Contains(res[0].Detail, "taken") {
		t.Fatalf("taken id = %+v", res[0])
	}
	if n := len(h.f.Calls("party")); n != 2 || len(h.sleeps) != 0 {
		t.Fatalf("a 4xx must not be retried: %d calls, sleeps %v", n, h.sleeps)
	}
}

func TestRetry_503IsRetriedWithBackoffThenSucceeds(t *testing.T) {
	h, l := newHarness(t, le1)
	h.f.Fail503["POST /v1/party/parties"] = 2
	res := load(t, l, "party", item("P1", false, 0, map[string]any{"legalName": "Acme"}))
	if res[0].Code != "" || res[0].TargetVersion != 1 {
		t.Fatalf("result = %+v", res[0])
	}
	calls := h.f.Calls("party")
	if len(calls) != 3 || calls[0].Header.Get("Idempotency-Key") != calls[2].Header.Get("Idempotency-Key") {
		t.Fatalf("calls = %d, want 3 with one Idempotency-Key", len(calls))
	}
	if len(h.sleeps) != 2 || h.sleeps[0] != time.Second || h.sleeps[1] != 2*time.Second {
		t.Fatalf("backoff = %v", h.sleeps)
	}
	if len(h.f.Parties) != 1 {
		t.Fatalf("parties = %d", len(h.f.Parties))
	}
}

func TestRetry_ExhaustedAttemptsRejectAndTripTheBreaker(t *testing.T) {
	h, l := newHarness(t, le1)
	h.f.Always503["party"] = true
	var items []domain.LoadItem
	for _, k := range []string{"A", "B", "C", "D", "E"} {
		items = append(items, item(k, false, 0, map[string]any{"legalName": k}))
	}
	res := load(t, l, "party", items...)
	for i, r := range res {
		if r.Code != target.CodeUnavailable {
			t.Fatalf("row %d = %+v", i, r)
		}
	}
	// Three rows used all five attempts; the other two were not tried.
	if n := len(h.f.Calls("party")); n != 15 {
		t.Fatalf("calls = %d, want 15", n)
	}
	if !strings.Contains(res[0].Detail, "503") || !strings.Contains(res[0].Detail, "5 attempts") {
		t.Fatalf("detail = %q", res[0].Detail)
	}
}

func TestCustomer_NeedsItsPartyLoadedFirstAndReportsTheRoleIDBack(t *testing.T) {
	h, l := newHarness(t, le1)
	cust := map[string]any{
		"partyKey": "P1", "accountNumber": "C-1", "customerGroupId": ids.New(), "paymentTermId": ids.New(), "postingProfileId": ids.New(),
		"currencyCode": "USD", "creditLimit": "1500.00", "creditHold": false, "blocked": "none",
	}
	if res := load(t, l, "customer", item("C1", false, 0, cust)); res[0].Code != target.CodeRefUnresolved || !strings.Contains(res[0].Detail, "party") {
		t.Fatalf("party not loaded = %+v", res[0])
	}
	party := item("P1", false, 0, map[string]any{"legalName": "Acme"})
	load(t, l, "party", party)
	_ = h.xref.Put(ctx(), domain.XrefKey{SourceSystem: system, Entity: "party", SourceKey: "P1"}, domain.XrefEntry{TargetID: party.TargetID, ContentHash: "h", TargetVersion: 1})

	res := load(t, l, "customer", item("C1", false, 0, cust))
	if res[0].Code != "" || res[0].TargetID == "" || res[0].TargetID == "C1" {
		t.Fatalf("customer = %+v", res[0])
	}
	if _, ok := h.f.Customers[res[0].TargetID]; !ok {
		t.Fatalf("the reported id %s is not the role the party service created", res[0].TargetID)
	}
	var sent targettest.Call
	for _, c := range h.f.Calls("party") {
		if strings.HasSuffix(c.Path, "/customer") {
			sent = c
		}
	}
	if sent.Path != "/v1/party/parties/"+party.TargetID+"/customer" || sent.Body["creditLimit"] != "1500.00" || sent.Body["creditHold"] != false || sent.Claims.Le != le1 {
		t.Fatalf("customer call = %+v", sent)
	}

	dup := load(t, l, "customer", item("C2", false, 0, cust))
	if dup[0].Code != "PTY-CUSTOMER-EXISTS" {
		t.Fatalf("duplicate account = %+v", dup[0])
	}
	missing := map[string]any{"partyKey": "P1", "accountNumber": "C-9"}
	if r := load(t, l, "customer", item("C3", false, 0, missing)); r[0].Code != target.CodeFieldRequired || !strings.Contains(r[0].Detail, "paymentTermId") {
		t.Fatalf("missing fields = %+v", r[0])
	}
	if r := load(t, l, "customer", item("C1", true, 1, cust)); r[0].Code != target.CodeUpdateUnavailable {
		t.Fatalf("update of a role = %+v", r[0])
	}
}

func TestVendor_CreatesTheRoleInTheRowsLegalEntity(t *testing.T) {
	// A run outside a project: each row names its legal entity.
	h, l := newHarness(t, "")
	party := item("P1", false, 0, map[string]any{"legalName": "Supplier", "legalEntityId": le1})
	load(t, l, "party", party)
	v := map[string]any{"partyId": party.TargetID, "accountNumber": "V-1", "vendorGroupId": ids.New(), "currencyCode": "EUR", "legalEntityId": strings.ToUpper(le2)}
	res := load(t, l, "vendor", item("V1", false, 0, v))
	if res[0].Code != "" {
		t.Fatalf("vendor = %+v", res[0])
	}
	var got targettest.Call
	for _, c := range h.f.Calls("party") {
		if strings.HasSuffix(c.Path, "/vendor") {
			got = c
		}
	}
	if got.Claims.Le != le2 {
		t.Fatalf("row legal entity not used: %+v", got.Claims)
	}
}

func TestProjectRunLoadsOnlyIntoTheProjectsLegalEntity(t *testing.T) {
	h, l := newHarness(t, le1)
	res := load(t, l, "party",
		item("P1", false, 0, map[string]any{"legalName": "Own", "legalEntityId": strings.ToUpper(le1)}),
		item("P2", false, 0, map[string]any{"legalName": "Elsewhere", "legalEntityId": le2}))
	if res[0].Code != "" || res[1].Code != target.CodeLegalEntity || !strings.Contains(res[1].Detail, "project") {
		t.Fatalf("results = %+v", res)
	}
	calls := h.f.Calls("party")
	if len(calls) != 1 || calls[0].Claims.Le != le1 {
		t.Fatalf("only the row in the project's legal entity may be sent, under it: %+v", calls)
	}
}

func TestProduct_CreatesADraftThenActivatesItSoRequiredAttributesApply(t *testing.T) {
	h, l := newHarness(t, le1)
	h.f.RequireAttribute = "colour"
	base := map[string]any{"productNumber": "WIDGET-1", "name": "Widget", "baseUom": "ea", "description": "d", "lifecycleState": "active"}

	first := item("I1", false, 0, base)
	res := load(t, l, "product", first)
	if res[0].Code != "PRD-ATTR-REQUIRED" || !strings.Contains(res[0].Detail, "in Passage as draft") {
		t.Fatalf("missing attribute = %+v", res[0])
	}
	var created *targettest.Product
	for _, p := range h.f.Products {
		created = p
	}
	if created == nil || created.State != "draft" {
		t.Fatalf("the refused activation must leave a draft: %+v", created)
	}
	if c := h.f.Calls("product")[0]; c.Body["lifecycleState"] != "draft" {
		t.Fatalf("created as %v, want draft", c.Body["lifecycleState"])
	}

	// The legacy row is fixed (attribute added, name changed): its content, and so the pipeline's key, changes, but
	// the reserved id does not. The rerun picks the draft up, applies the edit and activates it; no second product.
	fixed := first
	fixed.IdempotencyKey = "idem-I1-fixed"
	fixed.Fields = map[string]any{"productNumber": "WIDGET-1", "name": "Widget one", "baseUom": "ea", "description": "d", "attr.colour": "green"}
	res = load(t, l, "product", fixed)
	if res[0].Code != "" || res[0].TargetID != first.TargetID || res[0].TargetVersion != 2 || len(h.f.Products) != 1 {
		t.Fatalf("fixed rerun = %+v, products %d", res[0], len(h.f.Products))
	}
	if p := h.f.Products[first.TargetID]; p.State != "active" || p.Body["name"] != "Widget one" || p.Attributes["colour"] != "green" {
		t.Fatalf("product after the fixed rerun = %+v", p)
	}

	withAttr := map[string]any{"productNumber": "WIDGET-2", "name": "Widget 2", "baseUom": "ea", "attr.colour": "red", "attr.grade": "A"}
	it := item("I2", false, 0, withAttr)
	res = load(t, l, "item", it)
	if res[0].Code != "" || res[0].TargetID != it.TargetID || res[0].TargetVersion != 2 {
		t.Fatalf("item = %+v", res[0])
	}
	p := h.f.Products[it.TargetID]
	if p.State != "active" || p.Attributes["colour"] != "red" || p.Attributes["grade"] != "A" {
		t.Fatalf("stored product = %+v", p)
	}

	upd := it
	upd.Existing, upd.TargetVersion = true, 2
	upd.Fields = map[string]any{"productNumber": "WIDGET-2", "name": "Widget 2b", "baseUom": "ea", "attr.colour": "blue"}
	res = load(t, l, "product", upd)
	if res[0].Code != "" || res[0].TargetVersion != 3 || p.Body["name"] != "Widget 2b" || p.Attributes["colour"] != "blue" {
		t.Fatalf("update = %+v, product %+v", res[0], p)
	}

	phase := item("I3", false, 0, map[string]any{"productNumber": "OLD-1", "name": "Old", "baseUom": "ea", "lifecycleState": "phase_out", "attr.colour": "x"})
	if res = load(t, l, "product", phase); res[0].Code != "" || h.f.Products[phase.TargetID].State != "phase_out" || res[0].TargetVersion != 3 {
		t.Fatalf("phase_out = %+v %+v", res[0], h.f.Products[phase.TargetID])
	}
	if res = load(t, l, "product", item("I4", false, 0, map[string]any{"productNumber": "X", "name": "x", "baseUom": "ea", "lifecycleState": "retired"})); res[0].Code != target.CodeFieldRequired {
		t.Fatalf("unknown state = %+v", res[0])
	}
}

func TestOpenItems_OneRequestPerLegalEntityWithPerRowResults(t *testing.T) {
	h, l := newHarness(t, "")
	cust := ids.New()
	_ = h.xref.Put(ctx(), domain.XrefKey{SourceSystem: system, Entity: "customer", SourceKey: "C1"}, domain.XrefEntry{TargetID: cust, ContentHash: "h"})
	row := func(doc string, extra map[string]any) map[string]any {
		m := map[string]any{"legalEntityId": le1, "customerKey": "C1", "documentNumber": doc, "kind": "INVOICE", "side": "DEBIT", "documentDate": "2026-01-15", "dueDate": "2026-02-15", "currency": "USD", "originalAmount": "100.00", "openAmount": "40.50"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	h.f.SettledKeys["AR-3"] = true
	items := []domain.LoadItem{
		item("AR-1", false, 0, row("INV-1", nil)),
		item("AR-2", false, 0, row("INV-2", map[string]any{"legalEntityId": le2})),
		item("AR-3", false, 0, row("INV-3", nil)),
		item("AR-4", false, 0, row("INV-4", map[string]any{"customerKey": "C-NONE"})),
		item("AR-5", false, 0, row("INV-5", map[string]any{"currency": ""})),
	}
	res := load(t, l, "ar_open_item", items...)
	if res[0].Code != "" || res[0].TargetID == "" || res[1].Code != "" {
		t.Fatalf("accepted rows = %+v %+v", res[0], res[1])
	}
	if res[2].Code != "AR-OPENING-SETTLED" || res[3].Code != target.CodeRefUnresolved || res[4].Code != target.CodeFieldRequired {
		t.Fatalf("refused rows = %+v", res[2:])
	}
	calls := h.f.Calls("receivables")
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want one per legal entity", len(calls))
	}
	first := calls[0]
	if first.Claims.Le != le1 || first.Body["importSource"] != system || first.Body["atomic"] != false || first.Claims.Aud != "receivables" || strings.Join(first.Claims.Duties, ",") != "receivables.import.write,receivables.import.read" {
		t.Fatalf("first call = %+v", first)
	}
	sent := first.Body["items"].([]any)
	if len(sent) != 2 || sent[0].(map[string]any)["customerId"] != cust || sent[0].(map[string]any)["legacyKey"] != "AR-1" || sent[0].(map[string]any)["openAmount"] != "40.50" {
		t.Fatalf("items sent = %+v", sent)
	}
	if calls[1].Claims.Le != le2 {
		t.Fatalf("second call legal entity = %s", calls[1].Claims.Le)
	}

	// The same snapshot again changes nothing and still succeeds (re-runnable).
	again := load(t, l, "ar_open_item", items[0], items[1])
	if again[0].Code != "" || again[1].Code != "" || len(h.f.OpenItems) != 2 {
		t.Fatalf("rerun = %+v, items %d", again, len(h.f.OpenItems))
	}
}

func TestOpenItems_ARefusedBatchAndAnOutageRejectEveryRow(t *testing.T) {
	h, l := newHarness(t, le1)
	cust := ids.New()
	row := map[string]any{"customerId": cust, "documentNumber": "D", "kind": "INVOICE", "side": "DEBIT", "documentDate": "2026-01-15", "dueDate": "2026-02-15", "currency": "USD", "originalAmount": "1", "openAmount": "1"}
	h.f.Fail503["POST /v1/receivables/open-items:batch"] = 1
	res := load(t, l, "ar_open_item", item("A", false, 0, row), item("B", false, 0, row))
	if len(h.sleeps) != 1 || res[0].Code != "" || res[1].Code != "" {
		t.Fatalf("one 503 must be retried: sleeps %v, %+v", h.sleeps, res)
	}
	h.f.Always503["receivables"] = true
	res = load(t, l, "ar_open_item", item("C", false, 0, row))
	if res[0].Code != target.CodeUnavailable {
		t.Fatalf("outage = %+v", res[0])
	}
}

func TestEntitiesWithoutATargetAPIAreRejectedNotDropped(t *testing.T) {
	h, l := newHarness(t, le1)
	for _, entity := range []string{"ap_open_item", "price_list", "anything"} {
		res := load(t, l, entity, item("K1", false, 0, map[string]any{"x": 1}), item("K2", false, 0, nil))
		for _, r := range res {
			if r.Code != target.CodeNotAvailable || !strings.Contains(r.Detail, entity) {
				t.Fatalf("%s = %+v", entity, r)
			}
		}
	}
	if n := len(h.f.Calls("")); n != 0 {
		t.Fatalf("%d calls for entities with no target", n)
	}
	// A service the deployment has no address for is the same answer.
	l2 := target.New(target.Config{Signer: h.f.Signer, ProductURL: h.f.ProductURL}, target.Run{SourceSystem: system, LegalEntityID: le1})
	if r := load(t, l2, "party", item("P", false, 0, map[string]any{"legalName": "x"})); r[0].Code != target.CodeNotAvailable {
		t.Fatalf("party without address = %+v", r[0])
	}
}

func TestLegalEntityIsRequiredAndValidated(t *testing.T) {
	h, l := newHarness(t, "")
	res := load(t, l, "party", item("P1", false, 0, map[string]any{"legalName": "x"}), item("P2", false, 0, map[string]any{"legalName": "y", "legalEntityId": "nope"}), item("P3", false, 0, map[string]any{"legalName": "z", "legalEntityId": le1}))
	if res[0].Code != target.CodeLegalEntity || res[1].Code != target.CodeLegalEntity || res[2].Code != "" {
		t.Fatalf("results = %+v", res)
	}
	if n := len(h.f.Calls("party")); n != 1 {
		t.Fatalf("calls = %d", n)
	}
}

func TestLoadBatchNeedsATenantAndASigner(t *testing.T) {
	h, l := newHarness(t, le1)
	if _, err := l.LoadBatch(context.Background(), "party", []domain.LoadItem{item("P", false, 0, map[string]any{"legalName": "x"})}); err == nil {
		t.Fatal("a batch without a tenant must not be attempted")
	}
	unsigned := target.New(target.Config{PartyURL: h.f.PartyURL}, target.Run{SourceSystem: system, LegalEntityID: le1})
	if _, err := unsigned.LoadBatch(ctx(), "party", []domain.LoadItem{item("P", false, 0, map[string]any{"legalName": "x"})}); err == nil {
		t.Fatal("a loader without a signing key must refuse, not send unsigned calls")
	}
	if (target.Config{}).Configured() || (target.Config{PartyURL: "http://x"}).Configured() || !(target.Config{PartyURL: "http://x", Signer: h.f.Signer}).Configured() {
		t.Fatal("Configured needs a signer and an address")
	}
}

func TestConcurrentRowsKeepTheirOwnResults(t *testing.T) {
	h, _ := newHarness(t, le1)
	l := target.New(target.Config{PartyURL: h.f.PartyURL, Signer: h.f.Signer, Concurrency: 8}, target.Run{SourceSystem: system, LegalEntityID: le1})
	var items []domain.LoadItem
	for i := 0; i < 60; i++ {
		fields := map[string]any{"legalName": "ok"}
		if i%7 == 0 {
			fields = map[string]any{"tradeName": "no type"}
		}
		items = append(items, item(strings.Repeat("k", i+1), false, 0, fields))
	}
	res := load(t, l, "party", items...)
	for i, r := range res {
		if want := i%7 == 0; (r.Code == target.CodeFieldRequired) != want || (!want && r.TargetID != items[i].TargetID) {
			t.Fatalf("row %d = %+v", i, r)
		}
	}
}

func TestUnknownTargetsNamesOnlyFieldsNoLoaderSends(t *testing.T) {
	for _, c := range []struct {
		entity  string
		targets []string
		want    string
	}{
		{"party", []string{"legalName", "legalEntityId", "status", "_modifiedAt"}, ""},
		{"party", []string{"legalName", "name", "segment"}, "name,segment"},
		{"customer", []string{"partyKey", "accountNumber", "creditHold", "invoiceAccountPartyKey", "creditLimt"}, "creditLimt"},
		{"vendor", []string{"partyId", "vendorGroupId", "postingProfileId"}, "postingProfileId"},
		{"item", []string{"productNumber", "attr.colour", "attr.", "lifecycleState"}, "attr."},
		{"product", []string{"customerKey"}, "customerKey"},
		{"ar_open_item", []string{"customerKey", "cashDiscountAmount", "attr.x"}, "attr.x"},
		{"bom", []string{"productKey", "lines", "effectiveFrom", "anything"}, "anything"},
		{"route", []string{"productId", "operations", "routeCode", "siteId", "versionNo"}, ""},
		{"price_list", []string{"anything"}, ""},
	} {
		if got := strings.Join(target.UnknownTargets(c.entity, c.targets), ","); got != c.want {
			t.Errorf("%s %v = %q, want %q", c.entity, c.targets, got, c.want)
		}
	}
}
