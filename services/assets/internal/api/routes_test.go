package api_test

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/services/assets/internal/api"
)

// TestRoutesMatchOpenAPI keeps api/openapi.yaml in step with the router: every
// route the API registers is a documented path and method with its duty, and
// every documented path is registered.
func TestRoutesMatchOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	r := chi.NewRouter()
	api.New(newFake()).Register(r)
	registered := map[string]bool{}
	err = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[route] = true
		block := pathBlock(spec, route)
		if block == "" {
			t.Errorf("%s %s is not in openapi.yaml", method, route)
			return nil
		}
		if !strings.Contains(block, "\n    "+strings.ToLower(method)+":") {
			t.Errorf("%s %s: method not documented", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^  (/[^:]+):$`).FindAllStringSubmatch(spec, -1) {
		if !registered[m[1]] {
			t.Errorf("openapi.yaml documents %s, which is not registered", m[1])
		}
	}
}

// pathBlock returns the YAML under "  <path>:" up to the next path.
func pathBlock(spec, path string) string {
	start := strings.Index(spec, "\n  "+path+":\n")
	if start < 0 {
		return ""
	}
	rest := spec[start+1:]
	next := regexp.MustCompile(`\n  [/a-z]`).FindStringIndex(rest[1:])
	if next == nil {
		return rest
	}
	return rest[:next[0]+1]
}
