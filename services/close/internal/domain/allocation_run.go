package domain

import "github.com/shopspring/decimal"

type AllocationDest struct {
	Key        string          `json:"key"`
	Account    string          `json:"account"`
	Dimensions string          `json:"dimensions"`
	Weight     decimal.Decimal `json:"weight"`
}

type AllocationLine struct {
	Key        string          `json:"key"`
	Account    string          `json:"account"`
	Dimensions string          `json:"dimensions"`
	Amount     decimal.Decimal `json:"amount"`
}

type AllocationProposal struct {
	Destinations []AllocationLine `json:"destinations"`
	Offset       AllocationLine   `json:"offset"`
}

func ProposeAllocation(source decimal.Decimal, dests []AllocationDest, offsetAccount, offsetDimensions string, scale int32) (AllocationProposal, error) {
	shares := make([]Share, len(dests))
	for i, d := range dests {
		if d.Account == "" || d.Key == "" {
			return AllocationProposal{}, fail(CodeValidation, "each destination needs a key and an account")
		}
		shares[i] = Share{Key: d.Key, Weight: d.Weight}
	}
	parts, err := Allocate(source, shares, scale)
	if err != nil {
		return AllocationProposal{}, err
	}
	byKey := map[string]decimal.Decimal{}
	for _, p := range parts {
		byKey[p.Key] = p.Amount
	}
	out := AllocationProposal{Destinations: make([]AllocationLine, len(dests)), Offset: AllocationLine{Account: offsetAccount, Dimensions: offsetDimensions, Amount: source}}
	if offsetAccount == "" {
		return AllocationProposal{}, fail(CodeValidation, "offset account is required")
	}
	for i, d := range dests {
		out.Destinations[i] = AllocationLine{Key: d.Key, Account: d.Account, Dimensions: d.Dimensions, Amount: byKey[d.Key]}
	}
	return out, nil
}
