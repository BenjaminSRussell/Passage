package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func glRow(id, account, currency, balance string) GLBalance {
	return GLBalance{LegacyID: id, Account: account, Currency: currency, Balance: decimal.RequireFromString(balance)}
}

func TestBuildOpeningJournals_SidesAndKeys(t *testing.T) {
	rows := []GLBalance{glRow("A", "1100", "USD", "100"), glRow("B", "3000", "USD", "-100"), glRow("C", "1100", "JPY", "500"), glRow("D", "3000", "JPY", "-500"), glRow("Z", "2100", "USD", "0")}
	js, err := BuildOpeningJournals(rows, SourceAX2012, "USMF", "le", "L1", "2027-01-01")
	if err != nil || len(js) != 2 || js[0].Currency != "JPY" || js[1].LineCount != 2 || !js[1].Entry.Lines[1].Credit || js[0].Key == js[1].Key {
		t.Fatalf("journals %+v err %v", js, err)
	}
	again, _ := BuildOpeningJournals(rows, SourceAX2012, "USMF", "le", "L1", "2027-01-01")
	moved, _ := BuildOpeningJournals(rows, SourceAX2012, "USMF", "le", "L1", "2027-02-01")
	if again[1].Hash != js[1].Hash || moved[1].Hash == js[1].Hash || moved[1].Key != js[1].Key {
		t.Fatal("hash must follow the content and date, the key only the company, entity and currency")
	}
}

func TestBuildOpeningJournals_RefusesImbalanceAndHiddenRounding(t *testing.T) {
	_, err := BuildOpeningJournals([]GLBalance{glRow("A", "1100", "USD", "100"), glRow("B", "3000", "USD", "-90.50")}, SourceAX2012, "USMF", "le", "L1", "2027-01-01")
	if err == nil || !strings.Contains(err.Error(), "USD debits 100, credits 90.5, difference 9.5") {
		t.Fatalf("imbalance: %v", err)
	}
	_, err = BuildOpeningJournals([]GLBalance{glRow("A", "1100", "USD", "100.004"), glRow("B", "3000", "USD", "-100.004")}, SourceAX2012, "USMF", "le", "L1", "2027-01-01")
	if err == nil || !strings.Contains(err.Error(), "does not round") {
		t.Fatalf("rounding: %v", err)
	}
	if _, err := BuildOpeningJournals([]GLBalance{glRow("A", "1100", "JPY", "10.5"), glRow("B", "3000", "JPY", "-10.5")}, SourceAX2012, "USMF", "le", "L1", "2027-01-01"); err == nil {
		t.Fatal("a fractional yen was accepted")
	}
}
