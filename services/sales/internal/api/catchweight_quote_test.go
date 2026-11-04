package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// TestCreateQuotation_RefusesCatchWeightLine: the quotation store keeps no
// spec and an order made from it would carry a weight-priced net on a plain
// line, so a quotation line cannot be a catch-weight item (400, nothing stored).
func TestCreateQuotation_RefusesCatchWeightLine(t *testing.T) {
	req := quoteRequest(1)
	req.LegalEntityID = testLE
	req.Lines[0].Quantity = "10"
	n, lo, hi := "10", "9.8", "11"
	req.Lines[0].CatchWeight = &catchWeightSpec{Nominal: &n, Min: &lo, Max: &hi}
	raw, _ := json.Marshal(req)
	s := &slice4Store{}
	rec := serveBody(t, s, http.MethodPost, quotations, string(raw), principal(dutyQuotation), map[string]string{idempotencyKey: "q-cw"})
	if rec.Code != http.StatusBadRequest || s.quotation != nil || !strings.Contains(rec.Body.String(), "lines[0].catchWeight") {
		t.Fatalf("a catch-weight quotation line: %d %s, want 400 naming lines[0].catchWeight", rec.Code, rec.Body)
	}
}

// TestPatchOrder_RefusesCatchWeightAddLine: an added line is stored and priced
// without a spec, so a change cannot add a catch-weight line.
func TestPatchOrder_RefusesCatchWeightAddLine(t *testing.T) {
	c := &changeStore{}
	body := `{"reason":"add","addLines":[{"productId":"` + ids.New() + `","quantity":"3","unit":"CS","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() +
		`","unitPrice":"2","requestedShipDate":"2027-03-09","catchWeight":` + goodSpec + `}]}`
	rec := serveBody(t, c, http.MethodPatch, orders+"/"+ids.New(), body, principal(dutyWrite), map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusBadRequest || c.change != nil || !strings.Contains(rec.Body.String(), "addLines[0].catchWeight") {
		t.Fatalf("a catch-weight added line: %d %s, want 400 naming addLines[0].catchWeight", rec.Code, rec.Body)
	}
}
