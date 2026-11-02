package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/api"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/egress"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	connPath     = "/v1/integration/connection-profiles"
	connWrite    = "migration.run,migration.connection.write,migration.project.write,tenant.administer"
	connReadOnly = "migration.run,tenant.administer"
	sentinel     = "Sup3r-S3cret-sentinel-9f2c"
	acmeSQL      = `{"name":"Acme ERP","kind":"sql","sourceSystem":"custom_acme","config":{"driver":"postgres","host":"erp.acme.example","port":5432,"database":"erp","user":"passage","sslmode":"require","timezone":"Europe/Berlin","codepage":"windows-1252"}}`
)

type connBody struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Kind            string         `json:"kind"`
	SourceSystem    string         `json:"sourceSystem"`
	Config          map[string]any `json:"config"`
	SecretSet       bool           `json:"secretSet"`
	SecretUpdatedAt string         `json:"secretUpdatedAt"`
	Status          string         `json:"status"`
	Version         int            `json:"version"`
}

func decodeConn(t *testing.T, rec *httptest.ResponseRecorder) connBody {
	t.Helper()
	var c connBody
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return c
}

func testKeys(t *testing.T) *fieldcrypt.Keys {
	t.Helper()
	k, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{9}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type recordingProber struct {
	got    []probe.Target
	result probe.Result
}

func (p *recordingProber) Probe(_ context.Context, t probe.Target) probe.Result {
	p.got = append(p.got, t)
	return p.result
}

func connRouter(s store.Store, keys *fieldcrypt.Keys, p api.Prober) *chi.Mux {
	factory := func(context.Context, string) (api.Backend, error) {
		return api.Backend{Extractor: rowsExtractor{records()}, Loader: domain.NewMemLoader(), Reader: domain.NewMemLoader()}, nil
	}
	r := httpx.NewRouter()
	api.New(s, factory).WithConnections(keys, p).Register(r)
	return r
}

func createConn(t *testing.T, r http.Handler, payload string) connBody {
	t.Helper()
	rec := do(r, http.MethodPost, connPath, payload, connWrite, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	return decodeConn(t, rec)
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return logs
}

func TestConnections_CreateReplayGetListAndPatch(t *testing.T) {
	r := connRouter(store.NewMem(), testKeys(t), &recordingProber{})
	rec := do(r, http.MethodPost, connPath, acmeSQL, connWrite, true, "Idempotency-Key", "k1")
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") == "" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	c := decodeConn(t, rec)
	if c.Version != 1 || c.Status != "active" || c.SecretSet || c.SourceSystem != "custom_acme" || c.Config["host"] != "erp.acme.example" || !strings.HasSuffix(rec.Header().Get("Location"), c.ID) {
		t.Fatalf("created %+v", c)
	}
	if again := do(r, http.MethodPost, connPath, acmeSQL, connWrite, true, "Idempotency-Key", "k1"); again.Code != http.StatusOK || decodeConn(t, again).ID != c.ID {
		t.Fatalf("replay = %d %s", again.Code, again.Body)
	}
	if reuse := do(r, http.MethodPost, connPath, strings.Replace(acmeSQL, "Acme ERP", "Other", 1), connWrite, true, "Idempotency-Key", "k1"); reuse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse = %d %s", reuse.Code, reuse.Body)
	}
	if dup := do(r, http.MethodPost, connPath, acmeSQL, connWrite, true); dup.Code != http.StatusConflict || !strings.Contains(dup.Body.String(), store.CodeConnectionName) {
		t.Fatalf("duplicate name = %d %s", dup.Code, dup.Body)
	}

	if got := do(r, http.MethodGet, connPath+"/"+c.ID, "", connReadOnly, true); got.Code != http.StatusOK || decodeConn(t, got).Name != "Acme ERP" {
		t.Fatalf("get = %d %s", got.Code, got.Body)
	}
	if missing := do(r, http.MethodGet, connPath+"/00000000-0000-7000-8000-00000000ffff", "", connReadOnly, true); missing.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", missing.Code)
	}
	list := do(r, http.MethodGet, connPath+"?kind=sql&limit=1", "", connReadOnly, true)
	var page struct {
		Items      []connBody `json:"items"`
		NextCursor string     `json:"nextCursor"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || list.Code != http.StatusOK || len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	for _, q := range []string{"?kind=ftp", "?status=gone", "?cursor=zzz"} {
		if rec := do(r, http.MethodGet, connPath+q, "", connReadOnly, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", q, rec.Code)
		}
	}

	patch := func(payload string) *httptest.ResponseRecorder {
		return do(r, http.MethodPatch, connPath+"/"+c.ID, payload, connWrite, true)
	}
	if rec := patch(`{"name":"x"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"version"`) {
		t.Fatalf("patch without version = %d %s", rec.Code, rec.Body)
	}
	up := patch(`{"version":1,"name":"Acme ERP prod","status":"disabled","config":{"driver":"postgres","host":"erp2.acme.example","database":"erp","user":"passage"}}`)
	if up.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", up.Code, up.Body)
	}
	if got := decodeConn(t, up); got.Version != 2 || got.Name != "Acme ERP prod" || got.Status != "disabled" || got.Config["host"] != "erp2.acme.example" {
		t.Fatalf("patched %+v", got)
	}
	if stale := patch(`{"version":1,"name":"again"}`); stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), store.CodeVersionConflict) {
		t.Fatalf("stale = %d %s", stale.Code, stale.Body)
	}
	if bad := patch(`{"version":2,"config":{"driver":"db2","host":"x","database":"d","user":"u"}}`); bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "config.driver") {
		t.Fatalf("bad driver = %d %s", bad.Code, bad.Body)
	}
	if bad := patch(`{"version":2,"status":"paused"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d %s", bad.Code, bad.Body)
	}
	if rec := do(r, http.MethodPatch, connPath+"/"+c.ID, `{"version":2,"name":"z"}`, connReadOnly, true); rec.Code != http.StatusForbidden {
		t.Fatalf("patch without the write duty = %d", rec.Code)
	}
}

func TestConnections_RejectionsAndDuties(t *testing.T) {
	r := connRouter(store.NewMem(), testKeys(t), &recordingProber{})
	cases := []struct {
		name, payload, duties string
		tenant                bool
		want                  int
		contains              string
	}{
		{"no principal", acmeSQL, connWrite, false, 401, "AUTH-0001"},
		{"read duty cannot create", acmeSQL, connReadOnly, true, 403, "AUTH-0003"},
		{"unknown field", `{"name":"x","kind":"sql","sourceSystem":"ax2012","config":{},"x":1}`, connWrite, true, 400, "COM-0001"},
		{"bad kind", `{"name":"x","kind":"ftp","sourceSystem":"ax2012","config":{}}`, connWrite, true, 400, `"kind"`},
		{"bad source system", `{"name":"x","kind":"file","sourceSystem":"custom_ACME","config":{"dropDir":"a","format":"csv"}}`, connWrite, true, 400, `"sourceSystem"`},
		{"password in config", `{"name":"x","kind":"sql","sourceSystem":"ax2012","config":{"driver":"postgres","host":"h","database":"d","user":"u","password":"hunter2"}}`, connWrite, true, 400, "config.password"},
		{"token in config", `{"name":"x","kind":"rest","sourceSystem":"ax2012","config":{"baseUrl":"https://a.example","apiToken":"t"}}`, connWrite, true, 400, "config.apiToken"},
		{"unknown setting", `{"name":"x","kind":"sql","sourceSystem":"ax2012","config":{"driver":"postgres","host":"h","database":"d","user":"u","extra":1}}`, connWrite, true, 400, "config.extra"},
		{"credentials in url", `{"name":"x","kind":"rest","sourceSystem":"ax2012","config":{"baseUrl":"https://u:p@a.example"}}`, connWrite, true, 400, "config.baseUrl"},
		{"query in url", `{"name":"x","kind":"rest","sourceSystem":"ax2012","config":{"baseUrl":"https://a.example/?key=1"}}`, connWrite, true, 400, "config.baseUrl"},
		{"authenticated http", `{"name":"x","kind":"rest","sourceSystem":"ax2012","config":{"baseUrl":"http://a.example","authType":"bearer"}}`, connWrite, true, 400, "config.baseUrl"},
		{"basic without user", `{"name":"x","kind":"rest","sourceSystem":"ax2012","config":{"baseUrl":"https://a.example","authType":"basic"}}`, connWrite, true, 400, "config.username"},
		{"bad port", `{"name":"x","kind":"sql","sourceSystem":"ax2012","config":{"driver":"postgres","host":"h","port":70000,"database":"d","user":"u"}}`, connWrite, true, 400, "config.port"},
		{"host with scheme", `{"name":"x","kind":"sql","sourceSystem":"ax2012","config":{"driver":"postgres","host":"tcp://h","database":"d","user":"u"}}`, connWrite, true, 400, "config.host"},
		{"bad codepage", `{"name":"x","kind":"sql","sourceSystem":"ax2012","config":{"driver":"postgres","host":"h","database":"d","user":"u","codepage":"klingon"}}`, connWrite, true, 400, "config.codepage"},
		{"bad timezone", `{"name":"x","kind":"sql","sourceSystem":"ax2012","config":{"driver":"postgres","host":"h","database":"d","user":"u","timezone":"Mars/Base"}}`, connWrite, true, 400, "config.timezone"},
		{"absolute drop dir", `{"name":"x","kind":"file","sourceSystem":"ax2012","config":{"dropDir":"/etc","format":"csv"}}`, connWrite, true, 400, "config.dropDir"},
		{"dot-dot drop dir", `{"name":"x","kind":"file","sourceSystem":"ax2012","config":{"dropDir":"../x","format":"csv"}}`, connWrite, true, 400, "config.dropDir"},
		{"padded name", `{"name":" x","kind":"file","sourceSystem":"ax2012","config":{"dropDir":"a","format":"csv"}}`, connWrite, true, 400, `"name"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodPost, connPath, tc.payload, tc.duties, tc.tenant)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body, tc.want, tc.contains)
			}
			if strings.Contains(rec.Body.String(), "hunter2") {
				t.Fatalf("the rejected value was echoed: %s", rec.Body)
			}
		})
	}
	if rec := do(r, http.MethodPost, connPath, acmeSQL, connWrite, true, "X-Passage-Legal-Entity", testkit.TenantID(7)); rec.Code != http.StatusForbidden {
		t.Fatalf("legal-entity-scoped caller = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, connPath, "", "migration.run", true); rec.Code != http.StatusForbidden {
		t.Fatalf("list without tenant.administer = %d", rec.Code)
	}
}

func TestConnections_SecretIsWriteOnlyAndNeverLogged(t *testing.T) {
	logs := captureLogs(t)
	s := store.NewMem()
	r := connRouter(s, testKeys(t), &recordingProber{})
	c := createConn(t, r, acmeSQL)

	put := do(r, http.MethodPut, connPath+"/"+c.ID+"/secret", `{"secret":"`+sentinel+`"}`, connWrite, true)
	if put.Code != http.StatusOK {
		t.Fatalf("put = %d %s", put.Code, put.Body)
	}
	got := decodeConn(t, put)
	if !got.SecretSet || got.SecretUpdatedAt == "" || got.Version != 2 {
		t.Fatalf("put answer %+v", got)
	}
	bodies := []string{put.Body.String()}
	bodies = append(bodies, do(r, http.MethodGet, connPath+"/"+c.ID, "", connReadOnly, true).Body.String())
	bodies = append(bodies, do(r, http.MethodGet, connPath, "", connReadOnly, true).Body.String())
	bodies = append(bodies, do(r, http.MethodPatch, connPath+"/"+c.ID, `{"version":2,"name":"renamed"}`, connWrite, true).Body.String())
	for _, b := range bodies {
		if strings.Contains(b, sentinel) || strings.Contains(b, "ciphertext") || strings.Contains(b, "sealed") {
			t.Fatalf("a response leaks the secret or its ciphertext: %s", b)
		}
		var generic map[string]any
		_ = json.Unmarshal([]byte(b), &generic)
		if _, has := generic["secret"]; has {
			t.Fatalf("a response has a secret field: %s", b)
		}
	}
	if !strings.Contains(bodies[1], `"secretSet":true`) || !strings.Contains(bodies[2], `"secretSet":true`) {
		t.Fatalf("get and list must say the secret is set: %s %s", bodies[1], bodies[2])
	}
	// What the store holds is ciphertext bound to the tenant and the profile.
	sealed, keyID, err := s.ConnectionSecret(context.Background(), c.ID)
	if err != nil || keyID != "k1" || len(sealed) == 0 || bytes.Contains(sealed, []byte(sentinel)) {
		t.Fatalf("stored = %q %q %v", sealed, keyID, err)
	}
	if plain, err := testKeys(t).Open(testkit.TenantID(1), "migration.connection_profile.secret:"+c.ID, sealed); err != nil || string(plain) != sentinel {
		t.Fatalf("open = %q %v", plain, err)
	}
	if _, err := testKeys(t).Open(testkit.TenantID(2), "migration.connection_profile.secret:"+c.ID, sealed); err == nil {
		t.Fatal("another tenant opened the secret")
	}

	// Rejected writes and error paths leak nothing either.
	do(r, http.MethodPut, connPath+"/"+c.ID+"/secret", `{"secret":"`+sentinel+`","extra":1}`, connWrite, true)
	do(r, http.MethodPut, connPath+"/00000000-0000-7000-8000-00000000ffff/secret", `{"secret":"`+sentinel+`"}`, connWrite, true)
	do(r, http.MethodPut, connPath+"/"+c.ID+"/secret", `{"secret":"`+sentinel+`"}`, connReadOnly, true)
	do(r, http.MethodPost, connPath+"/"+c.ID+":test", ``, connWrite, true)
	if strings.Contains(logs.String(), sentinel) {
		t.Fatalf("the secret reached the logs:\n%s", logs.String())
	}
}

func TestConnections_SecretValidationAndWriteDuty(t *testing.T) {
	r := connRouter(store.NewMem(), testKeys(t), &recordingProber{})
	c := createConn(t, r, acmeSQL)
	path := connPath + "/" + c.ID + "/secret"
	for name, payload := range map[string]string{"empty": `{"secret":""}`, "missing": `{}`, "too long": `{"secret":"` + strings.Repeat("a", 4097) + `"}`, "nul": `{"secret":"a\u0000b"}`} {
		if rec := do(r, http.MethodPut, path, payload, connWrite, true); rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "aaaa") {
			t.Errorf("%s = %d %.200s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPut, path, `{"secret":"x"}`, connReadOnly, true); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty cannot set a secret: %d", rec.Code)
	}
	if rec := do(r, http.MethodPut, connPath+"/00000000-0000-7000-8000-00000000ffff/secret", `{"secret":"x"}`, connWrite, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown profile = %d", rec.Code)
	}
}

func TestConnections_WithoutFieldcryptKeysTheSecretRoutesAre501(t *testing.T) {
	r := connRouter(store.NewMem(), nil, &recordingProber{})
	c := createConn(t, r, acmeSQL) // settings need no keys
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"put":  do(r, http.MethodPut, connPath+"/"+c.ID+"/secret", `{"secret":"x"}`, connWrite, true),
		"test": do(r, http.MethodPost, connPath+"/"+c.ID+":test", ``, connWrite, true),
	} {
		if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), api.CodeSecretsNotConfig) {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	if get := do(r, http.MethodGet, connPath+"/"+c.ID, "", connReadOnly, true); get.Code != http.StatusOK {
		t.Fatalf("get = %d", get.Code)
	}
	// A rest profile without authentication sends no secret, so its test needs no keys either.
	open := createConn(t, r, `{"name":"Open API","kind":"rest","sourceSystem":"custom_acme","config":{"baseUrl":"https://api.acme.example","authType":"none"}}`)
	if rec := do(r, http.MethodPost, connPath+"/"+open.ID+":test", ``, connWrite, true); rec.Code != http.StatusOK {
		t.Fatalf("unauthenticated rest test without keys = %d %s", rec.Code, rec.Body)
	}
	// A file profile has no secret, so its test needs no keys.
	bare := connRouter(store.NewMem(), nil, nil)
	f := createConn(t, bare, `{"name":"Drop","kind":"file","sourceSystem":"custom_acme","config":{"dropDir":"acme/in","format":"csv"}}`)
	if rec := do(bare, http.MethodPost, connPath+"/"+f.ID+":test", ``, connWrite, true); rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), api.CodeProbeNotConfig) {
		t.Fatalf("file test without prober = %d %s", rec.Code, rec.Body)
	}
}

func TestConnections_TestOpensTheSecretAndReturnsTheProbeResult(t *testing.T) {
	logs := captureLogs(t)
	p := &recordingProber{result: probe.Result{OK: true, Kind: "sql", ServerVersion: "PostgreSQL 16", Schemas: []string{"public"}}}
	r := connRouter(store.NewMem(), testKeys(t), p)
	c := createConn(t, r, acmeSQL)
	do(r, http.MethodPut, connPath+"/"+c.ID+"/secret", `{"secret":"`+sentinel+`"}`, connWrite, true)
	rec := do(r, http.MethodPost, connPath+"/"+c.ID+":test", ``, connWrite, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), "PostgreSQL 16") || strings.Contains(rec.Body.String(), sentinel) {
		t.Fatalf("test = %d %s", rec.Code, rec.Body)
	}
	if len(p.got) != 1 || p.got[0].Secret != sentinel || p.got[0].Kind != "sql" || p.got[0].Config["host"] != "erp.acme.example" {
		t.Fatalf("probe target = %+v", p.got)
	}
	// A failed probe is still a 200 with the failure in the body, and its code (not the secret) is logged.
	p.result = probe.Result{OK: false, Kind: "sql", Failure: &probe.Failure{Code: probe.CodeAuthFailed, Message: "password authentication failed"}}
	if rec := do(r, http.MethodPost, connPath+"/"+c.ID+":test", ``, connWrite, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), probe.CodeAuthFailed) {
		t.Fatalf("failed probe = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, connPath+"/"+c.ID+":test", ``, connReadOnly, true); rec.Code != http.StatusForbidden {
		t.Fatalf("test without the write duty = %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, connPath+"/00000000-0000-7000-8000-00000000ffff:test", ``, connWrite, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown profile = %d", rec.Code)
	}
	if strings.Contains(logs.String(), sentinel) || !strings.Contains(logs.String(), probe.CodeAuthFailed) {
		t.Fatalf("logs:\n%s", logs.String())
	}
}

// Repointing a profile clears its secret, so a writer cannot send an existing password to a server of their choosing.
func TestConnections_PatchThatRepointsTheProfileClearsTheSecret(t *testing.T) {
	p := &recordingProber{result: probe.Result{OK: true, Kind: "sql"}}
	s := store.NewMem()
	r := connRouter(s, testKeys(t), p)
	c := createConn(t, r, acmeSQL)
	do(r, http.MethodPut, connPath+"/"+c.ID+"/secret", `{"secret":"`+sentinel+`"}`, connWrite, true)
	cfg := `"driver":"postgres","host":"erp.acme.example","port":5432,"database":"erp","user":"passage","sslmode":"require"`
	kept := do(r, http.MethodPatch, connPath+"/"+c.ID, `{"version":2,"name":"Acme ERP prod","config":{`+cfg+`,"timezone":"UTC"}}`, connWrite, true)
	if got := decodeConn(t, kept); kept.Code != http.StatusOK || !got.SecretSet || got.Version != 3 {
		t.Fatalf("a rename and a timezone keep the secret: %d %s", kept.Code, kept.Body)
	}
	for i, repoint := range []string{
		strings.Replace(cfg, "erp.acme.example", "collector.evil.example", 1),
		strings.Replace(cfg, `"sslmode":"require"`, `"sslmode":"disable"`, 1),
	} {
		if i > 0 {
			do(r, http.MethodPut, connPath+"/"+c.ID+"/secret", `{"secret":"`+sentinel+`"}`, connWrite, true)
		}
		cur := decodeConn(t, do(r, http.MethodGet, connPath+"/"+c.ID, "", connReadOnly, true))
		rec := do(r, http.MethodPatch, connPath+"/"+c.ID, `{"version":`+strconv.Itoa(cur.Version)+`,"config":{`+repoint+`}}`, connWrite, true)
		if got := decodeConn(t, rec); rec.Code != http.StatusOK || got.SecretSet || got.SecretUpdatedAt != "" {
			t.Fatalf("repoint %d must clear the secret: %d %s", i, rec.Code, rec.Body)
		}
		if sealed, _, _ := s.ConnectionSecret(context.Background(), c.ID); sealed != nil {
			t.Fatalf("repoint %d left the sealed secret in the store", i)
		}
	}
	p.got = nil
	do(r, http.MethodPost, connPath+"/"+c.ID+":test", ``, connWrite, true)
	if len(p.got) != 1 || p.got[0].Secret != "" {
		t.Fatalf("the probe of a repointed profile must carry no secret: %+v", p.got)
	}
}

func TestConnections_TestOfASecretSealedUnderAnotherKeyAsksForANewSecret(t *testing.T) {
	s := store.NewMem()
	r := connRouter(s, testKeys(t), &recordingProber{})
	c := createConn(t, r, acmeSQL)
	other, _ := fieldcrypt.NewKeys("kx", map[string][]byte{"kx": bytes.Repeat([]byte{1}, 32)})
	sealed, _ := other.Seal(testkit.TenantID(1), "migration.connection_profile.secret:"+c.ID, []byte("x"))
	if _, err := s.SetConnectionSecret(context.Background(), c.ID, sealed, "kx"); err != nil {
		t.Fatal(err)
	}
	rec := do(r, http.MethodPost, connPath+"/"+c.ID+":test", ``, connWrite, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), api.CodeSecretUnreadable) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// End to end through the real prober and egress guard: the profile's test reaches an httptest server only when its
// address is inside an operator-allowlisted CIDR.
func TestConnections_TestReachesAServerOnlyWhenItsCIDRIsAllowlisted(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	guard := func(allow string) *probe.Prober {
		pol, err := egress.ParsePolicy(allow)
		if err != nil {
			t.Fatal(err)
		}
		return &probe.Prober{Guard: &egress.Guard{Policy: pol, Timeout: 2 * time.Second,
			Resolver: func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("10.20.30.40")}, nil
			},
			TestDialFunc: func(ctx context.Context, n, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, n, srv.Listener.Addr().String())
			}}}
	}
	s := store.NewMem()
	setup := createConn(t, connRouter(s, testKeys(t), nil), `{"name":"Acme API","kind":"rest","sourceSystem":"custom_acme","config":{"baseUrl":"http://erp.acme.test:`+port+`","authType":"none"}}`)

	test := func(prober api.Prober) string {
		r := connRouter(s, testKeys(t), prober)
		rec := do(r, http.MethodPost, connPath+"/"+setup.ID+":test", ``, connWrite, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("test = %d %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	if body := test(guard("")); !strings.Contains(body, `"ok":false`) || !strings.Contains(body, probe.CodeEgressDenied) || hits != 0 {
		t.Fatalf("no allowlist: %s hits=%d", body, hits)
	}
	if body := test(guard("192.168.0.0/16")); !strings.Contains(body, probe.CodeEgressDenied) || hits != 0 {
		t.Fatalf("other CIDR: %s hits=%d", body, hits)
	}
	if body := test(guard("10.20.30.0/24")); !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"httpStatus":200`) || hits != 1 {
		t.Fatalf("allowlisted CIDR: %s hits=%d", body, hits)
	}
}

func TestCustomSourceSystem_RunProjectAndXrefFilter(t *testing.T) {
	s := store.NewMem()
	r := connRouter(s, testKeys(t), &recordingProber{})
	conn := createConn(t, r, acmeSQL)

	// A migrate project for a custom system, landing in a target legal entity, reading from the profile.
	proj := do(r, http.MethodPost, projectsPath, `{"name":"Acme migration","projectType":"migrate","sourceSystem":"custom_acme","targetLegalEntityId":"`+leTarget+`","connectionProfileId":"`+conn.ID+`"}`, connWrite, true)
	if proj.Code != http.StatusCreated || !strings.Contains(proj.Body.String(), `"connectionProfileId":"`+conn.ID+`"`) || !strings.Contains(proj.Body.String(), leTarget) {
		t.Fatalf("project = %d %s", proj.Code, proj.Body)
	}
	p := decodeProject(t, proj)
	// A run under the project and the custom source system.
	run := do(r, http.MethodPost, runsPath, `{"sourceSystem":"custom_acme","mode":"rehearsal","projectId":"`+p.ID+`","mapping":`+mapping+`}`, connWrite, true)
	if run.Code != http.StatusAccepted || !strings.Contains(run.Body.String(), `"sourceSystem":"custom_acme"`) {
		t.Fatalf("run = %d %s", run.Code, run.Body)
	}
	if rec := do(r, http.MethodGet, "/v1/integration/xref?sourceSystem=custom_acme", "", connReadOnly, true); rec.Code != http.StatusOK {
		t.Fatalf("xref filter = %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"custom_ACME", "custom_", "acme"} {
		if rec := do(r, http.MethodGet, "/v1/integration/xref?sourceSystem="+bad, "", connReadOnly, true); rec.Code != http.StatusBadRequest {
			t.Errorf("xref filter %q = %d", bad, rec.Code)
		}
		if rec := do(r, http.MethodPost, runsPath, `{"sourceSystem":"`+bad+`","mode":"rehearsal","mapping":`+mapping+`}`, connWrite, true); rec.Code != http.StatusBadRequest {
			t.Errorf("run with %q = %d", bad, rec.Code)
		}
	}

	// Project rules around the profile.
	post := func(payload string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, projectsPath, payload, connWrite, true)
	}
	if rec := post(`{"name":"Wrong system","projectType":"migrate","sourceSystem":"ax2012","connectionProfileId":"` + conn.ID + `"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "custom_acme") {
		t.Fatalf("mismatched source system = %d %s", rec.Code, rec.Body)
	}
	if rec := post(`{"name":"Ghost","projectType":"migrate","sourceSystem":"custom_acme","connectionProfileId":"00000000-0000-7000-8000-00000000ffff"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeConnectionUnknown) {
		t.Fatalf("unknown profile = %d %s", rec.Code, rec.Body)
	}
	if rec := post(`{"name":"Bad id","projectType":"migrate","sourceSystem":"custom_acme","connectionProfileId":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad profile id = %d %s", rec.Code, rec.Body)
	}
	if rec := post(`{"name":"Copy with profile","projectType":"copy","sourceLegalEntityId":"` + leSource + `","targetLegalEntityId":"` + leTarget + `","connectionProfileId":"` + conn.ID + `"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("copy with profile = %d %s", rec.Code, rec.Body)
	}
	if rec := post(`{"name":"Migrate with source LE","projectType":"migrate","sourceSystem":"custom_acme","sourceLegalEntityId":"` + leSource + `"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("migrate with a source legal entity = %d %s", rec.Code, rec.Body)
	}
	// Detach and re-attach through PATCH; a disabled profile cannot be attached.
	patch := func(payload string) *httptest.ResponseRecorder {
		return do(r, http.MethodPatch, projectsPath+"/"+p.ID, payload, connWrite, true)
	}
	if rec := patch(`{"version":1,"connectionProfileId":""}`); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "connectionProfileId") {
		t.Fatalf("detach = %d %s", rec.Code, rec.Body)
	}
	if rec := patch(`{"version":2,"connectionProfileId":"` + conn.ID + `"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), conn.ID) {
		t.Fatalf("attach = %d %s", rec.Code, rec.Body)
	}
	do(r, http.MethodPatch, connPath+"/"+conn.ID, `{"version":1,"status":"disabled"}`, connWrite, true)
	if rec := patch(`{"version":3,"connectionProfileId":""}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if rec := patch(`{"version":4,"connectionProfileId":"` + conn.ID + `"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("attach disabled = %d %s", rec.Code, rec.Body)
	}
}
