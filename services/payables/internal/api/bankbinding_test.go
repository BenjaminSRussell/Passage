package api_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/services/payables/internal/party"
)

func TestBankBinding_ApprovalRecordsAccountIdAndLast4ButNoPlaintext(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	rec := f.bob.send(f.router, http.MethodPost, "/v1/payables/payment-proposals/"+f.proposal+":approve", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	group := vendorGroup(t, decode(t, rec), vendorOne)
	if group["bankAccountId"] != "acct-usd-2333" || group["bankLast4"] != "2333" || strings.Contains(body, "00011122") || strings.Contains(body, "Fingerprint") || strings.Contains(body, "fingerprint") {
		t.Fatalf("group %v: the approver sees the account id and last 4 only", group)
	}
	if f.party.callCount() != 2 {
		t.Fatalf("party calls at approval = %d, want one per vendor", f.party.callCount())
	}
}

func TestBankBinding_UnchangedAccountGeneratesTheFile(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	if rec := f.step(f.erin, runID, "generate-file"); rec.Code != http.StatusOK {
		t.Fatalf("generate-file: %d %s", rec.Code, rec.Body)
	}
}

func TestBankBinding_AccountChangedAfterApprovalIsRefused(t *testing.T) {
	changes := map[string]func(f *fakeParty){
		"account number": func(f *fakeParty) { f.accounts[vendorOne][0]["accountNumber"] = "999999999999" },
		"routing number": func(f *fakeParty) { f.accounts[vendorOne][0]["routingNumber"] = "011000015" },
		"holder":         func(f *fakeParty) { f.accounts[vendorOne][0]["accountHolder"] = "Mallory" },
		"bank country":   func(f *fakeParty) { f.accounts[vendorOne][0]["bankCountry"] = "CA" },
		"account id":     func(f *fakeParty) { f.accounts[vendorOne][0]["id"] = "acct-new" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newRunFixture(t, fixtureOptions{})
			f.approve()
			runID := f.start()["id"].(string)
			change(f.party)
			body := wantProblem(t, f.step(f.erin, runID, "generate-file"), http.StatusUnprocessableEntity, "AP-RUN-017")
			if !strings.Contains(body["detail"].(string), vendorOne) {
				t.Fatalf("detail %v", body["detail"])
			}
			if got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "")); got["fileGeneratedAt"] != nil {
				t.Fatalf("no file may be saved: %v", got)
			}
		})
	}
}

func TestBankBinding_ApprovalWithPartyDownOrBadVendorLeavesTheDraft(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.party.mu.Lock()
	f.party.failures = 1
	f.party.mu.Unlock()
	wantProblem(t, f.bob.send(f.router, http.MethodPost, "/v1/payables/payment-proposals/"+f.proposal+":approve", ""), http.StatusBadGateway, "COM-0502")
	if f.proposalStatus() != "DRAFT" {
		t.Fatalf("proposal = %s", f.proposalStatus())
	}
	delete(f.party.accounts, vendorTwo)
	wantProblem(t, f.bob.send(f.router, http.MethodPost, "/v1/payables/payment-proposals/"+f.proposal+":approve", ""), http.StatusUnprocessableEntity, "AP-RUN-017")
	if f.proposalStatus() != "DRAFT" {
		t.Fatalf("proposal = %s", f.proposalStatus())
	}
}

func TestBankBinding_SelfApprovalNeverRevealsBankDetails(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	wantProblem(t, f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals/"+f.proposal+":approve", ""), http.StatusForbidden, "AP-PRP-003")
	if f.party.callCount() != 0 {
		t.Fatalf("party calls = %d", f.party.callCount())
	}
}

func TestBankBinding_ApprovalWithoutFieldKeysFailsClosed(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.api.WithVendorBanks(party.New(f.partyURL, nil).WithSigner(f.signer), nil)
	wantProblem(t, f.bob.send(f.router, http.MethodPost, "/v1/payables/payment-proposals/"+f.proposal+":approve", ""), http.StatusServiceUnavailable, "COM-0503")
	if f.proposalStatus() != "DRAFT" {
		t.Fatalf("proposal = %s: an unkeyed binding must never be stored", f.proposalStatus())
	}
}

func TestBankBinding_KeyRotationStillMatchesAndARemovedKeyDoesNotPass(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	rotated, err := fieldcrypt.NewKeys("k2", map[string][]byte{"k1": bytes.Repeat([]byte{9}, 32), "k2": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	f.api.WithVendorBanks(party.New(f.partyURL, nil).WithSigner(f.signer), rotated)
	if rec := f.step(f.erin, runID, "generate-file"); rec.Code != http.StatusOK {
		t.Fatalf("generate-file after rotation: %d %s", rec.Code, rec.Body)
	}

	g := newRunFixture(t, fixtureOptions{})
	g.approve()
	gone, _ := fieldcrypt.NewKeys("k2", map[string][]byte{"k2": bytes.Repeat([]byte{7}, 32)})
	runID = g.start()["id"].(string)
	g.api.WithVendorBanks(party.New(g.partyURL, nil).WithSigner(g.signer), gone)
	if rec := g.step(g.erin, runID, "generate-file"); rec.Code == http.StatusOK {
		t.Fatal("a binding whose key id is no longer configured must not pass")
	}
}
