package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const voyageBody = `{"reference":" MAEU-2611E ","currency":"usd","status":"POSTED",
	"legs":[{"from":"cnsha","to":"uslax","mode":"ocean"}],
	"containers":[{"number":"csqu3054383","type":"40HC","seal":"778812"}],
	"folios":[{"folioNo":"F-1","forwarder":"Kuehne","containers":["CSQU3054383"]}],
	"costLines":[{"costType":"freight","amount":"1000.00","allocationMethod":"WEIGHT"}],
	"receiptLines":[{"line":1,"value":"100","weight":"1"},{"line":2,"value":"100","weight":"1"},{"line":3,"value":"250","weight":"1"}],
	"allocations":[{"line":1,"amount":"1000"}]}`

func TestCreateVoyage_NormalisesAndIgnoresServerFields(t *testing.T) {
	f := &fakeStore{}
	rec := doWith(t, router(f), http.MethodPost, base+"/voyages", voyageBody, map[string]string{"Idempotency-Key": "v-1"}, api.DutyLandedCost)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	v := f.createdVoyage
	if v.Reference != "MAEU-2611E" || v.Currency != "USD" || v.Containers[0].Number != "CSQU3054383" || v.Legs[0].Seq != 1 || v.CostLines[0].CostType != "FREIGHT" {
		t.Fatalf("voyage not normalised: %+v", v)
	}
	if v.Status != "" || v.Allocations != nil {
		t.Fatalf("client set server-owned fields: %+v", v)
	}
	if len(f.idems) != 1 || f.idems[0].Key != "v-1" {
		t.Fatalf("idempotency = %+v", f.idems)
	}
}

// TestCreateVoyage_AC3410: CSQU3054384 fails the ISO 6346 check digit with TMS-0124.
func TestCreateVoyage_AC3410(t *testing.T) {
	body := strings.ReplaceAll(voyageBody, "csqu3054383", "CSQU3054384")
	body = strings.ReplaceAll(body, `"containers":["CSQU3054383"]`, `"containers":["CSQU3054384"]`)
	f := &fakeStore{}
	rec := do(t, router(f), http.MethodPost, base+"/voyages", body, api.DutyLandedCost)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	p := problem(t, rec)
	if p.Code != domain.CodeContainerCheck || p.Errors["containers[0].number"] == "" {
		t.Fatalf("problem = %+v", p)
	}
	if f.createdVoyage.Reference != "" {
		t.Fatal("an invalid voyage reached the store")
	}
}

func TestCreateVoyage_ValidationFields(t *testing.T) {
	body := strings.Replace(voyageBody, `"amount":"1000.00"`, `"amount":"10.005"`, 1)
	rec := do(t, router(&fakeStore{}), http.MethodPost, base+"/voyages", body, api.DutyLandedCost)
	if rec.Code != http.StatusBadRequest || problem(t, rec).Errors["costLines[0]"] == "" {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func estimatedVoyage() domain.Voyage {
	return domain.Voyage{ID: voyageID, Reference: "MAEU-2611E", Currency: "USD", Status: domain.VoyageEstimated,
		CostLines: []domain.VoyageCostLine{{CostType: "FREIGHT", Amount: dec("1000.00"), AllocationMethod: domain.MethodWeight}},
		ReceiptLines: []domain.VoyageReceiptLine{
			{Line: 1, Value: dec("100"), Weight: dec("1")}, {Line: 2, Value: dec("100"), Weight: dec("1")}, {Line: 3, Value: dec("250"), Weight: dec("1")},
		}}
}

// TestAllocateVoyage_AC346 returns the posted voyage with 333.33 / 333.33 / 333.34.
func TestAllocateVoyage_AC346(t *testing.T) {
	rec := do(t, router(&fakeStore{voyage: estimatedVoyage()}), http.MethodPost, base+"/landed-cost/voyages/"+voyageID+":allocate", ``, api.DutyLandedCost)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var v struct {
		Status      string
		Allocations []struct {
			Line   int
			Amount string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != domain.VoyagePosted || len(v.Allocations) != 3 || v.Allocations[2].Amount != "333.34" || v.Allocations[0].Amount != "333.33" {
		t.Fatalf("voyage = %+v", v)
	}
}

func TestVoyageReads(t *testing.T) {
	h := router(&fakeStore{voyage: estimatedVoyage()})
	if rec := do(t, h, http.MethodGet, base+"/voyages/"+voyageID, ``, api.DutyLandedCost); rec.Code != http.StatusOK {
		t.Fatalf("get = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, base+"/voyages?status=COSTED", ``, api.DutyLandedCost); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, base+"/voyages/nope", ``, api.DutyLandedCost); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id = %d", rec.Code)
	}
	missing := router(&fakeStore{voyageErr: store.NotFound("landed-cost voyage")})
	rec := do(t, missing, http.MethodPost, base+"/landed-cost/voyages/"+voyageID+":allocate", ``, api.DutyLandedCost)
	if rec.Code != http.StatusNotFound || problem(t, rec).Code != domain.CodeNotFound {
		t.Fatalf("missing = %d %s", rec.Code, rec.Body.String())
	}
}
