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
	pathRe := regexp.MustCompile(`^  (/v1/ledger/\S+):$`)
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
		"POST /v1/ledger/ledgers":                                                   dutyLedgerWrite,
		"GET /v1/ledger/ledgers/{id}":                                               dutyRead,
		"PUT /v1/ledger/charts/{chart}/accounts/{number}":                           dutyAccountWrite,
		"GET /v1/ledger/charts/{chart}/accounts":                                    dutyRead,
		"GET /v1/ledger/charts/{chart}/accounts/{number}":                           dutyRead,
		"POST /v1/ledger/journal-entries":                                           dutyPost,
		"GET /v1/ledger/journal-entries/{id}":                                       dutyRead,
		"POST /v1/ledger/journal-entries/{id}/reverse":                              dutyReverse,
		"GET /v1/ledger/journal-lines":                                              dutyRead,
		"POST /v1/ledger/validate":                                                  dutyPost,
		"GET /v1/ledger/balances":                                                   dutyBalanceRead,
		"POST /v1/ledger/balances:rebuild":                                          dutyRebuild,
		"GET /v1/ledger/reason-codes":                                               dutyRead,
		"GET /v1/ledger/reason-codes/{code}":                                        dutyRead,
		"PUT /v1/ledger/reason-codes/{code}":                                        dutyReasonWrite,
		"GET /v1/ledger/ledgers/{ledger}/account-structures":                        dutyRead,
		"GET /v1/ledger/ledgers/{ledger}/account-structures/{id}":                   dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/account-structures/{id}":                   dutyStructureWrite,
		"GET /v1/ledger/ledgers/{ledger}/dimension-values":                          dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/dimension-values/{attribute}/{value}":      dutyDimensionWrite,
		"GET /v1/ledger/description-templates":                                      dutyRead,
		"GET /v1/ledger/description-templates/{id}":                                 dutyRead,
		"PUT /v1/ledger/description-templates/{id}":                                 dutySetupWrite,
		"GET /v1/ledger/journal-names":                                              dutyRead,
		"GET /v1/ledger/journal-names/{code}":                                       dutyRead,
		"PUT /v1/ledger/journal-names/{code}":                                       dutySetupWrite,
		"GET /v1/ledger/journal-batches":                                            dutyRead,
		"POST /v1/ledger/journal-batches":                                           dutyJournalWrite,
		"GET /v1/ledger/journal-batches/{id}":                                       dutyRead,
		"PATCH /v1/ledger/journal-batches/{id}":                                     dutyJournalWrite,
		"POST /v1/ledger/journal-batches/{id}/submit":                               dutyJournalWrite,
		"POST /v1/ledger/journal-batches/{id}/approve":                              dutyJournalApprove,
		"POST /v1/ledger/journal-batches/{id}/reject":                               dutyJournalApprove,
		"POST /v1/ledger/journal-batches/{id}/post":                                 dutyPost,
		"POST /v1/ledger/journal-batches/{id}/cancel":                               dutyJournalWrite,
		"GET /v1/ledger/recurring-journals":                                         dutyRead,
		"GET /v1/ledger/recurring-journals/{id}":                                    dutyRead,
		"PUT /v1/ledger/recurring-journals/{id}":                                    dutyJournalWrite,
		"POST /v1/ledger/recurring-journals/{id}/pause":                             dutyJournalWrite,
		"POST /v1/ledger/recurring-journals/{id}/resume":                            dutyJournalWrite,
		"POST /v1/ledger/recurring-journals/{id}/generate":                          dutyJournalWrite,
		"GET /v1/ledger/recurring-journals/{id}/runs":                               dutyRead,
		"GET /v1/ledger/ledger-sets":                                                dutyRead,
		"GET /v1/ledger/ledgers/{id}/account-mappings":                              dutyRead,
		"POST /v1/ledger/ledger-sets":                                               dutySetupWrite,
		"GET /v1/ledger/ledger-sets/{setId}":                                        dutyRead,
		"POST /v1/ledger/ledger-sets/{setId}/journal-entries":                       dutyPost,
		"POST /v1/ledger/ledgers/{id}/account-mappings":                             dutySetupWrite,
		"GET /v1/ledger/ledgers/{ledger}/open-item-accounts":                        dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/open-item-accounts/{account}":              dutyAccountWrite,
		"GET /v1/ledger/ledgers/{ledger}/tolerance-groups":                          dutyRead,
		"GET /v1/ledger/ledgers/{ledger}/tolerance-groups/{code}":                   dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/tolerance-groups/{code}":                   dutyClearingRules,
		"GET /v1/ledger/ledgers/{ledger}/lookup-tables":                             dutyRead,
		"GET /v1/ledger/ledgers/{ledger}/lookup-tables/{id}":                        dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/lookup-tables/{id}":                        dutyRulesWrite,
		"GET /v1/ledger/ledgers/{ledger}/splitting-rules":                           dutyRead,
		"GET /v1/ledger/ledgers/{ledger}/splitting-rules/{id}":                      dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/splitting-rules/{id}":                      dutyRulesWrite,
		"GET /v1/ledger/ledgers/{ledger}/derivation-strategies":                     dutyRead,
		"GET /v1/ledger/ledgers/{ledger}/derivation-strategies/{version}":           dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/derivation-strategies/{version}":           dutyRulesWrite,
		"POST /v1/ledger/ledgers/{ledger}/derivation-strategies/{version}/activate": dutyRulesWrite,
		"GET /v1/ledger/journal-batches/{id}/attachments":                           dutyRead,
		"PUT /v1/ledger/journal-batches/{id}/attachments/{attachmentId}":            dutyJournalWrite,
		"DELETE /v1/ledger/journal-batches/{id}/attachments/{attachmentId}":         dutyJournalWrite,
		"GET /v1/ledger/journal-entries/{id}/attachments":                           dutyRead,
		"GET /v1/ledger/ledgers/{ledger}/advanced-rules":                            dutyRead,
		"GET /v1/ledger/ledgers/{ledger}/advanced-rules/{id}":                       dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/advanced-rules/{id}":                       dutyStructureWrite,
		"GET /v1/ledger/ledgers/{ledger}/validation-rules":                          dutyRead,
		"GET /v1/ledger/ledgers/{ledger}/validation-rules/{id}":                     dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/validation-rules/{id}":                     dutyRulesWrite,
		"GET /v1/ledger/ledgers/{ledger}/realignments":                              dutyRead,
		"POST /v1/ledger/ledgers/{ledger}/realignments":                             dutyRealign,
		"GET /v1/ledger/ledgers/{ledger}/realignments/{id}":                         dutyRead,
		"POST /v1/ledger/ledgers/{ledger}/realignments/{id}:post":                   dutyRealign,
		"POST /v1/ledger/ledgers/{ledger}/realignments/{id}:reverse":                dutyRealign,
		"POST /v1/ledger/ledgers/{ledger}/realignments/{id}:cancel":                 dutyRealign,
		"POST /v1/ledger/ledgers/{ledger}/open-item-accounts/{account}:activate":    dutyRebuild,
		"GET /v1/ledger/ledgers/{ledger}/open-item-activations":                     dutyRead,
		"GET /v1/ledger/open-items":                                                 dutyRead,
		"POST /v1/ledger/clearings:simulate":                                        dutyClearingWrite,
		"GET /v1/ledger/clearings":                                                  dutyRead,
		"POST /v1/ledger/clearings":                                                 dutyClearingWrite,
		"GET /v1/ledger/clearings/{id}":                                             dutyRead,
		"POST /v1/ledger/clearings/{id}/reset":                                      dutyClearingReset,
		"GET /v1/ledger/ledgers/{ledger}/auto-clearing-rules":                       dutyRead,
		"GET /v1/ledger/ledgers/{ledger}/auto-clearing-rules/{id}":                  dutyRead,
		"PUT /v1/ledger/ledgers/{ledger}/auto-clearing-rules/{id}":                  dutyClearingRules,
		"POST /v1/ledger/auto-clearing:run":                                         dutyClearingRun,
		"GET /v1/ledger/auto-clearing-runs":                                         dutyRead,
		"GET /v1/ledger/auto-clearing-runs/{id}":                                    dutyRead,
		"PATCH /v1/ledger/ledgers/{id}":                                             dutyLedgerWrite,
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
