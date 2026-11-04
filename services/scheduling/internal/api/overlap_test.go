package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestCreateOperation_PassesQuantityAndOverlap(t *testing.T) {
	f := &fakeStore{}
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/operations", operationBody(`,"quantity":"100","overlapQuantity":"25.5"`), allDuties, "op-ov")
	if rec.Code != http.StatusCreated || f.lastOp.Quantity == nil || *f.lastOp.Quantity != "100.0000" || f.lastOp.OverlapQuantity == nil || *f.lastOp.OverlapQuantity != "25.5000" {
		t.Fatalf("status %d store got %+v", rec.Code, f.lastOp)
	}
}

func TestCreateOperation_ValidatesQuantityAndOverlap(t *testing.T) {
	cases := []struct{ name, extra, field string }{
		{"quantity not a number", `,"quantity":"ten"`, `"quantity"`},
		{"zero quantity", `,"quantity":"0"`, `"quantity"`},
		{"negative quantity", `,"quantity":"-1"`, `"quantity"`},
		{"five decimals", `,"quantity":"1.00001"`, `"quantity"`},
		{"exponent", `,"quantity":"1e3"`, `"quantity"`},
		{"bare point", `,"quantity":".5"`, `"quantity"`},
		{"fifteen integer digits", `,"quantity":"100000000000000"`, `"quantity"`},
		{"overlap without quantity", `,"overlapQuantity":"5"`, `"overlapQuantity"`},
		{"overlap above quantity", `,"quantity":"10","overlapQuantity":"11"`, `"overlapQuantity"`},
		{"zero overlap", `,"quantity":"10","overlapQuantity":"0"`, `"overlapQuantity"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(newRouter(&fakeStore{}), http.MethodPost, "/v1/scheduling/operations", operationBody(c.extra), allDuties, "k-"+c.name)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) {
				t.Fatalf("status %d body %s, want 400 naming %s", rec.Code, rec.Body, c.field)
			}
		})
	}
}

func TestUpdateOperation_PassesQuantityAndOverlap(t *testing.T) {
	f := &fakeStore{}
	rec := send(newRouter(f), http.MethodPatch, opPath, `{"quantity":"80","overlapQuantity":"0"}`, allDuties, "kov", `"3"`)
	c := calls(f)
	if rec.Code != http.StatusOK || c.patch.Quantity == nil || c.patch.Quantity.String() != "80" || c.patch.OverlapQuantity == nil || !c.patch.OverlapQuantity.IsZero() {
		t.Fatalf("status %d patch %+v body %s", rec.Code, c.patch, rec.Body)
	}
	for field, body := range map[string]string{"quantity": `{"quantity":"0"}`, "overlapQuantity": `{"overlapQuantity":"-2"}`} {
		rec := send(newRouter(&fakeStore{}), http.MethodPatch, opPath, body, allDuties, "kb-"+field, `"3"`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body)
		}
	}
}
