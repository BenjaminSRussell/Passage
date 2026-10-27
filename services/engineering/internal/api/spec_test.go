package api_test

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
	specPathLine   = regexp.MustCompile(`^  (/\S+):$`)
	specMethodLine = regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
)

func TestRoutesMatchOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	path := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if m := specPathLine.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if m := specMethodLine.FindStringSubmatch(line); m != nil && path != "" {
			documented[strings.ToUpper(m[1])+" "+path] = true
		} else if line != "" && !strings.HasPrefix(line, " ") {
			path = ""
		}
	}
	r, _ := newRouter(&fakeStore{}, approves)
	registered := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[method+" "+route] = true
		return nil
	})
	var diffs []string
	for op := range registered {
		if !documented[op] {
			diffs = append(diffs, "registered but not in openapi.yaml: "+op)
		}
	}
	for op := range documented {
		if !registered[op] {
			diffs = append(diffs, "in openapi.yaml but not registered: "+op)
		}
	}
	sort.Strings(diffs)
	for _, d := range diffs {
		t.Error(d)
	}
}
