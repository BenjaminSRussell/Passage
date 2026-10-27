package api

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

var specOperation = regexp.MustCompile(`^    (get|post|put|patch|delete):$`)

// declaredDuties maps "path method" to the operation's x-passage-duty ("" when absent).
func declaredDuties(spec string) map[string]string {
	declared := map[string]string{}
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
			declared[operation] = ""
		case operation != "" && strings.HasPrefix(line, "      x-passage-duty: "):
			declared[operation] = strings.TrimPrefix(line, "      x-passage-duty: ")
		}
	}
	return declared
}

// The OpenAPI document lists exactly the served operations, and the duty it
// names for each business operation is the one the route enforces.
func TestOpenAPIMatchesRoutesAndDuties(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	router := httpx.NewRouter()
	New(nil).Register(router)
	served := map[string]bool{}
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[route+" "+strings.ToLower(method)] = true
		return nil
	})
	declared := declaredDuties(string(spec))
	for op := range served {
		if _, ok := declared[op]; !ok {
			t.Errorf("not documented: %s", op)
		}
	}
	for op, duty := range declared {
		if !served[op] {
			t.Errorf("documented but not served: %s", op)
			continue
		}
		if duty == "" {
			t.Errorf("no x-passage-duty: %s", op)
		}
		if duty == "public" || !strings.HasSuffix(op, " post") {
			continue
		}
		path := strings.TrimSuffix(op, " post")
		if rec := serve(t, path, `{}`, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s without duties: status %d, want 403", op, rec.Code)
		}
		if rec := serve(t, path, `{}`, duty); rec.Code == http.StatusForbidden {
			t.Errorf("%s with documented duty %s: still 403", op, duty)
		}
	}
	if len(served) < 9 {
		t.Fatalf("router walk yielded %d operations: %v", len(served), served)
	}
}
