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

// The OpenAPI document lists exactly the served business operations, each
// with its duty. Kernel health probes are not part of the service contract.
func TestOpenAPIDocumentsEveryOperation(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	withBody := New(nil)
	withBody.AllowBodyOutbound = true
	_ = chi.Walk(withBody.Routes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == "/healthz" || route == "/readyz" {
			return nil
		}
		served[route+" "+strings.ToLower(method)] = true
		return nil
	})
	// The AS2 listener (partners, no Passage identity) is documented in the same file.
	_ = chi.Walk(withBody.AS2Routes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == "/healthz" || route == "/readyz" {
			return nil
		}
		served[route+" "+strings.ToLower(method)] = true
		return nil
	})
	declared := declaredOperations(string(spec))
	for op := range served {
		hasDuty, ok := declared[op]
		switch {
		case !ok:
			t.Errorf("not documented: %s", op)
		case !hasDuty:
			t.Errorf("no x-passage-duty: %s", op)
		}
	}
	for op := range declared {
		if !served[op] {
			t.Errorf("documented but not served: %s", op)
		}
	}
	if len(served) != 59 {
		t.Fatalf("router walk yielded %d operations: %v", len(served), served)
	}
}
