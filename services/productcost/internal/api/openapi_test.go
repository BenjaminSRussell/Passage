package api

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestOpenAPI_MatchesRoutes checks that api/openapi.yaml lists exactly the
// guarded routes the service registers, each with the duty it enforces.
func TestOpenAPI_MatchesRoutes(t *testing.T) {
	f, err := os.Open("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	spec := map[string]string{} // "POST /v1/..." -> duty
	var inPaths bool
	var path, method string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case inPaths && strings.HasPrefix(line, "  /"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case inPaths && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") && strings.HasSuffix(line, ":"):
			method = strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(line), ":"))
		case inPaths && strings.HasPrefix(strings.TrimSpace(line), "x-passage-duty:"):
			spec[method+" "+path] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "x-passage-duty:"))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if spec["GET /readyz"] != "none" {
		t.Errorf("/readyz missing from the spec or not duty-free: %q", spec["GET /readyz"])
	}
	delete(spec, "GET /readyz")
	for _, rt := range New(nil, Config{}).routes() {
		key := rt.method + " " + rt.path
		duty, ok := spec[key]
		if !ok {
			t.Errorf("%s is served but not in openapi.yaml", key)
			continue
		}
		if duty != rt.duty {
			t.Errorf("%s: spec duty %q, handler enforces %q", key, duty, rt.duty)
		}
		delete(spec, key)
	}
	for key := range spec {
		t.Errorf("%s is in openapi.yaml but not served", key)
	}
}
