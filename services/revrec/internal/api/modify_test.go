package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

// modifyFake records what the handler forwards; the SQL and the arithmetic are covered by the store and domain tests.
type modifyFake struct {
	*fakeStore
	got store.ModificationRequest
	id  string
}

func (f *modifyFake) ModifyContract(_ context.Context, id string, req store.ModificationRequest) (store.Contract, store.Modification, error) {
	f.id, f.got = id, req
	c := f.contracts[id]
	c.Version++
	return c, store.Modification{ID: "m1", Type: req.Type, EffectiveDate: req.EffectiveDate, PriceBefore: c.TransactionPrice, PriceAfter: c.TransactionPrice.Add(req.PriceChange)}, nil
}

func (f *fakeStore) ModifyContract(context.Context, string, store.ModificationRequest) (store.Contract, store.Modification, error) {
	return store.Contract{}, store.Modification{}, nil
}

func TestModifyContract_ValidatesAndForwards(t *testing.T) {
	f := &modifyFake{fakeStore: newFakeStore()}
	h := newHandler(f)
	created := callAs(t, h, http.MethodPost, "/v1/revrec/contracts", bundleContract, allDuties, entityID, map[string]string{idempotencyKey: idemKey})
	var c contractOut
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &c) != nil {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	path := "/v1/revrec/contracts/" + c.ID + ":modify"
	support := c.Obligations[2].ID
	good := `{"type":"CATCH_UP","effectiveDate":"2027-06-30","transactionPriceChange":"-100.00","obligations":[{"obligationId":"` + support + `","ssp":"2600"}],"reason":"price list change"}`

	if rec := callAs(t, h, http.MethodPost, path, good, "revrec.view", entityID, nil); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer may not modify: %d", rec.Code)
	}
	for name, body := range map[string]string{
		"bad type":        `{"type":"WHOLE","effectiveDate":"2027-06-30","transactionPriceChange":"10"}`,
		"bad date":        `{"type":"CATCH_UP","effectiveDate":"June","transactionPriceChange":"10"}`,
		"bad change":      `{"type":"CATCH_UP","effectiveDate":"2027-06-30","transactionPriceChange":"ten"}`,
		"nothing changed": `{"type":"CATCH_UP","effectiveDate":"2027-06-30","transactionPriceChange":"0"}`,
		"bad ssp":         `{"type":"CATCH_UP","effectiveDate":"2027-06-30","transactionPriceChange":"0","obligations":[{"obligationId":"` + support + `","ssp":"0"}]}`,
		"bad obligation":  `{"type":"CATCH_UP","effectiveDate":"2027-06-30","transactionPriceChange":"0","obligations":[{"obligationId":"x","ssp":"5"}]}`,
	} {
		if rec := callAs(t, h, http.MethodPost, path, body, allDuties, entityID, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	rec := callAs(t, h, http.MethodPost, path, good, allDuties, entityID, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("modify: %d %s", rec.Code, rec.Body.String())
	}
	if f.id != c.ID || f.got.Type != "CATCH_UP" || f.got.PriceChange.String() != "-100" || f.got.ExpectedVersion != 1 || f.got.NewSSP[support].String() != "2600" || f.got.Reason != "price list change" {
		t.Errorf("forwarded %+v for %s", f.got, f.id)
	}
	var out modifyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Modification.ID != "m1" || out.Modification.PriceAfter != "10700.00" || out.Modification.EffectiveDate != "2027-06-30" {
		t.Errorf("response %s (%v)", rec.Body.String(), err)
	}
	if rec := callAs(t, h, http.MethodPost, "/v1/revrec/contracts/nope:modify", good, allDuties, entityID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown contract: %d", rec.Code)
	}
}
