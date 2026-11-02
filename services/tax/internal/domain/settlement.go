package domain

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

type Txn struct {
	ID              string `json:"id"`
	Code            string `json:"code"`
	Direction       string `json:"direction"`
	TaxDate         Date   `json:"tax_date"`
	Amount          Dec    `json:"amount"`
	SettlementRunID string `json:"settlement_run_id"`
}

type Period struct{ From, To Date }

type GLMovement interface {
	Movements(ctx context.Context, from, to time.Time) (map[string]decimal.Decimal, error)
}

type Poster interface {
	// TODO: PSG-12 replace with the ledger.Post client once services/ledger exists
	Post(ctx context.Context, idempotencyKey string, s Settlement) error
}

type CodeSettlement struct {
	Code      string `json:"code"`
	Direction string `json:"direction"`
	Amount    Dec    `json:"amount"`
}

type Settlement struct {
	Version        int              `json:"version"`
	PeriodFrom     Date             `json:"period_from"`
	PeriodTo       Date             `json:"period_to"`
	OutputTotal    Dec              `json:"output_tax_total"`
	InputTotal     Dec              `json:"input_tax_total"`
	NetAmount      Dec              `json:"net_amount"`
	TransactionIDs []string         `json:"transaction_ids"`
	Lines          []CodeSettlement `json:"lines"`
}

func MovementKey(code, direction string) string { return code + "|" + direction }

func PostingKey(settlementID string) string { return "post:tax:settlement:" + settlementID + ":settle" }

type bucket struct {
	code, direction string
	all, open       decimal.Decimal
	hasOpen         bool
}

func Settle(ctx context.Context, txns []Txn, period Period, priorVersion int, gl GLMovement) (Settlement, error) {
	buckets := map[string]*bucket{}
	out := Settlement{Version: priorVersion, PeriodFrom: period.From, PeriodTo: period.To, TransactionIDs: []string{}, Lines: []CodeSettlement{}}
	for _, t := range txns {
		if t.TaxDate.Before(period.From.Time) || t.TaxDate.After(period.To.Time) {
			continue
		}
		key := MovementKey(t.Code, t.Direction)
		b := buckets[key]
		if b == nil {
			b = &bucket{code: t.Code, direction: t.Direction}
			buckets[key] = b
		}
		b.all = b.all.Add(t.Amount.Decimal)
		if t.SettlementRunID == "" {
			b.open = b.open.Add(t.Amount.Decimal)
			b.hasOpen = true
			out.TransactionIDs = append(out.TransactionIDs, t.ID)
		}
	}
	movements, err := gl.Movements(ctx, period.From.Time, period.To.Time)
	if err != nil {
		return Settlement{}, fmt.Errorf("read GL tax movement: %w", err)
	}
	keys := map[string]bool{}
	for k := range buckets {
		keys[k] = true
	}
	for k := range movements {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		recorded := decimal.Zero
		if b := buckets[k]; b != nil {
			recorded = b.all
		}
		if !recorded.Equal(movements[k]) {
			return Settlement{}, &Error{Code: CodeSettlementMismatch, Field: k, Detail: fmt.Sprintf(
				"The recorded tax for %s does not match the general ledger tax account movement.", k)}
		}
	}
	for _, k := range sortedKeys(keys) {
		b := buckets[k]
		if b == nil || !b.hasOpen {
			continue
		}
		out.Lines = append(out.Lines, CodeSettlement{Code: b.code, Direction: b.direction, Amount: Dec{b.open}})
		if b.direction == DirInput {
			out.InputTotal = Dec{out.InputTotal.Add(b.open)}
		} else {
			out.OutputTotal = Dec{out.OutputTotal.Add(b.open)}
		}
	}
	out.NetAmount = Dec{out.OutputTotal.Sub(out.InputTotal.Decimal)}
	if len(out.TransactionIDs) > 0 {
		out.Version++
	}
	return out, nil
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func PostSettlement(ctx context.Context, p Poster, settlementID string, s Settlement) error {
	if len(s.TransactionIDs) == 0 {
		return nil
	}
	return p.Post(ctx, PostingKey(settlementID), s)
}
