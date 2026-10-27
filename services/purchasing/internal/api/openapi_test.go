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
// indentation: two spaces for a path under paths:, four for its methods. It
// fails the test when a path key repeats.
func openAPIOperations(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ops := map[string]bool{}
	seen := map[string]bool{}
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
			// A repeated path key is invalid YAML that lenient parsers merge silently.
			if seen[path] {
				t.Errorf("path %s appears twice in openapi.yaml; put all its methods under one key", path)
			}
			seen[path] = true
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

// customMethods lists the ":action" suffixes served by one POST route.
var customMethods = map[string][]string{
	"/v1/procurement/requisitions/{id}":    {"submit"},
	"/v1/procurement/agreements/{id}":      {"activate", "hold", "close"},
	"/v1/procurement/purchase-orders/{id}": {"submit", "approve", "reject", "confirm", "cancel"},
	"/v1/procurement/rfqs/{id}":            {"send", "close", "cancel", "award"},
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
		// Custom methods are dispatched inside requisitionAction and
		// purchaseAgreementAction; each action is its own spec operation.
		if actions, ok := customMethods[route]; ok && method == http.MethodPost {
			for _, action := range actions {
				routes[method+" "+route+":"+action] = true
			}
			return nil
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
