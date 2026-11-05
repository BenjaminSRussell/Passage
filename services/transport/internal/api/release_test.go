package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const releaseID = "0190f0a0-0000-7000-8000-0000000000f3"

// ReleaseLoad fills the releaser from the principal in ctx, as the Postgres store does.
func (f *fakeStore) ReleaseLoad(ctx context.Context, in domain.LoadRelease, idem store.Idem, now time.Time) (domain.LoadRelease, error) {
	f.release, f.releaseIdem = in, idem
	if f.releaseErr != nil {
		return domain.LoadRelease{}, f.releaseErr
	}
	p, _ := tenant.FromContext(ctx)
	in.ID, in.LegalEntityID, in.ReleasedBy, in.LegalEntityScope, in.ReleasedAt = releaseID, p.LegalEntityID, p.UserID, tenant.LegalEntityScopeList(p), now
	return in, nil
}

func TestReleaseLoad_PassesTheCascadeAndAnswersAccepted(t *testing.T) {
	now := t0
	f := &fakeStore{}
	h := clockRouter(f, &now)
	body := `{"carriers":[{"carrierId":"` + carrierA + `","rateQuoted":"120.5"},{"carrierId":"` + carrierB + `"}],"currency":"USD","timeoutMin":45}`
	rec := doWith(t, h, http.MethodPost, base+"/loads/"+loadID+":release", body, map[string]string{"Idempotency-Key": "release-1"}, api.DutyTender)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got domain.LoadRelease
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []domain.ReleasedCarrier{{CarrierID: carrierA, RateQuoted: "120.50"}, {CarrierID: carrierB}}
	if len(got.Carriers) != 2 || got.Carriers[0] != want[0] || got.Carriers[1] != want[1] || got.Currency != "USD" || got.TimeoutMin != 45 {
		t.Fatalf("release = %+v", got)
	}
	if got.ID != releaseID || got.LoadID != loadID || got.ReleasedBy != "user-test" || len(got.LegalEntityScope) != 1 || !got.ReleasedAt.Equal(t0) {
		t.Fatalf("release = %+v", got)
	}
	if f.releaseIdem.Key != "release-1" || f.releaseIdem.Fingerprint == "" {
		t.Fatalf("idempotency = %+v", f.releaseIdem)
	}
}

func TestReleaseLoad_DefaultsTheTimeout(t *testing.T) {
	f := &fakeStore{}
	rec := do(t, router(f), http.MethodPost, base+"/loads/"+loadID+":release", `{"carriers":[{"carrierId":"`+carrierA+`"}]}`, api.DutyTender)
	if rec.Code != http.StatusAccepted || f.release.TimeoutMin != 120 || f.release.Currency != "" {
		t.Fatalf("status = %d, release %+v", rec.Code, f.release)
	}
}

// The releaser and the scope are the caller's; a body that names them is refused.
func TestReleaseLoad_RefusesBadCascadesAndReleaserFields(t *testing.T) {
	for name, body := range map[string]string{
		"no carriers":         `{"carriers":[]}`,
		"duplicate carrier":   `{"carriers":[{"carrierId":"` + carrierA + `"},{"carrierId":"` + carrierA + `"}]}`,
		"currency, no rates":  `{"carriers":[{"carrierId":"` + carrierA + `"}],"currency":"USD"}`,
		"rates, no currency":  `{"carriers":[{"carrierId":"` + carrierA + `","rateQuoted":"10"}]}`,
		"timeout too short":   `{"carriers":[{"carrierId":"` + carrierA + `"}],"timeoutMin":4}`,
		"releasedBy in body":  `{"carriers":[{"carrierId":"` + carrierA + `"}],"releasedBy":"someone-else"}`,
		"scope in body":       `{"carriers":[{"carrierId":"` + carrierA + `"}],"legalEntityScope":["00000000-0000-0000-0000-0000000000e2"]}`,
		"upper-case carrier":  `{"carriers":[{"carrierId":"0190F0A0-0000-7000-8000-0000000000C2"}]}`,
		"rate with 3 decimal": `{"carriers":[{"carrierId":"` + carrierA + `","rateQuoted":"1.005"}],"currency":"USD"}`,
	} {
		f := &fakeStore{}
		rec := do(t, router(f), http.MethodPost, base+"/loads/"+loadID+":release", body, api.DutyTender)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, body %s", name, rec.Code, rec.Body.String())
		}
		if f.release.LoadID != "" {
			t.Errorf("%s: the store was called", name)
		}
	}
	if rec := do(t, router(&fakeStore{}), http.MethodPost, base+"/loads/NOT-A-UUID:release", `{}`, api.DutyTender); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad load id = %d", rec.Code)
	}
}

func TestReleaseLoad_PassesStoreRefusals(t *testing.T) {
	body := `{"carriers":[{"carrierId":"` + carrierA + `"}]}`
	rec := do(t, router(&fakeStore{releaseErr: conflict(domain.CodeTenderOpen)}), http.MethodPost, base+"/loads/"+loadID+":release", body, api.DutyTender)
	if rec.Code != http.StatusConflict || problem(t, rec).Code != domain.CodeTenderOpen {
		t.Fatalf("open tender: status = %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, router(&fakeStore{releaseErr: store.NotFound("load")}), http.MethodPost, base+"/loads/"+loadID+":release", body, api.DutyTender)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown load: status = %d", rec.Code)
	}
	// Neither the planning nor the carrier-response duty releases a load.
	if rec := do(t, router(&fakeStore{}), http.MethodPost, base+"/loads/"+loadID+":release", body, api.DutyPlan, api.DutyRespond); rec.Code != http.StatusForbidden {
		t.Fatalf("without transport.load.tender = %d, want 403", rec.Code)
	}
}
