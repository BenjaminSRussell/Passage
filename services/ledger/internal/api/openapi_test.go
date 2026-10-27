package api

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestOpenAPI_MatchesRoutes keeps api/openapi.yaml and Register in step: every
// mounted method+path is documented with its duty, and nothing else is.
func TestOpenAPI_MatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/api/v1/ledger/\S+):$`)
	methodRe := regexp.MustCompile(`^    (get|put|post|patch|delete):$`)
	dutyRe := regexp.MustCompile(`^      x-passage-duty: (\S+)$`)
	documented := map[string]string{}
	var path, op string
	for _, line := range strings.Split(string(raw), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			path, op = m[1], ""
			continue
		}
		if strings.HasPrefix(line, "  /") {
			path, op = "", ""
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil && path != "" {
			op = strings.ToUpper(m[1]) + " " + path
			documented[op] = ""
			continue
		}
		if m := dutyRe.FindStringSubmatch(line); m != nil && op != "" {
			documented[op] = m[1]
		}
	}

	r := chi.NewRouter()
	New(&fakeStore{}).Register(r)
	mounted := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted[method+" "+route] = true
		return nil
	})
	var missing, extra []string
	for op := range mounted {
		if _, ok := documented[op]; !ok {
			missing = append(missing, op)
		}
	}
	for op := range documented {
		if !mounted[op] {
			extra = append(extra, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("OpenAPI drift: undocumented routes %v; documented but not mounted %v", missing, extra)
	}

	duties := map[string]string{
		"POST /api/v1/ledger/ledgers":                                              dutyLedgerWrite,
		"GET /api/v1/ledger/ledgers/{id}":                                          dutyRead,
		"PUT /api/v1/ledger/charts/{chart}/accounts/{number}":                      dutyAccountWrite,
		"GET /api/v1/ledger/charts/{chart}/accounts":                               dutyRead,
		"POST /api/v1/ledger/journal-entries":                                      dutyPost,
		"GET /api/v1/ledger/journal-entries/{id}":                                  dutyRead,
		"POST /api/v1/ledger/journal-entries/{id}/reverse":                         dutyReverse,
		"GET /api/v1/ledger/journal-lines":                                         dutyRead,
		"POST /api/v1/ledger/validate":                                             dutyPost,
		"GET /api/v1/ledger/balances":                                              dutyBalanceRead,
		"POST /api/v1/ledger/balances:rebuild":                                     dutyRebuild,
		"GET /api/v1/ledger/reason-codes":                                          dutyRead,
		"GET /api/v1/ledger/reason-codes/{code}":                                   dutyRead,
		"PUT /api/v1/ledger/reason-codes/{code}":                                   dutyReasonWrite,
		"GET /api/v1/ledger/ledgers/{ledger}/account-structures":                   dutyRead,
		"GET /api/v1/ledger/ledgers/{ledger}/account-structures/{id}":              dutyRead,
		"PUT /api/v1/ledger/ledgers/{ledger}/account-structures/{id}":              dutyStructureWrite,
		"GET /api/v1/ledger/ledgers/{ledger}/dimension-values":                     dutyRead,
		"PUT /api/v1/ledger/ledgers/{ledger}/dimension-values/{attribute}/{value}": dutyDimensionWrite,
		"GET /api/v1/ledger/description-templates":                                 dutyRead,
		"GET /api/v1/ledger/description-templates/{id}":                            dutyRead,
		"PUT /api/v1/ledger/description-templates/{id}":                            dutySetupWrite,
		"GET /api/v1/ledger/journal-names":                                         dutyRead,
		"GET /api/v1/ledger/journal-names/{code}":                                  dutyRead,
		"PUT /api/v1/ledger/journal-names/{code}":                                  dutySetupWrite,
		"GET /api/v1/ledger/journal-batches":                                       dutyRead,
		"POST /api/v1/ledger/journal-batches":                                      dutyJournalWrite,
		"GET /api/v1/ledger/journal-batches/{id}":                                  dutyRead,
		"PATCH /api/v1/ledger/journal-batches/{id}":                                dutyJournalWrite,
		"POST /api/v1/ledger/journal-batches/{id}/submit":                          dutyJournalWrite,
		"POST /api/v1/ledger/journal-batches/{id}/approve":                         dutyJournalApprove,
		"POST /api/v1/ledger/journal-batches/{id}/reject":                          dutyJournalApprove,
		"POST /api/v1/ledger/journal-batches/{id}/post":                            dutyPost,
		"POST /api/v1/ledger/journal-batches/{id}/cancel":                          dutyJournalWrite,
	}
	for op, want := range duties {
		if got := documented[op]; got != want {
			t.Errorf("%s: x-passage-duty %q, want %q", op, got, want)
		}
	}
	if len(duties) != len(mounted) {
		t.Errorf("duty table covers %d routes, %d are mounted", len(duties), len(mounted))
	}
}
