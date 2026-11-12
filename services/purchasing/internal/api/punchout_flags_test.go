package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// #35 (35-3): a punchout line carries its catalog source, supplier part and approval flag; a flagged line keeps a requisition in
// the auto-approval band from being approved on submit.
func TestCreateRequisition_PunchoutFlags(t *testing.T) {
	h := newHarness(t)
	body := `{"legalEntityId":"` + leA + `","currency":"USD","lines":[{"description":"Pump seal kit","category":"PUNCHOUT","qty":"2","unit":"EA",
		"estimatedUnitPrice":"12.50","requestedDate":"2026-03-10","catalogSource":"PUNCHOUT","supplierPartId":"PS-100","needsApproval":true}]}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", user: userA, duties: []string{dutyRequisitionCreate}, body: body, headers: idem("po-1")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out store.Requisition
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if l := out.Lines[0]; l.CatalogSource != "PUNCHOUT" || l.SupplierPartID != "PS-100" || !l.NeedsApproval {
		t.Fatalf("line %+v", l)
	}
	for name, b := range map[string]string{
		"catalogSource": strings.Replace(body, `"catalogSource":"PUNCHOUT"`, `"catalogSource":"WEB"`, 1),
		"orphan flag":   strings.Replace(body, `"catalogSource":"PUNCHOUT",`, ``, 1),
	} {
		if rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", user: userA, duties: []string{dutyRequisitionCreate}, body: b, headers: idem("po-" + name)}); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestSubmitRequisition_FlaggedPunchoutLineIsNeverAutoApproved(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	h.fake.requisitions[id] = store.Requisition{ID: id, RequesterID: userA, Status: store.RequisitionDraft, Version: 1, Currency: "USD",
		Total: decimal.NewFromInt(25), Lines: []store.RequisitionLine{{Category: "PUNCHOUT", CatalogSource: "PUNCHOUT", NeedsApproval: true}}}
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":submit", user: userA, duties: []string{dutyRequisitionCreate},
		headers: map[string]string{"Idempotency-Key": "s1", "If-Match": `"1"`}})
	if rec.Code != http.StatusOK || strings.Join(h.fake.lastSubmit.chain, "|") != "MANAGER" {
		t.Fatalf("submit: %d %s chain %v", rec.Code, rec.Body, h.fake.lastSubmit.chain)
	}
}
