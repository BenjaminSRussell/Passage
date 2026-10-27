package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

const subcontractPath = "/v1/lean/subcontracting:preview"

func previewCall(body string) call {
	return call{method: "POST", path: subcontractPath, body: body, duties: DutyFlowWrite}
}

// AC-24.17: a kanban job of 20 on vendor V in po-per-kanban mode gives a PO line
// for 20 service units at the agreement price.
func TestSubcontractPreview_AC2417(t *testing.T) {
	body := `{"mode":"po-per-kanban",
		"agreements":[{"vendorId":"V","itemId":"svc-plating","unitPrice":"4.50","currency":"USD","validFrom":"2027-01-01"}],
		"jobs":[{"jobId":"job-1","vendorId":"V","itemId":"svc-plating","qty":"20","completedOn":"2027-03-10"}]}`
	rec := previewCall(body).do(newRouter(&fakeStore{}))
	var got subcontractResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if len(got.Lines) != 1 {
		t.Fatalf("lines %+v", got.Lines)
	}
	l := got.Lines[0]
	if l.Qty.String() != "20" || l.UnitPrice.String() != "4.5" || l.Amount.StringFixed(2) != "90.00" || l.PriceDate != "2027-03-10" || l.JobIDs[0] != "job-1" {
		t.Fatalf("line %+v", l)
	}
}

// FR-24.20: a job whose completion date no agreement covers is 422 LEAN-0005.
func TestSubcontractPreview_NoAgreement(t *testing.T) {
	body := `{"mode":"po-per-kanban",
		"agreements":[{"vendorId":"V","itemId":"svc-plating","unitPrice":"4.50","currency":"USD","validFrom":"2027-01-01","validTo":"2027-01-31"}],
		"jobs":[{"jobId":"job-1","vendorId":"V","itemId":"svc-plating","qty":"20","completedOn":"2027-02-01"}]}`
	rec := previewCall(body).do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != "LEAN-0005" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestSubcontractPreview_Rejects(t *testing.T) {
	agreement := `{"vendorId":"V","itemId":"s","unitPrice":"1","currency":"USD","validFrom":"2027-01-01"}`
	job := `{"jobId":"j","vendorId":"V","itemId":"s","qty":"1","completedOn":"2027-01-02"}`
	for name, body := range map[string]string{
		"unknown mode":   `{"mode":"weekly","agreements":[` + agreement + `],"jobs":[` + job + `]}`,
		"lower currency": `{"mode":"po-per-kanban","agreements":[{"vendorId":"V","itemId":"s","unitPrice":"1","currency":"usd","validFrom":"2027-01-01"}],"jobs":[]}`,
		"timestamp date": `{"mode":"po-per-kanban","agreements":[],"jobs":[{"jobId":"j","vendorId":"V","itemId":"s","qty":"1","completedOn":"2027-01-02T10:00:00Z"}]}`,
		"zero qty":       `{"mode":"po-per-kanban","agreements":[],"jobs":[{"jobId":"j","vendorId":"V","itemId":"s","qty":"0","completedOn":"2027-01-02"}]}`,
		"no job id":      `{"mode":"po-per-kanban","agreements":[],"jobs":[{"jobId":"","vendorId":"V","itemId":"s","qty":"1","completedOn":"2027-01-02"}]}`,
		"unknown field":  `{"mode":"po-per-kanban","agreements":[],"jobs":[],"vendor":"V"}`,
	} {
		rec := previewCall(body).do(newRouter(&fakeStore{}))
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := (call{method: "POST", path: subcontractPath, body: `{}`, duties: DutyKanbanWork}).do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
		t.Errorf("board duty must not preview subcontract lines: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: subcontractPath, body: `{}`, duties: DutyFlowWrite, noTenant: true}).do(newRouter(&fakeStore{})); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing tenant: %d", rec.Code)
	}
}

// FR-24.14: a board filtered to a flow shows its takt time against the slowest
// activity; without a flow filter there is no takt figure.
func TestBoard_TaktVsCycle(t *testing.T) {
	takt := 60
	fs := &fakeStore{flow: &store.Flow{ID: flowID, TaktTimeS: &takt, Activities: []store.Activity{
		{Name: "Mix", CycleTimeS: 45}, {Name: "Fill", CycleTimeS: 75},
	}}}
	rec := call{method: "GET", path: "/v1/lean/board?flowId=" + flowID, duties: DutyKanbanWork}.do(newRouter(fs))
	var body struct {
		Takt *taktDTO `json:"takt"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Takt == nil {
		t.Fatalf("board %d %s", rec.Code, rec.Body)
	}
	if *body.Takt != (taktDTO{TaktTimeS: 60, CycleTimeS: 75, BottleneckActivity: "Fill", WithinTakt: false}) {
		t.Fatalf("takt %+v", body.Takt)
	}
	rec = call{method: "GET", path: "/v1/lean/board", duties: DutyKanbanWork}.do(newRouter(fs))
	body.Takt = &taktDTO{}
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Takt != nil {
		t.Fatalf("unfiltered board has a takt figure: %s", rec.Body)
	}
	noTakt := &fakeStore{flow: &store.Flow{ID: flowID, Activities: []store.Activity{{Name: "Mix", CycleTimeS: 45}}}}
	rec = call{method: "GET", path: "/v1/lean/board?flowId=" + flowID, duties: DutyKanbanWork}.do(newRouter(noTakt))
	body.Takt = &taktDTO{}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Takt != nil {
		t.Fatalf("flow without takt: %d %s", rec.Code, rec.Body)
	}
}
