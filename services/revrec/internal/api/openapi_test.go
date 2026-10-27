package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// TestOpenAPI_MatchesRoutes keeps api/openapi.yaml and Register in step: every
// mounted method and path is documented and nothing else is, and each
// operation's x-passage-duty is the duty the route enforces (the documented
// duty alone passes authorization; no duty is 403).
func TestOpenAPI_MatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/v1/\S+):$`)
	methodRe := regexp.MustCompile(`^    (get|put|post|patch|delete):$`)
	dutyRe := regexp.MustCompile(`^      x-passage-duty: (\S+)`)
	documented := map[string]string{}
	var path, op string
	for _, line := range strings.Split(string(raw), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			path, op = m[1], ""
			continue
		}
		if strings.HasPrefix(line, "  /") || (line != "" && !strings.HasPrefix(line, " ")) {
			path, op = "", ""
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil && path != "" {
			op = strings.ToUpper(m[1]) + " " + path
			documented[op] = ""
			continue
		}
		if m := dutyRe.FindStringSubmatch(line); m != nil && op != "" {
			documented[op] = m[1]
		}
	}

	r := chi.NewRouter()
	New(nil).Register(r)
	mounted := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/v1/") {
			mounted[method+" "+route] = true
		}
		return nil
	})
	var missing, extra []string
	for op := range mounted {
		if _, ok := documented[op]; !ok {
			missing = append(missing, op)
		}
	}
	for op := range documented {
		if !mounted[op] {
			extra = append(extra, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("OpenAPI drift: undocumented routes %v; documented but not mounted %v", missing, extra)
	}

	h := newHandler(newFakeStore())
	for op, duty := range documented {
		if duty == "" {
			t.Errorf("%s has no x-passage-duty", op)
			continue
		}
		method, route, _ := strings.Cut(op, " ")
		target := strings.ReplaceAll(route, "{id}", "018f0000-0000-7000-8000-0000000000ff")
		for _, duties := range []string{"", duty} {
			req := httptest.NewRequest(method, target, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
			req.Header.Set(tenant.HeaderDuties, duties)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if forbidden := rec.Code == http.StatusForbidden; forbidden != (duties == "") {
				t.Errorf("%s with duties %q: status %d; the documented duty is %s", op, duties, rec.Code, duty)
			}
		}
	}
}
