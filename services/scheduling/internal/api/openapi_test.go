package api

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

var specOperation = regexp.MustCompile(`^    (get|post|put|patch|delete):$`)

// declaredOperations maps "path method" to whether the operation names a duty.
func declaredOperations(spec string) map[string]bool {
	declared := map[string]bool{}
	inPaths, path, operation := false, "", ""
	for _, line := range strings.Split(spec, "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case !inPaths:
		case strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":"):
			path, operation = strings.TrimSuffix(strings.TrimSpace(line), ":"), ""
		case specOperation.MatchString(line):
			operation = path + " " + specOperation.FindStringSubmatch(line)[1]
			declared[operation] = false
		case operation != "" && strings.HasPrefix(line, "      x-passage-duty: "):
			declared[operation] = true
		}
	}
	return declared
}

// The OpenAPI document lists exactly the served operations, each business
// operation with a duty.
func TestOpenAPIDocumentsEveryOperation(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	_ = chi.Walk(newRouter(&fakeStore{}).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[route+" "+strings.ToLower(method)] = true
		return nil
	})
	declared := declaredOperations(string(spec))
	for op := range served {
		hasDuty, ok := declared[op]
		switch {
		case !ok:
			t.Errorf("not documented: %s", op)
		case !hasDuty && !strings.HasPrefix(op, "/healthz") && !strings.HasPrefix(op, "/readyz"):
			t.Errorf("no x-passage-duty: %s", op)
		}
	}
	for op := range declared {
		if !served[op] {
			t.Errorf("documented but not served: %s", op)
		}
	}
	if len(served) < 14 {
		t.Fatalf("router walk yielded %d operations: %v", len(served), served)
	}
}
