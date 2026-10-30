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
	lines := domain.RunJournal("USD", twoGroups(), "2000", "1010", "4900")
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
	lines := domain.RunJournal("USD", twoGroups()[1:], "2000", "1010", "")
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

func TestCheckVendors_RejectsUnsupportedMethodAndMissingBankDetails(t *testing.T) {
	groups := twoGroups()
	cfg := domain.RunFileConfig{Creditors: map[string]domain.CreditorDTO{groups[0].VendorID: {}, groups[1].VendorID: {}}}
	if err := cfg.CheckVendors(groups); err != nil {
		t.Fatal(err)
	}
	var de *domain.Error
	delete(cfg.Creditors, groups[1].VendorID)
	if err := cfg.CheckVendors(groups); !errors.As(err, &de) || de.Code != domain.CodeRunConfig || !strings.Contains(de.Detail, groups[1].VendorID) {
		t.Fatalf("missing creditor = %v", err)
	}
	groups[0].Method = "ACH"
	if err := cfg.CheckVendors(groups); !errors.As(err, &de) || !strings.Contains(de.Detail, "ACH") {
		t.Fatalf("ACH group = %v", err)
	}
}

func TestBuildRunFile_OnePaymentPerGroupWithInvoiceRemittance(t *testing.T) {
	groups := twoGroups()
	cfg := domain.RunFileConfig{
		Debtor:        domain.PartyDTO{Name: "Passage Holding Inc", Address: domain.PostalAddressDTO{StreetName: "1 Market Street", PostCode: "94105", TownName: "San Francisco", Country: "US"}},
		DebtorAccount: domain.AccountDTO{OtherID: "000123456789"}, DebtorBIC: "CHASUS33",
		Profile: domain.BankProfileDTO{Variant: "US"},
		Creditors: map[string]domain.CreditorDTO{
			groups[0].VendorID: {PartyDTO: domain.PartyDTO{Name: "Acme", Address: domain.PostalAddressDTO{StreetName: "1 A St", PostCode: "10001", TownName: "New York", Country: "US"}}, AccountDTO: domain.AccountDTO{OtherID: "000111222333"}, RoutingNumber: "021000021"},
			groups[1].VendorID: {PartyDTO: domain.PartyDTO{Name: "Widgets", Address: domain.PostalAddressDTO{StreetName: "2 B St", PostCode: "10001", TownName: "New York", Country: "US"}}, AccountDTO: domain.AccountDTO{OtherID: "000444555666"}, RoutingNumber: "011000015"},
		},
	}
	run := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	file, payments, err := domain.BuildRunFile(cfg, runUUID, run, run, groups, map[string]string{"i1": "INV-1", "i2": "INV-2"})
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
	file, _, err = domain.BuildRunFile(cfg, runUUID, run, run, groups, map[string]string{"i1": "INV-1", "i2": "INV-2"})
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
