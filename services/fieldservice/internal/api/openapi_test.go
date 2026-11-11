package api

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// specOperations reads "METHOD path" pairs from api/openapi.yaml: a path is a
// two-space-indented key under paths:, its methods four-space-indented keys.
func specOperations(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]bool{}
	inPaths, path := false, ""
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case inPaths && strings.HasPrefix(line, "  /"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case inPaths && path != "" && len(line) > 4 && line[:4] == "    " && line[4] != ' ':
			method := strings.TrimSuffix(strings.TrimSpace(line), ":")
			switch method {
			case "get", "post", "put", "patch", "delete":
				ops[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	return ops
}

// Every registered route is documented in OpenAPI and nothing documented is missing from the router.
func TestOpenAPI_MatchesRoutes(t *testing.T) {
	r := chi.NewRouter()
	a := New(nil)
	a.Register(r)
	a.RegisterStreams(r)
	routes := map[string]bool{}
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	spec := specOperations(t)
	var missing, extra []string
	for op := range routes {
		if !spec[op] {
			missing = append(missing, op)
		}
	}
	for op := range spec {
		if !routes[op] {
			extra = append(extra, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("routes not in openapi.yaml: %v; documented but not routed: %v", missing, extra)
	}
}
