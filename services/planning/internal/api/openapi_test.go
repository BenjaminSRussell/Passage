package api

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestOpenAPI_MatchesRoutes keeps api/openapi.yaml and Register in step: every
// mounted method+path is documented with the duty the route enforces, and
// nothing else is documented.
func TestOpenAPI_MatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/v1/planning/\S+):$`)
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

	r := chi.NewRouter()
	New(&fakeStore{}).Register(r)
	mounted := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route != "/readyz" { // operational probe, not part of the API contract
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

	duties := map[string]string{
		"POST /v1/planning/plans/{planCode}:run":              dutyRun,
		"GET /v1/planning/plans/{planCode}/versions":          dutyRead,
		"GET /v1/planning/plans/{planCode}/versions/{number}": dutyRead,
		"GET /v1/planning/planned-orders":                     dutyRead,
		"GET /v1/planning/planned-orders/{id}":                dutyRead,
		"GET /v1/planning/planned-orders/{id}/pegging":        dutyRead,
		"GET /v1/planning/plans/{planCode}/diff":              dutyRead,
		"GET /v1/planning/messages":                           dutyRead,
		"GET /v1/planning/net-requirements":                   dutyRead,
		"PATCH /v1/planning/planned-orders/{id}":              dutyMaintainOrders,
		"DELETE /v1/planning/planned-orders/{id}":             dutyMaintainOrders,
		"POST /v1/planning/planned-orders:firm":               dutyFirmOrders,
		"GET /v1/planning/forecasts/{modelCode}/lines":        dutyRead,
		"PUT /v1/planning/forecasts/{modelCode}/lines":        dutyMaintainForecast,
		"POST /v1/planning/forecasts/{modelCode}:submit":      dutyMaintainForecast,
		"POST /v1/planning/forecasts/{modelCode}:publish":     dutyApproveForecast,
		"POST /v1/planning/forecasts/{modelCode}:reject":      dutyApproveForecast,
		"POST /v1/planning/ddmrp/buffers:evaluate":            dutyRead,
		"POST /v1/planning/quota-arrangements:allocate":       dutyRead,
		"POST /v1/planning/strategies:consume-forecast":       dutyRead,
		"POST /v1/planning/supply-schedule:project":           dutyRead,
		"POST /v1/planning/shelf-life:allocate":               dutyRead,
		"GET /v1/planning/coverage-groups":                    dutyRead,
		"POST /v1/planning/coverage-groups":                   dutyMaintainSetup,
		"GET /v1/planning/coverage-groups/{code}":             dutyRead,
		"PUT /v1/planning/coverage-groups/{code}":             dutyMaintainSetup,
		"GET /v1/planning/strategies":                         dutyRead,
		"POST /v1/planning/strategies":                        dutyMaintainSetup,
		"GET /v1/planning/strategies/{code}":                  dutyRead,
		"PUT /v1/planning/strategies/{code}":                  dutyMaintainSetup,
		"GET /v1/planning/item-coverages":                     dutyRead,
		"POST /v1/planning/item-coverages":                    dutyMaintainSetup,
		"GET /v1/planning/item-coverages/{id}":                dutyRead,
		"PUT /v1/planning/item-coverages/{id}":                dutyMaintainSetup,
		"GET /v1/planning/buffer-profiles":                    dutyRead,
		"POST /v1/planning/buffer-profiles":                   dutyMaintainSetup,
		"GET /v1/planning/buffer-profiles/{id}":               dutyRead,
		"PUT /v1/planning/buffer-profiles/{id}":               dutyMaintainSetup,
		"GET /v1/planning/ddmrp/buffers":                      dutyRead,
		"POST /v1/planning/ddmrp/buffers:recalculate":         dutyRun,
		"GET /v1/planning/quota-arrangements":                 dutyRead,
		"POST /v1/planning/quota-arrangements":                dutyMaintainSetup,
		"GET /v1/planning/quota-arrangements/{id}":            dutyRead,
		"PUT /v1/planning/quota-arrangements/{id}":            dutyMaintainSetup,
		"GET /v1/planning/scenarios":                          dutyRead,
		"POST /v1/planning/scenarios":                         dutySimulate,
		"GET /v1/planning/scenarios/{code}":                   dutyRead,
		"PUT /v1/planning/scenarios/{code}":                   dutySimulate,
		"POST /v1/planning/scenarios/{code}:run":              dutySimulate,
	}
	for op, want := range duties {
		if got := documented[op]; got != want {
			t.Errorf("%s: x-passage-duty %q, want %q", op, got, want)
		}
	}
	if len(duties) != len(mounted) {
		t.Errorf("duty table covers %d routes, %d are mounted", len(duties), len(mounted))
	}
}
