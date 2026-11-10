package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

func TestConvert_ValidatesAndNeedsDuty(t *testing.T) {
	path := "/v1/budget/encumbrances/" + lineID + ":convert"
	good := `{"purchaseOrderId":"` + docID + `","purchaseOrderLineId":"` + lineID + `"}`
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, good, "budget.encumbrance.convert", "conv-1"); rec.Code != http.StatusOK {
		t.Fatalf("convert: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, good, "budget.check", "conv-1"); rec.Code != http.StatusForbidden {
		t.Fatalf("without the check duty: %d, want 403", rec.Code)
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, good, "budget.encumbrance.convert", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("without Idempotency-Key: %d, want 400", rec.Code)
	}
	cases := map[string]struct{ body, field string }{
		"no order":      {`{"purchaseOrderLineId":"` + lineID + `"}`, "purchaseOrderId"},
		"no order line": {`{"purchaseOrderId":"` + docID + `"}`, "purchaseOrderLineId"},
		"bad line id":   {`{"purchaseOrderId":"` + docID + `","purchaseOrderLineId":"x"}`, "purchaseOrderLineId"},
	}
	for name, c := range cases {
		rec := serve(t, &fakeStore{}, http.MethodPost, path, c.body, "budget.encumbrance.convert", "conv-2")
		var p httpx.Problem
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s: status = %d errors = %v", name, rec.Code, p.Errors)
		}
	}
}
