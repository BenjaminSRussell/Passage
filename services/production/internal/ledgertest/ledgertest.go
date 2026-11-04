// Package ledgertest runs the real ledger service for production's integration tests (W21 AC4, issue #478).
//
// The ledger lives in another Go module and its packages are internal, so it cannot be mounted in this process: Start
// builds services/ledger/cmd/ledger from this tree (or takes PASSAGE_TEST_LEDGER_BIN), runs it against a throwaway
// schema on the PASSAGE_TEST_DATABASE_URL server and trusts only production's service key. Production reaches it
// with the real ledgerclient over HTTP with signed service tokens, as it does when deployed. Tests that call Start are
// skipped when PASSAGE_TEST_DATABASE_URL is unset or under -short. Only tests import this package.
package ledgertest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// EnvBin names a prebuilt ledger binary; empty builds one from this tree once per test process.
const EnvBin = "PASSAGE_TEST_LEDGER_BIN"

// Caller is the service name the ledger trusts and the client signs as.
const Caller = "production"

// Server is a running ledger service.
type Server struct {
	URL    string
	Client *ledgerclient.Client
	http   *http.Client
}

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

func binary(t testing.TB) string {
	t.Helper()
	if bin := os.Getenv(EnvBin); bin != "" {
		return bin
	}
	buildOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		ledgerDir := filepath.Join(filepath.Dir(file), "..", "..", "..", "ledger")
		// One fixed path per checkout and test package, rebuilt (from the Go build cache) by each test process, so runs
		// leave no temp dirs behind and packages testing in parallel do not replace each other's binary.
		cwd, _ := os.Getwd()
		dir := filepath.Join(os.TempDir(), fmt.Sprintf("passage-ledgertest-%x", sha256.Sum256([]byte(ledgerDir+"\x00"+cwd)))[:40])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(dir, "ledger")
		cmd := exec.Command("go", "build", "-o", builtBin, "./cmd/ledger")
		cmd.Dir = ledgerDir
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ledger: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBin
}

func freeAddr(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// Start runs a ledger on its own schema and returns it with a client signing as production.
func Start(t testing.TB) *Server {
	t.Helper()
	dbURL := os.Getenv(testkit.EnvURL)
	if dbURL == "" {
		t.Skipf("%s not set; skipping real-ledger test", testkit.EnvURL)
	}
	if testing.Short() {
		t.Skip("real-ledger test builds and runs the ledger service; skipped under -short")
	}
	bin := binary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "t_ledger_" + strings.ReplaceAll(ids.New(), "-", "")
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "drop schema "+schema+" cascade")
		admin.Close()
	})
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	logFile, err := os.Create(filepath.Join(t.TempDir(), "ledger.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "PASSAGE_ENV=dev",
		"DATABASE_URL=" + u.String(), "MIGRATE_ON_START=true", "LEDGER_PERIOD_CHECK=disabled",
		"HTTP_ADDR=" + addr, "ADMIN_ADDR=" + freeAddr(t),
		tenant.EnvServiceName + "=ledger",
		tenant.EnvServicePublicKeys + "=" + Caller + "=" + base64.StdEncoding.EncodeToString(pub),
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			t.Logf("ledger log:\n%s", tail(logFile.Name()))
		}
		_ = logFile.Close()
	})
	base := "http://" + addr
	hc := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for {
		select {
		case err := <-exited:
			exited <- err
			t.Fatalf("ledger exited before it was ready: %v\n%s", err, tail(logFile.Name()))
		default:
		}
		if res, err := hc.Get(base + "/readyz"); err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ledger not ready on %s\n%s", base, tail(logFile.Name()))
		}
		time.Sleep(200 * time.Millisecond)
	}
	signer, err := tenant.NewServiceSigner(Caller, priv)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ledgerclient.New(base, Caller, signer, hc)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{URL: base, Client: client, http: hc}
}

func tail(path string) string {
	b, _ := os.ReadFile(path)
	if len(b) > 4000 {
		b = b[len(b)-4000:]
	}
	return string(b)
}

// AccountType is the ledger account type a production account number takes: 1xxx asset, 2xxx liability, 3xxx equity,
// 4xxx revenue, anything else expense.
func AccountType(number string) string {
	switch {
	case strings.HasPrefix(number, "1"):
		return "asset"
	case strings.HasPrefix(number, "2"):
		return "liability"
	case strings.HasPrefix(number, "3"):
		return "equity"
	case strings.HasPrefix(number, "4"):
		return "revenue"
	}
	return "expense"
}

// CreateLedger opens a ledger for legalEntity in currency on a chart holding accounts, as a finance user through the
// gateway headers, and returns its id. The ledger is open from 2000-01-01 so any posting date the subledger stores is
// accepted.
func (s *Server) CreateLedger(t testing.TB, tenantID, legalEntity, chart, currency string, accounts []string) string {
	t.Helper()
	s.PutAccounts(t, tenantID, legalEntity, chart, accounts)
	open := "2000-01-01"
	var created struct {
		ID string `json:"id"`
	}
	code := strings.ToUpper(strings.ReplaceAll(legalEntity, "-", ""))[:10]
	s.user(t, http.MethodPost, "/v1/ledger/ledgers", tenantID, legalEntity, "ledger.ledger.write",
		map[string]any{"legalEntityId": legalEntity, "code": code, "name": "Demo plant " + code, "chartCode": chart, "currency": currency, "firstOpenDate": open}, &created)
	if !ids.Valid(created.ID) {
		t.Fatalf("ledger create returned no id")
	}
	return created.ID
}

// PutAccounts opens (or replaces) postable main accounts on chart.
func (s *Server) PutAccounts(t testing.TB, tenantID, legalEntity, chart string, accounts []string) {
	t.Helper()
	for _, n := range accounts {
		s.user(t, http.MethodPut, "/v1/ledger/charts/"+chart+"/accounts/"+n, tenantID, legalEntity, "ledger.account.write",
			map[string]any{"name": "Account " + n, "accountType": AccountType(n)}, nil)
	}
}

func (s *Server) user(t testing.TB, method, path, tenantID, legalEntity, duty string, body, out any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, s.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, tenantID)
	req.Header.Set(tenant.HeaderLegalEntity, legalEntity)
	req.Header.Set(tenant.HeaderUser, "finance-setup")
	req.Header.Set(tenant.HeaderDuties, duty)
	req.Header.Set("Idempotency-Key", ids.New())
	res, err := s.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if res.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, buf.String())
	}
	if out != nil {
		if err := json.Unmarshal(buf.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v %s", method, path, err, buf.String())
		}
	}
}
