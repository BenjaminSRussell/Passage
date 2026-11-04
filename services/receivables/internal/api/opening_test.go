package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const openingCustomer = "11111111-1111-4111-8111-111111111111"

func openingRow(key, kind, side string) string {
	return `{"legacyKey":"` + key + `","documentNumber":"D-` + key + `","customerId":"` + openingCustomer + `","kind":"` + kind + `","side":"` + side + `",
		"documentDate":"2026-09-01","dueDate":"2026-10-01","currency":"USD","originalAmount":"500.00","openAmount":"250.00"}`
}

func openingBody(atomic string, rows ...string) string {
	return `{"legalEntityId":"` + testLE + `","importSource":"SAP-PRD","atomic":` + atomic + `,"items":[` + strings.Join(rows, ",") + `]}`
}

func TestOpeningImport_NeedsTheImportDuty(t *testing.T) {
	h := router(&fakeStore{})
	for _, duties := range []string{"ARView", "ARImport", "party.import.write"} {
		rec := do(h, http.MethodPost, "/v1/receivables/open-items:batch", openingBody("false", openingRow("A", "INVOICE", "DEBIT")), duties, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d", duties, rec.Code)
		}
	}
	rec := do(h, http.MethodPost, "/v1/receivables/open-items:batch", openingBody("false", openingRow("A", "INVOICE", "DEBIT")), "receivables.import.write", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body)
	}
}

// The loader reads an open item back by id (receivables.import.read); its list and every other route stay closed.
func TestOpenItemRead_ImportReadDuty(t *testing.T) {
	h := router(&fakeStore{})
	for _, duties := range []string{"receivables.import.read", "ARView"} {
		rec := do(h, http.MethodGet, "/v1/receivables/open-items/"+fakeOpenItemID, "", duties, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"documentNumber":"D-1"`) || rec.Header().Get("ETag") != `"2"` {
			t.Fatalf("%s: %d %s", duties, rec.Code, rec.Body)
		}
	}
	for path, want := range map[string]int{"/v1/receivables/open-items/" + openingCustomer: http.StatusNotFound, "/v1/receivables/open-items/not-a-uuid": http.StatusNotFound} {
		if rec := do(h, http.MethodGet, path, "", "receivables.import.read", nil); rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
	for _, c := range []struct{ method, path string }{{http.MethodGet, "/v1/receivables/open-items"}, {http.MethodGet, "/v1/receivables/open-item-imports"}, {http.MethodPost, "/v1/receivables/open-items:batch"}} {
		if rec := do(h, c.method, c.path, "{}", "receivables.import.read", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if rec := do(h, http.MethodGet, "/v1/receivables/open-items/"+fakeOpenItemID, "", "receivables.import.write", nil); rec.Code != http.StatusForbidden {
		t.Errorf("import write alone reads: %d", rec.Code)
	}
}

func TestOpeningImport_RowErrorsAreReportedPerRow(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	bad := strings.Replace(openingRow("B", "CREDIT_NOTE", "DEBIT"), `"currency":"USD"`, `"currency":"usd"`, 1)
	body := openingBody("false", openingRow("A", "INVOICE", "DEBIT"), bad, openingRow("A", "INVOICE", "DEBIT"), openingRow("C", "PAYMENT", "CREDIT"))
	rec := do(h, http.MethodPost, "/v1/receivables/open-items:batch", body, "receivables.import.write", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Created, Rejected int
		Results           []struct {
			Index                 int
			Outcome, Code, Detail string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Created != 2 || out.Rejected != 2 || len(out.Results) != 4 {
		t.Fatalf("counts: %+v", out)
	}
	if r := out.Results[1]; r.Code != "AR-OPENING-INVALID" || !strings.Contains(r.Detail, "side") || !strings.Contains(r.Detail, "currency") {
		t.Errorf("row 1: %+v", r)
	}
	if r := out.Results[2]; r.Code != "AR-OPENING-DUPLICATE-KEY" || !strings.Contains(r.Detail, "items[0]") {
		t.Errorf("row 2: %+v", r)
	}
	if c := f.opening.Items[3].Row; c.Side != "CREDIT" || c.Kind != "PAYMENT" {
		t.Errorf("credit row forwarded as %+v", c)
	}
}

func TestOpeningImport_AtomicRefusalIs422WithTheRows(t *testing.T) {
	h := router(&fakeStore{})
	bad := strings.Replace(openingRow("B", "INVOICE", "DEBIT"), `"openAmount":"250.00"`, `"openAmount":"900.00"`, 1)
	rec := do(h, http.MethodPost, "/v1/receivables/open-items:batch", openingBody("true", openingRow("A", "INVOICE", "DEBIT"), bad), "receivables.import.write", nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"committed":false`) {
		t.Fatalf("want 422 with committed=false, got %d: %s", rec.Code, rec.Body)
	}
}

func TestOpeningImport_BatchFieldsAndScope(t *testing.T) {
	h := router(&fakeStore{})
	for name, body := range map[string]string{
		"empty":  `{"legalEntityId":"` + testLE + `","importSource":"SAP-PRD","items":[]}`,
		"source": `{"legalEntityId":"` + testLE + `","importSource":"sap prd","items":[` + openingRow("A", "INVOICE", "DEBIT") + `]}`,
		"entity": `{"legalEntityId":"x","importSource":"SAP-PRD","items":[` + openingRow("A", "INVOICE", "DEBIT") + `]}`,
	} {
		if rec := do(h, http.MethodPost, "/v1/receivables/open-items:batch", body, "receivables.import.write", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, rec.Code)
		}
	}
	other := strings.Replace(openingBody("false", openingRow("A", "INVOICE", "DEBIT")), testLE, "00000000-0000-0000-0000-0000000000b2", 1)
	if rec := do(h, http.MethodPost, "/v1/receivables/open-items:batch", other, "receivables.import.write", nil); rec.Code != http.StatusForbidden {
		t.Errorf("another legal entity: want 403, got %d", rec.Code)
	}
}

func TestOpenItems_ImportSourceFilter(t *testing.T) {
	h := router(&fakeStore{})
	if rec := do(h, http.MethodGet, "/v1/receivables/open-items?importSource=bad%20source", "", "ARView", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/receivables/open-items?importSource=SAP-PRD", "", "ARView", nil); rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/receivables/open-item-imports", "", "ARView", nil); rec.Code != http.StatusOK {
		t.Errorf("imports: want 200, got %d", rec.Code)
	}
}
