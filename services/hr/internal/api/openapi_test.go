package api

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// TestOpenAPIMatchesRoutes keeps api/openapi.yaml and Register in step: every
// mounted /v1 method and path is documented and nothing else is, and each
// operation's x-passage-duty is the one the route enforces (every other HR
// duty together is still 403).
func TestOpenAPIMatchesRoutes(t *testing.T) {
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

	all := []string{dutyWorkerMaintain, dutyWorkerInquire, dutyPIIRead, dutyActionMaintain, dutyOrgMaintain, dutyOrgInquire,
		dutyAbsenceAdmin, dutyEssAbsence, dutyMssApprove, dutySetupAdmin, dutyEssBenefits,
		dutyIncidentReport, dutyIncidentAdmin, dutyQuestionnaireAdmin, dutyEssQuestionnaire}
	for op, duty := range documented {
		if duty == "" {
			t.Errorf("%s has no x-passage-duty", op)
			continue
		}
		var others []string
		for _, d := range all {
			if d != duty {
				others = append(others, d)
			}
		}
		method, route, _ := strings.Cut(op, " ")
		target := strings.NewReplacer("{id}", testkit.TenantID(9), "{skillCode}", "WELDING").Replace(route)
		if rec := serve(t, tid(), method, target, "", others...); rec.Code != http.StatusForbidden {
			t.Errorf("%s with every duty but %s: status = %d, want 403", op, duty, rec.Code)
		}
	}
}
