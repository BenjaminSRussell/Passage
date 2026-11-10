package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
)

// Codes of credit-note lines the consumer cannot apply. They are permanent: the event is parked on the dead-letter topic.
const (
	// CodeCreditNoContract: no live revenue contract for the credited line's order in the legal entity.
	CodeCreditNoContract = "REV-0300"
	// CodeCreditAmbiguous: more than one live revenue contract for the order.
	CodeCreditAmbiguous = "REV-0301"
	// CodeCreditNoObligation: the line reverses a deferred amount and the contract has no RATABLE or MILESTONE obligation for
	// its product.
	CodeCreditNoObligation = "REV-0302"
	// CodeCreditExceeds: the deferred amount exceeds what the obligations still hold, unrecognized plus recognized not yet
	// reversed.
	CodeCreditExceeds = "REV-0303"
	// CodeCreditCurrency: the credit note is in another currency than the contract.
	CodeCreditCurrency = "REV-0304"
)

// CreditNote is the header of a booked credit note (billing creditnote.booked).
type CreditNote struct {
	ID            string
	Number        string
	LegalEntityID string
	Currency      string
	PostingDate   time.Time
	EventID       string
}

// CreditLine is one credited line: the order and product of the original line and the deferred part billing debited to 2400.
type CreditLine struct {
	LineNo         int
	OriginalLineNo int
	OrderID        string
	ProductID      string
	Deferred       decimal.Decimal
}

// ObligationReversal is how one obligation absorbed its share of a credited line.
type ObligationReversal struct {
	ObligationID string          `json:"obligationId"`
	Share        decimal.Decimal `json:"share"`
	Reduced      decimal.Decimal `json:"reduced"`
	Reversed     decimal.Decimal `json:"reversed"`
}

// Reversal is the recorded reversal of one credited line.
type Reversal struct {
	ID          string
	ContractID  string
	Deferred    decimal.Decimal
	Reduced     decimal.Decimal
	Reversed    decimal.Decimal
	Obligations []ObligationReversal
}

func creditProblem(code, detail string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, code, detail)
}

// ApplyCreditLine reverses one credited line in one transaction (domain.PlanReversal): its deferred amount is split over the
// deferred obligations of the line's product by allocation, each share reduces the obligation's SCHEDULED lines pro rata and
// what exceeds them is recorded as recognized revenue reversed, for the posting sweep to book. The contract row is locked first,
// as milestone satisfaction does, so reversals of one contract never interleave. A line already applied is returned with
// applied=false and changes nothing; a rule failure is a 422 problem with a REV-03xx code.
func (s *Postgres) ApplyCreditLine(ctx context.Context, cn CreditNote, l CreditLine) (Reversal, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Reversal{}, false, err
	}
	var out Reversal
	applied := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if found, err := existingReversal(ctx, tx, cn.ID, l.LineNo, &out); err != nil || found {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, currency::text from revenue_contract
			where legal_entity_id = $1::uuid and order_id = $2::uuid and status <> 'CANCELLED' order by id for update`, cn.LegalEntityID, l.OrderID)
		if err != nil {
			return err
		}
		type contractRow struct{ id, currency string }
		contracts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (contractRow, error) {
			var c contractRow
			return c, r.Scan(&c.id, &c.currency)
		})
		if err != nil {
			return err
		}
		switch {
		case len(contracts) == 0:
			return creditProblem(CodeCreditNoContract, "no revenue contract for order "+l.OrderID+" in the credit note's legal entity")
		case len(contracts) > 1:
			return creditProblem(CodeCreditAmbiguous, "more than one revenue contract for order "+l.OrderID)
		case contracts[0].currency != cn.Currency:
			return creditProblem(CodeCreditCurrency, "the credit note is in "+cn.Currency+", the contract in "+contracts[0].currency)
		}
		out.ContractID = contracts[0].id
		if found, err := existingReversal(ctx, tx, cn.ID, l.LineNo, &out); err != nil || found {
			return err
		}
		plans, err := planCreditLine(ctx, tx, out.ContractID, cn.Currency, l)
		if err != nil {
			return err
		}
		out.ID, out.Deferred, out.Reduced, out.Reversed, out.Obligations = ids.New(), l.Deferred, decimal.Zero, decimal.Zero, plans
		for _, o := range plans {
			out.Reduced, out.Reversed = out.Reduced.Add(o.Reduced), out.Reversed.Add(o.Reversed)
		}
		raw, err := json.Marshal(plans)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into credit_note_reversal (tenant_id, id, legal_entity_id, contract_id, credit_note_id, credit_note_number,
			line_no, original_line_no, product_id, currency, posting_date, deferred_amount, deferred_reduced, revenue_reversed, obligations, event_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date, $12, $13, $14, $15, $16)`,
			p.TenantID, out.ID, cn.LegalEntityID, out.ContractID, cn.ID, cn.Number, l.LineNo, l.OriginalLineNo, l.ProductID, cn.Currency,
			cn.PostingDate.Format(dateLayout), out.Deferred.String(), out.Reduced.String(), out.Reversed.String(), raw, cn.EventID); err != nil {
			return writeErr(err)
		}
		if _, err := touchContracts(ctx, tx, []string{out.ContractID}); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return Reversal{}, false, err
	}
	return out, applied, nil
}

func existingReversal(ctx context.Context, tx pgx.Tx, creditNoteID string, lineNo int, out *Reversal) (bool, error) {
	var deferred, reduced, reversed string
	var raw []byte
	err := tx.QueryRow(ctx, `select id::text, contract_id::text, deferred_amount::text, deferred_reduced::text, revenue_reversed::text, obligations
		from credit_note_reversal where credit_note_id = $1::uuid and line_no = $2`, creditNoteID, lineNo).Scan(&out.ID, &out.ContractID, &deferred, &reduced, &reversed, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if out.Deferred, err = decimal.NewFromString(deferred); err != nil {
		return false, err
	}
	if out.Reduced, err = decimal.NewFromString(reduced); err != nil {
		return false, err
	}
	if out.Reversed, err = decimal.NewFromString(reversed); err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, &out.Obligations)
}

// planCreditLine splits the line's deferred amount over the deferred obligations of its product, applies each share to the
// obligation's SCHEDULED lines (updated here) and returns what each obligation absorbed.
func planCreditLine(ctx context.Context, tx pgx.Tx, contractID, currency string, l CreditLine) ([]ObligationReversal, error) {
	places := money.MinorUnits(currency)
	rows, err := tx.Query(ctx, `select id::text, allocated_amount::text from performance_obligation
		where contract_id = $1::uuid and product_id = $2::uuid and method = any($3::text[]) order by line_no`, contractID, l.ProductID, domain.DeferredMethods())
	if err != nil {
		return nil, err
	}
	type obligationRow struct {
		id        string
		allocated decimal.Decimal
	}
	obligations, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (obligationRow, error) {
		var o obligationRow
		var allocated string
		if err := r.Scan(&o.id, &allocated); err != nil {
			return o, err
		}
		o.allocated, err = decimal.NewFromString(allocated)
		return o, err
	})
	if err != nil {
		return nil, err
	}
	if len(obligations) == 0 {
		return nil, creditProblem(CodeCreditNoObligation, "the contract has no RATABLE or MILESTONE obligation for product "+l.ProductID)
	}
	weights := make([]decimal.Decimal, len(obligations))
	for i, o := range obligations {
		weights[i] = o.allocated
	}
	shares := domain.Apportion(l.Deferred, weights, places)
	out := make([]ObligationReversal, 0, len(obligations))
	for i, o := range obligations {
		plan, err := applyShare(ctx, tx, contractID, o.id, shares[i], places)
		if err != nil {
			return nil, err
		}
		out = append(out, plan)
	}
	return out, nil
}

func applyShare(ctx context.Context, tx pgx.Tx, contractID, obligationID string, share decimal.Decimal, places int32) (ObligationReversal, error) {
	out := ObligationReversal{ObligationID: obligationID, Share: share, Reduced: decimal.Zero, Reversed: decimal.Zero}
	if !share.IsPositive() {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select id::text, amount::text from revenue_schedule where obligation_id = $1::uuid and status = $2
		order by seq for update`, obligationID, domain.ScheduleScheduled)
	if err != nil {
		return out, err
	}
	type lineRow struct {
		id     string
		amount decimal.Decimal
	}
	lines, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (lineRow, error) {
		var l lineRow
		var amount string
		if err := r.Scan(&l.id, &amount); err != nil {
			return l, err
		}
		l.amount, err = decimal.NewFromString(amount)
		return l, err
	})
	if err != nil {
		return out, err
	}
	unrecognized := decimal.Zero
	amounts := make([]decimal.Decimal, len(lines))
	for i, l := range lines {
		amounts[i], unrecognized = l.amount, unrecognized.Add(l.amount)
	}
	var recognizedText string
	if err := tx.QueryRow(ctx, `select (coalesce((select sum(amount) from revenue_schedule where obligation_id = $1::uuid and status = $2), 0)
		- coalesce((select sum((o ->> 'reversed')::numeric) from credit_note_reversal r cross join lateral jsonb_array_elements(r.obligations) o
		  where r.contract_id = $3::uuid and (o ->> 'obligationId')::uuid = $1::uuid), 0))::text`, obligationID, domain.ScheduleRecognized, contractID).Scan(&recognizedText); err != nil {
		return out, err
	}
	recognized, err := decimal.NewFromString(recognizedText)
	if err != nil {
		return out, err
	}
	plan, ok := domain.PlanReversal(share, unrecognized, recognized)
	if !ok {
		return out, creditProblem(CodeCreditExceeds, fmt.Sprintf("obligation %s: a credit of %s exceeds the %s unrecognized and %s recognized it still holds",
			obligationID, share, unrecognized, recognized))
	}
	out.Reduced, out.Reversed = plan.Reduced, plan.Reversed
	for i, cut := range domain.ReduceLines(amounts, plan.Reduced, places) {
		if !cut.IsPositive() {
			continue
		}
		if _, err := tx.Exec(ctx, `update revenue_schedule set amount = amount - $2::numeric, credited_amount = credited_amount + $2::numeric,
			status = case when amount - $2::numeric = 0 then $3 else status end where id = $1::uuid`, lines[i].id, cut.String(), domain.ScheduleReversed); err != nil {
			return out, err
		}
	}
	return out, nil
}
