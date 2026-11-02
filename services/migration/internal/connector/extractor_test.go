package connector_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
)

// pagesOf is a reader whose next position is chosen by the test.
type pagesOf struct {
	next  func(pos string) string
	reads int
}

func (p *pagesOf) Read(_ context.Context, q connector.Query) (connector.Page, error) {
	p.reads++
	return connector.Page{Rows: []connector.Row{{"id": strconv.Itoa(p.reads)}}, Next: p.next(q.Position)}, nil
}

func extractorOver(r connector.Reader, maxPages int) *connector.Extractor {
	return &connector.Extractor{MaxPages: maxPages, Bindings: map[string]connector.Binding{"customer": {
		KeyFields: []string{"id"},
		Open:      func(context.Context) (connector.Opened, error) { return connector.Opened{Reader: r}, nil },
	}}}
}

func TestExtractor_FailsWhenTheNextPositionDoesNotChange(t *testing.T) {
	r := &pagesOf{next: func(string) string { return "same" }}
	_, _, err := extractorOver(r, 0).ExtractSince(context.Background(), "customer", "")
	if err == nil || !strings.Contains(err.Error(), "same next page position") || r.reads != 2 {
		t.Fatalf("after %d reads: %v", r.reads, err)
	}
}

func TestExtractor_FailsAfterTheMaximumPageCount(t *testing.T) {
	r := &pagesOf{next: func(pos string) string { return pos + "x" }} // always a new position, never the end
	_, _, err := extractorOver(r, 5).ExtractSince(context.Background(), "customer", "")
	if err == nil || !strings.Contains(err.Error(), "more than 5 pages") || r.reads != 5 {
		t.Fatalf("after %d reads: %v", r.reads, err)
	}
	r = &pagesOf{next: func(pos string) string {
		if len(pos) >= 4 {
			return ""
		}
		return pos + "x"
	}}
	recs, _, err := extractorOver(r, 5).ExtractSince(context.Background(), "customer", "")
	if err != nil || len(recs) != 5 {
		t.Fatalf("five pages within a bound of five: %d records, %v", len(recs), err)
	}
}

func TestPreview_EndsAtItsOverallDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // a source that never answers in time
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	defer srv.Close()
	env, base := restEnv(t, srv)
	old := connector.PreviewTimeout
	connector.PreviewTimeout = 200 * time.Millisecond
	defer func() { connector.PreviewTimeout = old }()
	start := time.Now()
	_, err := connector.Preview(context.Background(), env, restSpec(base, map[string]any{"path": "/customers"}, "", nil), nil, 5)
	if err == nil || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("preview took %v: %v", time.Since(start), err)
	}
}
