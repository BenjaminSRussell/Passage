package api

import (
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
)

const (
	rollPath   = "/v1/product-costing/versions/" + someID + "/roll"
	calculate  = "CostingCalculate"
	rollItemID = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6c01"
)

func rollBody(siteID, itemID string) string {
	return `{"siteId":"` + siteID + `","items":[{"id":"` + itemID + `","kind":"Purchased","price":"4.00"}]}`
}

func TestRollStoredVersion_NeedsCalculateDutyKeyUuidsAndRollsTheRequestItems(t *testing.T) {
	f := &fakeStore{}
	h := storedRouter(f)
	if rec := (call{http.MethodPost, rollPath, rollBody(ids.New(), rollItemID), view, "k", ""}).do(h); rec.Code != http.StatusForbidden {
		t.Fatalf("without CostingCalculate: %d", rec.Code)
	}
	for name, c := range map[string]call{
		"no key":      {http.MethodPost, rollPath, rollBody(ids.New(), rollItemID), calculate, "", ""},
		"bad site":    {http.MethodPost, rollPath, rollBody("site-1", rollItemID), calculate, "k", ""},
		"bad item id": {http.MethodPost, rollPath, rollBody(ids.New(), "FG-100"), calculate, "k", ""},
		"no items":    {http.MethodPost, rollPath, `{"siteId":"` + ids.New() + `","items":[]}`, calculate, "k", ""},
	} {
		if rec := c.do(h); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if f.calls != 0 {
		t.Fatal("a refused request reached the store")
	}
	site := ids.New()
	rec := call{http.MethodPost, rollPath, rollBody(site, rollItemID), calculate, " roll-1 ", ""}.do(h)
	if rec.Code != http.StatusCreated || f.roll == nil {
		t.Fatalf("roll: %d %s", rec.Code, rec.Body.String())
	}
	if f.roll.Key != "roll-1" || f.roll.SiteID != site || f.roll.VersionID != someID || f.roll.Options.Mode != domain.ModeMultilevel || f.roll.Options.View != domain.ViewLegal {
		t.Fatalf("store got %+v", f.roll)
	}
	if len(f.roll.Run.Results) != 1 || f.roll.Run.Results[0].Unit.String() != "4" {
		t.Fatalf("rolled results %+v", f.roll.Run.Results)
	}
}
