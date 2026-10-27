package api

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

var specOperation = regexp.MustCompile(`^    (get|put|post|patch|delete):$`)

// declaredOperations reads "path method" → has x-passage-duty from the paths section.
func declaredOperations(spec string) map[string]bool {
	declared := map[string]bool{}
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
			declared[operation] = false
		case operation != "" && strings.HasPrefix(line, "      x-passage-duty: "):
			declared[operation] = true
		}
	}
	return declared
}

// The router serves the `{id}:{command}` custom methods and `{id}:split` on
// the {id} route; the spec documents them on their own templated paths.
func servedOperations(t *testing.T) map[string]bool {
	t.Helper()
	r := httpx.NewRouter()
	New(&fakeStore{}, Config{}).Register(r)
	served := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == "/healthz" || route == "/readyz" {
			return nil
		}
		if route == "/v1/production/orders/{id}" && method == http.MethodPost {
			served["/v1/production/orders/{id}:split post"] = true
			route = "/v1/production/orders/{id}:{command}"
		}
		if route == "/v1/production/journals/{id}" && method == http.MethodPost {
			route = "/v1/production/journals/{id}:post"
		}
		if route == "/v1/production/as-built/{id}" && method == http.MethodPost {
			route = "/v1/production/as-built/{id}:detach"
		}
		served[route+" "+strings.ToLower(method)] = true
		return nil
	})
	return served
}

func TestOpenAPIMatchesTheRouter(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	declared := declaredOperations(string(raw))
	served := servedOperations(t)
	if len(served) != 29 {
		t.Fatalf("router walk found %d operations: %v", len(served), served)
	}
	var problems []string
	for op := range served {
		if hasDuty, ok := declared[op]; !ok {
			problems = append(problems, "not documented: "+op)
		} else if !hasDuty {
			problems = append(problems, "no x-passage-duty: "+op)
		}
	}
	for op := range declared {
		if !served[op] {
			problems = append(problems, "documented but not served: "+op)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// Every duty and error code the handlers use is named in the spec.
func TestOpenAPINamesDutiesAndCodes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	for _, name := range []string{DutyRead, DutyWrite, DutyRelease, DutyJournalPost, DutyEnd, DutyZeroOutput, DutyCostRead,
		DutyOverConsume, DutySetupRead, DutySetupWrite, store.CodeJournalNotFound, store.CodeJournalNotAllowed, store.CodeJournalPosted,
		store.CodeJournalEmpty, store.CodeInvalidJournalLine, store.CodeOperationFinished, store.CodePostingBeforeOrder,
		store.CodeUnpostedJournals, store.CodeEndBeforeLastPosted, domain.CodeNoPostingProfile, domain.CodeOverConsumption,
		domain.CodeUnbalanced, domain.CodeInvalidHours, DutyRework, domain.OriginRework, domain.CodeReworkReference,
		domain.CodeAsBuiltState, domain.CodeAsBuiltNotOnBOM, domain.CodeAsBuiltExceedsConsumed, domain.CodeAsBuiltFinishedCount,
		domain.CodeSerialDuplicate, store.CodeAsBuiltNotFound, domain.CodeSplitQuantity, domain.CodeMergeRefused, domain.CodeBulkPartial,
		domain.OriginSplit, DutyBOMAfterRelease, domain.CodeEditNotAllowed, domain.CodeEditTargetExists, domain.CodeEditTargetInUse,
		domain.CodeEditTargetMissing, domain.CodeTriggerCondition, domain.CodeTriggerFunctionUnsupported, domain.CodeTriggerFunctionFailed,
		store.CodeTriggerPointNotFound, store.CodeTriggerPointExists, store.CodeTriggerPointFired, store.CodeTriggerOperationMissing,
		store.CodeTriggerOrderClosed, string(domain.FnInsertOperations), string(domain.FnCreateReworkOrder), string(domain.FnNotify),
		unknownActionCode, "PRODUCTION_ILLEGAL_TRANSITION", "PRODUCTION_RAF_EXCEEDS_REMAINING", "PRODUCTION_FIELD_NOT_EDITABLE",
		"PRODUCTION_END_REQUIRES_RAF", "PRODUCTION_ZERO_OUTPUT_NOT_PERMITTED", "PRODUCTION_ORDER_NOT_FOUND", "conflict_version",
		"precondition_required", "idempotency_key_reuse"} {
		if !strings.Contains(spec, name) {
			t.Errorf("openapi.yaml does not mention %s", name)
		}
	}
}
