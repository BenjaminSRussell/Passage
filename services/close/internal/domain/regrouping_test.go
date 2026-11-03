package domain_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func TestRegroup_RemainingTermSplitsShortAndLong(t *testing.T) {
	key := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	items := []domain.OpenItem{
		{ID: "a", Account: "2100", Amount: decimal.RequireFromString("40000.00"), Due: date("2026-09-30"), Currency: "USD"},
		{ID: "b", Account: "2100", Amount: decimal.RequireFromString("120000.00"), Due: date("2028-03-31"), Currency: "USD"},
		{ID: "c", Account: "2100", Amount: decimal.RequireFromString("200000.00"), Due: date("2032-03-31"), Currency: "USD"},
	}
	lines, err := domain.Regroup(items, nil, []domain.RegroupingRule{{
		Code: "LT", SortMethod: domain.SortRemainingTerm, Sequence: 1,
		Buckets: []domain.TermBucket{
			{FromDays: 0, ToDays: 365, AdjustmentAccount: "2100", TargetAccount: "2100"},
			{FromDays: 366, ToDays: 0, AdjustmentAccount: "1600", TargetAccount: "2100"},
		},
	}}, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("want one entry for the long-term bucket, got %+v", lines)
	}
	if lines[0].AdjustmentAccount != "1600" || lines[0].TargetAccount != "2100" || !lines[0].Amount.Equal(decimal.RequireFromString("320000.00")) {
		t.Fatalf("got %+v", lines[0])
	}
	if lines[0].ReverseOn != "2026-04-01" {
		t.Fatalf("reversal %s, want 2026-04-01", lines[0].ReverseOn)
	}
}

func TestRegroup_DebitCreditBalanceMovesOppositeSign(t *testing.T) {
	key := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	items := []domain.OpenItem{
		{ID: "v1", Account: "2000", Party: "V7", Amount: decimal.RequireFromString("1250.00"), Currency: "USD", NormalBalance: "credit"},
		{ID: "c1", Account: "1200", Party: "C3", Amount: decimal.RequireFromString("-800.00"), Currency: "USD", NormalBalance: "debit"},
	}
	lines, err := domain.Regroup(items, nil, []domain.RegroupingRule{
		{Code: "VEND", SortMethod: domain.SortDebitCredit, Sequence: 1, SourceAccount: "2000", AdjustmentAccount: "1210", TargetAccount: "2000"},
		{Code: "CUST", SortMethod: domain.SortDebitCredit, Sequence: 2, SourceAccount: "1200", AdjustmentAccount: "2010", TargetAccount: "1200"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %+v", lines)
	}
	by := map[string]domain.ReclassLine{}
	for _, l := range lines {
		by[l.RuleCode] = l
	}
	if !by["VEND"].Amount.Equal(decimal.RequireFromString("1250.00")) || by["VEND"].AdjustmentAccount != "1210" {
		t.Fatalf("vendor: %+v", by["VEND"])
	}
	if !by["CUST"].Amount.Equal(decimal.RequireFromString("800.00")) || by["CUST"].AdjustmentAccount != "2010" {
		t.Fatalf("customer: %+v", by["CUST"])
	}
}

func TestRegroup_AccountBalanceSignMovesOverdraft(t *testing.T) {
	key := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	balances := []domain.Balance{{Account: "1100", Amount: decimal.RequireFromString("-500.00")}}
	lines, err := domain.Regroup(nil, balances, []domain.RegroupingRule{{
		Code: "BANK", SortMethod: domain.SortAccountSign, Sequence: 1, SourceAccount: "1100",
		AdjustmentAccount: "2500", TargetAccount: "1100", NormalBalance: "debit",
	}}, key)
	if err != nil || len(lines) != 1 || !lines[0].Amount.Equal(decimal.RequireFromString("500.00")) {
		t.Fatalf("got %+v %v", lines, err)
	}
}

func TestRegroup_NoDueDateAndOverdueFallInShortestBucket(t *testing.T) {
	key := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	items := []domain.OpenItem{
		{ID: "overdue", Account: "2100", Amount: decimal.NewFromInt(10), Due: date("2026-01-01"), Currency: "USD"},
		{ID: "nodue", Account: "2100", Amount: decimal.NewFromInt(20), Currency: "USD"},
	}
	lines, err := domain.Regroup(items, nil, []domain.RegroupingRule{{
		Code: "LT", SortMethod: domain.SortRemainingTerm, Sequence: 1,
		Buckets: []domain.TermBucket{
			{FromDays: 0, ToDays: 365, AdjustmentAccount: "2100", TargetAccount: "2100"},
			{FromDays: 366, ToDays: 0, AdjustmentAccount: "1600", TargetAccount: "2100"},
		},
	}}, key)
	if err != nil || len(lines) != 0 {
		t.Fatalf("short-term items must not reclassify, got %+v %v", lines, err)
	}
}

func TestRegroup_ItemUsedByFirstMatchingRuleOnly(t *testing.T) {
	key := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	items := []domain.OpenItem{{ID: "a", Account: "2100", Amount: decimal.NewFromInt(50), Due: date("2032-03-31"), Currency: "USD"}}
	lines, err := domain.Regroup(items, nil, []domain.RegroupingRule{
		{Code: "A", SortMethod: domain.SortRemainingTerm, Sequence: 1, Buckets: []domain.TermBucket{{FromDays: 366, ToDays: 0, AdjustmentAccount: "1600", TargetAccount: "2100"}}},
		{Code: "B", SortMethod: domain.SortRemainingTerm, Sequence: 2, Buckets: []domain.TermBucket{{FromDays: 366, ToDays: 0, AdjustmentAccount: "1601", TargetAccount: "2100"}}},
	}, key)
	if err != nil || len(lines) != 1 || lines[0].RuleCode != "A" {
		t.Fatalf("got %+v %v", lines, err)
	}
}

func date(s string) *time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return &t
}
