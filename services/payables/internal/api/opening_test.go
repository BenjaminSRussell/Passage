package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
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
	router http.Handler
	loader caller
}

func newOpeningFixture(t *testing.T) openingFixture {
	f := newRunFixture(t, fixtureOptions{skipProposal: true, noSeedItems: true})
	f.api.WithOpeningItems(f.pg)
	router := httpx.NewRouter()
	f.api.Register(router)
	return openingFixture{runFixture: f, router: router, loader: caller{duties: openingDuties, user: "factory-operator", entity: entityA}}
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
	rec := f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "10001", openingItem("A-1", vendorOne, "10001")))
	wantProblem(t, rec, http.StatusConflict, "AP-OPN-002")
}

func TestOpeningBatch_ControlTotalMismatchRefusesWholeBatch(t *testing.T) {
	f := newOpeningFixture(t)
	rec := f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "9999", openingItem("A-1", vendorOne, "10000")))
	body := wantProblem(t, rec, http.StatusUnprocessableEntity, "AP-OPN-003")
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
	body := wantProblem(t, rec, http.StatusUnprocessableEntity, "AP-OPN-001")
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
	errs := wantProblem(t, rec, http.StatusUnprocessableEntity, "AP-OPN-001")["errors"].(map[string]any)
	for _, k := range []string{"items[1].amountMinor", "items[1].currency", "items[1].dueDate", "items[2].reference"} {
		if _, ok := errs[k]; !ok {
			t.Fatalf("missing %s in %s", k, rec.Body)
		}
	}

	f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-1", "100", openingItem("A-1", vendorOne, "100")))
	reuse := f.loader.send(f.router, http.MethodPost, openingPath, openingBody("cutover-2", "100", openingItem("A-1", vendorOne, "100")))
	body := wantProblem(t, reuse, http.StatusUnprocessableEntity, "AP-OPN-004")
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
