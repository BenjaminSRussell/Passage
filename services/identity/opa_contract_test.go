package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	contractTenant   = "tenant-a"
	seededDutyCount  = 97
	unassignedUserID = "user-without-duty"
)

type seededDuty struct {
	Flag        bool `json:"flag"`
	Permissions []struct {
		Resource string   `json:"resource"`
		Methods  []string `json:"methods"`
	} `json:"permissions"`
}

func globRegexp(pattern string) *regexp.Regexp {
	expr := regexp.QuoteMeta(pattern)
	expr = strings.ReplaceAll(expr, `\{,/\*\*\}`, `(/.*)?`)
	expr = strings.ReplaceAll(expr, `\*`, `[^/:]*`)
	return regexp.MustCompile("^" + expr + "$")
}

func concreteResource(pattern string) string {
	if base, ok := strings.CutSuffix(pattern, "{,/**}"); ok {
		return base + "/item-1"
	}
	return strings.ReplaceAll(pattern, "*", "item-1")
}

func (d seededDuty) grants(method, resource string) bool {
	for _, permission := range d.Permissions {
		if !globRegexp(permission.Resource).MatchString(resource) {
			continue
		}
		for _, m := range permission.Methods {
			if m == method {
				return true
			}
		}
	}
	return false
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startRealPolicy(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa binary is required in CI")
		}
		t.Skip("opa binary not on PATH")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	stderr := &syncBuffer{}
	cmd := exec.Command(bin, "run", "--server", "--addr", addr, "../../policies/authz")
	cmd.Stderr = stderr
	cmd.Stdout = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	base := "http://" + addr
	probe := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-exited:
			t.Fatalf("opa exited early: %v\n%s", err, stderr.String())
		case <-deadline:
			t.Fatalf("opa server did not become healthy\n%s", stderr.String())
		default:
		}
		if resp, err := probe.Get(base + "/health"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func putAssignments(t *testing.T, base string, assignments map[string][]string) {
	t.Helper()
	body, err := json.Marshal(map[string]map[string][]string{contractTenant: assignments})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, base+"/v1/data/assignments", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("seed assignments status = %d", resp.StatusCode)
	}
}

type contractHarness struct {
	duties  map[string]seededDuty
	names   []string
	userFor map[string]string
	serve   func(user, method, resource string) *httptest.ResponseRecorder
}

func newContractHarness(t *testing.T) contractHarness {
	t.Helper()
	base := startRealPolicy(t)

	raw, err := os.ReadFile("../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	var seeded struct {
		Duties map[string]seededDuty `json:"duties"`
	}
	if err := json.Unmarshal(raw, &seeded); err != nil {
		t.Fatal(err)
	}
	if len(seeded.Duties) != seededDutyCount {
		t.Fatalf("seeded duties = %d, want %d", len(seeded.Duties), seededDutyCount)
	}
	names := make([]string, 0, len(seeded.Duties))
	for name := range seeded.Duties {
		names = append(names, name)
	}
	sort.Strings(names)

	assignments := map[string][]string{}
	userFor := map[string]string{}
	for i, name := range names {
		user := fmt.Sprintf("user-%d", i)
		userFor[name] = user
		assignments[user] = []string{name}
	}
	putAssignments(t, base, assignments)

	client, err := NewOPAClient(base, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	serve := func(user, method, resource string) *httptest.ResponseRecorder {
		handler := RequireOPA(client, method, resource)(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		)
		req := httptest.NewRequest(method, resource, nil)
		ctx := tenant.With(req.Context(), tenant.Principal{TenantID: contractTenant, UserID: user})
		req = req.WithContext(context.WithValue(ctx, verifiedPrincipalKey{}, true))
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	return contractHarness{duties: seeded.Duties, names: names, userFor: userFor, serve: serve}
}

func assertForbiddenWithDuty(t *testing.T, resp *httptest.ResponseRecorder, who string) {
	t.Helper()
	if resp.Code != http.StatusForbidden {
		t.Errorf("%s: status = %d, want 403", who, resp.Code)
		return
	}
	if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("%s: content type = %q, want application/problem+json", who, ct)
	}
	var problem struct {
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &problem); err != nil || problem.Errors["duty_missing"] == "" {
		t.Errorf("%s: body lacks errors.duty_missing: %s", who, resp.Body.String())
	}
}

func TestDutyContractAgainstRealPolicy(t *testing.T) {
	h := newContractHarness(t)

	type pair struct{ method, resource string }
	seen := map[pair]bool{}
	var pairs []pair
	for _, name := range h.names {
		for _, permission := range h.duties[name].Permissions {
			for _, method := range permission.Methods {
				p := pair{method, concreteResource(permission.Resource)}
				if !seen[p] {
					seen[p] = true
					pairs = append(pairs, p)
				}
			}
		}
	}

	for _, p := range pairs {
		t.Run(p.method+" "+p.resource, func(t *testing.T) {
			for _, name := range h.names {
				resp := h.serve(h.userFor[name], p.method, p.resource)
				if h.duties[name].grants(p.method, p.resource) {
					if resp.Code < 200 || resp.Code > 299 {
						t.Errorf("%q should be allowed: status = %d, want 2xx", name, resp.Code)
					}
					continue
				}
				assertForbiddenWithDuty(t, resp, fmt.Sprintf("%q should be denied", name))
			}
			assertForbiddenWithDuty(t, h.serve(unassignedUserID, p.method, p.resource), "unassigned user")
		})
	}
}

func TestDutyContractExpectations(t *testing.T) {
	h := newContractHarness(t)
	tests := []struct {
		duty, method, resource string
		wantAllowed            bool
		wantDutyMissing        string
	}{
		{duty: "Maintain vendors", method: http.MethodPut, resource: "/v1/vendors", wantAllowed: true},
		{duty: "Maintain vendors", method: http.MethodPut, resource: "/v1/vendors/vendor-a", wantAllowed: true},
		{duty: "View vendors", method: http.MethodGet, resource: "/v1/vendors/vendor-a", wantAllowed: true},
		{duty: "View vendors", method: http.MethodPut, resource: "/v1/vendors", wantDutyMissing: "Maintain vendors"},
		{duty: "Maintain vendors", method: http.MethodPut, resource: "/v1/vendors-extra/vendor-a"},
		{duty: "inventory.journal.write", method: http.MethodPost, resource: "/v1/inventory/journals/j1:post"},
		{duty: "inventory.journal.write", method: http.MethodPost, resource: "/v1/inventory/journals/j1:approve"},
		{duty: "inventory.transfer.write", method: http.MethodPost, resource: "/v1/inventory/transfer-orders/t1:ship"},
		{duty: "inventory.transfer.write", method: http.MethodPost, resource: "/v1/inventory/transfer-orders/t1:receive"},
		{duty: "ledger.journal.write", method: http.MethodPost, resource: "/v1/ledger/journal-batches/b1/approve"},
		{duty: "ledger.journal.write", method: http.MethodPost, resource: "/v1/ledger/journal-batches/b1/post"},
		{duty: "ledger.journal.approve", method: http.MethodPost, resource: "/v1/ledger/journal-batches/b1/post"},
		{duty: "ledger.journal.post", method: http.MethodPost, resource: "/v1/ledger/journal-entries/e1/reverse"},
		{duty: "ledger.journal.reverse", method: http.MethodPost, resource: "/v1/ledger/journal-entries"},
		{duty: "ReceivingMaintain", method: http.MethodPost, resource: "/v1/receiving/receipts/r1:post"},
		{duty: "ReceivingMaintain", method: http.MethodPost, resource: "/v1/receiving/receipts/r1:correct"},
		{duty: "ReceivingMaintain", method: http.MethodPost, resource: "/v1/receiving/receipts/r1:cancel"},
		{duty: "PurchaseOrderMaintain", method: http.MethodPost, resource: "/v1/procurement/purchase-orders/p1:approve"},
		{duty: "RFQMaintain", method: http.MethodPost, resource: "/v1/procurement/rfqs/q1:award"},
		{duty: "ledger.journal.read", method: http.MethodGet, resource: "/v1/reporting/runs/r1"},
		{duty: "reporting.statement.view", method: http.MethodGet, resource: "/v1/reporting/runs/r1/cells/a/b/lines"},
		{duty: "reporting.statement.view", method: http.MethodGet, resource: "/v1/reporting/definitions/d1/export"},
		{duty: "ReceivingPost", method: http.MethodPost, resource: "/v1/receiving/receipts/r1:post", wantAllowed: true},
		{duty: "ledger.journal.read", method: http.MethodGet, resource: "/v1/reporting/runs/r1/cells/a/b/lines", wantAllowed: true},
		{duty: "party.read", method: http.MethodGet, resource: "/v1/party/parties/p1", wantAllowed: true},
		{duty: "party.write", method: http.MethodPost, resource: "/v1/party/pending-changes/x:approve"},
		{duty: "pii.read", method: http.MethodGet, resource: "/v1/party/parties/p1", wantDutyMissing: "party.read"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s %s", tt.duty, tt.method, tt.resource), func(t *testing.T) {
			resp := h.serve(h.userFor[tt.duty], tt.method, tt.resource)
			if tt.wantAllowed {
				if resp.Code < 200 || resp.Code > 299 {
					t.Fatalf("status = %d, want 2xx", resp.Code)
				}
				return
			}
			if resp.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.Code)
			}
			if tt.wantDutyMissing != "" {
				var problem struct {
					Errors map[string]string `json:"errors"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &problem); err != nil || problem.Errors["duty_missing"] != tt.wantDutyMissing {
					t.Fatalf("body = %s, want duty_missing %q", resp.Body.String(), tt.wantDutyMissing)
				}
			}
		})
	}
}

func TestPartyReadDeniedWithoutDuties(t *testing.T) {
	h := newContractHarness(t)
	resp := h.serve(unassignedUserID, http.MethodGet, "/v1/party/parties/p1")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
	var problem struct {
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &problem); err != nil || problem.Errors["duty_missing"] != "party.read" {
		t.Fatalf("body = %s, want duty_missing party.read", resp.Body.String())
	}
}

var coveredServices = []string{"ledger", "fx", "tax", "close", "finreport", "inventory", "purchasing", "receiving", "payables", "party"}

var dutyConstant = regexp.MustCompile(`(?m)\b[dD]uty[A-Za-z0-9]*\s*(?:string\s*)?=\s*"([^"]+)"`)
var dutyCallArgument = regexp.MustCompile(`(?:RequireDuty|HasDuty)\s*\(\s*([^)]*)\)`)

func TestEveryServiceDutyIsSeeded(t *testing.T) {
	raw, err := os.ReadFile("../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	var seeded struct {
		Duties map[string]json.RawMessage `json:"duties"`
	}
	if err := json.Unmarshal(raw, &seeded); err != nil {
		t.Fatal(err)
	}
	for _, service := range coveredServices {
		root := filepath.Join("..", service, "internal")
		var files []string
		err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
				return nil
			}
			files = append(files, file)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, file := range files {
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range dutyConstant.FindAllStringSubmatch(string(src), -1) {
				found++
				if _, ok := seeded.Duties[match[1]]; !ok {
					t.Errorf("%s: duty %q (%s) is not seeded in policies/authz/data.json", service, match[1], file)
				}
			}
			for _, match := range dutyCallArgument.FindAllStringSubmatch(string(src), -1) {
				if argument := strings.TrimSpace(match[1]); !strings.HasPrefix(argument, "duty") && !strings.HasPrefix(argument, "Duty") {
					t.Errorf("%s: %s passes %q to RequireDuty/HasDuty; use a duty* constant", service, file, argument)
				}
			}
		}
		if found == 0 {
			t.Errorf("%s: no duty constants found; the scan is stale", service)
		}
	}
}
