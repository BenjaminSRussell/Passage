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

// TestOpenAPI_MatchesRoutes checks that api/openapi.yaml documents exactly the
// routes the handler serves (method and path).
func TestOpenAPI_MatchesRoutes(t *testing.T) {
	f, err := os.Open("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pathRe := regexp.MustCompile(`^  (/[^:]*):\s*$`)
	opRe := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	documented := map[string]bool{}
	current := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := pathRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if m := opRe.FindStringSubmatch(line); m != nil && current != "" {
			documented[strings.ToUpper(m[1])+" "+current] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	h := New(nil, nil)
	r.Mount("/", h.router())
	served := map[string]bool{}
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(strings.ReplaceAll(route, "/*/", "/"), "/")
		served[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(served) < 20 || len(documented) < 20 {
		t.Fatalf("parsed %d served and %d documented routes; the walk or the parser is broken", len(served), len(documented))
	}
	diff := func(a, b map[string]bool) []string {
		var out []string
		for k := range a {
			if !b[k] {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
	if missing := diff(served, documented); len(missing) > 0 {
		t.Errorf("routes missing from openapi.yaml: %v", missing)
	}
	if extra := diff(documented, served); len(extra) > 0 {
		t.Errorf("openapi.yaml documents routes the handler does not serve: %v", extra)
	}
}
