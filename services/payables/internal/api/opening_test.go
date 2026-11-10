package api_test

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	openingDuties = "opening-item.import,opening-item.import.read,open-item.read,payment-proposal.write"
	openingPath   = "/v1/payables/opening-batches"
	unknownVendor = "00000000-0000-0000-0000-0000000000c9"
)

func openingItem(ref, vendor, amount string) map[string]any {
	n := json.Number(amount)
	return map[string]any{"reference": ref, "vendorId": vendor, "invoiceNumber": "OLD-" + ref, "invoiceDate": "2025-11-01", "dueDate": "2026-01-05", "currency": "USD", "amountMinor": n}
}

func openingBody(key, total string, items ...map[string]any) string {
	raw, _ := json.Marshal(map[string]any{"legalEntityId": entityA, "batchKey": key, "controlTotalsMinor": map[string]any{"USD": json.Number(total)}, "items": items})
	return string(raw)
}

type openingFixture struct {
	*runFixture
	loader caller
}

func newOpeningFixture(t *testing.T, opts ...fixtureOptions) openingFixture {
	o := fixtureOptions{}
	if len(opts) > 0 {
		o = opts[0]
	}
	o.skipProposal, o.noSeedItems = true, true
	f := newRunFixture(t, o)
	f.api.WithOpeningItems(f.pg)
	router := httpx.NewRouter()
	f.api.Register(router)
	f.router = router
	return openingFixture{runFixture: f, loader: caller{duties: openingDuties, user: "factory-operator", entity: entityA}}
}

func TestOpeningBatch_ImportReplayAndReadBack(t *testing.T) {
	f := newOpeningFixture(t)
	body := openingBody("cutover-1", "15000", openingItem("A-1", vendorOne, "10000"), openingItem("A-2", vendorTwo, "5000"))

	created := f.loader.send(f.router, http.MethodPost, openingPath, body)
	if got := decode(t, created); created.Code != http.StatusCreated || got["itemCount"] != float64(2) || got["totalsMinor"].(map[string]any)["USD"] != float64(15000) {
		t.Fatalf("import: %d %s", created.Code, created.Body)
	}
	replay := f.loader.send(f.router, http.MethodPost, openingPath, body)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || decode(t, replay)["id"] != decode(t, created)["id"] {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body)
	}
	listed := f.loader.send(f.router, http.MethodGet, openingPath+"?legal_entity="+entityA, "")
	items := decode(t, listed)["items"].([]any)
	if listed.Code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["itemCount"] != float64(2) {
		t.Fatalf("list: %d %s", listed.Code, listed.Body)
	}

	open := f.loader.send(f.router, http.MethodGet, "/v1/payables/open-items", "")
	if open.Code != http.StatusOK || strings.Count(open.Body.String(), `"status":"OPEN"`) != 2 || !strings.Contains(open.Body.String(), `"amount":"100"`) {
		t.Fatalf("open items: %d %s", open.Code, open.Body)
	}
	proposal := f.loader.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-1", nil))
	if proposal.Code != http.StatusCreated || !strings.Contains(proposal.Body.String(), `"openItemId"`) {
		t.Fatalf("proposal over opening items: %d %s", proposal.Code, proposal.Body)
	}
	if len(f.ledger.bodies) != 0 || len(f.ledger.lookups) != 0 {
		t.Fatalf("import must not call the ledger: %d posts, %d lookups", len(f.ledger.bodies), len(f.ledger.lookups))
	}
}

func TestOpeningBatch_SameKeyDifferentContentIsRefused(t *testing.T) {
	f := newOpeningFixture(t)
	f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "10000", openingItem("A-1", vendorOne, "10000")))
	moved := openingItem("A-1", vendorOne, "10000")
	moved["dueDate"] = "2026-02-05"
	rec := f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "10000", moved))
	wantProblem(t, rec, http.StatusConflict, "AP-OPB-002")
}

func TestOpeningBatch_ControlTotalMismatchRefusesWholeBatch(t *testing.T) {
	f := newOpeningFixture(t)
	rec := f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "9999", openingItem("A-1", vendorOne, "10000")))
	body := wantProblem(t, rec, http.StatusUnprocessableEntity, "AP-OPB-003")
	if !strings.Contains(body["errors"].(map[string]any)["controlTotalsMinor.USD"].(string), "declared 9999") {
		t.Fatalf("errors: %s", rec.Body)
	}
	list := f.loader.send(f.router, http.MethodGet, openingPath+"?legal_entity="+entityA, "")
	if len(decode(t, list)["items"].([]any)) != 0 {
		t.Fatalf("a refused batch stored something: %s", list.Body)
	}
}

func TestOpeningBatch_UnknownVendorNamesTheRow(t *testing.T) {
	f := newOpeningFixture(t)
	rec := f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "15000", openingItem("A-1", vendorOne, "10000"), openingItem("A-2", unknownVendor, "5000")))
	body := wantProblem(t, rec, http.StatusUnprocessableEntity, "AP-OPB-001")
	if _, ok := body["errors"].(map[string]any)["items[2].vendorId"]; !ok {
		t.Fatalf("errors: %s", rec.Body)
	}
	if list := f.loader.send(f.router, http.MethodGet, openingPath+"?legal_entity="+entityA, ""); len(decode(t, list)["items"].([]any)) != 0 {
		t.Fatalf("a refused batch stored something: %s", list.Body)
	}
}

func TestOpeningBatch_RowValidationAndReferenceReuse(t *testing.T) {
	f := newOpeningFixture(t)
	bad := openingItem("A-1", vendorOne, "0")
	bad["currency"], bad["dueDate"] = "usd", "2025-10-01"
	rec := f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "1", bad, openingItem("A-1", vendorOne, "1")))
	errs := wantProblem(t, rec, http.StatusUnprocessableEntity, "AP-OPB-001")["errors"].(map[string]any)
	for _, k := range []string{"items[1].amountMinor", "items[1].currency", "items[1].dueDate", "items[2].reference"} {
		if _, ok := errs[k]; !ok {
			t.Fatalf("missing %s in %s", k, rec.Body)
		}
	}

	f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "100", openingItem("A-1", vendorOne, "100")))
	reuse := f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-2", "100", openingItem("A-1", vendorOne, "100")))
	body := wantProblem(t, reuse, http.StatusUnprocessableEntity, "AP-OPB-004")
	if !strings.Contains(body["errors"].(map[string]any)["items[1].reference"].(string), "cutover-1") {
		t.Fatalf("errors: %s", reuse.Body)
	}
}

func TestOpeningBatch_LegalEntityIsolationAndAuthorization(t *testing.T) {
	f := newOpeningFixture(t)
	f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "10000", openingItem("A-1", vendorOne, "10000")))

	other := caller{duties: openingDuties, user: "factory-operator", entity: entityB}
	list := other.send(f.router, http.MethodGet, openingPath, "")
	if list.Code != http.StatusOK || len(decode(t, list)["items"].([]any)) != 0 {
		t.Fatalf("entity B sees entity A batches: %d %s", list.Code, list.Body)
	}
	if open := other.send(f.router, http.MethodGet, "/v1/payables/open-items", ""); strings.Contains(open.Body.String(), "OLD-A-1") {
		t.Fatalf("entity B sees entity A items: %s", open.Body)
	}
	wantProblem(t, other.send(f.router, http.MethodPost, openingPath, openingBody("cutover-9", "1", openingItem("B-1", vendorOne, "1"))), http.StatusForbidden, "AUTH-0003")
	crossEntity := strings.Replace(openingBody("cutover-3", "1", openingItem("C-1", vendorOne, "1")), entityA, entityB, 1)
	wantProblem(t, f.loader.send(f.router, http.MethodPost, openingPath, crossEntity), http.StatusForbidden, "AUTH-0003")

	reader := caller{duties: "open-item.read", user: "clerk", entity: entityA}
	if rec := reader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-4", "1", openingItem("D-1", vendorOne, "1"))); rec.Code != http.StatusForbidden {
		t.Fatalf("a user without the import duty: %d %s", rec.Code, rec.Body)
	}
}

func TestOpeningBatch_OpenItemReadShowsTheOpeningMarker(t *testing.T) {
	f := newOpeningFixture(t)
	f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "10000", openingItem("A-1", vendorOne, "10000")))
	open := f.loader.send(f.router, http.MethodGet, "/v1/payables/open-items", "")
	if !strings.Contains(open.Body.String(), `"dueDateSource":"OPENING"`) || !strings.Contains(open.Body.String(), `"openingReference":"A-1"`) {
		t.Fatalf("open items: %d %s", open.Code, open.Body)
	}
}

func TestOpeningBatch_OversizedAmountIsARowErrorNotAServerFault(t *testing.T) {
	f := newOpeningFixture(t)
	big := openingItem("A-1", vendorOne, "100000000000000")
	big["currency"] = "JPY"
	body, _ := json.Marshal(map[string]any{"legalEntityId": entityA, "batchKey": "cutover-1", "controlTotalsMinor": map[string]any{"JPY": json.Number("100000000000000")}, "items": []any{big}})
	errs := wantProblem(t, f.loader.send(f.router, http.MethodPost, openingPath, string(body)), http.StatusUnprocessableEntity, "AP-OPB-001")["errors"].(map[string]any)
	if _, ok := errs["items[1].amountMinor"]; !ok {
		t.Fatalf("errors: %v", errs)
	}
}

func TestOpeningBatch_PartyNotConfiguredIs503AndStoresNothing(t *testing.T) {
	f := newOpeningFixture(t, fixtureOptions{noParty: true})
	wantProblem(t, f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "10000", openingItem("A-1", vendorOne, "10000"))), http.StatusServiceUnavailable, "COM-0503")
	if list := f.loader.send(f.router, http.MethodGet, openingPath, ""); len(decode(t, list)["items"].([]any)) != 0 {
		t.Fatalf("stored on 503: %s", list.Body)
	}
}

func TestOpeningBatch_ConcurrentBatchesWithTheSameReferenceNeverFault(t *testing.T) {
	f := newOpeningFixture(t)
	codes := make([]int, 4)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := "cutover-" + string(rune('a'+i))
			codes[i] = f.loader.send(f.router, http.MethodPost, openingPath, openingBody(key, "10000", openingItem("A-1", vendorOne, "10000"))).Code
		}()
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusUnprocessableEntity:
		default:
			t.Fatalf("codes = %v, want only 201 and 422", codes)
		}
	}
	if created != 1 {
		t.Fatalf("codes = %v, want exactly one batch to take the reference", codes)
	}
}

func TestOpeningBatch_ListPagesWithAKeysetCursor(t *testing.T) {
	f := newOpeningFixture(t)
	for _, k := range []string{"one", "two", "three"} {
		if rec := f.loader.send(f.router, http.MethodPost, openingPath, openingBody(k, "100", openingItem("R-"+k, vendorOne, "100"))); rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", k, rec.Code, rec.Body)
		}
	}
	first := decode(t, f.loader.send(f.router, http.MethodGet, openingPath+"?limit=2", ""))
	cursor, _ := first["nextCursor"].(string)
	if len(first["items"].([]any)) != 2 || cursor == "" {
		t.Fatalf("first page: %v", first)
	}
	second := decode(t, f.loader.send(f.router, http.MethodGet, openingPath+"?limit=2&cursor="+cursor, ""))
	if len(second["items"].([]any)) != 1 || second["nextCursor"] != "" || second["items"].([]any)[0].(map[string]any)["batchKey"] != "one" {
		t.Fatalf("second page: %v", second)
	}
	wantProblem(t, f.loader.send(f.router, http.MethodGet, openingPath+"?cursor=bogus", ""), http.StatusBadRequest, "COM-0001")
}

func TestOpeningBatch_MigrationFactoryServiceTokenHoldsOnlyItsGrant(t *testing.T) {
	f := newOpeningFixture(t)
	pub, priv, _ := ed25519.GenerateKey(nil)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("payables", map[string]ed25519.PublicKey{"migration-factory": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	signer, _ := tenant.NewServiceSigner("migration-factory", priv)
	operator := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityA, UserID: "operator"}
	send := func(only []string, method, body string) *httptest.ResponseRecorder {
		token, err := signer.MintDuties("payables", operator, entityA, only)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, openingPath, strings.NewReader(body))
		req.Header.Set(tenant.HeaderServiceToken, token)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec
	}
	if rec := send(nil, http.MethodPost, openingBody("cutover-1", "10000", openingItem("A-1", vendorOne, "10000"))); rec.Code != http.StatusCreated {
		t.Fatalf("import with a service token: %d %s", rec.Code, rec.Body)
	}
	if rec := send(nil, http.MethodGet, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cutover-1"`) {
		t.Fatalf("read back with a service token: %d %s", rec.Code, rec.Body)
	}
	if rec := send([]string{"opening-item.import.read"}, http.MethodPost, openingBody("cutover-2", "1", openingItem("B-1", vendorOne, "1"))); rec.Code != http.StatusForbidden {
		t.Fatalf("a read-only token imported: %d %s", rec.Code, rec.Body)
	}
}

func TestOpeningBatch_OpeningItemIsPaidAndClearsTheApControlAccount(t *testing.T) {
	validator, err := domain.NewXSDValidator(payables.Pain001XSD)
	if err != nil {
		t.Skipf("xmllint unavailable: %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })
	f := newOpeningFixture(t, fixtureOptions{validator: validator})
	f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "10000", openingItem("A-1", vendorOne, "10000")))
	if f.ledger.posted() != 0 {
		t.Fatalf("import posted %d journals", f.ledger.posted())
	}
	created := f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-1", nil))
	if created.Code != http.StatusCreated {
		t.Fatalf("proposal: %d %s", created.Code, created.Body)
	}
	f.proposal = decode(t, created)["id"].(string)
	f.approve()
	runID := f.start()["id"].(string)
	for _, step := range []string{"generate-file", "post-journal", "settle", "complete"} {
		if rec := f.step(f.erin, runID, step); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", step, rec.Code, rec.Body)
		}
	}
	lines := map[string]bool{}
	for _, l := range f.ledger.bodies[0]["lines"].([]any) {
		line := l.(map[string]any)
		lines[line["account"].(string)+" "+line["debitCredit"].(string)+" "+line["amount"].(map[string]any)["value"].(string)] = true
	}
	if f.ledger.posted() != 1 || !lines["2000 D 100.00"] || !lines["1010 C 100.00"] {
		t.Fatalf("journals = %d, lines = %v, want one run journal debiting AP control 100.00 against the bank", f.ledger.posted(), lines)
	}
	items, err := f.pg.List(f.ctx(), store.Filter{LegalEntityID: entityA}, 10)
	if err != nil || len(items) != 1 || items[0].Status != "SETTLED" || items[0].DueDateSource != "OPENING" {
		t.Fatalf("items = %+v err=%v", items, err)
	}
}
