package api

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	qevents "github.com/BenjaminSRussell/passage/services/quality/internal/events"
)

// pathBlocks splits the OpenAPI paths section into path -> its indented block.
func pathBlocks(spec string) map[string]string {
	out := map[string]string{}
	re := regexp.MustCompile(`(?m)^  (/\S+):$`)
	locs := re.FindAllStringSubmatchIndex(spec, -1)
	end := strings.Index(spec, "\ncomponents:")
	for i, loc := range locs {
		stop := end
		if i+1 < len(locs) {
			stop = locs[i+1][0]
		}
		out[spec[loc[2]:loc[3]]] = spec[loc[1]:stop]
	}
	return out
}

// TestSpecs_MatchRoutes keeps openapi.yaml and asyncapi.yaml in step with the
// router and the event types: every route and method is documented with its
// duty, every documented path is routed, and every event is in AsyncAPI.
func TestSpecs_MatchRoutes(t *testing.T) {
	openapi, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	asyncapi, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	blocks := pathBlocks(string(openapi))
	r := httpx.NewRouter()
	New(newFake()).Register(r)
	routed := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == "/healthz" {
			return nil
		}
		routed[route] = true
		block, ok := blocks[route]
		if !ok {
			t.Errorf("openapi.yaml does not document %s", route)
			return nil
		}
		op := "\n    " + strings.ToLower(method) + ":"
		at := strings.Index(block, op)
		if at < 0 {
			t.Errorf("openapi.yaml does not document %s %s", method, route)
			return nil
		}
		if route != "/readyz" && !strings.Contains(block[at:], "x-passage-duty: QM_") {
			t.Errorf("%s %s has no x-passage-duty", method, route)
		}
		return nil
	})
	for path := range blocks {
		if !routed[path] {
			t.Errorf("openapi.yaml documents %s but no route is registered", path)
		}
	}
	for _, typ := range qevents.All() {
		if !events.ValidType(typ) || !strings.Contains(string(asyncapi), "name: "+typ) || !strings.Contains(string(asyncapi), "address: "+events.Topic(typ)) {
			t.Errorf("asyncapi.yaml does not document %s on %s", typ, events.Topic(typ))
		}
	}
}
