package store

import (
	"context"
	"encoding/json"
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
	// CodeModificationAfterCredit was REV-0405, the refusal of a contract that credit notes touched; such contracts are modified on
	// their amounts net of the credit notes now, and the code is kept only so that it is not reused.
	CodeModificationAfterCredit = "REV-0405"
)

// ModificationBillingKeyPrefix keys the billing request of a modification.
const ModificationBillingKeyPrefix = "revrec-modification:"

// ModificationRequest is POST /v1/revrec/contracts/{id}:modify.
type ModificationRequest struct {
	Type          string
	EffectiveDate time.Time
	PriceChange   decimal.Decimal
	// NewSSP revises the standalone selling price of obligations, by obligation id.
	NewSSP map[string]decimal.Decimal
	// Scope reduces (0 < scope < 1) or removes (0) obligations, by obligation id: the share of the remaining performance still promised.
	Scope           map[string]decimal.Decimal
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
	Reversal      decimal.Decimal
	Obligations   []domain.ObligationChange
	// Billing is the billing request queued for the price change (empty when the price did not change).
	BillingID string
}

// creditTotals is what credit notes did to one obligation: reduced off its schedule and reversed out of its recognized revenue.
type creditTotals struct {
	reduced, reversed decimal.Decimal
}

// obligationCredits returns, per obligation of the contract, what credit notes reduced and reversed, plus the revenue earlier
// modifications took back (modReversed).
func obligationCredits(ctx context.Context, tx pgx.Tx, contractID string) (map[string]creditTotals, map[string]decimal.Decimal, error) {
	credits := map[string]creditTotals{}
	rows, err := tx.Query(ctx, `select (o ->> 'obligationId'), coalesce(sum((o ->> 'reduced')::numeric), 0)::text, coalesce(sum((o ->> 'reversed')::numeric), 0)::text
		from credit_note_reversal r cross join lateral jsonb_array_elements(r.obligations) o where r.contract_id = $1::uuid group by 1`, contractID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, red, rev string
		if err := rows.Scan(&id, &red, &rev); err != nil {
			rows.Close()
			return nil, nil, err
		}
		credits[id] = creditTotals{reduced: decimal.RequireFromString(red), reversed: decimal.RequireFromString(rev)}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	mods := map[string]decimal.Decimal{}
	rows, err = tx.Query(ctx, `select obligation_id::text, sum(revenue_reversed)::text from modification_reversal where contract_id = $1::uuid group by 1`, contractID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, rev string
		if err := rows.Scan(&id, &rev); err != nil {
			return nil, nil, err
		}
		mods[id] = decimal.RequireFromString(rev)
	}
	return credits, mods, rows.Err()
}

// billingBody is the request body of billing's POST /v1/billing/contract-modifications.
type billingBody struct {
	ModificationID string `json:"modificationId"`
	ContractID     string `json:"contractId"`
	LegalEntityID  string `json:"legalEntityId"`
	OrderID        string `json:"orderId"`
	CustomerID     string `json:"customerId"`
	Currency       string `json:"currency"`
	PriceChange    string `json:"priceChange"`
	EffectiveDate  string `json:"effectiveDate"`
	Reason         string `json:"reason,omitempty"`
	// Lines is the allocation change of each product (signed); they add up to PriceChange.
	Lines []billingLine `json:"lines"`
}

type billingLine struct {
	ProductID string `json:"productId"`
	Amount    string `json:"amount"`
}

// ModifyContract re-allocates a contract's existing obligations (#29, ASC 606-10-25-10 to 25-13) after a price change or a revision of
// standalone selling prices, prospectively or with a cumulative catch-up (domain.Modify). In one transaction it updates the
// allocated amounts and SSPs, rewrites the unrecognized schedule lines, adds the catch-up line where no line is left to carry it (a
// satisfied milestone's is recognized at once and published as schedule.recognized, a ratable one is taken by the next recognition
// run), records the modification with each obligation's allocation delta dated at the effective date (the roll-forward adds it in that
// period), bumps the contract version and publishes contract.allocated again. Obligations may be reduced or removed (Scope), a
// negative catch-up or a price concession on transferred performance is recorded as a modification_reversal (posted Dr 4000 / Cr 2400
// by the journal postings), a contract credit notes touched is modified on its net amounts, and a price change queues the billing
// request (modification_billing) that has billing invoice or credit it.
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
		c, err := loadTree(ctx, tx, "id = $1", contractID)
		if err != nil {
			return err
		}
		if req.EffectiveDate.Before(c.ContractDate) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeModificationBeforeContract, "the effective date is before the contract date "+c.ContractDate.Format(dateLayout))
		}
		credits, modReversed, err := obligationCredits(ctx, tx, contractID)
		if err != nil {
			return err
		}
		// A contract credit notes touched is modified on its amounts net of them: each obligation's allocation less what the credit
		// notes reduced and reversed on it, the transaction price less all of that, and recognized revenue less every reversal
		// (credit notes and earlier modifications). What is stored adds the credit notes back, so the stored allocation keeps
		// matching the roll-forward's additions and adjustments.
		creditedTotal := decimal.Zero
		for _, ct := range credits {
			creditedTotal = creditedTotal.Add(ct.reduced).Add(ct.reversed)
		}
		netPrice := c.TransactionPrice.Sub(creditedTotal)
		in := domain.ModificationInput{Type: req.Type, EffectiveDate: req.EffectiveDate, Currency: c.Currency, TransactionPrice: netPrice,
			PriceChange: req.PriceChange, ResidualRule: c.ResidualRule, NewSSP: req.NewSSP, Scope: req.Scope}
		known := map[string]bool{}
		for _, o := range c.Obligations {
			known[o.ID] = true
			ct := credits[o.ID]
			mo := domain.ModObligation{ID: o.ID, Method: o.Method, SSP: o.SSP, SSPSource: o.SSPSource,
				Allocated: o.Allocated.Sub(ct.reduced).Sub(ct.reversed), Reversed: ct.reversed.Add(modReversed[o.ID])}
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
		for id := range req.Scope {
			if !known[id] {
				return httpx.NewProblem(http.StatusUnprocessableEntity, "REV-0001", "obligation "+id+" is not on this contract")
			}
		}
		res, err := domain.Modify(in)
		if err != nil {
			return err
		}
		mod.PriceBefore, mod.PriceAfter, mod.CatchUp, mod.Reversal, mod.Obligations = netPrice, res.TransactionPriceAfter, res.CatchUp, res.Reversal, res.Obligations
		type scopeChange struct {
			ObligationID string `json:"obligationId"`
			Scope        string `json:"scope"`
		}
		scopes := []scopeChange{}
		for _, o := range c.Obligations {
			if sc, ok := req.Scope[o.ID]; ok {
				scopes = append(scopes, scopeChange{ObligationID: o.ID, Scope: sc.String()})
			}
		}
		scopeJSON, err := json.Marshal(scopes)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into contract_modification (tenant_id, id, legal_entity_id, contract_id, modification_type, effective_date,
			price_before, price_after, catch_up_amount, reason, created_by, reversal_amount, scope_changes) values ($1,$2,$3,$4,$5,$6::date,$7,$8,$9,$10,$11,$12,$13)`,
			p.TenantID, mod.ID, c.LegalEntityID, contractID, req.Type, req.EffectiveDate.Format(dateLayout), netPrice.String(),
			res.TransactionPriceAfter.String(), res.CatchUp.String(), req.Reason, p.UserID, res.Reversal.String(), scopeJSON); err != nil {
			return err
		}
		if !req.PriceChange.IsZero() {
			products := map[string]string{}
			for _, o := range c.Obligations {
				products[o.ID] = o.ProductID
			}
			deltas := map[string]decimal.Decimal{}
			var order []string
			for _, ch := range res.Obligations {
				pid := products[ch.ID]
				if _, ok := deltas[pid]; !ok {
					order = append(order, pid)
				}
				deltas[pid] = deltas[pid].Add(ch.AllocatedAfter.Sub(ch.AllocatedBefore))
			}
			lines := []billingLine{}
			for _, pid := range order {
				if !deltas[pid].IsZero() {
					lines = append(lines, billingLine{ProductID: pid, Amount: fixed(deltas[pid], c.Currency)})
				}
			}
			body, err := json.Marshal(billingBody{Lines: lines, ModificationID: mod.ID, ContractID: contractID, LegalEntityID: c.LegalEntityID, OrderID: c.OrderID,
				CustomerID: c.CustomerID, Currency: c.Currency, PriceChange: fixed(req.PriceChange, c.Currency), EffectiveDate: req.EffectiveDate.Format(dateLayout),
				Reason: req.Reason})
			if err != nil {
				return err
			}
			mod.BillingID = ids.New()
			if _, err := tx.Exec(ctx, `insert into modification_billing (tenant_id, id, legal_entity_id, modification_id, contract_id, idempotency_key, body)
				values ($1,$2,$3,$4,$5,$6,$7)`, p.TenantID, mod.BillingID, c.LegalEntityID, mod.ID, contractID, ModificationBillingKeyPrefix+mod.ID, body); err != nil {
				return err
			}
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
			ct := credits[ch.ID]
			gross := ch.AllocatedAfter.Add(ct.reduced).Add(ct.reversed)
			ssp := ch.SSPAfter
			if !ssp.IsPositive() {
				// A removed obligation keeps its last SSP (the column must stay positive); its allocation is what it recognized.
				ssp = ch.SSPBefore
			}
			if _, err := tx.Exec(ctx, `update performance_obligation set allocated_amount = $2, ssp_amount = $3 where id = $1`,
				ch.ID, gross.String(), ssp.String()); err != nil {
				return err
			}
			for _, l := range ch.Lines {
				// A line the modification takes to zero is cancelled (REVERSED), as a credit note does, so it no longer keeps the
				// contract open.
				if _, err := tx.Exec(ctx, `update revenue_schedule set amount = $2, status = case when $2::numeric = 0 then $3 else status end
					where id = $1 and status = 'SCHEDULED'`, l.ID, l.Amount.String(), domain.ScheduleReversed); err != nil {
					return err
				}
			}
			if ch.Reversal.IsPositive() {
				if _, err := tx.Exec(ctx, `insert into modification_reversal (tenant_id, id, legal_entity_id, contract_id, modification_id, obligation_id,
					currency, posting_date, revenue_reversed) values ($1,$2,$3,$4,$5,$6,$7,$8::date,$9)`, p.TenantID, ids.New(), c.LegalEntityID,
					contractID, mod.ID, ch.ID, c.Currency, day, ch.Reversal.String()); err != nil {
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
		if _, err := tx.Exec(ctx, `update revenue_contract c set version = c.version + 1, transaction_price = $2::numeric + $3::numeric,
		  status = case when exists (
		      select 1 from performance_obligation o
		      left join revenue_schedule s on s.tenant_id = o.tenant_id and s.obligation_id = o.id
		      where o.tenant_id = c.tenant_id and o.contract_id = c.id and (o.method = 'USAGE' or s.status = 'SCHEDULED'))
		    then 'OPEN' else 'COMPLETE' end
		  where c.id = $1`, contractID, res.TransactionPriceAfter.String(), creditedTotal.String()); err != nil {
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
