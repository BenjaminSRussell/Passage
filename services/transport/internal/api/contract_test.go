package api_test

import (
	"bufio"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
)

type operation struct{ method, path, duty string }

var (
	pathLine   = regexp.MustCompile(`^  (/v1/transport/\S+):$`)
	methodLine = regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	dutyLine   = regexp.MustCompile(`^      x-passage-duty: (\S+)$`)
)

// specOperations reads method, path and x-passage-duty from openapi.yaml.
func specOperations(t *testing.T) []operation {
	t.Helper()
	f, err := os.Open("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ops []operation
	var path string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := pathLine.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if m := methodLine.FindStringSubmatch(line); m != nil && path != "" {
			ops = append(ops, operation{method: strings.ToUpper(m[1]), path: path})
		} else if m := dutyLine.FindStringSubmatch(line); m != nil && len(ops) > 0 {
			ops[len(ops)-1].duty = m[1]
		} else if line == "components:" {
			break
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return ops
}

// TestOpenAPI_MatchesRoutes checks that openapi.yaml documents exactly the
// mounted routes, and that each route answers 403 without its documented duty
// and passes the duty check with it.
func TestOpenAPI_MatchesRoutes(t *testing.T) {
	spec := specOperations(t)
	r := chi.NewRouter()
	api.New(&fakeStore{}).Register(r)
	var mounted []string
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted = append(mounted, method+" "+route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var documented []string
	for _, op := range spec {
		documented = append(documented, op.method+" "+op.path)
	}
	sort.Strings(mounted)
	sort.Strings(documented)
	if strings.Join(mounted, "\n") != strings.Join(documented, "\n") {
		t.Fatalf("routes and openapi.yaml differ\nmounted:\n%s\ndocumented:\n%s", strings.Join(mounted, "\n"), strings.Join(documented, "\n"))
	}
	ids := strings.NewReplacer("{id}", loadID)
	for _, op := range spec {
		if op.duty == "" {
			t.Fatalf("%s %s has no x-passage-duty", op.method, op.path)
		}
		path := ids.Replace(op.path)
		if rec := do(t, r, op.method, path, `{}`); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s without a duty = %d, want 403", op.method, op.path, rec.Code)
		}
		if rec := do(t, r, op.method, path, `{}`, op.duty); rec.Code == http.StatusForbidden {
			t.Fatalf("%s %s with %s = 403", op.method, op.path, op.duty)
		}
	}
}
