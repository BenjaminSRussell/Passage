package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// MaxRetroLines bounds the invoice lines one recompute request replays.
const MaxRetroLines = 5000

// RetroLine is one posted invoice line replayed by a retroactive recompute.
type RetroLine struct {
	InvoiceLineID string
	InvoiceDate   time.Time
	Value         decimal.Decimal
	Quantity      decimal.Decimal
	Currency      string
}

// RetroInput replays the invoices posted in an agreement's scope between valid_from and its
// activation (FR-26.23). RunKey (the Idempotency-Key) names the run.
type RetroInput struct {
	AgreementID string
	RunKey      string
	Lines       []RetroLine
}

// RetroResult reports a retroactive recompute (FR-26.23, BR-26.14).
type RetroResult struct {
	ActivatedOn time.Time
	// ReaccruedLines counts the replayed invoice lines that were not accrued before (PRC-0006).
	ReaccruedLines int
	Deltas         []domain.RetroDelta
	// Replayed is true when the run key was used before and the stored result is returned.
	Replayed bool
}

func retroLineID(runKey, period string) string { return "retro:" + runKey + ":" + period }

// RecomputeRetro replays posted invoice lines for a retroactively activated agreement
// (FR-26.23, SAP VBOF). The agreement is activated when it is created, so its activation day
// is its creation day; an agreement whose valid_from is not before that day is refused. Each
// replayed line that was not accrued before joins its period's cumulative basis (recorded
// with a zero amount and retroactive=true, so a later redelivery of the same line is not
// counted twice); every period is then recomputed at its cumulative basis and the delta
// against what it already accrued posts once per period, with retroactive=true and its
// rebateaccrual.posted event in the same transaction (BR-26.7, BR-26.14). Posted invoices are
// never re-priced. A repeated run key returns the stored deltas without replaying.
func (s *Postgres) RecomputeRetro(ctx context.Context, in RetroInput) (RetroResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out RetroResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock the agreement: recomputes of one agreement serialize.
		if _, err := tx.Exec(ctx, `select 1 from rebate_agreement where id = $1 for no key update`, in.AgreementID); err != nil {
			return err
		}
		a, err := loadAgreement(ctx, tx, `where id = $1`, in.AgreementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return agreementNotFound(in.AgreementID)
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select (created_at at time zone 'UTC')::date from rebate_agreement where id = $1`, a.ID).Scan(&out.ActivatedOn); err != nil {
			return err
		}
		replay, err := retroReplay(ctx, tx, a, in.RunKey)
		if err != nil {
			return err
		}
		if replay != nil {
			replay.ActivatedOn = out.ActivatedOn
			out = *replay
			return nil
		}
		if !domain.IsRetroactive(a, out.ActivatedOn) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation,
				"agreement "+a.ID+" is not retroactive: valid_from is not before its activation on "+dayStr(out.ActivatedOn)+"; accrue invoices through /rebates/accruals")
		}
		if err := checkRetroLines(a, out.ActivatedOn, in.Lines); err != nil {
			return err
		}
		periods := map[string]bool{}
		for _, l := range in.Lines {
			periods[domain.PeriodKey(a.Period, l.InvoiceDate)] = true
		}
		for key := range periods {
			if _, err := tx.Exec(ctx, `insert into rebate_period (tenant_id, agreement_id, period_key) values ($1, $2, $3) on conflict do nothing`,
				p.TenantID, a.ID, key); err != nil {
				return err
			}
		}
		state, err := lockPeriods(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		for _, l := range in.Lines {
			key := domain.PeriodKey(a.Period, l.InvoiceDate)
			existing, err := scanAccrual(tx.QueryRow(ctx, accrualColumns+` where agreement_id = $1 and invoice_line_id = $2`, a.ID, l.InvoiceLineID))
			if err == nil {
				if !sameInvoiceLine(existing, AccrualInput{InvoiceDate: l.InvoiceDate, Value: l.Value, Quantity: l.Quantity}) {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse,
						"invoice line "+l.InvoiceLineID+" was already accrued with a different date, value or quantity")
				}
				continue // already in the period basis
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into rebate_accrual (tenant_id, id, legal_entity_id, agreement_id, period_key, invoice_line_id,
				invoice_date, basis_value, basis_qty, accrued_amount, currency, retroactive)
				values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, 0, $10, true)`,
				p.TenantID, ids.New(), a.LegalEntityID, a.ID, key, l.InvoiceLineID, l.InvoiceDate, l.Value.String(), l.Quantity.String(), a.Currency); err != nil {
				return fmt.Errorf("insert replayed invoice line: %w", err)
			}
			st := state[key]
			st.CumValue, st.CumQty = st.CumValue.Add(l.Value), st.CumQty.Add(l.Quantity)
			state[key] = st
			out.ReaccruedLines++
		}
		periodsInOrder := make([]domain.RetroPeriod, 0, len(state))
		for _, st := range state {
			periodsInOrder = append(periodsInOrder, st)
		}
		sort.Slice(periodsInOrder, func(i, j int) bool { return periodsInOrder[i].Period < periodsInOrder[j].Period })
		for _, st := range periodsInOrder {
			if _, err := tx.Exec(ctx, `update rebate_period set basis_value = $3::numeric, basis_qty = $4::numeric where agreement_id = $1 and period_key = $2`,
				a.ID, st.Period, st.CumValue.String(), st.CumQty.String()); err != nil {
				return err
			}
		}
		out.Deltas = domain.RetroDeltas(a, periodsInOrder)
		for _, d := range out.Deltas {
			lineID := retroLineID(in.RunKey, d.Period)
			if _, err := tx.Exec(ctx, `update rebate_period set accrued_total = $3::numeric where agreement_id = $1 and period_key = $2`,
				a.ID, d.Period, d.Target.String()); err != nil {
				return err
			}
			// The delta row carries the period's cumulative basis, so a replayed run can report its target.
			accrualID := ids.New()
			if _, err := tx.Exec(ctx, `insert into rebate_accrual (tenant_id, id, legal_entity_id, agreement_id, period_key, invoice_line_id,
				invoice_date, basis_value, basis_qty, accrued_amount, currency, retroactive)
				values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11, true)`,
				p.TenantID, accrualID, a.LegalEntityID, a.ID, d.Period, lineID, out.ActivatedOn, d.CumValue.String(), d.CumQty.String(),
				d.Delta.String(), a.Currency); err != nil {
				return fmt.Errorf("insert retro delta: %w", err)
			}
			evt, err := events.New(pevents.RebateAccrualPosted, eventSource, accrualID, p.TenantID, pevents.RebateAccrualPostedData{
				AccrualID: accrualID, LegalEntityID: a.LegalEntityID, AgreementID: a.ID, PeriodID: d.Period, InvoiceLineID: lineID,
				Amount: d.Delta.String(), Currency: a.Currency, DebitAccount: a.ExpenseAccount, CreditAccount: a.AccrualAccount, Retroactive: true})
			if err != nil {
				return err
			}
			if err := outbox.Enqueue(ctx, tx, "rebateaccrual", accrualID, evt); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// checkRetroLines refuses lines outside the replay window, in another currency, or repeated.
func checkRetroLines(a domain.RebateAgreement, activatedOn time.Time, lines []RetroLine) error {
	seen := map[string]bool{}
	for _, l := range lines {
		switch {
		case seen[l.InvoiceLineID]:
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "invoice line "+l.InvoiceLineID+" appears more than once")
		case strings.HasPrefix(l.InvoiceLineID, "retro:"):
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "invoice line ids starting with retro: are reserved")
		case l.Currency != a.Currency:
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "invoice line "+l.InvoiceLineID+" is not in the agreement currency "+a.Currency)
		case !domain.InRetroScope(a, activatedOn, l.InvoiceDate):
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation,
				"invoice line "+l.InvoiceLineID+" is dated "+dayStr(l.InvoiceDate)+", outside "+dayStr(a.ValidFrom)+" to the activation on "+dayStr(activatedOn))
		}
		seen[l.InvoiceLineID] = true
	}
	return nil
}

// lockPeriods reads every period of an agreement with its cumulative basis and accrued total,
// locking the rows in key order.
func lockPeriods(ctx context.Context, tx pgx.Tx, agreementID string) (map[string]domain.RetroPeriod, error) {
	rows, err := tx.Query(ctx, `select period_key, basis_value::text, basis_qty::text, accrued_total::text from rebate_period
		where agreement_id = $1 order by period_key for update`, agreementID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]domain.RetroPeriod{}
	for rows.Next() {
		var key, bv, bq, acc string
		if err := rows.Scan(&key, &bv, &bq, &acc); err != nil {
			return nil, fmt.Errorf("scan rebate period: %w", err)
		}
		out[key] = domain.RetroPeriod{Period: key, CumValue: decimal.RequireFromString(bv), CumQty: decimal.RequireFromString(bq),
			Posted: decimal.RequireFromString(acc)}
	}
	return out, rows.Err()
}

// retroReplay returns the stored result of a run key that was already used, or nil.
func retroReplay(ctx context.Context, tx pgx.Tx, a domain.RebateAgreement, runKey string) (*RetroResult, error) {
	prefix := retroLineID(runKey, "")
	rows, err := tx.Query(ctx, `select period_key, basis_value::text, basis_qty::text, accrued_amount::text from rebate_accrual
		where agreement_id = $1 and left(invoice_line_id, length($2)) = $2 order by period_key`, a.ID, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out *RetroResult
	for rows.Next() {
		var key, bv, bq, amt string
		if err := rows.Scan(&key, &bv, &bq, &amt); err != nil {
			return nil, fmt.Errorf("scan retro delta: %w", err)
		}
		if out == nil {
			out = &RetroResult{Replayed: true, Deltas: []domain.RetroDelta{}}
		}
		v, q, delta := decimal.RequireFromString(bv), decimal.RequireFromString(bq), decimal.RequireFromString(amt)
		target := domain.PeriodTarget(a, v, q)
		out.Deltas = append(out.Deltas, domain.RetroDelta{Period: key, Target: target, Posted: target.Sub(delta), Delta: delta, CumValue: v, CumQty: q})
	}
	return out, rows.Err()
}
