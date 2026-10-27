package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// CodeValuationTypeRequired and CodeCategoryLocked are message-catalog codes
// for split valuation (FR-20.16).
const (
	CodeValuationTypeRequired = "CST-VAL-001"
	CodeCategoryLocked        = "CST-VAL-002"
)

// SettleByValuationType settles a product whose stock is split by valuation
// type (FR-20.16, SAP OMWC/OMWD). Each type is an independent cost pool: its
// receipts settle only its own issues, under the same method. Types are
// resolved by PartitionByValuationType.
//
// The merged Result sums the types; perType holds each type's own Result.
// With no types anywhere the result equals Settle.
func SettleByValuationType(txns []Txn, opt Options, defaultType string) (merged Result, perType map[string]Result, err error) {
	types, parts, err := PartitionByValuationType(txns, defaultType)
	if err != nil {
		return Result{}, nil, err
	}
	perType = make(map[string]Result, len(parts))
	merged = Result{IssueCosts: map[string]decimal.Decimal{}, RemainingQty: decimal.Zero, RemainingValue: decimal.Zero}
	for _, vt := range types {
		res, err := Settle(parts[vt], opt)
		if err != nil {
			return Result{}, nil, err
		}
		perType[vt] = res
		for _, s := range res.Settlements {
			s.Seq = len(merged.Settlements) + 1
			merged.Settlements = append(merged.Settlements, s)
		}
		for id, c := range res.IssueCosts {
			merged.IssueCosts[id] = merged.IssueCosts[id].Add(c)
		}
		merged.Exceptions = append(merged.Exceptions, res.Exceptions...)
		merged.Layers = append(merged.Layers, res.Layers...)
		merged.RemainingQty = merged.RemainingQty.Add(res.RemainingQty)
		merged.RemainingValue = merged.RemainingValue.Add(res.RemainingValue)
	}
	return merged, perType, nil
}

// PartitionByValuationType splits a product's transactions into one sequence
// per valuation type and returns the types in sorted order. With no valuation
// type on any transaction and no defaultType, the product is not split and
// everything lands in the unnamed type "".
//
// For a split product a receipt takes its own type, else defaultType, else it
// is refused with CST-VAL-001. An issue takes its own type, else the type of
// the receipt it marks (FR-20.8), else defaultType, else the one type holding
// stock at that point in the sequence (or the only type seen so far when none
// holds stock). When several types hold stock it is refused with CST-VAL-001
// (BR-20.14). An issue that marks a receipt of another type is refused.
func PartitionByValuationType(txns []Txn, defaultType string) ([]string, map[string][]Txn, error) {
	seq, err := orderTxns(txns)
	if err != nil {
		return nil, nil, err
	}
	split := defaultType != ""
	for _, t := range seq {
		split = split || t.ValuationType != ""
	}
	if !split {
		return []string{""}, map[string][]Txn{"": seq}, nil
	}
	receiptType := map[string]string{}
	for _, t := range seq {
		if t.Kind != Receipt {
			continue
		}
		vt := t.ValuationType
		if vt == "" {
			vt = defaultType
		}
		if vt == "" {
			return nil, nil, &Error{CodeValuationTypeRequired, "receipt " + t.ID + " must name a valuation type: the product is split valued"}
		}
		receiptType[t.ID] = vt
	}
	stock := map[string]decimal.Decimal{}
	parts := map[string][]Txn{}
	for _, t := range seq {
		vt := t.ValuationType
		if t.Kind == Receipt {
			vt = receiptType[t.ID]
			stock[vt] = stock[vt].Add(t.Qty)
			parts[vt] = append(parts[vt], t)
			continue
		}
		if marked, ok := receiptType[t.MarkedReceiptID]; ok {
			if vt != "" && vt != marked {
				return nil, nil, &Error{CodeInvalidTxn, fmt.Sprintf("issue %s of valuation type %s marks receipt %s of valuation type %s", t.ID, vt, t.MarkedReceiptID, marked)}
			}
			vt = marked
		}
		if vt == "" {
			vt = defaultType
		}
		if vt == "" {
			if vt, err = soleTypeWithStock(stock, t); err != nil {
				return nil, nil, err
			}
		}
		stock[vt] = stock[vt].Sub(decimal.Min(t.Qty, stock[vt]))
		parts[vt] = append(parts[vt], t)
	}
	types := make([]string, 0, len(parts))
	for vt := range parts {
		types = append(types, vt)
	}
	sort.Strings(types)
	return types, parts, nil
}

// soleTypeWithStock names the valuation type an untyped issue of a split
// product draws from: the only type holding stock, or, when none holds stock,
// the only type seen so far. Anything else must be named (CST-VAL-001).
func soleTypeWithStock(stock map[string]decimal.Decimal, t Txn) (string, error) {
	var holding []string
	for vt, q := range stock {
		if q.IsPositive() {
			holding = append(holding, vt)
		}
	}
	if len(holding) == 1 {
		return holding[0], nil
	}
	if len(holding) == 0 && len(stock) == 1 {
		for vt := range stock {
			return vt, nil
		}
	}
	sort.Strings(holding)
	if len(holding) == 0 {
		return "", &Error{CodeValuationTypeRequired, fmt.Sprintf("issue %s must name a valuation type: no valuation type holds stock", t.ID)}
	}
	return "", &Error{CodeValuationTypeRequired, fmt.Sprintf("issue %s must name a valuation type: stock is held in %v", t.ID, holding)}
}

// CanChangeCategory reports whether a product's valuation category may change,
// given the quantity it holds across all valuation types (FR-20.16). A category
// is locked while any stock exists, including negative stock.
func CanChangeCategory(onHandQty decimal.Decimal) error {
	if !onHandQty.IsZero() {
		return &Error{CodeCategoryLocked, "valuation category cannot change while the product holds stock"}
	}
	return nil
}
