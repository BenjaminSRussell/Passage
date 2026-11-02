package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const (
	defaultVendor = "00000000-0000-0000-0000-0000000000f1"
	inlineVendor  = "00000000-0000-0000-0000-0000000000f2"
)

func purchaseBody(extra string) string {
	return strings.Replace(runBody, `"production"`, `"purchase"`+extra, 1)
}

// FR-22.3: a planned purchase takes the default vendor of the item's stored
// coverage; the run input's vendorId wins over it.
func TestRun_DefaultVendorFromStoredCoverage(t *testing.T) {
	c := requirementCoverage()
	c.PlannedOrderType, c.VendorID = "purchase", defaultVendor
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: c}}
	_, req := runVersion(t, st, bareBody, "k-vendor-stored")
	o := onlyOrder(t, req)
	if o.OrderType != domain.OrderPurchase || o.VendorID != defaultVendor {
		t.Fatalf("order %+v; want a purchase with the stored default vendor", o)
	}
	for _, m := range req.Result.Messages {
		if m.Action == domain.ActionNoVendor {
			t.Fatalf("message %+v for an order with a vendor", m)
		}
	}
	st = &fakeStore{runCovs: map[string]store.RunCoverage{fgID: c}}
	_, req = runVersion(t, st, strings.Replace(bareBody, `"onHand"`, `"vendorId":"`+inlineVendor+`","onHand"`, 1), "k-vendor-inline")
	if o := onlyOrder(t, req); o.VendorID != inlineVendor {
		t.Fatalf("order %+v; want the inline vendor", o)
	}
}

// PLAN-0008: no quota source and no default vendor leaves the order planned,
// with one warning for the item carrying the vendorless quantity.
func TestRun_PurchaseWithoutVendorRaisesPlan0008(t *testing.T) {
	_, req := runVersion(t, &fakeStore{}, purchaseBody(""), "k-no-vendor")
	o := onlyOrder(t, req)
	if o.VendorID != "" {
		t.Fatalf("order %+v", o)
	}
	var got []domain.Message
	for _, m := range req.Result.Messages {
		if m.Action == domain.ActionNoVendor {
			got = append(got, m)
		}
	}
	if len(got) != 1 || got[0].ItemID != fgID || got[0].SupplyRef != o.Number || !got[0].Qty.Equal(o.Qty) {
		t.Fatalf("messages %+v; want one PLAN-0008 for %s", req.Result.Messages, o.Number)
	}
	// A production order needs no vendor.
	_, req = runVersion(t, &fakeStore{}, runBody, "k-production")
	for _, m := range req.Result.Messages {
		if m.Action == domain.ActionNoVendor {
			t.Fatalf("message %+v for a production order", m)
		}
	}
}

// A quota source's vendor is kept ahead of the default vendor.
func TestRun_QuotaVendorWinsOverDefaultVendor(t *testing.T) {
	c := requirementCoverage()
	c.PlannedOrderType, c.VendorID = "purchase", defaultVendor
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: c}, runQuotas: []domain.RunQuota{purchaseQuota(false)}}
	_, req := runVersion(t, st, bareBody, "k-vendor-quota")
	o := onlyOrder(t, req)
	if o.QuotaSourceID == "" || o.VendorID == "" || o.VendorID == defaultVendor {
		t.Fatalf("order %+v; want the quota source's vendor", o)
	}
}

func TestRun_RejectsMalformedVendor(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", purchaseBody(`,"vendorId":"v-1"`), "planning.run", true, "k-bad-vendor")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "items[0].vendorId") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

// A simulation stamps the stored default vendor too (FR-22.15 reads the same
// snapshot), and a purchase left without one raises PLAN-0008 in its version.
func TestScenarioRun_DefaultVendorAndPlan0008(t *testing.T) {
	c := requirementCoverage()
	c.PlannedOrderType, c.VendorID = "purchase", defaultVendor
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: c}}
	if rec := do(t, st, http.MethodPost, scenarioPath, upBody, dutySim, true, ""); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, st, http.MethodPost, scenarioPath+"/UP20:run", bareBody, dutySim, true, "sim-vendor"); rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	if orders := st.lastSim.Result.Orders; len(orders) != 1 || orders[0].VendorID != defaultVendor {
		t.Fatalf("simulated orders %+v; want the stored default vendor", orders)
	}
	c.VendorID = ""
	st.runCovs = map[string]store.RunCoverage{fgID: c}
	if rec := do(t, st, http.MethodPost, scenarioPath+"/UP20:run", bareBody, dutySim, true, "sim-no-vendor"); rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	var got int
	for _, m := range st.lastSim.Result.Messages {
		if m.Action == domain.ActionNoVendor {
			got++
		}
	}
	if got != 1 || st.lastSim.Result.Orders[0].VendorID != "" {
		t.Fatalf("simulation %+v; want one PLAN-0008 and a vendorless order", st.lastSim.Result)
	}
}
