package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	rev "github.com/BenjaminSRussell/passage/services/revrec/internal/events"
)

const (
	// CodeModificationBeforeContract: the effective date is before the contract date.
	CodeModificationBeforeContract = "REV-0401"
	// CodeModificationAfterCredit: a contract that credit notes touched cannot be re-allocated.
	CodeModificationAfterCredit = "REV-0405"
)

// ModificationRequest is POST /v1/revrec/contracts/{id}:modify.
type ModificationRequest struct {
	Type          string
	EffectiveDate time.Time
	PriceChange   decimal.Decimal
	// NewSSP revises the standalone selling price of obligations, by obligation id.
	NewSSP          map[string]decimal.Decimal
	Reason          string
	ExpectedVersion int
}

// Modification is the stored record of one modification with the per-obligation outcome.
type Modification struct {
	ID            string
	Type          string
	EffectiveDate time.Time
	PriceBefore   decimal.Decimal
	PriceAfter    decimal.Decimal
	CatchUp       decimal.Decimal
	Obligations   []domain.ObligationChange
}

// ModifyContract re-allocates a contract's existing obligations (#29, ASC 606-10-25-10 to 25-13) after a price change or a revision of
// standalone selling prices, prospectively or with a cumulative catch-up (domain.Modify). In one transaction it updates the
// allocated amounts and SSPs, rewrites the unrecognized schedule lines, adds the catch-up line where no line is left to carry it (a
// satisfied milestone's is recognized at once and published as schedule.recognized, a ratable one is taken by the next recognition
// run), records the modification with each obligation's allocation delta dated at the effective date (the roll-forward adds it in that
// period), bumps the contract version and publishes contract.allocated again. A contract that credit notes touched is refused
// (REV-0405): the credit notes already moved the schedule.
func (s *Postgres) ModifyContract(ctx context.Context, contractID string, req ModificationRequest) (Contract, Modification, error) {
	p, err := principal(ctx)
	if err != nil {
		return Contract{}, Modification{}, err
	}
	var out Contract
	mod := Modification{ID: ids.New(), Type: req.Type, EffectiveDate: req.EffectiveDate}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		var version int
		err := tx.QueryRow(ctx, `select status, version from revenue_contract where id = $1 for update`, contractID).Scan(&status, &version)
		if err == pgx.ErrNoRows {
			return notFound("contract")
		}
		if err != nil {
			return err
		}
		switch {
		case status == domain.ContractCancelled:
			return httpx.NewProblem(http.StatusConflict, CodeContractCancelled, "the contract is cancelled and cannot be modified")
		case req.ExpectedVersion != 0 && req.ExpectedVersion != version:
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "the contract changed since it was read; reload and review it")
		}
		var credited int
		if err := tx.QueryRow(ctx, `select count(*) from credit_note_reversal where contract_id = $1`, contractID).Scan(&credited); err != nil {
			return err
		}
		if credited > 0 {
			return httpx.NewProblem(http.StatusConflict, CodeModificationAfterCredit, "credit notes already changed this contract's schedule; it cannot be re-allocated")
		}
		c, err := loadTree(ctx, tx, "id = $1", contractID)
		if err != nil {
			return err
		}
		if req.EffectiveDate.Before(c.ContractDate) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeModificationBeforeContract, "the effective date is before the contract date "+c.ContractDate.Format(dateLayout))
		}
		in := domain.ModificationInput{Type: req.Type, EffectiveDate: req.EffectiveDate, Currency: c.Currency, TransactionPrice: c.TransactionPrice,
			PriceChange: req.PriceChange, ResidualRule: c.ResidualRule, NewSSP: req.NewSSP}
		known := map[string]bool{}
		for _, o := range c.Obligations {
			known[o.ID] = true
			mo := domain.ModObligation{ID: o.ID, Method: o.Method, SSP: o.SSP, SSPSource: o.SSPSource, Allocated: o.Allocated}
			for _, sc := range o.Schedule {
				mo.Lines = append(mo.Lines, domain.ModLine{ID: sc.ID, Date: sc.Date, Amount: sc.Amount, Status: sc.Status})
			}
			in.Obligations = append(in.Obligations, mo)
		}
		for id := range req.NewSSP {
			if !known[id] {
				return httpx.NewProblem(http.StatusUnprocessableEntity, "REV-0001", "obligation "+id+" is not on this contract")
			}
		}
		res, err := domain.Modify(in)
		if err != nil {
			return err
		}
		mod.PriceBefore, mod.PriceAfter, mod.CatchUp, mod.Obligations = c.TransactionPrice, res.TransactionPriceAfter, res.CatchUp, res.Obligations
		if _, err := tx.Exec(ctx, `insert into contract_modification (tenant_id, id, legal_entity_id, contract_id, modification_type, effective_date,
			price_before, price_after, catch_up_amount, reason, created_by) values ($1,$2,$3,$4,$5,$6::date,$7,$8,$9,$10,$11)`,
			p.TenantID, mod.ID, c.LegalEntityID, contractID, req.Type, req.EffectiveDate.Format(dateLayout), c.TransactionPrice.String(),
			res.TransactionPriceAfter.String(), res.CatchUp.String(), req.Reason, p.UserID); err != nil {
			return err
		}
		methods := map[string]string{}
		lastSeq := map[string]int{}
		for _, o := range c.Obligations {
			methods[o.ID] = o.Method
			for _, sc := range o.Schedule {
				lastSeq[o.ID] = max(lastSeq[o.ID], sc.Seq)
			}
		}
		day := req.EffectiveDate.Format(dateLayout)
		for _, ch := range res.Obligations {
			if _, err := tx.Exec(ctx, `update performance_obligation set allocated_amount = $2, ssp_amount = $3 where id = $1`,
				ch.ID, ch.AllocatedAfter.String(), ch.SSPAfter.String()); err != nil {
				return err
			}
			for _, l := range ch.Lines {
				if _, err := tx.Exec(ctx, `update revenue_schedule set amount = $2 where id = $1 and status = 'SCHEDULED'`, l.ID, l.Amount.String()); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `insert into allocation_delta (tenant_id, id, modification_id, legal_entity_id, contract_id, obligation_id, effective_date,
				allocated_before, allocated_after, delta, ssp_before, ssp_after, catch_up) values ($1,$2,$3,$4,$5,$6,$7::date,$8,$9,$10,$11,$12,$13)`,
				p.TenantID, ids.New(), mod.ID, c.LegalEntityID, contractID, ch.ID, day, ch.AllocatedBefore.String(), ch.AllocatedAfter.String(),
				ch.AllocatedAfter.Sub(ch.AllocatedBefore).String(), ch.SSPBefore.String(), ch.SSPAfter.String(), ch.CatchUp.String()); err != nil {
				return err
			}
			if ch.NewLine == nil {
				continue
			}
			lineID, lineStatus := ids.New(), domain.ScheduleScheduled
			var recognizedOn any
			if ch.NewLine.Recognized {
				lineStatus, recognizedOn = domain.ScheduleRecognized, day
			}
			if _, err := tx.Exec(ctx, `insert into revenue_schedule (tenant_id, id, obligation_id, seq, scheduled_date, amount, status, recognized_on)
				values ($1,$2,$3,$4,$5::date,$6,$7,$8::date)`, p.TenantID, lineID, ch.ID, lastSeq[ch.ID]+1, day, ch.NewLine.Amount.String(), lineStatus, recognizedOn); err != nil {
				return err
			}
			if ch.NewLine.Recognized {
				if err := enqueueRecognized(ctx, tx, p.TenantID, rev.ScheduleRecognizedData{
					ScheduleID: lineID, ObligationID: ch.ID, ContractID: contractID, LegalEntityID: c.LegalEntityID,
					PeriodID: req.EffectiveDate.Format(periodLayout), RecognizedOn: day, Currency: c.Currency, Amount: fixed(ch.NewLine.Amount, c.Currency),
					DeferredAccount: deferredAccount, RevenueAccount: revenueAccount,
				}); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `update revenue_contract c set version = c.version + 1, transaction_price = $2::numeric,
		  status = case when exists (
		      select 1 from performance_obligation o
		      left join revenue_schedule s on s.tenant_id = o.tenant_id and s.obligation_id = o.id
		      where o.tenant_id = c.tenant_id and o.contract_id = c.id and (o.method = 'USAGE' or s.status = 'SCHEDULED'))
		    then 'OPEN' else 'COMPLETE' end
		  where c.id = $1`, contractID, res.TransactionPriceAfter.String()); err != nil {
			return err
		}
		out, err = loadTree(ctx, tx, "id = $1", contractID)
		if err != nil {
			return err
		}
		return enqueueAllocated(ctx, tx, p.TenantID, out)
	})
	if err != nil {
		return Contract{}, Modification{}, err
	}
	return out, mod, nil
}
