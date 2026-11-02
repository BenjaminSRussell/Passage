package api

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

var specOperation = regexp.MustCompile(`^    (get|post|put|patch|delete):$`)

type declaredOp struct {
	duty     string
	idemKey  bool
	declared bool
}

// declaredOperations maps "path method" to its duty and whether it takes an Idempotency-Key.
func declaredOperations(spec string) map[string]*declaredOp {
	declared := map[string]*declaredOp{}
	inPaths, path := false, ""
	var current *declaredOp
	for _, line := range strings.Split(spec, "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case !inPaths:
		case strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":"):
			path, current = strings.TrimSuffix(strings.TrimSpace(line), ":"), nil
		case specOperation.MatchString(line):
			current = &declaredOp{declared: true}
			declared[path+" "+specOperation.FindStringSubmatch(line)[1]] = current
		case current != nil && strings.HasPrefix(line, "      x-passage-duty: "):
			current.duty = strings.TrimPrefix(line, "      x-passage-duty: ")
		case current != nil && strings.Contains(line, "#/components/parameters/IdempotencyKey"):
			current.idemKey = true
		}
	}
	return declared
}

// The OpenAPI document lists exactly the served operations, each business
// operation with the duty the router enforces and every keyed command with the
// Idempotency-Key parameter.
func TestOpenAPIDocumentsEveryOperation(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	_ = chi.Walk(newRouter(&fakeStore{}).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[route+" "+strings.ToLower(method)] = true
		return nil
	})
	declared := declaredOperations(string(spec))
	duties := map[string]string{
		"/v1/shopfloor/workers post":                                               DutySetup,
		"/v1/shopfloor/workers get":                                                DutyView,
		"/v1/shopfloor/workers/{id} get":                                           DutyView,
		"/v1/shopfloor/registrations post":                                         DutyRegister,
		"/v1/shopfloor/registrations get":                                          DutyView,
		"/v1/shopfloor/pay:calculate post":                                         DutyCalculate,
		"/v1/shopfloor/pay:approve post":                                           DutyApprove,
		"/v1/shopfloor/worker-days get":                                            DutyView,
		"/v1/shopfloor/machines post":                                              DutySetup,
		"/v1/shopfloor/machines get":                                               DutyView,
		"/v1/shopfloor/machine-events post":                                        DutyIngest,
		"/v1/shopfloor/downtimes get":                                              DutyView,
		"/v1/shopfloor/downtimes/{id}:code post":                                   DutyRegister,
		"/v1/shopfloor/andon get":                                                  DutyView,
		"/v1/shopfloor/oee get":                                                    DutyView,
		"/v1/shopfloor/oee:rollup post":                                            DutyOEERollup,
		"/v1/shopfloor/sessions:badge post":                                        DutyKioskLogin,
		"/v1/shopfloor/clocked-in get":                                             DutyView,
		"/v1/shopfloor/pay:export post":                                            DutyExport,
		"/v1/shopfloor/pay-exports get":                                            DutyExport,
		"/v1/shopfloor/pay-exports/{id} get":                                       DutyExport,
		"/v1/shopfloor/process-messages post":                                      DutyProcessCreate,
		"/v1/shopfloor/process-messages get":                                       DutyView,
		"/v1/shopfloor/process-messages/{id} get":                                  DutyView,
		"/v1/shopfloor/process-messages/{id}/deliveries/{destination}:report post": DutyProcessDeliver,
		"/v1/shopfloor/process-messages/{id}:reprocess post":                       DutyProcessCreate,
		"/v1/shopfloor/process-messages/{id}:delete post":                          DutyProcessCreate,
		"/v1/shopfloor/process-messages:retry-due post":                            DutyProcessDeliver,
		"/v1/shopfloor/jobs post":                                                  DutyJobSchedule,
		"/v1/shopfloor/jobs get":                                                   DutyRegister,
		"/v1/shopfloor/jobs/{id} get":                                              DutyRegister,
		"/v1/shopfloor/jobs/{id}:feedback post":                                    DutyRegister,
		"/v1/shopfloor/registrations/{id}:correct post":                            DutyTimeCorrect,
		"/v1/shopfloor/registration-corrections get":                               DutyView,
		"/v1/shopfloor/pay:revise post":                                            DutyPayRevise,
		"/v1/shopfloor/registrations:auto-clock-out post":                          DutyAutoClockOut,
		"/v1/shopfloor/auto-clock-outs get":                                        DutyView,
		"/v1/shopfloor/time-profiles post":                                         DutySetup,
		"/v1/shopfloor/time-profiles get":                                          DutyView,
		"/v1/shopfloor/time-profiles/{id} get":                                     DutyView,
		"/v1/shopfloor/time-profiles/{id} put":                                     DutySetup,
		"/v1/shopfloor/pay-agreements post":                                        DutySetup,
		"/v1/shopfloor/pay-agreements get":                                         DutyView,
		"/v1/shopfloor/pay-agreements/{id} get":                                    DutyView,
		"/v1/shopfloor/pay-agreements/{id} put":                                    DutySetup,
		"/v1/shopfloor/absence-codes post":                                         DutySetup,
		"/v1/shopfloor/absence-codes get":                                          DutyView,
		"/v1/shopfloor/absence-codes/{id} get":                                     DutyView,
		"/v1/shopfloor/absence-codes/{id} put":                                     DutySetup,
		"/v1/shopfloor/edge-nodes post":                                            DutyEdgeConfigure,
		"/v1/shopfloor/edge-nodes get":                                             DutyView,
		"/v1/shopfloor/edge-nodes/{id} get":                                        DutyView,
		"/v1/shopfloor/edge-nodes/{id} put":                                        DutyEdgeConfigure,
		"/v1/shopfloor/edge-nodes/{id}/config-versions post":                       DutyEdgeConfigure,
		"/v1/shopfloor/edge-nodes/{id}/config-versions get":                        DutyView,
		"/v1/shopfloor/edge-nodes/{id}/config get":                                 DutyEdgeConnect,
		"/v1/shopfloor/edge-nodes/{id}:heartbeat post":                             DutyEdgeConnect,
		"/v1/shopfloor/workers/{id}:assign-setup post":                             DutySetup,
	}
	keyed := map[string]bool{
		"/v1/shopfloor/workers post":                                               true,
		"/v1/shopfloor/pay:calculate post":                                         true,
		"/v1/shopfloor/pay:approve post":                                           true,
		"/v1/shopfloor/machines post":                                              true,
		"/v1/shopfloor/downtimes/{id}:code post":                                   true,
		"/v1/shopfloor/pay:export post":                                            true,
		"/v1/shopfloor/process-messages/{id}/deliveries/{destination}:report post": true,
		"/v1/shopfloor/process-messages/{id}:reprocess post":                       true,
		"/v1/shopfloor/process-messages/{id}:delete post":                          true,
		"/v1/shopfloor/registrations/{id}:correct post":                            true,
		"/v1/shopfloor/pay:revise post":                                            true,
		"/v1/shopfloor/time-profiles post":                                         true,
		"/v1/shopfloor/pay-agreements post":                                        true,
		"/v1/shopfloor/absence-codes post":                                         true,
		"/v1/shopfloor/edge-nodes post":                                            true,
		"/v1/shopfloor/edge-nodes/{id}/config-versions post":                       true,
		"/v1/shopfloor/workers/{id}:assign-setup post":                             true,
	}
	for op := range served {
		d, ok := declared[op]
		if !ok {
			t.Errorf("not documented: %s", op)
			continue
		}
		if want := duties[op]; d.duty != want {
			t.Errorf("%s: documented duty %q, router enforces %q", op, d.duty, want)
		}
		if d.idemKey != keyed[op] {
			t.Errorf("%s: Idempotency-Key documented = %v, want %v", op, d.idemKey, keyed[op])
		}
	}
	for op := range declared {
		if !served[op] {
			t.Errorf("documented but not served: %s", op)
		}
	}
	if len(served) != len(duties)+2 {
		t.Fatalf("router walk yielded %d operations: %v", len(served), served)
	}
}
