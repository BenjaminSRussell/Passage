package api

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

const punchoutBody = `{"legalEntityId":"` + leA + `","currency":"USD","lines":[{"description":"Pump seal kit","category":"PUNCHOUT","qty":"2","unit":"EA",
	"estimatedUnitPrice":"12.50","requestedDate":"2026-03-10","catalogSource":"PUNCHOUT","supplierPartId":"PS-100","needsApproval":true},
	{"description":"O-ring set","category":"PUNCHOUT","qty":"1","unit":"EA","estimatedUnitPrice":"3","requestedDate":"2026-03-10","catalogSource":"PUNCHOUT","supplierPartId":"PS-200"}]}`

// ediToken is the token the EDI engine signs when it turns a returned cart into the requester's requisition.
func ediToken(t *testing.T) map[string]string {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("edi", priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("purchasing", map[string]ed25519.PublicKey{"edi": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	token, err := signer.MintDuties("purchasing", tenant.Principal{TenantID: tenantA, LegalEntityID: leA, UserID: userA}, leA, []string{dutyRequisitionCreate})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{tenant.HeaderServiceToken: token, "Idempotency-Key": "punchout:s1"}
}

// #35 (35-3): only the EDI engine sets the punchout fields; a requester cannot.
func TestCreateRequisition_OnlyThePunchoutReturnSetsPunchoutFields(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", user: userA, duties: []string{dutyRequisitionCreate}, body: punchoutBody, headers: idem("u-1")}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a requester setting punchout fields: %d %s", rec.Code, rec.Body)
	}
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: punchoutBody, headers: ediToken(t)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("edi: %d %s", rec.Code, rec.Body)
	}
	var out store.Requisition
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if l := out.Lines[0]; l.CatalogSource != "PUNCHOUT" || l.SupplierPartID != "PS-100" || !l.NeedsApproval || !out.NeedsApproval || out.RequesterID != userA {
		t.Fatalf("requisition %+v", out)
	}
	if rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: strings.Replace(punchoutBody, `"PUNCHOUT","supplierPartId":"PS-100"`, `"WEB","supplierPartId":"PS-100"`, 1), headers: ediToken(t)}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("catalogSource WEB: %d %s", rec.Code, rec.Body)
	}
}

// #35 (35-3): editing the lines keeps the punchout fields of the punchout lines (as the web editor sends them, without the fields),
// refuses a requester's punchout line, and a renamed flagged line still leaves the requisition needing approval.
func TestPatchRequisition_PunchoutFieldsAreSticky(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: punchoutBody, headers: ediToken(t)})
	var created store.Requisition
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	path := "/v1/procurement/requisitions/" + created.ID
	patch := func(key, lines string, version int) *http.Response {
		r := h.do(call{method: "PATCH", path: path, user: userA, duties: []string{dutyRequisitionCreate}, body: `{"lines":` + lines + `}`,
			headers: map[string]string{"Idempotency-Key": key, "If-Match": `"` + string(rune('0'+version)) + `"`}})
		return r.Result()
	}
	// The editor sends the lines back without the punchout fields and a new quantity: the flags stay.
	same := `[{"description":"Pump seal kit","category":"PUNCHOUT","qty":"3","unit":"EA","estimatedUnitPrice":"12.50","requestedDate":"2026-03-10"},
		{"description":"O-ring set","category":"PUNCHOUT","qty":"1","unit":"EA","estimatedUnitPrice":"3","requestedDate":"2026-03-10","supplierPartId":"PS-200","catalogSource":"PUNCHOUT"}]`
	if res := patch("e-1", same, 1); res.StatusCode != http.StatusOK {
		t.Fatalf("edit: %d", res.StatusCode)
	}
	got := h.fake.requisitions[created.ID]
	if l := got.Lines[0]; l.CatalogSource != "PUNCHOUT" || l.SupplierPartID != "PS-100" || !l.NeedsApproval {
		t.Fatalf("the flagged line lost its fields: %+v", l)
	}
	if l := got.Lines[1]; l.SupplierPartID != "PS-200" || l.NeedsApproval {
		t.Fatalf("second line %+v", l)
	}
	// Clearing the flag, or claiming another supplier part, is refused.
	cleared := strings.Replace(same, `"supplierPartId":"PS-200","catalogSource":"PUNCHOUT"`, `"supplierPartId":"PS-999","catalogSource":"PUNCHOUT"`, 1)
	if res := patch("e-2", cleared, 2); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown supplier part: %d", res.StatusCode)
	}
	newLine := `[{"description":"Gasket","category":"MRO","qty":"1","unit":"EA","estimatedUnitPrice":"1","requestedDate":"2026-03-10","needsApproval":false,"catalogSource":"PUNCHOUT"}]`
	if res := patch("e-3", newLine, 2); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a requester's punchout line: %d", res.StatusCode)
	}
	// Renaming the flagged line drops its fields, but the requisition still needs approval on submit.
	renamed := `[{"description":"Seal kit (renamed)","category":"PUNCHOUT","qty":"3","unit":"EA","estimatedUnitPrice":"12.50","requestedDate":"2026-03-10"}]`
	if res := patch("e-4", renamed, 2); res.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d", res.StatusCode)
	}
	if rec := h.do(call{method: "POST", path: path + ":submit", user: userA, duties: []string{dutyRequisitionCreate},
		headers: map[string]string{"Idempotency-Key": "s-1", "If-Match": `"3"`}}); rec.Code != http.StatusOK || strings.Join(h.fake.lastSubmit.chain, "|") != "MANAGER" {
		t.Fatalf("submit: %d %s chain %v", rec.Code, rec.Body, h.fake.lastSubmit.chain)
	}
}

func TestSubmitRequisition_FlaggedPunchoutLineIsNeverAutoApproved(t *testing.T) {
	h := newHarness(t)
	submit := func(r store.Requisition) []string {
		t.Helper()
		r.ID, r.RequesterID, r.Status, r.Version, r.Currency, r.Total = ids.New(), userA, store.RequisitionDraft, 1, "USD", decimal.NewFromInt(25)
		h.fake.requisitions[r.ID] = r
		rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + r.ID + ":submit", user: userA, duties: []string{dutyRequisitionCreate},
			headers: map[string]string{"Idempotency-Key": "s-" + r.ID, "If-Match": `"1"`}})
		if rec.Code != http.StatusOK {
			t.Fatalf("submit: %d %s", rec.Code, rec.Body)
		}
		return h.fake.lastSubmit.chain
	}
	// In the auto-approval band, an unflagged punchout line is approved on submit...
	if chain := submit(store.Requisition{Lines: []store.RequisitionLine{{Category: "PUNCHOUT", CatalogSource: "PUNCHOUT"}}}); len(chain) != 0 {
		t.Fatalf("unflagged chain %v", chain)
	}
	// ...a flagged line, or a requisition that held one, goes to the manager.
	if chain := submit(store.Requisition{Lines: []store.RequisitionLine{{Category: "PUNCHOUT", CatalogSource: "PUNCHOUT", NeedsApproval: true}}}); strings.Join(chain, "|") != "MANAGER" {
		t.Fatalf("flagged line chain %v", chain)
	}
	if chain := submit(store.Requisition{NeedsApproval: true, Lines: []store.RequisitionLine{{Category: "PUNCHOUT"}}}); strings.Join(chain, "|") != "MANAGER" {
		t.Fatalf("flagged requisition chain %v", chain)
	}
}

// #35 (35-3): a requester's edit cannot clear a punchout line's flag by sending it as false, may send the fields back on a line it
// matches by description, and flags a punchout line whose price it raises or whose currency it changes.
func TestPatchRequisition_PunchoutEditsCannotDropTheFlag(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: punchoutBody, headers: ediToken(t)})
	var created store.Requisition
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	path := "/v1/procurement/requisitions/" + created.ID
	version := 1
	patch := func(key, body string) store.Requisition {
		t.Helper()
		r := h.do(call{method: "PATCH", path: path, user: userA, duties: []string{dutyRequisitionCreate}, body: body,
			headers: map[string]string{"Idempotency-Key": key, "If-Match": `"` + strconv.Itoa(version) + `"`}})
		if r.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", key, r.Code, r.Body)
		}
		version++
		return h.fake.requisitions[created.ID]
	}
	seal := `{"description":"Pump seal kit","category":"PUNCHOUT","qty":"2","unit":"EA","estimatedUnitPrice":"12.50","requestedDate":"2026-03-10"`
	oring := `{"description":"O-ring set","category":"PUNCHOUT","qty":"1","unit":"EA","estimatedUnitPrice":"%s","requestedDate":"2026-03-10"%s}`
	// needsApproval:false on the flagged line, and the O-ring sent back with its catalog source but no supplier part.
	got := patch("f-1", `{"lines":[`+seal+`,"catalogSource":"PUNCHOUT","supplierPartId":"PS-100","needsApproval":false},`+
		fmt.Sprintf(oring, "3", `,"catalogSource":"PUNCHOUT"`)+`]}`)
	if l := got.Lines[0]; !l.NeedsApproval || l.SupplierPartID != "PS-100" {
		t.Fatalf("flag cleared: %+v", l)
	}
	if l := got.Lines[1]; l.NeedsApproval || l.SupplierPartID != "PS-200" || l.CatalogSource != "PUNCHOUT" {
		t.Fatalf("O-ring matched by description: %+v", l)
	}
	// A lower price keeps the O-ring unflagged; a higher one flags it.
	if got = patch("f-2", `{"lines":[`+seal+`},`+fmt.Sprintf(oring, "2.50", "")+`]}`); got.Lines[1].NeedsApproval {
		t.Fatalf("lower price flagged: %+v", got.Lines[1])
	}
	if got = patch("f-3", `{"lines":[`+seal+`},`+fmt.Sprintf(oring, "3.01", "")+`]}`); !got.Lines[1].NeedsApproval {
		t.Fatalf("raised price not flagged: %+v", got.Lines[1])
	}
	// A blank price is estimated again, so the returned price check no longer holds either.
	if got = patch("f-4", `{"lines":[`+fmt.Sprintf(oring, "", "")+`]}`); !got.Lines[0].NeedsApproval {
		t.Fatalf("blank price not flagged: %+v", got.Lines[0])
	}
}

// #35 (35-3): changing the currency of a requisition with punchout lines flags them; the returned prices were in the old currency.
func TestPatchRequisition_CurrencyChangeFlagsPunchoutLines(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: punchoutBody, headers: ediToken(t)})
	var created store.Requisition
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	body := `{"currency":"EUR","lines":[{"description":"O-ring set","category":"PUNCHOUT","qty":"1","unit":"EA","estimatedUnitPrice":"3","requestedDate":"2026-03-10"}]}`
	r := h.do(call{method: "PATCH", path: "/v1/procurement/requisitions/" + created.ID, user: userA, duties: []string{dutyRequisitionCreate}, body: body,
		headers: map[string]string{"Idempotency-Key": "c-1", "If-Match": `"1"`}})
	if r.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", r.Code, r.Body)
	}
	if l := h.fake.requisitions[created.ID].Lines[0]; !l.NeedsApproval || l.SupplierPartID != "PS-200" {
		t.Fatalf("line after currency change %+v", l)
	}
}
