package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	groupOne = "00000000-0000-0000-0000-0000000000e1"
	groupTwo = "00000000-0000-0000-0000-0000000000e2"
	runUUID  = "01a18adf-e224-7683-b387-9a5cee7e87e1"
)

func twoGroups() []domain.ProposalGroup {
	return []domain.ProposalGroup{
		{ID: groupOne, VendorID: "00000000-0000-0000-0000-0000000000c1", Currency: "EUR", Method: "TRANSFER", Net: d("31.33"), Valued: d("33.91"), Discount: d("2"),
			Lines: []domain.ProposalLine{{OpenItemID: "i1", OpenAmount: d("33.33"), Pay: d("31.33"), Rate: d("1.0825"), Valued: d("33.91")}}},
		{ID: groupTwo, VendorID: "00000000-0000-0000-0000-0000000000c2", Currency: "USD", Method: "WIRE", Net: d("500"), Valued: d("500"),
			Lines: []domain.ProposalLine{{OpenItemID: "i2", OpenAmount: d("500"), Pay: d("500"), Rate: d("1"), Valued: d("500")}}},
	}
}

func TestRunJournal_BalancesGrossControlAgainstBankAndDiscount(t *testing.T) {
	lines := domain.RunJournal("USD", twoGroups(), "2000", "1010", "4900", "", domain.VendorGroupDims{})
	type row struct {
		account string
		credit  bool
		amount  string
	}
	want := []row{{"2000", false, "36.08"}, {"2000", false, "500"}, {"1010", true, "533.91"}, {"4900", true, "2.17"}}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v, want %d", lines, len(want))
	}
	debit, credit := d("0"), d("0")
	for i, l := range lines {
		if l.Account != want[i].account || l.Credit != want[i].credit || l.Amount.String() != want[i].amount {
			t.Errorf("line %d = %+v, want %+v", i, l, want[i])
		}
		if l.Credit {
			credit = credit.Add(l.Amount)
		} else {
			debit = debit.Add(l.Amount)
		}
	}
	if !debit.Equal(credit) || !debit.Equal(d("536.08")) {
		t.Fatalf("debit %s credit %s, want both 536.08", debit, credit)
	}
}

func TestRunJournal_NoDiscountLineWithoutDiscount(t *testing.T) {
	lines := domain.RunJournal("USD", twoGroups()[1:], "2000", "1010", "", "", domain.VendorGroupDims{})
	if len(lines) != 2 || lines[1].Account != "1010" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestPaymentAndFileNumbers_FitTheBankReferencePattern(t *testing.T) {
	if got := domain.PaymentNumber(groupOne); got != "P-"+strings.Repeat("0", 30)+"e1" {
		t.Fatalf("payment number = %s", got)
	}
	if got := domain.RunFileNumber(runUUID); got != "Re2247683b3879a5cee7e87e1" {
		t.Fatalf("file number = %s, want R + the last 24 hex digits of the run id", got)
	}
}

func TestCheckRunnable_RejectsMethodAndCurrency(t *testing.T) {
	groups := twoGroups()
	groups[0].Currency = "USD"
	if err := domain.CheckRunnable("USD", groups); err != nil {
		t.Fatal(err)
	}
	var de *domain.Error
	if err := domain.CheckRunnable("USD", twoGroups()); !errors.As(err, &de) || de.Code != domain.CodeRunConfig || !strings.Contains(de.Detail, "EUR") || !strings.Contains(de.Detail, "realized FX") {
		t.Fatalf("a EUR group in a USD legal entity = %v", err)
	}
	groups[0].Method = "ACH"
	if err := domain.CheckRunnable("USD", groups); !errors.As(err, &de) || !strings.Contains(de.Detail, "ACH") {
		t.Fatalf("ACH group = %v", err)
	}
}

func TestBuildRunFile_RefusesAVendorWithoutBankDetails(t *testing.T) {
	groups := twoGroups()
	var de *domain.Error
	_, _, err := domain.BuildRunFile(domain.RunFileConfig{}, map[string]domain.CreditorDTO{groups[0].VendorID: {}}, runUUID, time.Now(), time.Now(), groups, nil)
	if !errors.As(err, &de) || de.Code != domain.CodeRunPayee || !strings.Contains(de.Detail, groups[1].VendorID) {
		t.Fatalf("missing creditor = %v", err)
	}
}

func TestRunJournal_CreditsWithheldTaxAndStillBalances(t *testing.T) {
	groups := twoGroups()[1:]
	groups[0].Withhold(0, "WHT30", d("150"), 2)
	lines := domain.RunJournal("USD", groups, "2000", "1010", "4900", "2300", domain.VendorGroupDims{})
	if len(lines) != 3 || lines[1].Account != "1010" || !lines[1].Amount.Equal(d("350")) || lines[2].Account != "2300" || !lines[2].Amount.Equal(d("150")) {
		t.Fatalf("lines = %+v, want debit 500, bank credit 350, withholding credit 150", lines)
	}
}

func TestWithhold_ReducesNetAndValuedPerLineAndGroup(t *testing.T) {
	g := twoGroups()[0]
	g.Withhold(0, "WHT30", d("3.13"), 2)
	l := g.Lines[0]
	if !g.Net.Equal(d("28.20")) || !g.Withheld.Equal(d("3.13")) || !l.Withheld.Equal(d("3.13")) || l.WithholdingCode != "WHT30" || !l.Valued.Equal(d("30.53")) || !g.Valued.Equal(d("30.53")) {
		t.Fatalf("group %+v line %+v", g, l)
	}
	if !domain.HasWithholding([]domain.ProposalGroup{g}) || domain.HasWithholding(twoGroups()) {
		t.Fatal("HasWithholding must follow the group totals")
	}
}

func TestBuildRunFile_OnePaymentPerGroupWithInvoiceRemittance(t *testing.T) {
	groups := twoGroups()
	cfg := domain.RunFileConfig{
		Debtor:        domain.PartyDTO{Name: "Passage Holding Inc", Address: domain.PostalAddressDTO{StreetName: "1 Market Street", PostCode: "94105", TownName: "San Francisco", Country: "US"}},
		DebtorAccount: domain.AccountDTO{OtherID: "000123456789"}, DebtorBIC: "CHASUS33",
		Profile: domain.BankProfileDTO{Variant: "US"},
	}
	creditors := map[string]domain.CreditorDTO{
		groups[0].VendorID: {PartyDTO: domain.PartyDTO{Name: "Acme", Address: domain.PostalAddressDTO{StreetName: "1 A St", PostCode: "10001", TownName: "New York", Country: "US"}}, AccountDTO: domain.AccountDTO{OtherID: "000111222333"}, RoutingNumber: "021000021"},
		groups[1].VendorID: {PartyDTO: domain.PartyDTO{Name: "Widgets", Address: domain.PostalAddressDTO{StreetName: "2 B St", PostCode: "10001", TownName: "New York", Country: "US"}}, AccountDTO: domain.AccountDTO{OtherID: "000444555666"}, RoutingNumber: "011000015"},
	}
	run := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	file, payments, err := domain.BuildRunFile(cfg, creditors, runUUID, run, run, groups, map[string]string{"i1": "INV-1", "i2": "INV-2"})
	if err != nil {
		t.Fatal(err)
	}
	if file.PaymentCount != 2 || file.FileNumber != domain.RunFileNumber(runUUID) || !file.ControlSum.Equal(d("531.33")) {
		t.Fatalf("file = %+v, want 2 payments and control sum 31.33 + 500", file)
	}
	if len(payments) != 2 || payments[0].EndToEndID != domain.PaymentNumber(groupOne) || !payments[0].Amount.Equal(d("31.33")) || !payments[0].Valued.Equal(d("33.91")) {
		t.Fatalf("payments = %+v", payments)
	}
	for _, want := range []string{"INV-1", "INV-2", domain.PaymentNumber(groupTwo), "<ReqdExctnDt>"} {
		if !strings.Contains(string(file.Content), want) {
			t.Errorf("file lacks %q", want)
		}
	}
	cfg.FileNumber = "CUSTOM-1"
	file, _, err = domain.BuildRunFile(cfg, creditors, runUUID, run, run, groups, map[string]string{"i1": "INV-1", "i2": "INV-2"})
	if err != nil || file.FileNumber != "CUSTOM-1" {
		t.Fatalf("custom file number: %s %v", file.FileNumber, err)
	}
}

func TestHasDiscount(t *testing.T) {
	groups := twoGroups()
	if !domain.HasDiscount(groups) || domain.HasDiscount(groups[1:]) {
		t.Fatal("HasDiscount must follow the group discount totals")
	}
}

func TestRunJournal_ControlDebitsCarryTheVendorGroupCodePerItem(t *testing.T) {
	groups := []domain.ProposalGroup{{ID: groupOne, VendorID: "v1", Currency: "USD", Method: "TRANSFER", Net: d("30"), Valued: d("30"),
		Lines: []domain.ProposalLine{
			{OpenItemID: "i1", OpenAmount: d("10"), Pay: d("10"), Rate: d("1"), Valued: d("10")},
			{OpenItemID: "i2", OpenAmount: d("5"), Pay: d("5"), Rate: d("1"), Valued: d("5")},
			{OpenItemID: "i3", OpenAmount: d("15"), Pay: d("15"), Rate: d("1"), Valued: d("15")},
		}}}
	dims := domain.VendorGroupDims{Attribute: "VENDGRP", Codes: map[string]string{"i1": "DOM", "i3": "DOM"}}
	lines := domain.RunJournal("USD", groups, "2000", "1010", "", "", dims)
	if len(lines) != 3 {
		t.Fatalf("lines = %+v, want two control debits (DOM and untagged) and the bank credit", lines)
	}
	if lines[0].Dimensions["VENDGRP"] != "DOM" || lines[0].Amount.String() != "25" || len(lines[1].Dimensions) != 0 || lines[1].Amount.String() != "5" || lines[2].Account != "1010" || lines[2].Amount.String() != "30" {
		t.Fatalf("lines = %+v, want DOM 25, untagged 5, bank 30", lines)
	}
	if off := domain.RunJournal("USD", groups, "2000", "1010", "", "", domain.VendorGroupDims{Codes: dims.Codes}); len(off) != 2 || len(off[0].Dimensions) != 0 || off[0].Amount.String() != "30" {
		t.Fatalf("with the dimension off the control debit stays one untagged line: %+v", off)
	}
}
