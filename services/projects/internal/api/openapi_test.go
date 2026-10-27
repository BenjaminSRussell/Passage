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

// TestOpenAPIMatchesRoutes compares the method and path of every mounted
// /v1/projects route with the operations in api/openapi.yaml, both ways.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	r := chi.NewRouter()
	New(newFake()).Register(r)
	var routes []string
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/v1/projects") {
			routes = append(routes, method+" "+strings.TrimSuffix(route, "/"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pathLine := regexp.MustCompile(`^  (/v1/projects[^:]*):$`)
	methodLine := regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	var spec []string
	current := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := pathLine.FindStringSubmatch(sc.Text()); m != nil {
			current = m[1]
			continue
		}
		if strings.HasPrefix(sc.Text(), "components:") {
			current = ""
		}
		if m := methodLine.FindStringSubmatch(sc.Text()); m != nil && current != "" {
			spec = append(spec, strings.ToUpper(m[1])+" "+current)
		}
	}
	sort.Strings(routes)
	sort.Strings(spec)
	if strings.Join(routes, "\n") != strings.Join(spec, "\n") {
		t.Fatalf("routes and OpenAPI differ\nroutes:\n%s\n\nopenapi:\n%s", strings.Join(routes, "\n"), strings.Join(spec, "\n"))
	}
}
