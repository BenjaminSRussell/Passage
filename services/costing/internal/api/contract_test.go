package api

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

// TestOpenAPIDocumentsEveryRoute keeps api/openapi.yaml in step with Register:
// every mounted route has a path entry with its method, and the spec lists no
// operation the router does not serve.
func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	paths := spec[strings.Index(spec, "\npaths:\n"):strings.Index(spec, "\ncomponents:\n")]
	blocks := map[string]string{}
	for _, chunk := range strings.Split(paths, "\n  /")[1:] {
		path, body, _ := strings.Cut(chunk, ":\n")
		blocks["/"+path] = body
	}
	r := httpx.NewRouter()
	New(&fakeStore{}).Register(r)
	served := 0
	err = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == "/healthz" { // liveness comes from the kernel router, not this service
			return nil
		}
		served++
		body, ok := blocks[route]
		if !ok {
			t.Errorf("%s %s is served but has no path in openapi.yaml", method, route)
			return nil
		}
		if !strings.Contains(body, "\n    "+strings.ToLower(method)+":\n") && !strings.HasPrefix(body, "    "+strings.ToLower(method)+":\n") {
			t.Errorf("%s %s: the path has no %s operation in openapi.yaml", method, route, strings.ToLower(method))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	documented := strings.Count(paths, "\n    get:\n") + strings.Count(paths, "\n    post:\n")
	if documented != served {
		t.Errorf("openapi.yaml documents %d operations, the router serves %d", documented, served)
	}
}
