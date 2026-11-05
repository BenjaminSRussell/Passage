package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const releaseID = "0190f0a0-0000-7000-8000-0000000000f3"

// ReleaseLoad fills the releaser from the principal in ctx and moves the load
// to TENDERED, as the Postgres store does.
func (f *fakeStore) ReleaseLoad(ctx context.Context, in domain.LoadRelease, version int, idem store.Idem, now time.Time) (domain.LoadRelease, error) {
	f.release, f.releaseVer, f.releaseIdem = in, version, idem
	if f.releaseErr != nil {
		return domain.LoadRelease{}, f.releaseErr
	}
	p, _ := tenant.FromContext(ctx)
	in.ID, in.LegalEntityID, in.ReleasedBy, in.LegalEntityScope, in.ReleasedAt = releaseID, p.LegalEntityID, p.UserID, tenant.LegalEntityScopeList(p), now
	in.LoadStatus, in.LoadVersion = domain.LoadTendered, version+1
	return in, nil
}

// releaseLoad posts a release with the load version as If-Match.
func releaseLoad(t *testing.T, h http.Handler, body string, header map[string]string, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	h2 := map[string]string{"If-Match": `"1"`}
	for k, v := range header {
		if v == "" {
			delete(h2, k)
			continue
		}
		h2[k] = v
	}
	return doWith(t, h, http.MethodPost, base+"/loads/"+loadID+":release", body, h2, duties...)
}

func TestReleaseLoad_PassesTheCascadeAndAnswersAccepted(t *testing.T) {
	now := t0
	f := &fakeStore{}
	h := clockRouter(f, &now)
	body := `{"carriers":[{"carrierId":"` + carrierA + `","rateQuoted":"120.5"},{"carrierId":"` + carrierB + `"}],"currency":"USD","timeoutMin":45}`
	rec := releaseLoad(t, h, body, map[string]string{"Idempotency-Key": "release-1", "If-Match": `W/"4"`}, api.DutyTender)
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
	// The response carries the load's status after the release.
	if got.LoadStatus != domain.LoadTendered || got.LoadVersion != 5 || f.releaseVer != 4 {
		t.Fatalf("load after release = %s v%d, If-Match passed %d", got.LoadStatus, got.LoadVersion, f.releaseVer)
	}
	if f.releaseIdem.Key != "release-1" || f.releaseIdem.Fingerprint == "" {
		t.Fatalf("idempotency = %+v", f.releaseIdem)
	}
}

func TestReleaseLoad_DefaultsTheTimeout(t *testing.T) {
	f := &fakeStore{}
	rec := releaseLoad(t, router(f), `{"carriers":[{"carrierId":"`+carrierA+`"}]}`, nil, api.DutyTender)
	if rec.Code != http.StatusAccepted || f.release.TimeoutMin != 120 || f.release.Currency != "" {
		t.Fatalf("status = %d, release %+v", rec.Code, f.release)
	}
}

// The load version is required: 428 without If-Match, 400 when it is not a version.
func TestReleaseLoad_RequiresIfMatch(t *testing.T) {
	body := `{"carriers":[{"carrierId":"` + carrierA + `"}]}`
	f := &fakeStore{}
	rec := releaseLoad(t, router(f), body, map[string]string{"If-Match": ""}, api.DutyTender)
	if rec.Code != http.StatusPreconditionRequired || problem(t, rec).Code != "COM-0428" {
		t.Fatalf("no If-Match = %d %s", rec.Code, rec.Body.String())
	}
	rec = releaseLoad(t, router(f), body, map[string]string{"If-Match": `"latest"`}, api.DutyTender)
	if rec.Code != http.StatusBadRequest || problem(t, rec).Errors["If-Match"] == "" {
		t.Fatalf("bad If-Match = %d %s", rec.Code, rec.Body.String())
	}
	if f.release.LoadID != "" {
		t.Fatal("the store was called without a load version")
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
		rec := releaseLoad(t, router(f), body, nil, api.DutyTender)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, body %s", name, rec.Code, rec.Body.String())
		}
		if f.release.LoadID != "" {
			t.Errorf("%s: the store was called", name)
		}
	}
	if rec := doWith(t, router(&fakeStore{}), http.MethodPost, base+"/loads/NOT-A-UUID:release", `{}`, map[string]string{"If-Match": `"1"`}, api.DutyTender); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad load id = %d", rec.Code)
	}
}

func TestReleaseLoad_PassesStoreRefusals(t *testing.T) {
	body := `{"carriers":[{"carrierId":"` + carrierA + `"}]}`
	for name, c := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"open tender":      {conflict(domain.CodeTenderOpen), http.StatusConflict, domain.CodeTenderOpen},
		"already tendered": {conflict(domain.CodeLoadState), http.StatusConflict, domain.CodeLoadState},
		"stale version":    {httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "changed"), http.StatusPreconditionFailed, store.CodeVersionConflict},
		"failed DG check":  {httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeDGSegregation, "dg"), http.StatusUnprocessableEntity, domain.CodeDGSegregation},
		"unknown load":     {store.NotFound("load"), http.StatusNotFound, domain.CodeNotFound},
	} {
		rec := releaseLoad(t, router(&fakeStore{releaseErr: c.err}), body, nil, api.DutyTender)
		if rec.Code != c.status || problem(t, rec).Code != c.code {
			t.Errorf("%s: status = %d, body %s", name, rec.Code, rec.Body.String())
		}
	}
	// Neither the planning nor the carrier-response duty releases a load.
	if rec := releaseLoad(t, router(&fakeStore{}), body, nil, api.DutyPlan, api.DutyRespond); rec.Code != http.StatusForbidden {
		t.Fatalf("without transport.load.tender = %d, want 403", rec.Code)
	}
}
