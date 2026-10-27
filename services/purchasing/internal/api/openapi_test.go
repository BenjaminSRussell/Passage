package api

import (
	"bufio"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// openAPIOperations reads "METHOD path" pairs from api/openapi.yaml by
// indentation: two spaces for a path under paths:, four for its methods.
func openAPIOperations(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ops := map[string]bool{}
	inPaths, path := false, ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "paths:":
			inPaths = true
		case !inPaths:
		case strings.HasPrefix(line, "  /"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     "):
			method := strings.TrimSuffix(strings.TrimSpace(line), ":")
			switch method {
			case "get", "post", "patch", "put", "delete":
				ops[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	return ops
}

func TestOpenAPI_MatchesRegisteredRoutes(t *testing.T) {
	r := chi.NewRouter()
	New(newFake()).Register(r)
	routes := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == "/readyz" {
			return nil
		}
		route = strings.TrimSuffix(route, "/")
		if route == "/v1/procurement/requisitions/{id}" && method == http.MethodPost {
			route += ":submit" // the custom method is dispatched inside requisitionAction
		}
		routes[method+" "+route] = true
		return nil
	})
	spec := openAPIOperations(t)
	if len(routes) == 0 || len(spec) == 0 {
		t.Fatalf("found %d routes and %d spec operations", len(routes), len(spec))
	}
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
	if len(missing)+len(extra) > 0 {
		t.Fatalf("routes not in openapi.yaml: %v; openapi.yaml operations with no route: %v", missing, extra)
	}
}
