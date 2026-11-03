package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const employeeVendor = "00000000-0000-0000-0000-0000000000ee"

func proposalVendors(t *testing.T, f *runFixture, id string) map[string]bool {
	t.Helper()
	body := decode(t, f.alice.send(f.router, http.MethodGet, "/v1/payables/payment-proposals/"+id, ""))
	out := map[string]bool{}
	for _, g := range body["groups"].([]any) {
		out[g.(map[string]any)["vendorId"].(string)] = true
	}
	return out
}

func TestProposal_ExpenseReimbursementToAnUnknownPayeeNeverBlocksTheRealVendors(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{skipProposal: true})
	if _, _, err := f.pg.CreateFromInvoice(f.ctx(), store.OpenItem{LegalEntityID: entityA, VendorID: employeeVendor, SourceInvoiceID: "00000000-0000-0000-0000-0000000000e1",
		InvoiceNumber: "EXP-00000000", InvoiceDate: "2026-01-04", DueDate: "2026-01-04", DueDateSource: store.DueDateDefaulted, Currency: "USD",
		Amount: decimal.NewFromInt(80), PayeeUnverified: true}); err != nil {
		t.Fatal(err)
	}

	created := f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-1", nil))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	createdBody := decode(t, created)
	first := createdBody["id"].(string)
	excluded, _ := createdBody["excludedItems"].([]any)
	if len(excluded) != 1 || excluded[0].(map[string]any)["vendorId"] != employeeVendor || excluded[0].(map[string]any)["reason"] != store.ReasonPayeeUnverified {
		t.Fatalf("excludedItems = %v; want the reimbursement with reason PAYEE_UNVERIFIED", createdBody["excludedItems"])
	}
	items := decode(t, f.alice.send(f.router, http.MethodGet, "/v1/payables/open-items?vendor="+employeeVendor, ""))
	if list, _ := items["items"].([]any); len(list) != 1 || list[0].(map[string]any)["payeeUnverified"] != true {
		t.Fatalf("open item list = %v; want payeeUnverified true", items)
	}
	if got := proposalVendors(t, f, first); got[employeeVendor] || !got[vendorOne] || !got[vendorTwo] {
		t.Fatalf("proposal vendors = %v; want the real vendors and not the unknown employee payee", got)
	}
	if rec := f.bob.send(f.router, http.MethodPost, "/v1/payables/payment-proposals/"+first+":approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("approve with bank bindings: %d %s", rec.Code, rec.Body)
	}

	f.party.mu.Lock()
	f.party.accounts[employeeVendor] = []map[string]any{partyAccount("Erin Employee", "000777888999", "021000021", "USD")}
	f.party.mu.Unlock()
	created = f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-2", nil))
	if created.Code != http.StatusCreated {
		t.Fatalf("create after the payee joined the master: %d %s", created.Code, created.Body)
	}
	second := decode(t, created)["id"].(string)
	if got := proposalVendors(t, f, second); len(got) != 1 || !got[employeeVendor] {
		t.Fatalf("second proposal vendors = %v; want the employee payee once it is in the master", got)
	}
	if rec := f.bob.send(f.router, http.MethodPost, "/v1/payables/payment-proposals/"+second+":approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("approve employee payment: %d %s", rec.Code, rec.Body)
	}
}

func TestProposal_OnlyUnverifiedReimbursementsDueIsA422NamingTheReason(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{skipProposal: true, noSeedItems: true})
	if _, _, err := f.pg.CreateFromInvoice(f.ctx(), store.OpenItem{LegalEntityID: entityA, VendorID: employeeVendor, SourceInvoiceID: "00000000-0000-0000-0000-0000000000e2",
		InvoiceNumber: "EXP-00000001", InvoiceDate: "2026-01-02", DueDate: "2026-01-02", DueDateSource: store.DueDateDefaulted, Currency: "USD",
		Amount: decimal.NewFromInt(80), PayeeUnverified: true}); err != nil {
		t.Fatal(err)
	}
	problem := wantProblem(t, f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-only", nil)), http.StatusUnprocessableEntity, domain.CodeNothingDue)
	if detail, _ := problem["detail"].(string); !strings.Contains(detail, store.ReasonPayeeUnverified) {
		t.Fatalf("detail = %q; want it to name %s", detail, store.ReasonPayeeUnverified)
	}
	metrics := httptest.NewRecorder()
	f.api.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "payables_payee_unverified_excluded_total 1") {
		t.Fatalf("metrics = %s", metrics.Body)
	}
}
