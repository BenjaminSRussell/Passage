package api_test

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

var specOperation = regexp.MustCompile(`^    (get|post|put|patch|delete):$`)

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

func TestOpenAPIMatchesRoutesAndDuties(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	router, _ := newRouter(fakeDB{}, nil)
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
	tenantID := testkit.TenantID(1)
	for op, duty := range declared {
		if !served[op] {
			t.Errorf("documented but not served: %s", op)
			continue
		}
		if duty == "" {
			t.Errorf("no x-passage-duty: %s", op)
		}
		if duty == "public" {
			continue
		}
		path, method, _ := strings.Cut(op, " ")
		if rec := do(router, strings.ToUpper(method), path, `{}`, tenantID, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s without duties: status %d, want 403", op, rec.Code)
		}
		if rec := do(router, strings.ToUpper(method), path, `{}`, tenantID, duty); rec.Code == http.StatusForbidden {
			t.Errorf("%s with documented duty %s: still 403", op, duty)
		}
	}
	if len(served) < 8 {
		t.Fatalf("router walk yielded %d operations: %v", len(served), served)
	}
}

func TestOpenAPIDutiesAreSeededInPolicy(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile("../../../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, duty := range declaredDuties(string(spec)) {
		if duty != "public" && !strings.Contains(string(policy), `"`+duty+`":`) {
			t.Errorf("%s: duty %q is not seeded in policies/authz/data.json", op, duty)
		}
	}
}
