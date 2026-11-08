package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func (f *fakeStore) ListLegalEntitySettings(context.Context) ([]store.LegalEntitySetting, error) {
	return []store.LegalEntitySetting{{LegalEntityID: ids.New()}}, nil
}

func (f *fakeStore) GetLegalEntitySetting(_ context.Context, id string) (store.LegalEntitySetting, error) {
	return store.LegalEntitySetting{LegalEntityID: id, CogsAtShipment: true, Version: 2}, nil
}

var savedEntitySetting store.LegalEntitySettingInput

func (f *fakeStore) SaveLegalEntitySetting(_ context.Context, id string, version int, in store.LegalEntitySettingInput, idem store.Idem) (store.LegalEntitySetting, error) {
	f.lastIdem = idem
	savedEntitySetting = in
	if version != 2 {
		return store.LegalEntitySetting{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "stale")
	}
	return store.LegalEntitySetting{LegalEntityID: id, CogsAtShipment: in.CogsAtShipment, AsnEnabled: in.AsnEnabled, Version: 3}, nil
}

func TestLegalEntitySetting_ReadSaveValidateAndDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	le := ids.New()
	path := "/v1/fulfillment/legal-entity-settings/" + le
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/legal-entity-settings", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cogsAtShipment") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, path, "", nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), `"cogsAtShipment":true`) {
		t.Fatalf("get: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/legal-entity-settings/nope", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	body := `{"cogsAtShipment":true,"asnEnabled":false}`
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"Idempotency-Key": "e-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"x"`, "Idempotency-Key": "e-1"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	for _, bad := range []string{`{"asnEnabled":true}`, `{"cogsAtShipment":true}`} {
		rec := do(t, h, http.MethodPut, path, bad, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "e-2"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"1"`, "Idempotency-Key": "e-3"}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "e-4"})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || !savedEntitySetting.CogsAtShipment || savedEntitySetting.AsnEnabled {
		t.Fatalf("save: %d %q %s %+v", rec.Code, rec.Header().Get("ETag"), rec.Body.String(), savedEntitySetting)
	}
	if savedEntitySetting.CanChangeCogs {
		t.Fatalf("a supervisor without %s may not change cogs_at_shipment: %+v", store.DutyCogsPolicy, savedEntitySetting)
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "e-6",
		"X-Passage-Duties": "fulfillment.warehouse.release," + store.DutyCogsPolicy}); rec.Code != http.StatusOK || !savedEntitySetting.CanChangeCogs {
		t.Fatalf("save with the ledger setup duty: %d %+v", rec.Code, savedEntitySetting)
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "e-5", "X-Passage-Duties": "fulfillment.warehouse.view"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer save: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "e-7", "X-Passage-Duties": store.DutyCogsPolicy}); rec.Code != http.StatusForbidden {
		t.Fatalf("ledger setup alone (no fulfillment.warehouse.release) save: %d", rec.Code)
	}
}

func TestLegalEntitySetting_GS1PrefixAndExtensionAreValidated(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/legal-entity-settings/" + ids.New()
	hdr := map[string]string{"If-Match": `"2"`, "Idempotency-Key": "g-1"}
	for _, extra := range []string{`,"gs1CompanyPrefix":"12345"`, `,"gs1CompanyPrefix":"1234567890123"`, `,"gs1CompanyPrefix":"12345A7"`,
		`,"ssccExtensionDigit":10`, `,"ssccExtensionDigit":-1`, `,"packagingFeeBillable":null`, `,"ssccExtensionDigit":"5"`} {
		if rec := do(t, h, http.MethodPut, path, `{"cogsAtShipment":false,"asnEnabled":false`+extra+`}`, hdr); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", extra, rec.Code, rec.Body.String())
		}
	}
	ok := `{"cogsAtShipment":false,"asnEnabled":false,"gs1CompanyPrefix":"9876543","ssccExtensionDigit":5,"packagingFeeBillable":true}`
	if rec := do(t, h, http.MethodPut, path, ok, hdr); rec.Code != http.StatusOK {
		t.Fatalf("valid: %d %s", rec.Code, rec.Body.String())
	}
	if g, d, f := savedEntitySetting.GS1CompanyPrefix, savedEntitySetting.SSCCExtensionDigit, savedEntitySetting.PackagingFeeBillable; !g.Set || g.Value == nil || *g.Value != "9876543" ||
		!d.Set || d.Value == nil || *d.Value != 5 || !f.Set || !f.Value {
		t.Fatalf("valid: %+v", savedEntitySetting)
	}
	// Left out, the saved values stay (a client that predates the fields).
	hdr["Idempotency-Key"] = "g-2"
	if rec := do(t, h, http.MethodPut, path, `{"cogsAtShipment":false,"asnEnabled":false}`, hdr); rec.Code != http.StatusOK ||
		savedEntitySetting.GS1CompanyPrefix.Set || savedEntitySetting.SSCCExtensionDigit.Set || savedEntitySetting.PackagingFeeBillable.Set {
		t.Fatalf("left out: %d %+v", rec.Code, savedEntitySetting)
	}
	// Null returns the prefix and digit to the process default.
	hdr["Idempotency-Key"] = "g-3"
	if rec := do(t, h, http.MethodPut, path, `{"cogsAtShipment":false,"asnEnabled":false,"gs1CompanyPrefix":null,"ssccExtensionDigit":null}`, hdr); rec.Code != http.StatusOK ||
		!savedEntitySetting.GS1CompanyPrefix.Set || savedEntitySetting.GS1CompanyPrefix.Value != nil || !savedEntitySetting.SSCCExtensionDigit.Set || savedEntitySetting.SSCCExtensionDigit.Value != nil {
		t.Fatalf("null: %d %+v", rec.Code, savedEntitySetting)
	}
}

func TestLegalEntitySetting_RTPFieldsAreValidated(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/legal-entity-settings/" + ids.New()
	hdr := map[string]string{"If-Match": `"2"`, "Idempotency-Key": "r-1"}
	for _, extra := range []string{`,"rtpChargeDays":29`, `,"rtpChargeDays":366`, `,"rtpChargeDays":null`, `,"rtpChargeDays":"90"`, `,"rtpTrackingEnabled":null`, `,"rtpTrackingEnabled":"yes"`} {
		if rec := do(t, h, http.MethodPut, path, `{"cogsAtShipment":false,"asnEnabled":false`+extra+`}`, hdr); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", extra, rec.Code, rec.Body.String())
		}
	}
	ok := `{"cogsAtShipment":false,"asnEnabled":false,"rtpTrackingEnabled":false,"rtpChargeDays":30}`
	if rec := do(t, h, http.MethodPut, path, ok, hdr); rec.Code != http.StatusOK {
		t.Fatalf("valid: %d %s", rec.Code, rec.Body.String())
	}
	if e, d := savedEntitySetting.RTPTrackingEnabled, savedEntitySetting.RTPChargeDays; !e.Set || e.Value || !d.Set || d.Value != 30 {
		t.Fatalf("valid: %+v", savedEntitySetting)
	}
	hdr["Idempotency-Key"] = "r-2"
	if rec := do(t, h, http.MethodPut, path, `{"cogsAtShipment":false,"asnEnabled":false}`, hdr); rec.Code != http.StatusOK ||
		savedEntitySetting.RTPTrackingEnabled.Set || savedEntitySetting.RTPChargeDays.Set {
		t.Fatalf("left out: %d %+v", rec.Code, savedEntitySetting)
	}
}

// A post that names a flag passes it as given; one that omits it leaves the
// choice to the legal entity setting.
func TestPostPackingSlip_OmittedFlagsTakeTheSetting(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/fulfillment/shipments/" + ids.New() + ":post-packing-slip"
	cost := `"costs":[{"shipmentLineId":"` + ids.New() + `","unitCost":"1","costSnapshotId":"s"}]`
	hdr := map[string]string{"Idempotency-Key": "post-set", "If-Match": `"3"`}
	if rec := do(t, h, http.MethodPost, path, `{"postingDate":"2026-10-21","currency":"USD",`+cost+`}`, hdr); rec.Code != http.StatusCreated ||
		!fs.lastPost.CogsFromSetting || !fs.lastPost.AsnFromSetting || fs.lastPost.CogsAtShipment || fs.lastPost.EmitASN {
		t.Fatalf("omitted: %d %+v", rec.Code, fs.lastPost)
	}
	hdr["Idempotency-Key"] = "post-set-2"
	if rec := do(t, h, http.MethodPost, path, `{"postingDate":"2026-10-21","currency":"USD","cogsAtShipment":false,"asnEnabled":true,`+cost+`}`, hdr); rec.Code != http.StatusCreated ||
		fs.lastPost.CogsFromSetting || fs.lastPost.AsnFromSetting || fs.lastPost.CogsAtShipment || !fs.lastPost.EmitASN {
		t.Fatalf("named: %d %+v", rec.Code, fs.lastPost)
	}
}
