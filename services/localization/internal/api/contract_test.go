package api

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// specOperations reads "METHOD /path" pairs from api/openapi.yaml: paths are
// keys indented by two spaces, methods keys indented by four.
func specOperations(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	path := ""
	inPaths := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && len(line) > 0 && line[0] != ' ':
			inPaths = false
		case inPaths && strings.HasPrefix(line, "  /"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case inPaths && path != "":
			for _, m := range []string{"get", "post", "put", "patch", "delete"} {
				if line == "    "+m+":" {
					out = append(out, strings.ToUpper(m)+" "+path)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestOpenAPIMatchesRoutes checks AC6 "OpenAPI files match code": every
// business route the API registers is documented and every documented
// operation is served (/readyz is the kernel probe, not part of the contract).
func TestOpenAPIMatchesRoutes(t *testing.T) {
	r := chi.NewRouter()
	New(nil).Register(r)
	var served []string
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route != "/readyz" {
			served = append(served, method+" "+route)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(served)
	spec := specOperations(t)
	if strings.Join(served, "\n") != strings.Join(spec, "\n") {
		t.Fatalf("routes and OpenAPI differ\nserved:\n%s\nspec:\n%s", strings.Join(served, "\n"), strings.Join(spec, "\n"))
	}
}
