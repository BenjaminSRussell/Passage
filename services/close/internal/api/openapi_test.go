package api_test

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/close/internal/api"
)

func TestOpenAPI_MatchesRoutesAndDuties(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/v1/close/\S+):$`)
	methodRe := regexp.MustCompile(`^    (get|put|post|patch|delete):$`)
	dutyRe := regexp.MustCompile(`^      x-passage-duty: (\S+)$`)
	documented := map[string]string{}
	var path, op string
	for _, line := range strings.Split(string(raw), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			path, op = m[1], ""
			continue
		}
		if strings.HasPrefix(line, "  /") {
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
	r := httpx.NewRouter()
	api.New(fakeStore{}).Register(r)
	mounted := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/v1/close/") {
			mounted[method+" "+route] = true
		}
		return nil
	})
	var drift []string
	for op := range mounted {
		if _, ok := documented[op]; !ok {
			drift = append(drift, "undocumented "+op)
		}
	}
	for op := range documented {
		if !mounted[op] {
			drift = append(drift, "not mounted "+op)
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		t.Fatalf("OpenAPI drift: %v", drift)
	}
	for op, duty := range map[string]string{
		"POST /v1/close/year-end-runs":           "close.yearend.run",
		"GET /v1/close/year-end-runs/{runId}":    "close.yearend.read",
		"PUT /v1/close/periods/{fiscalPeriodId}": "period.maintain",
	} {
		if documented[op] != duty {
			t.Errorf("%s documents duty %q, want %q", op, documented[op], duty)
		}
	}
}
