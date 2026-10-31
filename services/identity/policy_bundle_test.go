package identity_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/services/identity"
)

const (
	bundleKeyID = "identity-policy"
	bundleToken = "0123456789abcdef0123456789abcdef"
)

type bundleFixture struct {
	admin    *pgxpool.Pool
	app      *pgxpool.Pool
	tenantA  string
	tenantB  string
	compiler *identity.PolicyBundleCompiler
	pubPath  string
	build    func(operator string) *identity.PolicyBundleCompiler
}

func newBundleFixture(t *testing.T, operator string) bundleFixture {
	t.Helper()
	admin, app, tenantA, tenantB := setupWithAdmin(t)
	if operator == "" {
		operator = tenantA
	}
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `insert into identity_operator (tenant_id) values ($1)`, tenantA); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`insert into identity_role_assignments (tenant_id, assignment_id, user_id, role) values ($1, gen_random_uuid(), $2, 'Maintain users')`,
		`insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, valid_from, valid_to) values ($1, gen_random_uuid(), $2, 'Maintain tenants', now() - interval '2 days', now() - interval '1 day')`,
		`insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, legal_entity_id) values ($1, gen_random_uuid(), $2, 'Maintain legal entities', '` + legalA + `')`,
	} {
		if _, err := admin.Exec(ctx, q, tenantA, userID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role) values ($1, gen_random_uuid(), $2, 'Maintain legal entities')`, tenantB, userID); err != nil {
		t.Fatal(err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := identity.NewBundleSigner(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), bundleKeyID)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPath := filepath.Join(t.TempDir(), "pub.pem")
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o600); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile("../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	rego, err := os.ReadFile("../../policies/authz/authz.rego")
	if err != nil {
		t.Fatal(err)
	}
	catalogue, err := identity.LoadCatalogue(raw)
	if err != nil {
		t.Fatal(err)
	}
	build := func(operator string) *identity.PolicyBundleCompiler {
		compiler, err := identity.NewPolicyBundleCompiler(app, catalogue, raw, rego, operator, signer)
		if err != nil {
			t.Fatal(err)
		}
		return compiler
	}
	return bundleFixture{admin: admin, app: app, tenantA: tenantA, tenantB: tenantB, compiler: build(operator), pubPath: pubPath, build: build}
}

func untar(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name], _ = io.ReadAll(tr)
	}
}

func TestCompiledBundleCarriesOnlyActiveTenantWideAssignmentsBySubject(t *testing.T) {
	f := newBundleFixture(t, "")
	bundle, err := f.compiler.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	files := untar(t, bundle.Archive)
	for _, name := range []string{"data.json", "authz.rego", ".manifest", ".signatures.json"} {
		if len(files[name]) == 0 {
			t.Errorf("bundle is missing %s", name)
		}
	}
	var data struct {
		Assignments map[string]map[string][]string `json:"assignments"`
		Duties      map[string]json.RawMessage     `json:"duties"`
	}
	if err := json.Unmarshal(files["data.json"], &data); err != nil || len(data.Duties) == 0 {
		t.Fatalf("data.json: %v duties=%d", err, len(data.Duties))
	}
	want := map[string]map[string][]string{
		f.tenantA: {"subject-" + f.tenantA: {"Maintain users"}},
		f.tenantB: {"subject-" + f.tenantB: {"Maintain legal entities"}},
	}
	got, _ := json.Marshal(data.Assignments)
	expected, _ := json.Marshal(want)
	if string(got) != string(expected) {
		t.Fatalf("assignments = %s, want %s", got, expected)
	}
	var manifest struct{ Revision string }
	if err := json.Unmarshal(files[".manifest"], &manifest); err != nil || manifest.Revision != bundle.Revision || bundle.Revision == "" {
		t.Fatalf("manifest = %s err = %v revision = %q", files[".manifest"], err, bundle.Revision)
	}
}

func TestRevisionChangesWhenAnAssignmentChanges(t *testing.T) {
	f := newBundleFixture(t, "")
	before, err := f.compiler.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.compiler.Compile(context.Background())
	if err != nil || again.Revision != before.Revision {
		t.Fatalf("unchanged data must keep the revision: %v %q %q", err, before.Revision, again.Revision)
	}
	if _, err := f.admin.Exec(context.Background(), `update identity_role_assignments set valid_to = now() where tenant_id = $1 and role = 'Maintain users'`, f.tenantA); err != nil {
		t.Fatal(err)
	}
	after, err := f.compiler.Compile(context.Background())
	if err != nil || after.Revision == before.Revision {
		t.Fatalf("revoked assignment must change the revision: %v %q", err, after.Revision)
	}
}

func TestCompileRefusesWhenOperatorIsNotRegistered(t *testing.T) {
	f := newBundleFixture(t, "")
	if _, err := f.build(f.tenantB).Compile(context.Background()); err == nil {
		t.Fatal("a compiler whose operator is not registered must not publish an empty policy")
	}
}

func TestPolicyCompileReadPolicyNeedsOperatorSessionAndFlag(t *testing.T) {
	f := newBundleFixture(t, "")
	ctx := context.Background()
	read := func(tenantID, flag string) int {
		var n int
		err := pgx.BeginFunc(ctx, f.app, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true), set_config('passage.policy_compile', $2, true)`, tenantID, flag); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `select count(*) from identity_role_assignments where tenant_id <> $1`, tenantID).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := read(f.tenantA, "on"); n == 0 {
		t.Error("operator with the compile flag must see other tenants' assignments")
	}
	if n := read(f.tenantA, ""); n != 0 {
		t.Errorf("operator without the flag sees %d foreign rows", n)
	}
	if n := read(f.tenantB, "on"); n != 0 {
		t.Errorf("non-operator tenant with the flag sees %d foreign rows", n)
	}
}

func opaEval(t *testing.T, f bundleFixture, bundle []byte, input string) (string, error) {
	t.Helper()
	bin, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa binary is required in CI")
		}
		t.Skip("opa binary not on PATH")
	}
	path := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	verified := filepath.Join(t.TempDir(), "verified.tar.gz")
	if out, err := exec.Command(bin, "build", "--bundle", path, "--verification-key", f.pubPath, "--verification-key-id", bundleKeyID, "--signing-alg", "RS256", "--output", verified).CombinedOutput(); err != nil {
		return string(out), err
	}
	cmd := exec.Command(bin, "eval", "--format", "raw", "--bundle", verified, "--stdin-input", "data.authz.decision.allow")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestOPAAcceptsTheSignedBundleAndRejectsTampering(t *testing.T) {
	f := newBundleFixture(t, "")
	bundle, err := f.compiler.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	granted := `{"subject":{"tenant_id":"` + f.tenantA + `","user_id":"subject-` + f.tenantA + `"},"action":"GET","resource":"/v1/identity/users"}`
	out, err := opaEval(t, f, bundle.Archive, granted)
	if err != nil || !strings.Contains(out, "true") {
		t.Fatalf("signed bundle must verify and allow the assigned duty: %v %s", err, out)
	}
	crossTenant := strings.Replace(granted, f.tenantA, f.tenantB, 1)
	if out, err := opaEval(t, f, bundle.Archive, crossTenant); err != nil || strings.Contains(out, "true") {
		t.Fatalf("a subject must not carry a duty into another tenant: %v %s", err, out)
	}

	files := untar(t, bundle.Archive)
	files["data.json"] = bytes.Replace(files["data.json"], []byte(`"Maintain users"]`), []byte(`"Maintain users","Maintain tenants"]`), 1)
	tampered := repack(t, files)
	if out, err := opaEval(t, f, tampered, granted); err == nil {
		t.Fatalf("a tampered bundle must fail signature verification: %s", out)
	}
}

func repack(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBundleEndpointAuthenticatesAndSupportsConditionalGet(t *testing.T) {
	f := newBundleFixture(t, "")
	api, err := identity.NewPolicyBundleAPI(f.compiler, bundleToken)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	api.Register(router)
	do := func(authorization, ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, identity.PolicyBundlePath, nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for name, header := range map[string]string{"no credentials": "", "wrong token": "Bearer wrong", "wrong scheme": "Basic " + bundleToken, "empty bearer": "Bearer "} {
		if rec := do(header, ""); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: status = %d, want 401 with a Bearer challenge", name, rec.Code)
		}
	}
	rec := do("Bearer "+bundleToken, "")
	etag := rec.Header().Get("ETag")
	if rec.Code != http.StatusOK || etag == "" || rec.Header().Get("Content-Type") != "application/gzip" || len(untar(t, rec.Body.Bytes())["data.json"]) == 0 {
		t.Fatalf("bundle = %d etag=%q type=%q", rec.Code, etag, rec.Header().Get("Content-Type"))
	}
	if rec := do("Bearer "+bundleToken, etag); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("matching ETag = %d, want an empty 304", rec.Code)
	}
}

func TestBundleEndpointFailsClosedWhenCompilationFails(t *testing.T) {
	f := newBundleFixture(t, "")
	api, err := identity.NewPolicyBundleAPI(f.build(f.tenantB), bundleToken)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	api.Register(router)
	req := httptest.NewRequest(http.MethodGet, identity.PolicyBundlePath, nil)
	req.Header.Set("Authorization", "Bearer "+bundleToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("ETag") != "" {
		t.Fatalf("status = %d etag=%q, want 503 and no bundle", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestBundleConstructorsRejectWeakInput(t *testing.T) {
	if _, err := identity.NewBundleSigner([]byte("not pem"), bundleKeyID); err == nil {
		t.Error("a non-PEM key must be rejected")
	}
	if _, err := identity.NewPolicyBundleAPI(nil, bundleToken); err == nil {
		t.Error("a nil compiler must be rejected")
	}
	f := newBundleFixture(t, "")
	if _, err := identity.NewPolicyBundleAPI(f.compiler, "short"); err == nil {
		t.Error("a short bearer token must be rejected")
	}
}

func TestBundleSignerRejectsKeysUnder2048Bits(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for name, material := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8} {
		if _, err := identity.NewBundleSigner(material, bundleKeyID); err == nil || !strings.Contains(err.Error(), "2048") {
			t.Errorf("%s 1024-bit key: err = %v, want a 2048-bit minimum error", name, err)
		}
	}
}

func TestSidecarConfigActivatesTheSignedBundle(t *testing.T) {
	bin, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa binary is required in CI")
		}
		t.Skip("opa binary not on PATH")
	}
	f := newBundleFixture(t, "")
	api, err := identity.NewPolicyBundleAPI(f.compiler, bundleToken)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	api.Register(router)
	server := httptest.NewTLSServer(router)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	caPath, tokenPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "token")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(bundleToken), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile("../../deploy/policies/opa/opa-sidecar.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, block, found := strings.Cut(string(manifest), "  config.yaml: |\n")
	if !found {
		t.Fatal("opa-sidecar.yaml has no config.yaml")
	}
	var config strings.Builder
	for _, line := range strings.Split(block, "\n") {
		config.WriteString(strings.TrimPrefix(line, "    ") + "\n")
	}
	rendered := strings.NewReplacer(
		"https://identity-bundle:8443", server.URL,
		"/etc/passage/opa-bundle/ca.pem", caPath,
		"/etc/passage/opa-bundle/token", tokenPath,
		"min_delay_seconds: 10", "min_delay_seconds: 1",
		"max_delay_seconds: 20", "max_delay_seconds: 2",
		"persist: true", "persist: false",
	).Replace(config.String())
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	publicPEM, err := os.ReadFile(f.pubPath)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	cmd := exec.Command(bin, "run", "--server", "--addr", addr, "--config-file", configPath)
	cmd.Env = append(os.Environ(), "OPA_BUNDLE_PUBLIC_KEY="+strings.ReplaceAll(strings.TrimSpace(string(publicPEM)), "\n", `\n`))
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	query := `{"input":{"subject":{"tenant_id":"` + f.tenantA + `","user_id":"subject-` + f.tenantA + `"},"action":"GET","resource":"/v1/identity/users"}}`
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Post("http://"+addr+"/v1/data/authz/decision/allow", "application/json", strings.NewReader(query))
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if strings.Contains(string(body), `"result":true`) {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("sidecar never activated the signed bundle\n%s", logs.String()))
}
