package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/migration/internal/api"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	pkgPath   = "/v1/integration/config-packages"
	applyDuty = "migration.run,migration.package.apply,tenant.administer"
)

var pkgSigner = domain.HMACSigner{Key: []byte("tenant-key")}

func signedPkg(t *testing.T, mutate func(*domain.Package)) (string, domain.Package) {
	t.Helper()
	p := domain.Package{Version: "1.0.0", SchemaVersion: 1,
		Files: []domain.PackageFile{
			{Name: "uom.json", Entity: "uom", Records: []domain.PackageRecord{{Key: "EA", Body: json.RawMessage(`{"name":"Each"}`)}, {Key: "KG", Body: json.RawMessage(`{"name":"Kilogram"}`)}}},
			{Name: "item.json", Entity: "item", Records: []domain.PackageRecord{{Key: "I1", Body: json.RawMessage(`{"uom":"EA"}`)}}},
		},
		Manifest: domain.Manifest{Files: []domain.ManifestFile{{Name: "uom.json"}, {Name: "item.json", DependsOn: []string{"uom.json"}}}},
	}
	p, err := domain.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := domain.Sign(p, pkgSigner)
	if mutate != nil {
		mutate(&p) // after signing: a tampered package
	}
	raw, _ := json.Marshal(map[string]any{"package": p, "signature": base64.StdEncoding.EncodeToString(sig)})
	return string(raw), p
}

func pkgRouter(s store.Store, target domain.PackageTarget, factoryCalls *atomic.Int32) *chi.Mux {
	r := httpx.NewRouter()
	api.New(s, api.NotConfigured).WithPackages(func(context.Context) (api.PackageBackend, error) {
		factoryCalls.Add(1)
		return api.PackageBackend{Target: target, Signer: pkgSigner}, nil
	}).Register(r)
	return r
}

type counted struct {
	Counts map[string]int `json:"counts"`
	Files  []struct {
		Name   string
		Create []string
	}
	PackageID string `json:"packageId"`
	AppliedAt string `json:"appliedAt"`
	Code      string `json:"code"`
}

func parse(t *testing.T, body string) counted {
	t.Helper()
	var c counted
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return c
}

func TestDiffPackage_CountsWithoutWriting(t *testing.T) {
	target := domain.NewMemPackageTarget()
	target.Data["uom"] = map[string]map[string]any{"EA": {"name": "Each"}}
	var calls atomic.Int32
	r := pkgRouter(store.NewMem(), target, &calls)
	body, _ := signedPkg(t, nil)
	rec := do(r, http.MethodPost, pkgPath+":diff", body, duties, true)
	got := parse(t, rec.Body.String())
	if rec.Code != http.StatusOK || got.Counts["create"] != 2 || got.Counts["update"] != 0 || got.Counts["unchanged"] != 1 || len(got.Files) != 2 || got.Files[0].Name != "uom.json" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if len(target.Upserts) != 0 {
		t.Fatal("diff must not write")
	}
	if rec := do(r, http.MethodPost, pkgPath+":diff", body, "other.duty", true); rec.Code != http.StatusForbidden {
		t.Fatalf("diff needs the run duty: %d", rec.Code)
	}
}

func TestApplyPackage_WritesRecordsAppliedAndReplaysByKey(t *testing.T) {
	target := domain.NewMemPackageTarget()
	var calls atomic.Int32
	st := store.NewMem()
	r := pkgRouter(st, target, &calls)
	body, _ := signedPkg(t, nil)
	rec := do(r, http.MethodPost, pkgPath+":apply", body, applyDuty, true, "Idempotency-Key", "k1")
	first := parse(t, rec.Body.String())
	if rec.Code != http.StatusOK || first.Counts["create"] != 3 || first.PackageID == "" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if len(target.Data["uom"]) != 2 || len(target.Data["item"]) != 1 || fmt.Sprint(target.Upserts) != "[uom item]" {
		t.Fatalf("target = %+v %v", target.Data, target.Upserts)
	}
	saved, err := st.GetConfigPackage(context.Background(), first.PackageID)
	if err != nil || len(saved.Applied) != 1 || saved.Applied[0].DiffCounts.Create != 3 {
		t.Fatalf("applied history = %+v, %v", saved, err)
	}
	before := calls.Load()
	rec = do(r, http.MethodPost, pkgPath+":apply", body, applyDuty, true, "Idempotency-Key", "k1")
	replay := parse(t, rec.Body.String())
	if rec.Code != http.StatusOK || replay.PackageID != first.PackageID || replay.AppliedAt != first.AppliedAt || replay.Counts["create"] != 3 || calls.Load() != before {
		t.Fatalf("replay must return the original response without touching the backend: %d %s", rec.Code, rec.Body)
	}
	if saved, _ = st.GetConfigPackage(context.Background(), first.PackageID); len(saved.Applied) != 1 {
		t.Fatal("a replay must not append another applied entry")
	}
	// a second key applies again, reports no changes and appends a second entry
	rec = do(r, http.MethodPost, pkgPath+":apply", body, applyDuty, true, "Idempotency-Key", "k2")
	if got := parse(t, rec.Body.String()); rec.Code != http.StatusOK || got.Counts["create"]+got.Counts["update"] != 0 || got.Counts["unchanged"] != 3 {
		t.Fatalf("second apply: %d %s", rec.Code, rec.Body)
	}
	other, _ := signedPkg(t, func(p *domain.Package) { p.Version = "2.0.0" })
	if rec := do(r, http.MethodPost, pkgPath+":apply", other, applyDuty, true, "Idempotency-Key", "k1"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("same key with another package: %d %s", rec.Code, rec.Body)
	}
}

func TestApplyPackage_Rejections(t *testing.T) {
	target := domain.NewMemPackageTarget()
	var calls atomic.Int32
	r := pkgRouter(store.NewMem(), target, &calls)
	tampered, _ := signedPkg(t, func(p *domain.Package) { p.Files[0].Records[0].Body = json.RawMessage(`{"name":"Evil"}`) })
	newer, _ := signedPkg(t, nil)
	newerPkg := domain.Package{}
	_ = json.Unmarshal([]byte(newer), &struct {
		Package *domain.Package `json:"package"`
	}{&newerPkg})
	newerPkg.SchemaVersion = 2
	newerSig, _ := domain.Sign(newerPkg, pkgSigner)
	newerBody, _ := json.Marshal(map[string]any{"package": newerPkg, "signature": newerSig})
	cyclic := domain.Package{Version: "1", SchemaVersion: 1,
		Files:    []domain.PackageFile{{Name: "a", Entity: "a", Records: []domain.PackageRecord{{Key: "k", Body: json.RawMessage(`{}`)}}}},
		Manifest: domain.Manifest{Files: []domain.ManifestFile{{Name: "a", DependsOn: []string{"a"}}}}}
	cyclic, _ = domain.Seal(cyclic)
	cycSig, _ := domain.Sign(cyclic, pkgSigner)
	cycBody, _ := json.Marshal(map[string]any{"package": cyclic, "signature": cycSig})
	for name, c := range map[string]struct {
		body, code string
		status     int
	}{
		"tampered": {tampered, domain.CodePkgSignature, 422},
		"newer":    {string(newerBody), domain.CodePkgSchemaNewer, 422},
		"cyclic":   {string(cycBody), domain.CodePkgInvalid, 422},
	} {
		rec := do(r, http.MethodPost, pkgPath+":apply", c.body, applyDuty, true, "Idempotency-Key", "k-"+name)
		if rec.Code != c.status || parse(t, rec.Body.String()).Code != c.code {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "Evil") {
			t.Errorf("%s: problem leaks a record body", name)
		}
	}
	if len(target.Upserts) != 0 {
		t.Fatal("refused packages must write nothing")
	}
}

func TestPackages_ValidationDutyAndFactoryNotCalledForMalformedRequests(t *testing.T) {
	var calls atomic.Int32
	r := pkgRouter(store.NewMem(), domain.NewMemPackageTarget(), &calls)
	good, _ := signedPkg(t, nil)
	for name, c := range map[string]struct {
		path, body, duty string
		headers          []string
		status           int
	}{
		"no key":        {":apply", good, applyDuty, nil, 400},
		"malformed":     {":apply", `{"package":`, applyDuty, []string{"Idempotency-Key", "k"}, 400},
		"no package":    {":apply", `{"signature":"AQ=="}`, applyDuty, []string{"Idempotency-Key", "k"}, 400},
		"no signature":  {":diff", `{"package":{"version":"1","schemaVersion":1,"manifest":{"files":[]},"files":[]}}`, duties, nil, 400},
		"unknown field": {":diff", `{"package":null,"signature":"AQ==","extra":1}`, duties, nil, 400},
		"apply duty":    {":apply", good, duties, []string{"Idempotency-Key", "k"}, 403},
		"long version":  {":apply", strings.Replace(good, `"version":"1.0.0"`, `"version":"`+strings.Repeat("9", 21)+`"`, 1), applyDuty, []string{"Idempotency-Key", "k"}, 400},
		"no version":    {":diff", strings.Replace(good, `"version":"1.0.0"`, `"version":""`, 1), duties, nil, 400},
	} {
		if rec := do(r, http.MethodPost, pkgPath+c.path, c.body, c.duty, true, c.headers...); rec.Code != c.status {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("the package target factory ran %d times for rejected requests", calls.Load())
	}
	if rec := do(r, http.MethodPost, pkgPath+":diff", good, "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", rec.Code)
	}
}

func TestPackages_DefaultBackendIs501AndBackendErrorsAre500(t *testing.T) {
	r := httpx.NewRouter()
	api.New(store.NewMem(), api.NotConfigured).Register(r)
	good, _ := signedPkg(t, nil)
	for _, path := range []string{":diff", ":apply"} {
		rec := do(r, http.MethodPost, pkgPath+path, good, applyDuty, true, "Idempotency-Key", "k")
		if rec.Code != http.StatusNotImplemented || parse(t, rec.Body.String()).Code != api.CodePackageNotConfig {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	r = httpx.NewRouter()
	api.New(store.NewMem(), api.NotConfigured).WithPackages(func(context.Context) (api.PackageBackend, error) {
		return api.PackageBackend{}, errors.New("vault down token=abc")
	}).Register(r)
	if rec := do(r, http.MethodPost, pkgPath+":diff", good, duties, true); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "token=abc") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestPackages_ListAndGet(t *testing.T) {
	var calls atomic.Int32
	r := pkgRouter(store.NewMem(), domain.NewMemPackageTarget(), &calls)
	good, _ := signedPkg(t, nil)
	id := parse(t, do(r, http.MethodPost, pkgPath+":apply", good, applyDuty, true, "Idempotency-Key", "k").Body.String()).PackageID
	rec := do(r, http.MethodGet, pkgPath, "", duties, true)
	var list struct {
		Items []struct {
			ID        string
			FileCount int
			Applied   []map[string]any
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != id || list.Items[0].FileCount != 2 || len(list.Items[0].Applied) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = do(r, http.MethodGet, pkgPath+"/"+id, "", duties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"files":[{"name":"uom.json"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, pkgPath+"/00000000-0000-7000-8000-000000000999", "", duties, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, pkgPath+"?limit=0", "", duties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}
}

// racingStore misses the idempotency lookup, as when a concurrent request with the same key records first.
type racingStore struct{ *store.Mem }

func (racingStore) ConfigPackageByKey(context.Context, string) (store.ConfigPackage, bool, error) {
	return store.ConfigPackage{}, false, nil
}

func TestApplyPackage_ConcurrentSameKeyReturnsTheRecordedResponse(t *testing.T) {
	var calls atomic.Int32
	mem := store.NewMem()
	r := pkgRouter(racingStore{mem}, domain.NewMemPackageTarget(), &calls)
	body, _ := signedPkg(t, nil)
	first := parse(t, do(r, http.MethodPost, pkgPath+":apply", body, applyDuty, true, "Idempotency-Key", "k1").Body.String())
	rec := do(r, http.MethodPost, pkgPath+":apply", body, applyDuty, true, "Idempotency-Key", "k1")
	second := parse(t, rec.Body.String())
	if rec.Code != http.StatusOK || second.PackageID != first.PackageID || second.AppliedAt != first.AppliedAt || second.Counts["create"] != 3 {
		t.Fatalf("the losing request must answer with the original response: %d %s", rec.Code, rec.Body)
	}
	if saved, _ := mem.GetConfigPackage(context.Background(), first.PackageID); len(saved.Applied) != 1 {
		t.Fatalf("the losing request must not append a second entry: %+v", saved.Applied)
	}
}
