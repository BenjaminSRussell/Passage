package api_test

import (
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

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
	first := decode(t, created)["id"].(string)
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
