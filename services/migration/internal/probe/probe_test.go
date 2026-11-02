package probe_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/egress"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
)

// pretend makes host names resolve to a private address and sends every final connection to the local test server,
// so the guard's policy decides on the pretend address while the bytes reach the server.
func pretend(t *testing.T, allow, addr string, local string) *egress.Guard {
	t.Helper()
	pol, err := egress.ParsePolicy(allow)
	if err != nil {
		t.Fatal(err)
	}
	return &egress.Guard{
		Policy:  pol,
		Timeout: 2 * time.Second,
		Resolver: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(addr)}, nil
		},
		TestDialFunc: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, local)
		},
	}
}

func restTarget(baseURL, auth, secret string, extra map[string]any) probe.Target {
	cfg := map[string]any{"baseUrl": baseURL, "authType": auth}
	for k, v := range extra {
		cfg[k] = v
	}
	return probe.Target{Kind: domain.ConnREST, Config: cfg, Secret: secret}
}

func TestREST_SucceedsOnlyWhenTheServersCIDRIsAllowlisted(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	target := restTarget("http://erp.acme.test:"+port, "none", "", nil)

	denied := (&probe.Prober{Guard: pretend(t, "", "10.20.30.40", srv.Listener.Addr().String())}).Probe(context.Background(), target)
	if denied.OK || denied.Failure == nil || denied.Failure.Code != probe.CodeEgressDenied || hits != 0 {
		t.Fatalf("not allowlisted: %+v hits=%d", denied, hits)
	}
	wrong := (&probe.Prober{Guard: pretend(t, "10.99.0.0/16", "10.20.30.40", srv.Listener.Addr().String())}).Probe(context.Background(), target)
	if wrong.OK || hits != 0 {
		t.Fatalf("another CIDR: %+v hits=%d", wrong, hits)
	}
	ok := (&probe.Prober{Guard: pretend(t, "10.20.30.0/24", "10.20.30.40", srv.Listener.Addr().String())}).Probe(context.Background(), target)
	if !ok.OK || ok.HTTPStatus != 200 || hits != 1 || ok.Failure != nil {
		t.Fatalf("allowlisted: %+v hits=%d", ok, hits)
	}
	// Loopback stays out of reach even when the operator lists it.
	loop := (&probe.Prober{Guard: pretend(t, "127.0.0.0/8", "127.0.0.1", srv.Listener.Addr().String())}).Probe(context.Background(), target)
	if loop.OK || loop.Failure.Code != probe.CodeEgressDenied || hits != 1 {
		t.Fatalf("loopback: %+v", loop)
	}
	literal := (&probe.Prober{Guard: &egress.Guard{Timeout: time.Second}}).Probe(context.Background(), restTarget("http://"+srv.Listener.Addr().String(), "none", "", nil))
	if literal.OK || literal.Failure.Code != probe.CodeEgressDenied || hits != 1 {
		t.Fatalf("loopback literal: %+v", literal)
	}
}

func TestREST_AuthHeadersAndFailureCodes(t *testing.T) {
	var gotAuth, gotKey string
	var gotUser, gotPass string
	status := http.StatusOK
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotKey = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
		gotUser, gotPass, _ = r.BasicAuth()
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	g := pretend(t, "10.20.30.0/24", "10.20.30.40", srv.Listener.Addr().String())
	g.TLS = &tls.Config{RootCAs: pool, ServerName: "example.com"}
	// The test certificate is for example.com; point the URL at that name.
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	base := "https://example.com:" + port
	p := &probe.Prober{Guard: g}

	if r := p.Probe(context.Background(), restTarget(base, "bearer", "tok-123", nil)); !r.OK || gotAuth != "Bearer tok-123" {
		t.Fatalf("bearer: %+v auth=%q", r, gotAuth)
	}
	if r := p.Probe(context.Background(), restTarget(base, "basic", "pw", map[string]any{"username": "svc"})); !r.OK || gotUser != "svc" || gotPass != "pw" {
		t.Fatalf("basic: %+v %q %q", r, gotUser, gotPass)
	}
	if r := p.Probe(context.Background(), restTarget(base, "header", "key-9", map[string]any{"headerName": "X-Api-Key"})); !r.OK || gotKey != "key-9" {
		t.Fatalf("header: %+v key=%q", r, gotKey)
	}
	if r := p.Probe(context.Background(), restTarget(base, "bearer", "", nil)); r.OK || r.Failure.Code != probe.CodeNoSecret {
		t.Fatalf("missing secret: %+v", r)
	}
	status = http.StatusUnauthorized
	if r := p.Probe(context.Background(), restTarget(base, "bearer", "bad", nil)); r.OK || r.Failure.Code != probe.CodeAuthFailed || r.HTTPStatus != 401 || strings.Contains(r.Failure.Message, "bad") {
		t.Fatalf("401: %+v", r)
	}
	status = http.StatusInternalServerError
	if r := p.Probe(context.Background(), restTarget(base, "none", "", nil)); r.OK || r.Failure.Code != probe.CodeProbeFailed {
		t.Fatalf("500: %+v", r)
	}
	if r := p.Probe(context.Background(), restTarget(base, "none", "", map[string]any{"probePath": "/redir"})); r.OK || r.HTTPStatus != 302 || !strings.Contains(r.Failure.Message, "redirect") {
		t.Fatalf("redirect must not be followed: %+v", r)
	}
}

func TestREST_ErrorMessagesNeverCarryTheSecret(t *testing.T) {
	g := &egress.Guard{Timeout: time.Second, Resolver: func(context.Context, string) ([]netip.Addr, error) { return nil, nil }}
	r := (&probe.Prober{Guard: g}).Probe(context.Background(), restTarget("https://hunter2-token.example", "bearer", "hunter2-token", nil))
	if r.OK || r.Failure == nil || strings.Contains(r.Failure.Message, "hunter2-token") {
		t.Fatalf("%+v", r)
	}
}

func TestFile_ListsMatchingFilesUnderTheDropRootOnly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "acme", "customers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"CustTable_1.csv", "CustTable_2.csv", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("a,b\n1,2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secretDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretDir, "passwd"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretDir, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	p := &probe.Prober{DropRoot: root}
	file := func(dir, pattern string) probe.Target {
		cfg := map[string]any{"dropDir": dir, "format": "csv"}
		if pattern != "" {
			cfg["filePattern"] = pattern
		}
		return probe.Target{Kind: domain.ConnFile, Config: cfg}
	}
	if r := p.Probe(context.Background(), file("acme/customers", "*.csv")); !r.OK || r.FileCount != 2 || len(r.Files) != 2 || r.Files[0].Name != "CustTable_1.csv" && r.Files[1].Name != "CustTable_1.csv" || r.Files[0].Bytes != 8 {
		t.Fatalf("pattern: %+v", r)
	}
	if r := p.Probe(context.Background(), file("acme/customers", "")); !r.OK || r.FileCount != 3 {
		t.Fatalf("no pattern: %+v", r)
	}
	if r := p.Probe(context.Background(), file("acme/missing", "")); r.OK || r.Failure.Code != probe.CodeDropMissing {
		t.Fatalf("missing: %+v", r)
	}
	if r := p.Probe(context.Background(), file("escape", "")); r.OK || r.Failure.Code != probe.CodeDropMissing {
		t.Fatalf("a symlink out of the root must not be followed: %+v", r)
	}
	if r := p.Probe(context.Background(), file("../etc", "")); r.OK {
		t.Fatalf("dot-dot: %+v", r)
	}
	if r := (&probe.Prober{}).Probe(context.Background(), file("acme/customers", "")); r.OK || r.Failure.Code != probe.CodeDropNotSet {
		t.Fatalf("no drop root: %+v", r)
	}
}

func TestSQL_ListsSchemasTablesAndColumnsThroughTheGuard(t *testing.T) {
	url := os.Getenv(testkit.EnvURL)
	if url == "" {
		t.Skipf("%s not set; skipping database test", testkit.EnvURL)
	}
	pool := testkit.DB(t)
	if _, err := pool.Exec(context.Background(), `create table probe_customer (id integer primary key, name text not null, credit numeric)`); err != nil {
		t.Fatal(err)
	}
	cc, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	local := net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port)))
	target := probe.Target{Kind: domain.ConnSQL, Secret: "pw", Config: map[string]any{
		"driver": "postgres", "host": "erp.acme.test", "port": float64(cc.Port), "database": cc.Database, "user": cc.User, "sslmode": "disable"}}

	if r := (&probe.Prober{Guard: pretend(t, "", "10.20.30.40", local)}).Probe(context.Background(), target); r.OK || r.Failure.Code != probe.CodeEgressDenied {
		t.Fatalf("not allowlisted: %+v", r)
	}
	r := (&probe.Prober{Guard: pretend(t, "erp.acme.test:"+strconv.Itoa(int(cc.Port)), "10.20.30.40", local)}).Probe(context.Background(), target)
	if !r.OK || !strings.HasPrefix(r.ServerVersion, "PostgreSQL") || r.Failure != nil {
		t.Fatalf("allowlisted by host:port: %+v", r)
	}
	var found *probe.Table
	for i := range r.Tables {
		if r.Tables[i].Name == "probe_customer" {
			found = &r.Tables[i]
		}
	}
	if found == nil {
		t.Fatalf("table not listed: %+v", r.Tables)
	}
	if len(found.Columns) != 3 || found.Columns[0].Name != "id" || found.Columns[0].Nullable || found.Columns[1].Type != "text" || !found.Columns[2].Nullable {
		t.Fatalf("columns: %+v", found.Columns)
	}
	for _, s := range r.Schemas {
		if s == "pg_catalog" || s == "information_schema" {
			t.Fatalf("system schema listed: %v", r.Schemas)
		}
	}
	if len(r.Schemas) == 0 {
		t.Fatal("no schemas")
	}
	noSecret := target
	noSecret.Secret = ""
	if r := (&probe.Prober{Guard: pretend(t, "10.20.30.0/24", "10.20.30.40", local)}).Probe(context.Background(), noSecret); r.OK || r.Failure.Code != probe.CodeNoSecret {
		t.Fatalf("no secret: %+v", r)
	}
	other := target
	other.Config = map[string]any{"driver": "postgres", "host": "erp.acme.test", "port": float64(cc.Port), "database": "does_not_exist", "user": cc.User, "sslmode": "disable"}
	if r := (&probe.Prober{Guard: pretend(t, "10.20.30.0/24", "10.20.30.40", local)}).Probe(context.Background(), other); r.OK || r.Failure == nil {
		t.Fatalf("unknown database: %+v", r)
	}
}

// The validation list and the driver registry must name the same drivers, or a profile could validate and then find
// no driver (or a linked driver could never be chosen).
func TestSQLDriverRegistryMatchesTheValidatedDrivers(t *testing.T) {
	got, want := probe.SQLDriverNames(), slices.Sorted(slices.Values(domain.SQLDrivers))
	if !slices.Equal(got, want) {
		t.Fatalf("registry %v, domain.SQLDrivers %v", got, want)
	}
	r := (&probe.Prober{Guard: &egress.Guard{}}).Probe(context.Background(), probe.Target{Kind: domain.ConnSQL, Secret: "x",
		Config: map[string]any{"driver": "db2", "host": "erp.acme.example", "database": "erp", "user": "svc"}})
	if r.OK || r.Failure.Code != probe.CodeProbeFailed || !strings.Contains(r.Failure.Message, "db2") {
		t.Fatalf("unregistered driver: %+v", r)
	}
}

func TestProbe_WithoutAGuardNothingIsDialled(t *testing.T) {
	p := &probe.Prober{}
	for _, target := range []probe.Target{
		{Kind: domain.ConnSQL, Secret: "x", Config: map[string]any{"driver": "postgres", "host": "erp.acme.example", "database": "erp", "user": "svc"}},
		restTarget("https://api.acme.example", "none", "", nil),
	} {
		if r := p.Probe(context.Background(), target); r.OK || r.Failure.Code != probe.CodeEgressDenied {
			t.Fatalf("%s without a guard: %+v", target.Kind, r)
		}
	}
}
