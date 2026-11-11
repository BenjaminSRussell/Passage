package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

func (f *fakeStore) GetFXSetting(_ context.Context, le string) (store.FXSetting, error) {
	if f.fx.LegalEntityID == le {
		return f.fx, nil
	}
	return store.DefaultFXSetting(le), nil
}

func (f *fakeStore) PutFXSetting(_ context.Context, in store.FXSetting) (store.FXSetting, error) {
	f.fx = in
	return in, nil
}

func (f *fakeStore) ReversePayment(_ context.Context, id string, version int) (store.Payment, error) {
	f.reversed, f.lastVersion = id, version
	return store.Payment{ID: id, Status: "REVERSED", Version: version + 1}, nil
}

func TestFXSettingRoutes(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	put := func(body, duty string) int {
		return do(h, http.MethodPut, "/v1/receivables/fx-settings", body, duty, nil).Code
	}
	good := `{"legalEntityId":"` + testLE + `","crossCurrencyEnabled":true,"functionalCurrency":"USD"}`
	if code := put(good, "receivables.view"); code != http.StatusForbidden {
		t.Errorf("a viewer may not write: %d", code)
	}
	for name, body := range map[string]string{
		"enabled without a functional currency": `{"legalEntityId":"` + testLE + `","crossCurrencyEnabled":true}`,
		"lower case currency":                   `{"legalEntityId":"` + testLE + `","functionalCurrency":"usd"}`,
		"bad account":                           `{"legalEntityId":"` + testLE + `","functionalCurrency":"USD","gainAccount":"71 00"}`,
		"no legal entity":                       `{"functionalCurrency":"USD"}`,
	} {
		if code := put(body, "receivables.fxsetting.manage"); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
	rec := do(h, http.MethodPut, "/v1/receivables/fx-settings", good, "receivables.fxsetting.manage", nil)
	var got store.FXSetting
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.GainAccount != "7100" || got.LossAccount != "7200" || !got.CrossCurrencyEnabled {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(h, http.MethodGet, "/v1/receivables/fx-settings?legalEntityId="+testLE, "", "receivables.view", nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.FunctionalCurrency != "USD" {
		t.Errorf("get: %d %s", rec.Code, rec.Body.String())
	}
	if rec = do(h, http.MethodGet, "/v1/receivables/fx-settings", "", "receivables.view", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("get without a legal entity: %d", rec.Code)
	}
}

func TestReversePayment_NeedsItsOwnDutyAndAVersion(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	id := ids.New()
	path := "/v1/receivables/payments/" + id + ":reverse"
	if rec := do(h, http.MethodPost, path, "", "receivables.payment.post", map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusForbidden {
		t.Errorf("the payment duty may not reverse: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, path, "", "receivables.payment.reverse", nil); rec.Code == http.StatusOK {
		t.Errorf("no If-Match was accepted")
	}
	rec := do(h, http.MethodPost, path, "", "receivables.payment.reverse", map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || f.reversed != id || f.lastVersion != 3 {
		t.Errorf("reverse: %d %s (%s v%d)", rec.Code, rec.Body.String(), f.reversed, f.lastVersion)
	}
}

func TestApplyPayment_ItemAmountReachesTheStore(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	body := `{"settlements":[{"openItemId":"` + ids.New() + `","amount":"90.00","itemAmount":"100.00"}],"onAccount":"0"}`
	rec := do(h, http.MethodPost, "/v1/receivables/payments/"+ids.New()+":apply", body, "receivables.payment.post", map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || len(f.lastPlan.Allocations) != 1 || f.lastPlan.Allocations[0].ItemCurrencyAmount.String() != "100" {
		t.Fatalf("apply: %d %s %+v", rec.Code, rec.Body.String(), f.lastPlan)
	}
}
