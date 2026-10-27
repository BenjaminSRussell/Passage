package api

import (
	"bufio"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

var (
	pathLine   = regexp.MustCompile(`^  (/api/\S+):$`)
	methodLine = regexp.MustCompile(`^    (get|put|post|patch|delete):$`)
)

// openAPIOperations reads "METHOD path" pairs from api/openapi.yaml.
func openAPIOperations(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ops := map[string]bool{}
	current := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := pathLine.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if m := methodLine.FindStringSubmatch(line); m != nil && current != "" {
			ops[strings.ToUpper(m[1])+" "+current] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return ops
}

// The routes the API registers and the operations api/openapi.yaml documents
// are the same set. The colon custom methods share one wildcard route.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	documented := openAPIOperations(t)
	registered := map[string]bool{}
	r := chi.NewRouter()
	New(&fakeStore{}).Register(r)
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.ReplaceAll(route, "/*/", "/")
		if strings.HasSuffix(route, "/definitions/*") {
			for _, action := range []string{"publish", "validate"} {
				registered[method+" "+strings.TrimSuffix(route, "*")+"{code}:"+action] = true
			}
			return nil
		}
		registered[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var missing, extra []string
	for op := range registered {
		if !documented[op] {
			missing = append(missing, op)
		}
	}
	for op := range documented {
		if !registered[op] {
			extra = append(extra, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("routes not in openapi.yaml: %v; openapi operations without a route: %v", missing, extra)
	}
}
