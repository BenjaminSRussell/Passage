package domain

import (
	"strings"
	"testing"
)

func TestAddressValidation(t *testing.T) {
	ok := Address{Line1: " 1 Main St ", City: "Springfield", PostalCode: "62701", Country: "us"}.Normalize()
	if f := ok.Validate(); len(f) != 0 {
		t.Fatalf("valid address: %v", f)
	}
	if ok.Country != "US" || ok.Line1 != "1 Main St" || ok.Summary() != "Springfield, US" || ok.Format() != "1 Main St, Springfield, 62701, US" {
		t.Fatalf("normalized = %+v summary %q format %q", ok, ok.Summary(), ok.Format())
	}
	bad := Address{Line1: strings.Repeat("x", 201), Country: "USA", Line2: strings.Repeat("y", 201)}.Normalize().Validate()
	for _, key := range []string{"address.line1", "address.line2", "address.city", "address.postalCode", "address.country"} {
		if bad[key] == "" {
			t.Errorf("%s not reported in %v", key, bad)
		}
	}
}

func TestBankAccountAndPreferredName(t *testing.T) {
	if v, ok := NormalizeBankAccount(" de89 3704-0044 0532 0130 00 "); !ok || v != "DE89370400440532013000" {
		t.Fatalf("iban = %q %v", v, ok)
	}
	for _, v := range []string{"", "1234567", "DE89!3704004405", strings.Repeat("1", 35)} {
		if _, ok := NormalizeBankAccount(v); ok {
			t.Errorf("%q accepted", v)
		}
	}
	if v, ok := NormalizePreferredName("  Sam "); !ok || v != "Sam" {
		t.Fatalf("name = %q %v", v, ok)
	}
	if _, ok := NormalizePreferredName(strings.Repeat("n", 101)); ok {
		t.Fatal("101-character name accepted")
	}
}

func TestProfileChangeRouting(t *testing.T) {
	if ChangeNeedsApproval(ChangePreferredName) || !ChangeNeedsApproval(ChangeHomeAddress) || !ChangeNeedsApproval(ChangeBankAccount) {
		t.Fatal("only address and bank changes need approval")
	}
	if !ManagerMayDecide(ChangeHomeAddress) || ManagerMayDecide(ChangeBankAccount) {
		t.Fatal("managers decide addresses, never bank accounts")
	}
	for _, c := range []struct {
		status, decision, next string
		moved                  bool
		code                   string
	}{
		{ChangePending, DecisionApprove, ChangeApproved, true, ""},
		{ChangePending, DecisionReject, ChangeRejected, true, ""},
		{ChangePending, "CANCEL", ChangeCanceled, true, ""},
		{ChangeApproved, DecisionApprove, ChangeApproved, false, ""},
		{ChangeCanceled, "CANCEL", ChangeCanceled, false, ""},
		{ChangeApproved, DecisionReject, "", false, CodeProfileChangeState},
		{ChangeCanceled, DecisionApprove, "", false, CodeProfileChangeState},
		{ChangePending, "MAYBE", "", false, "COM-0001"},
	} {
		next, moved, err := NextChangeStatus(c.status, c.decision)
		code := ""
		if err != nil {
			code = err.(*Error).Code
		}
		if next != c.next || moved != c.moved || code != c.code {
			t.Errorf("%s + %s = %q %v %q, want %q %v %q", c.status, c.decision, next, moved, code, c.next, c.moved, c.code)
		}
	}
}
