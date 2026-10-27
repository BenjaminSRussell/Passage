package api

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

var (
	specPath      = regexp.MustCompile(`^  (/api/v1/receivables[^ ]*):$`)
	specOperation = regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	// A custom method on an id ({id}:apply) is served by the {ref} route that dispatches it.
	idAction = regexp.MustCompile(`\{id\}(:[a-z-]+)?`)
)

// specOperations reads "route method" pairs from openapi.yaml, mapped onto chi's
// route patterns, and whether each declares its x-passage-duty.
func specOperations(spec string) map[string]bool {
	out := map[string]bool{}
	path, op := "", ""
	for _, line := range strings.Split(spec, "\n") {
		switch {
		case specPath.MatchString(line):
			path, op = idAction.ReplaceAllString(specPath.FindStringSubmatch(line)[1], "{ref}"), ""
		case path != "" && specOperation.MatchString(line):
			op = path + " " + specOperation.FindStringSubmatch(line)[1]
			out[op] = out[op] || false
		case op != "" && strings.HasPrefix(line, "      x-passage-duty: "):
			out[op] = true
		case line == "components:":
			path, op = "", ""
		}
	}
	return out
}

func TestOpenAPIMatchesTheRoutes(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	declared := specOperations(string(spec))
	served := map[string]bool{}
	_ = chi.Walk(router(&fakeStore{}).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[route+" "+strings.ToLower(method)] = true
		return nil
	})
	if len(served) < 30 {
		t.Fatalf("router walk found %d operations; it is not seeing the routes", len(served))
	}
	var problems []string
	for op := range served {
		if hasDuty, ok := declared[op]; !ok {
			problems = append(problems, "not documented: "+op)
		} else if !hasDuty {
			problems = append(problems, "no x-passage-duty: "+op)
		}
	}
	for op := range declared {
		if !served[op] {
			problems = append(problems, "not served: "+op)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
