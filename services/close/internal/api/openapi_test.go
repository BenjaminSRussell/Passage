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

func TestAsyncAPI_ListsSliceBEvents(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{
		"dev.passage.close.yearend.completed.v1",
		"dev.passage.close.period.statuschanged.v1",
		"dev.passage.close.run.created.v1",
		"dev.passage.close.task.completed.v1",
		"dev.passage.close.allocation.completed.v1",
		"dev.passage.close.accrual.scheduled.v1",
		"dev.passage.close.regrouping.completed.v1",
	} {
		if !strings.Contains(string(raw), typ) {
			t.Errorf("asyncapi missing %s", typ)
		}
	}
}

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
		"POST /v1/close/year-end-runs":                                            "close.yearend.run",
		"GET /v1/close/year-end-runs/{runId}":                                     "close.yearend.read",
		"PUT /v1/close/periods/{fiscalPeriodId}":                                  "period.maintain",
		"GET /v1/close/periods":                                                   "period.maintain",
		"PATCH /v1/close/periods/{fiscalPeriodId}/modules/{module}":               "period.maintain",
		"POST /v1/close/periods/{fiscalPeriodId}/modules/{module}:request-reopen": "period.reopen",
		"POST /v1/close/reopen-requests/{requestId}:approve":                      "period.reopen",
		"POST /v1/close/allocation-runs":                                          "close.allocation.run",
		"POST /v1/close/runs":                                                     "close.checklist.write",
		"POST /v1/close/accrual-schemes/{code}:apply":                             "close.accrual.run",
		"POST /v1/close/regroupings":                                              "close.regrouping.run",
	} {
		if documented[op] != duty {
			t.Errorf("%s documents duty %q, want %q", op, documented[op], duty)
		}
	}
}
