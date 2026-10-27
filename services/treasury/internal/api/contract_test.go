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

	tevents "github.com/BenjaminSRussell/passage/services/treasury/internal/events"
)

// specOperations reads "METHOD /path" pairs from api/openapi.yaml: paths are
// keys indented by two spaces, method keys indented by four.
func specOperations(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	path, inPaths := "", false
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

// TestOpenAPIMatchesRoutes checks AC6: every registered route is documented and
// every documented operation is served.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	r := chi.NewRouter()
	New(newFakeStore()).Register(r)
	var served []string
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served = append(served, method+" "+route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(served)
	if spec := specOperations(t); strings.Join(served, "\n") != strings.Join(spec, "\n") {
		t.Fatalf("routes and OpenAPI differ\nserved:\n%s\nspec:\n%s", strings.Join(served, "\n"), strings.Join(spec, "\n"))
	}
}

// TestAsyncAPIListsEveryEvent checks that each published event type is a message in events/asyncapi.yaml.
func TestAsyncAPIListsEveryEvent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "events", "asyncapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range tevents.All() {
		if !strings.Contains(string(raw), "name: "+typ) {
			t.Fatalf("asyncapi.yaml has no message named %s", typ)
		}
	}
}
