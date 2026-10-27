package api

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

// TestOpenAPI_MatchesRoutes keeps api/openapi.yaml in step with Register: every
// mounted method and path is documented and nothing documented is missing.
// /healthz and /readyz are mounted by main and documented too.
func TestOpenAPI_MatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	path := ""
	pathLine := regexp.MustCompile(`^  (/\S*):\s*$`)
	methodLine := regexp.MustCompile(`^    (get|put|post|patch|delete):\s*$`)
	for _, line := range strings.Split(string(raw), "\n") {
		if m := pathLine.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if m := methodLine.FindStringSubmatch(line); m != nil && path != "" {
			documented[strings.ToUpper(m[1])+" "+path] = true
		} else if strings.HasPrefix(line, "components:") {
			break
		}
	}
	r := httpx.NewRouter()
	New(nil).Register(r)
	mounted := map[string]bool{"GET /healthz": true, "GET /readyz": true}
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	diff := func(a, b map[string]bool) []string {
		var out []string
		for k := range a {
			if !b[k] {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
	if missing := diff(mounted, documented); len(missing) > 0 {
		t.Errorf("routes missing from openapi.yaml: %v", missing)
	}
	if extra := diff(documented, mounted); len(extra) > 0 {
		t.Errorf("openapi.yaml documents routes that are not mounted: %v", extra)
	}
}
