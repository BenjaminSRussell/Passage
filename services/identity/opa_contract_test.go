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
	contractTenant       = "tenant-a"
	unassignedUserID     = "user-without-duty"
	deniedSamplesPerPair = 2
)

type seededDuty struct {
	Flag        bool `json:"flag"`
	Permissions []struct {
		Resource string   `json:"resource"`
		Methods  []string `json:"methods"`
	} `json:"permissions"`
}

var globRegexps sync.Map

func globRegexp(pattern string) *regexp.Regexp {
	if cached, ok := globRegexps.Load(pattern); ok {
		return cached.(*regexp.Regexp)
	}
	expr := regexp.QuoteMeta(pattern)
	expr = strings.ReplaceAll(expr, `\{,/\*\*\}`, `(/.*)?`)
	expr = strings.ReplaceAll(expr, `\*`, `[^/:]*`)
	compiled := regexp.MustCompile("^" + expr + "$")
	globRegexps.Store(pattern, compiled)
	return compiled
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
	if len(seeded.Duties) == 0 {
		t.Fatal("no duties seeded")
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

	for i, p := range pairs {
		t.Run(p.method+" "+p.resource, func(t *testing.T) {
			for _, name := range h.names {
				if h.duties[name].grants(p.method, p.resource) {
					if resp := h.serve(h.userFor[name], p.method, p.resource); resp.Code < 200 || resp.Code > 299 {
						t.Errorf("%q should be allowed: status = %d, want 2xx", name, resp.Code)
					}
				}
			}
			for k := 0; k < deniedSamplesPerPair; k++ {
				name := h.names[(i*31+k*131)%len(h.names)]
				if !h.duties[name].grants(p.method, p.resource) {
					assertForbiddenWithDuty(t, h.serve(h.userFor[name], p.method, p.resource), fmt.Sprintf("%q should be denied", name))
				}
			}
			assertForbiddenWithDuty(t, h.serve(unassignedUserID, p.method, p.resource), "unassigned user")
		})
	}

	t.Run("every duty is denied a route it does not grant", func(t *testing.T) {
		for j, name := range h.names {
			for step := 0; step < len(pairs); step++ {
				p := pairs[(j*53+step)%len(pairs)]
				if h.duties[name].grants(p.method, p.resource) {
					continue
				}
				assertForbiddenWithDuty(t, h.serve(h.userFor[name], p.method, p.resource), fmt.Sprintf("%q should be denied %s %s", name, p.method, p.resource))
				break
			}
		}
	})
}

func TestDutyContractExpectations(t *testing.T) {
	h := newContractHarness(t)
	tests := []struct {
		duty, method, resource string
		wantAllowed            bool
		wantDutyMissing        string
	}{
		{duty: "View effective permissions", method: http.MethodGet, resource: "/v1/identity/users/u1/effective-permissions", wantAllowed: true},
		{duty: "Maintain users", method: http.MethodGet, resource: "/v1/identity/users/u1/effective-permissions", wantAllowed: true},
		{duty: "View effective permissions", method: http.MethodGet, resource: "/v1/identity/users/u1", wantDutyMissing: "Maintain users"},
		{duty: "View effective permissions", method: http.MethodPost, resource: "/v1/identity/users/u1/effective-permissions", wantDutyMissing: "Maintain users"},
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
		{duty: "ledger.journal.postledger", method: http.MethodPost, resource: "/v1/ledger/journal-entries"},
		{duty: "ReceivingMaintain", method: http.MethodPost, resource: "/v1/receiving/receipts/r1:post"},
		{duty: "ReceivingMaintain", method: http.MethodPost, resource: "/v1/receiving/receipts/r1:correct"},
		{duty: "ReceivingMaintain", method: http.MethodPost, resource: "/v1/receiving/receipts/r1:cancel"},
		{duty: "PurchaseOrderMaintain", method: http.MethodPost, resource: "/v1/procurement/purchase-orders/p1:approve"},
		{duty: "RFQMaintain", method: http.MethodPost, resource: "/v1/procurement/rfqs/q1:award"},
		{duty: "ledger.journal.read", method: http.MethodGet, resource: "/v1/reporting/runs/r1"},
		{duty: "reporting.statement.view", method: http.MethodGet, resource: "/v1/reporting/runs/r1/cells/a/b/lines"},
		{duty: "reporting.statement.view", method: http.MethodGet, resource: "/v1/reporting/definitions/d1/export"},
		{duty: "ReceivingPost", method: http.MethodPost, resource: "/v1/receiving/receipts/r1:post", wantAllowed: true},
		{duty: "VendorInvoicePost", method: http.MethodPost, resource: "/v1/receiving/invoice-adjustments:post", wantAllowed: true},
		{duty: "VendorInvoicePost", method: http.MethodPost, resource: "/v1/receiving/plan-settlements:run", wantAllowed: true},
		{duty: "VendorInvoicePost", method: http.MethodPost, resource: "/v1/receiving/consignment-settlements:run", wantAllowed: true},
		{duty: "VendorInvoicePost", method: http.MethodPost, resource: "/v1/receiving/ers-settlements:run", wantAllowed: true},
		{duty: "InvoiceAuditRelease", method: http.MethodPost, resource: "/v1/receiving/invoices/i1:release-audit-block", wantAllowed: true},
		{duty: "VendorInvoicePost", method: http.MethodPost, resource: "/v1/receiving/invoices/i1:release-audit-block"},
		{duty: "InvoiceAuditRelease", method: http.MethodPost, resource: "/v1/receiving/invoices:post"},
		{duty: "MatchingPolicyMaintain", method: http.MethodPut, resource: "/v1/receiving/audit-policy", wantAllowed: true},
		{duty: "MatchingPolicyMaintain", method: http.MethodPut, resource: "/v1/receiving/ers-vendors/v1", wantAllowed: true},
		{duty: "InvoiceAuditRelease", method: http.MethodPost, resource: "/v1/receiving/invoices/i1:reject-audit-block", wantAllowed: true},
		{duty: "VendorInvoicePost", method: http.MethodPost, resource: "/v1/receiving/invoices/i1:reject-audit-block"},
		{duty: "ErsPriceConfirm", method: http.MethodPut, resource: "/v1/receiving/receipts/r1/lines/1/ers-price-confirmation", wantAllowed: true},
		{duty: "VendorInvoicePost", method: http.MethodPut, resource: "/v1/receiving/receipts/r1/lines/1/ers-price-confirmation"},
		{duty: "ErsPriceConfirm", method: http.MethodPost, resource: "/v1/receiving/ers-settlements:run"},
		{duty: "ErsPriceConfirm", method: http.MethodPost, resource: "/v1/receiving/invoices:post"},
		{duty: "VendorInvoicePost", method: http.MethodPut, resource: "/v1/receiving/audit-policy"},
		{duty: "VendorInvoicePost", method: http.MethodPut, resource: "/v1/receiving/ers-vendors/v1"},
		{duty: "ReceivingPost", method: http.MethodPost, resource: "/v1/receiving/invoice-adjustments:post"},
		{duty: "ledger.journal.read", method: http.MethodGet, resource: "/v1/reporting/runs/r1/cells/a/b/lines", wantAllowed: true},
		{duty: "party.read", method: http.MethodGet, resource: "/v1/party/parties/p1", wantAllowed: true},
		{duty: "party.write", method: http.MethodPost, resource: "/v1/party/pending-changes/x:approve"},
		{duty: "pii.read", method: http.MethodGet, resource: "/v1/party/parties/p1", wantDutyMissing: "party.read"},
		{duty: "party.approve", method: http.MethodPut, resource: "/v1/party/legal-entities/le1/protected-fields", wantDutyMissing: "party.approval_policy.write"},
		{duty: "party.approval_policy.write", method: http.MethodPut, resource: "/v1/party/legal-entities/le1/protected-fields", wantAllowed: true},
		{duty: "Approve production promotion", method: http.MethodPost, resource: "/v1/platform/releases", wantAllowed: true},
		{duty: "Rollback environment", method: http.MethodPost, resource: "/v1/platform/releases", wantAllowed: true},
		{duty: "View platform releases", method: http.MethodPost, resource: "/v1/platform/releases"},
		{duty: "Rollback environment", method: http.MethodGet, resource: "/v1/platform/releases"},
		{duty: "party.read", method: http.MethodGet, resource: "/v1/party/vendors/v1", wantAllowed: true},
		{duty: "party.read", method: http.MethodGet, resource: "/v1/party/vendors/v1/bank-accounts", wantAllowed: true},
		{duty: "party.read", method: http.MethodGet, resource: "/v1/party/vendors/v1/bank-accounts:reveal", wantDutyMissing: "party.vendor_bank.reveal"},
		{duty: "party.vendor_bank.reveal", method: http.MethodGet, resource: "/v1/party/vendors/v1/bank-accounts:reveal", wantAllowed: true},
		{duty: "party.write", method: http.MethodPost, resource: "/v1/party/vendors/v1/bank-accounts", wantDutyMissing: "party.vendor_bank.write"},
		{duty: "party.vendor_bank.write", method: http.MethodPost, resource: "/v1/party/vendors/v1/bank-accounts", wantAllowed: true},
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

var (
	dutyDefinition = regexp.MustCompile(`(?m)\b(\w*[dD]uty\w*)\s*(?:string\s*)?=\s*(?:"([^"]+)"|(?:[A-Za-z_]\w*\.)?(\w*[dD]uty\w*)\s*$)`)
	dutyCall       = regexp.MustCompile(`\b(?:RequireDuty|HasDuty)\s*\(\s*([^,)]*(?:\([^)]*\))?[^,)]*)[,)]`)
	dutyListCall   = regexp.MustCompile(`\b(?:RequireAnyDuty|requireAnyDuty|requireAny|anyDuty)\s*[({]([^)}]*)[)}]`)

	dutyFuncDeclaration = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?(?:RequireDuty|HasDuty|RequireAnyDuty|requireAnyDuty|requireAny|anyDuty)\(`)
	identifierPath      = regexp.MustCompile(`^(?:[A-Za-z_]\w*\.)?([A-Za-z_]\w*)$`)
	dutyLiteral         = regexp.MustCompile(`^"([^"]+)"$`)
)

var dynamicDutyArguments = map[string]string{
	"assets/internal/api/ops.go duty":                       "requireAny loops over caller-listed duty constants",
	"assets/internal/api/ops.go duties":                     "requireAny forwards the duty constants its callers list",
	"sales/internal/api/api.go duties":                      "requireAny forwards the duty constants its callers list",
	"batch/internal/jobs/productclient.go duty":             "loops over the product duties the handler declares",
	"consolidation/internal/api/api.go rt.duty":             "route table of duty constants",
	"platform-health/internal/api/api.go duty":              "anyDutyPolicy loops over the release duty constants the route lists",
	"platform-health/internal/api/release.go duty":          "requiredReleaseDuty returns one of the release duty constants",
	"eam/internal/api/api.go d":                             "anyDuty loops over caller-listed duty constants",
	"fieldservice/internal/store/generic.go accessDuty":     "category access duty read from catalogue rows",
	"fieldservice/internal/store/generic.go cat.AccessDuty": "category access duty read from catalogue rows",
	"fieldservice/internal/store/intake.go cat.AccessDuty":  "category access duty read from catalogue rows",
	"identity/effective_api.go duty":                        "requireAnyDuty loops over caller-listed duties",
	"identity/effective_api.go role":                        "role names validated against the seeded catalogue",
	"identity/role_assignments_api.go req.Role":             "role names validated against the seeded catalogue",
	"intercompany/internal/api/api.go rt.duty":              "route table of duty constants",
	"org/internal/api/translation.go o.writeDuty":           "translationOwners table of duty constants",
	"pricing/internal/api/fund.go duty":                     "anyDuty loops over caller-listed duty constants",
	"productcost/internal/api/api.go rt.duty":               "route table of duty constants",
	"production/internal/api/api.go commandDuty[cmd]":       "commandDuty table of duty constants",
	"production/internal/api/batch.go duty":                 "commandDuty lookup of duty constants",
	"receivables/internal/api/api.go duty":                  "require() receives duty constants from handlers",
	"sales/internal/api/api.go d":                           "anyDuty loops over caller-listed duty constants",
	"sales/internal/api/orders.go need":                     "releaseDuty returns duty constants",
	"warehouse/internal/api/receiving.go duty":              "anyDuty loops over caller-listed duty constants",
	"warehouse/internal/api/receiving.go need":              "direction picks a duty constant",
}

func dutyScanRoots(t *testing.T) map[string]string {
	t.Helper()
	roots := map[string]string{"migration-factory": filepath.Join("..", "..", "tools", "migration-factory")}
	entries, err := os.ReadDir("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			roots[entry.Name()] = filepath.Join("..", entry.Name())
		}
	}
	return roots
}

func goSourceFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go") {
			files = append(files, file)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func codeLines(src string) []string {
	var lines []string
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || dutyFuncDeclaration.MatchString(trimmed) {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func TestDutyArgumentPatterns(t *testing.T) {
	cases := map[string]string{
		"dutyRead":             "dutyRead",
		"store.DutyPostDirect": "DutyPostDirect",
		"duty":                 "duty",
		"commandDuty[cmd]":     "",
		"dutyFor(x)":           "",
		"req.Role":             "Role",
	}
	for argument, want := range cases {
		got := ""
		if match := identifierPath.FindStringSubmatch(argument); match != nil {
			got = match[1]
		}
		if got != want {
			t.Errorf("%q: identifier = %q, want %q", argument, got, want)
		}
	}
	if dutyLiteral.FindStringSubmatch(`"party.read"`)[1] != "party.read" {
		t.Error("string literal not recognised")
	}
}

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

	platformScopeDuties := map[string]string{}
	for _, file := range goSourceFiles(t, filepath.Join("..", "..", "platform", "go", "tenant")) {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range dutyDefinition.FindAllStringSubmatch(string(src), -1) {
			if match[2] != "" {
				platformScopeDuties[match[1]] = match[2]
			}
		}
	}
	usedExemptions := map[string]bool{}
	scanned := 0
	for service, root := range dutyScanRoots(t) {
		files := goSourceFiles(t, root)
		values := map[string]string{}
		aliases := map[string]string{}
		sources := map[string][]string{}
		for _, file := range files {
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			sources[file] = codeLines(string(src))
			for _, match := range dutyDefinition.FindAllStringSubmatch(strings.Join(sources[file], "\n"), -1) {
				if lowered := strings.ToLower(match[1]); strings.HasPrefix(lowered, "code") || strings.HasPrefix(lowered, "err") {
					continue
				}
				if match[2] != "" {
					values[match[1]] = match[2]
				} else {
					aliases[match[1]] = match[3]
				}
			}
		}
		for name, target := range aliases {
			if _, ok := values[target]; !ok {
				t.Errorf("%s: duty alias %s = %s does not resolve to a string constant", service, name, target)
				continue
			}
			values[name] = values[target]
		}

		found := 0
		for name, value := range values {
			found++
			if _, ok := seeded.Duties[value]; !ok {
				t.Errorf("%s: duty %q (%s) is not seeded in policies/authz/data.json", service, value, name)
			}
		}
		for _, file := range files {
			relative := filepath.ToSlash(strings.TrimPrefix(file, root+string(filepath.Separator)))
			for _, line := range sources[file] {
				var arguments []string
				for _, match := range dutyCall.FindAllStringSubmatch(line, -1) {
					arguments = append(arguments, strings.TrimSpace(match[1]))
				}
				for _, match := range dutyListCall.FindAllStringSubmatch(line, -1) {
					for _, argument := range strings.Split(match[1], ",") {
						if argument = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(argument), "...")); argument != "" {
							arguments = append(arguments, argument)
						}
					}
				}
				for _, argument := range arguments {
					if literal := dutyLiteral.FindStringSubmatch(argument); literal != nil {
						found++
						if _, ok := seeded.Duties[literal[1]]; !ok {
							t.Errorf("%s: duty %q (%s) is not seeded in policies/authz/data.json", service, literal[1], file)
						}
						continue
					}
					if identifier := identifierPath.FindStringSubmatch(argument); identifier != nil {
						if _, ok := values[identifier[1]]; ok {
							continue
						}
						if _, ok := platformScopeDuties[identifier[1]]; ok {
							continue
						}
					}
					key := service + "/" + relative + " " + argument
					if _, ok := dynamicDutyArguments[key]; ok {
						usedExemptions[key] = true
						continue
					}
					t.Errorf("%s: %s passes %q to RequireDuty/HasDuty; pass a duty constant or a string literal", service, file, argument)
				}
			}
		}
		if len(files) > 0 && found > 0 {
			scanned++
		}
	}
	if scanned == 0 {
		t.Error("no service duties found; the scan is stale")
	}
	for key, reason := range dynamicDutyArguments {
		if !usedExemptions[key] {
			t.Errorf("dynamicDutyArguments entry %q (%s) matches no call; remove it", key, reason)
		}
	}
}
