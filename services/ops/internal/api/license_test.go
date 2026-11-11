package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
	"github.com/BenjaminSRussell/passage/services/ops/migrations"
)

func licenseEnv(t *testing.T, now time.Time) (*env, ed25519.PrivateKey) {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouterWithoutTenant()
	operator := ids.New()
	New(st, Config{MaxLagSeconds: 300, OperatorTenantID: operator, LicensePublicKey: pub, Now: func() time.Time { return now }}).Register(r)
	return &env{router: r, tenant: operator, pool: pool, admin: admin}, priv
}

func signedLicense(priv ed25519.PrivateKey, tenantID, expires string, full int) string {
	payload := `{"tenantId":"` + tenantID + `","issuedAt":"2026-01-01","expiresAt":"` + expires + `","fullUsers":` + string(rune('0'+full)) + `,"operationalUsers":5,"selfserviceUsers":50}`
	b, _ := json.Marshal(map[string]string{"payload": base64.StdEncoding.EncodeToString([]byte(payload)), "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(payload)))})
	return string(b)
}

func (e *env) put(path, user, duties, ifMatch, body string) *httptestRecorder {
	return e.doWith("PUT", path, user, duties, map[string]string{"If-Match": ifMatch}, body)
}

func TestLicenseInstallVerifiesSignatureAndVersions(t *testing.T) {
	e, priv := licenseEnv(t, time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC))
	tid := ids.New()
	path := "/v1/ops/tenants/" + tid + "/license"
	body := signedLicense(priv, tid, "2026-12-31", 9)

	if rec := e.put(path, "alice", DutyView, "0", body); rec.Code != http.StatusForbidden {
		t.Fatalf("install needs its duty: %d", rec.Code)
	}
	if rec := e.do("PUT", path, "alice", DutyLicenseInstall, "", body); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("If-Match is required: %d %s", rec.Code, rec.Body)
	}
	// a licence signed with another key, a changed payload and another tenant's licence are all refused
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if rec := e.put(path, "alice", DutyLicenseInstall, "0", signedLicense(other, tid, "2026-12-31", 9)); rec.Code != http.StatusUnprocessableEntity || code(t, rec) != domain.CodeLicenseSignature {
		t.Fatalf("wrong key: %d %s", rec.Code, rec.Body)
	}
	if rec := e.put(path, "alice", DutyLicenseInstall, "0", signedLicense(priv, ids.New(), "2026-12-31", 9)); rec.Code != http.StatusUnprocessableEntity || code(t, rec) != domain.CodeLicenseInvalid {
		t.Fatalf("another tenant's licence: %d %s", rec.Code, rec.Body)
	}
	if rec := e.put(path, "alice", DutyLicenseInstall, "0", `{"payload":"!!","signature":"!!"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("not base64: %d", rec.Code)
	}
	if rec := e.get("/v1/ops/tenants/"+tid+"/license", "bob", DutyView); rec.Code != http.StatusNotFound || code(t, rec) != store.CodeLicenseNone {
		t.Fatalf("nothing installed: %d %s", rec.Code, rec.Body)
	}

	rec := e.put(path, "alice", DutyLicenseInstall, "0", body)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` || !strings.Contains(rec.Body.String(), `"state":"GRACE"`) {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	if rec := e.put(path, "alice", DutyLicenseInstall, "0", body); rec.Code != http.StatusPreconditionFailed || code(t, rec) != domain.CodeLicenseStale {
		t.Fatalf("a stale version: %d %s", rec.Code, rec.Body)
	}
	// renewal under the version read
	if rec := e.put(path, "alice", DutyLicenseInstall, "1", signedLicense(priv, tid, "2027-12-31", 9)); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("renewal: %d %s", rec.Code, rec.Body)
	}
	rec = e.get("/v1/ops/tenants/"+tid+"/license?activeFull=3", "bob", DutyView)
	var v struct {
		Version int64 `json:"version"`
		Status  struct {
			State        string   `json:"state"`
			DaysToExpiry int      `json:"daysToExpiry"`
			Codes        []string `json:"codes"`
		} `json:"status"`
		Messages []string `json:"messages"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != http.StatusOK || v.Version != 2 || v.Status.State != "VALID" || v.Status.DaysToExpiry != 350 || len(v.Status.Codes) != 0 {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	// over-use is GRACE with the catalogue text
	rec = e.get("/v1/ops/tenants/"+tid+"/license?activeFull=12", "bob", DutyView)
	if !strings.Contains(rec.Body.String(), `"state":"GRACE"`) || !strings.Contains(rec.Body.String(), "LIC-1802: License in grace period: 12 full users active, 9 licensed.") {
		t.Fatalf("over-use: %s", rec.Body)
	}
	if rec := e.get("/v1/ops/tenants/"+tid+"/license?activeFull=-4", "bob", DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad count: %d", rec.Code)
	}
	a := New(store.New(e.pool), Config{OperatorTenantID: e.tenant, Now: func() time.Time { return time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC) }})
	if m := httpGet(a.Metrics()); !strings.Contains(m, `passage_license_days_to_expiry{tenant_id="`+tid+`"} 350`) {
		t.Fatalf("metrics: %s", m)
	}
}

func TestInstallRefusedWithoutAConfiguredKey(t *testing.T) {
	e := newEnv(t, "us-east") // no public key
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	tid := ids.New()
	if rec := e.put("/v1/ops/tenants/"+tid+"/license", "alice", DutyLicenseInstall, "0", signedLicense(priv, tid, "2026-12-31", 9)); rec.Code != http.StatusUnprocessableEntity || code(t, rec) != domain.CodeLicenseSignature {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestScorecardFromRecordedSteps(t *testing.T) {
	e := newEnv(t, "us-east")
	if rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-sc", `{"to":"eu-west"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	if rec := e.get("/v1/ops/failovers/evt-sc/scorecard", "bob", DutyView); !strings.Contains(rec.Body.String(), `"passed":false`) || !strings.Contains(rec.Body.String(), "not every step") {
		t.Fatalf("no steps yet: %s", rec.Body)
	}
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for i, a := range []string{"fence-old-primary", "promote-cnpg", "switch-mirrormaker", "activate-temporal", "flip-dns", "verify-ledger-hash-chain"} {
		region := "eu-west"
		if i == 0 {
			region = "us-east"
		}
		b, _ := json.Marshal(map[string]any{"order": i + 1, "region": region, "status": "DONE", "startedAt": t0.Add(time.Duration(i) * 5 * time.Minute), "finishedAt": t0.Add(time.Duration(i+1) * 5 * time.Minute)})
		if rec := e.do("PUT", "/v1/ops/failovers/evt-sc/steps/"+a, "alice", DutyFailover, "", string(b)); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", a, rec.Code, rec.Body)
		}
	}
	rec := e.get("/v1/ops/failovers/evt-sc/scorecard", "bob", DutyView)
	var sc domain.Scorecard
	_ = json.Unmarshal(rec.Body.Bytes(), &sc)
	// fence starts at t0, flip-dns ends at t0+25min
	if rec.Code != http.StatusOK || !sc.Passed || sc.RTOSeconds == nil || *sc.RTOSeconds != 1500 || sc.RPOSeconds != 0 || !sc.ChainVerified {
		t.Fatalf("scorecard: %d %s", rec.Code, rec.Body)
	}
	if rec := e.get("/v1/ops/failovers/evt-sc/scorecard", "bob", DutyReport); rec.Code != http.StatusForbidden {
		t.Fatalf("view duty: %d", rec.Code)
	}
}
