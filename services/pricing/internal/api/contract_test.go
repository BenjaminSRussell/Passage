package api_test

import (
	"bufio"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/api"
)

// openAPIOperations lists "METHOD /path" for every operation in api/openapi.yaml.
func openAPIOperations(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ops []string
	path := ""
	inPaths := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case inPaths && strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case inPaths && path != "":
			for _, m := range []string{"get", "post", "put", "patch", "delete"} {
				if line == "    "+m+":" {
					ops = append(ops, strings.ToUpper(m)+" "+path)
				}
			}
		}
	}
	sort.Strings(ops)
	return ops
}

// TestOpenAPIMatchesRoutes keeps api/openapi.yaml and the router in step (AC6).
func TestOpenAPIMatchesRoutes(t *testing.T) {
	r := httpx.NewRouter()
	api.New(newFake(), api.Config{}).Register(r)
	var routes []string
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/v1/") {
			routes = append(routes, method+" "+route)
		}
		return nil
	})
	sort.Strings(routes)
	if len(routes) < 7 {
		t.Fatalf("expected the seven pricing routes, walked %v", routes)
	}
	if want := openAPIOperations(t); strings.Join(routes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("router and OpenAPI differ\nrouter:\n%s\nopenapi:\n%s", strings.Join(routes, "\n"), strings.Join(want, "\n"))
	}
}
