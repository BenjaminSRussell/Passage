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

// Every route the router serves is documented in api/openapi.yaml with the same method, and
// the spec documents nothing the router does not serve.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	var path string
	pathRe := regexp.MustCompile(`^  (/api/v1/\S+):$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	for _, line := range strings.Split(string(spec), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if m := methodRe.FindStringSubmatch(line); m != nil && path != "" {
			documented[strings.ToUpper(m[1])+" "+path] = true
		}
	}
	served := map[string]bool{}
	r := chi.NewRouter()
	New(unreachableStore{t}).Register(r)
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[method+" "+strings.TrimSuffix(route, "/")] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var missing, extra []string
	for k := range served {
		if !documented[k] {
			missing = append(missing, k)
		}
	}
	for k := range documented {
		if !served[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 || len(served) == 0 {
		t.Fatalf("undocumented routes %v; documented but not served %v", missing, extra)
	}
}
