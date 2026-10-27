package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestReconcileCashFlow(t *testing.T) {
	cf := CashFlow{
		NetIncome:            decimal.RequireFromString("80.00"),
		OperatingAdjustments: decimal.RequireFromString("20.00"),
		WorkingCapital:       WorkingCapitalMovement(decimal.RequireFromString("30.00"), decimal.RequireFromString("-10.00")),
		Investing:            decimal.RequireFromString("-50.00"),
		Financing:            decimal.RequireFromString("5.25"),
	}
	tests := []struct {
		name         string
		delta        string
		unclassified int
		wantCode     string
	}{
		{"net flow equals change in cash", "15.25", 0, ""},
		{"one cent short", "15.24", 0, CodeCashFlow},
		{"unclassified account blocks", "15.25", 2, CodeUnclassified},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ReconcileCashFlow(cf, decimal.RequireFromString(tc.delta), tc.unclassified)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var de *Error
			if !errors.As(err, &de) || de.Code != tc.wantCode {
				t.Fatalf("err = %v, want %s", err, tc.wantCode)
			}
		})
	}
}

func TestWorkingCapitalMovement(t *testing.T) {
	tests := []struct {
		name      string
		asset     string
		liability string
		want      string
	}{
		{"asset growth consumes cash", "100", "0", "-100"},
		{"liability growth provides cash", "0", "40", "40"},
		{"both move", "25.5", "-4.5", "-30"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WorkingCapitalMovement(decimal.RequireFromString(tc.asset), decimal.RequireFromString(tc.liability))
			if !got.Equal(decimal.RequireFromString(tc.want)) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
