package api

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestOpenAPIMatchesRoutes checks that api/openapi.yaml documents exactly the
// duty-guarded routes the API mounts, each with the same method and duty.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathLine := regexp.MustCompile(`^  (/v1/[^:]+(?::[a-z]+)?):$`)
	methodLine := regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	dutyLine := regexp.MustCompile(`^      x-passage-duty: (\S+)$`)
	var documented []string
	var path, method string
	for _, line := range strings.Split(string(raw), "\n") {
		if m := pathLine.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if strings.HasPrefix(line, "  /") || !strings.HasPrefix(line, " ") {
			path = ""
		}
		if m := methodLine.FindStringSubmatch(line); m != nil {
			method = strings.ToUpper(m[1])
		}
		if m := dutyLine.FindStringSubmatch(line); m != nil && path != "" {
			documented = append(documented, method+" "+path+" "+m[1])
		}
	}
	var mounted []string
	for _, rt := range New(nil).routes() {
		mounted = append(mounted, rt.method+" "+rt.path+" "+rt.duty)
	}
	sort.Strings(documented)
	sort.Strings(mounted)
	if strings.Join(documented, "\n") != strings.Join(mounted, "\n") {
		t.Fatalf("openapi.yaml documents\n%s\nbut the API mounts\n%s", strings.Join(documented, "\n"), strings.Join(mounted, "\n"))
	}
}
