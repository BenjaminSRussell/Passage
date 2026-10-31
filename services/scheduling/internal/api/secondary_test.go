package api

import (
	"net/http"
	"strings"
	"testing"
)

const toolID = "00000000-0000-0000-0000-0000000000e1"

func TestCreateOperation_PassesSecondaryResources(t *testing.T) {
	f := &fakeStore{}
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/operations", operationBody(`,"secondaryResourceIds":["`+strings.ToUpper(toolID)+`"]`), allDuties, "op-s")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if got := f.lastOp.SecondaryResourceIDs; len(got) != 1 || got[0] != toolID {
		t.Fatalf("store got secondaries %v", got)
	}
}

func TestCreateOperation_ValidatesSecondaryResources(t *testing.T) {
	six := make([]string, 0, 6)
	for i := range 6 {
		six = append(six, `"00000000-0000-0000-0000-00000000f00`+string(rune('0'+i))+`"`)
	}
	cases := []struct{ name, extra string }{
		{"not a UUID", `,"secondaryResourceIds":["TOOL-1"]`},
		{"repeated", `,"secondaryResourceIds":["` + toolID + `","` + toolID + `"]`},
		{"also a candidate", `,"secondaryResourceIds":["` + resID + `"]`},
		{"more than five", `,"secondaryResourceIds":[` + strings.Join(six, ",") + `]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeStore{}
			rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/operations", operationBody(c.extra), allDuties, "op-x")
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"secondaryResourceIds"`) {
				t.Fatalf("status %d body %s, want 400 naming secondaryResourceIds", rec.Code, rec.Body)
			}
			if f.lastOp.OrderID != "" {
				t.Fatal("an invalid request reached the store")
			}
		})
	}
}
