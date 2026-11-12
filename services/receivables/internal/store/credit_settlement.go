package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

const (
	// CodeCreditSettleInvalid: the request does not name a settlement this customer's items allow.
	CodeCreditSettleInvalid = "AR-SETTLE-INVALID"
	// CodeNotCredit: the item is not an open credit item.
	CodeNotCredit = "AR-SETTLE-NOT-CREDIT"
	// CodeNotDebit: a counterpart is not an open debit item.
	CodeNotDebit = "AR-SETTLE-NOT-DEBIT"
)

// CreditDebit is one debit item to reduce and by how much (a positive magnitude). Amount is what comes off the credit, in the
// credit's currency. For a debit in another currency (the entity settles across currencies) ItemAmount is what comes off the
// debit in its currency; without it, it is priced at what Amount is worth in the debit's currency on the settlement date.
type CreditDebit struct {
	OpenItemID string          `json:"openItemId"`
	Amount     decimal.Decimal `json:"amount"`
	ItemAmount decimal.Decimal `json:"itemAmount"`
}

// CreditSettlementRequest offsets one credit item against debit items of the same customer and currency: the named debits
// with their amounts, or, with AgainstOriginal, the open items of the invoice a credit note credits (oldest due date and
// installment first), as much as the credit holds.
type CreditSettlementRequest struct {
	LegalEntityID   string
	CreditItemID    string
	Debits          []CreditDebit
	AgainstOriginal bool
}

// CreditSettlementLine is one debit item after the settlement.
type CreditSettlementLine struct {
	OpenItemID     string          `json:"openItemId"`
	DocumentNumber string          `json:"documentNumber"`
	Amount         decimal.Decimal `json:"amount"`
	OpenAfter      decimal.Decimal `json:"openAfter"`
	ItemStatus     string          `json:"itemStatus"`
	// ItemCurrency, ItemAmount and RealizedFX are set on a cross-currency settlement: what came off the debit in its currency and
	// the realized exchange difference in the functional currency (positive a gain).
	ItemCurrency string           `json:"itemCurrency,omitempty"`
	ItemAmount   *decimal.Decimal `json:"itemAmount,omitempty"`
	RealizedFX   *decimal.Decimal `json:"realizedFx,omitempty"`
}

// CreditSettlementResult is the credit item after the settlement and the debits it reduced.
type CreditSettlementResult struct {
	BatchID         string                 `json:"batchId"`
	CreditItemID    string                 `json:"creditItemId"`
	CreditOpenAfter decimal.Decimal        `json:"creditOpenAfter"`
	CreditStatus    string                 `json:"creditStatus"`
	Total           decimal.Decimal        `json:"total"`
	Settlements     []CreditSettlementLine `json:"settlements"`
}

func creditSettleHash(req CreditSettlementRequest) string {
	text := fmt.Sprintf("%s|%s|%t", req.LegalEntityID, req.CreditItemID, req.AgainstOriginal)
	for _, d := range req.Debits {
		text += "|" + d.OpenItemID + "=" + d.Amount.String() + "/" + d.ItemAmount.String()
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func settleInvalid(detail string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCreditSettleInvalid, detail)
}

// SettleCredit offsets a credit item against debit items in one transaction (FR-30.1, 30-3). Every item is locked (NOWAIT,
// BR-30.7) and must belong to one customer, legal entity and currency; each debit is reduced by its amount, the credit by
// their total, which may not exceed what the credit holds open. An item reaching zero is SETTLED. The ledger is not
// touched: the credit and the debits are both in 1200. A repeat with the same Idempotency-Key and body replays the result.
func (s *Postgres) SettleCredit(ctx context.Context, req CreditSettlementRequest, idempotencyKey string) (CreditSettlementResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out CreditSettlementResult
	book, err := s.prefetchCreditRates(ctx, req)
	if err != nil {
		return out, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		hash := creditSettleHash(req)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		credit, err := lockAnyOpenItem(ctx, tx, req.CreditItemID)
		if err != nil {
			return err
		}
		if credit.LegalEntityID != req.LegalEntityID {
			return httpx.NewProblem(http.StatusNotFound, CodeOpenItemNotFound, "open item not found")
		}
		if !credit.Amount.IsNegative() || !credit.OpenAmount.IsNegative() {
			return httpx.NewProblem(http.StatusConflict, CodeNotCredit, "open item "+credit.DocumentNumber+" is not an open credit")
		}
		room := credit.OpenAmount.Neg()
		debits := req.Debits
		if req.AgainstOriginal {
			if debits, err = originalDebits(ctx, tx, p.TenantID, credit, room); err != nil {
				return err
			}
		}
		if len(debits) == 0 {
			return settleInvalid("no debit item to settle against")
		}
		total := decimal.Zero
		seen := map[string]bool{}
		out = CreditSettlementResult{BatchID: ids.New(), CreditItemID: credit.ID, Settlements: []CreditSettlementLine{}}
		type move struct {
			item   OpenItem
			amount decimal.Decimal
			// itemAmount is what comes off the debit (its currency); price is set for a cross-currency move.
			itemAmount decimal.Decimal
			price      *settlementPricing
		}
		var fxSet *FXSetting
		var moves []move
		for _, d := range debits {
			if !ids.Valid(d.OpenItemID) || !d.Amount.IsPositive() {
				return settleInvalid("every debit needs an open item and an amount greater than 0")
			}
			if seen[d.OpenItemID] {
				return settleInvalid("open item " + d.OpenItemID + " appears twice")
			}
			seen[d.OpenItemID] = true
			item, err := lockAnyOpenItem(ctx, tx, d.OpenItemID)
			if err != nil {
				return err
			}
			switch {
			case item.LegalEntityID != credit.LegalEntityID || item.CustomerID != credit.CustomerID:
				return settleInvalid("open item " + item.DocumentNumber + " belongs to another customer or legal entity")
			case !item.Amount.IsPositive() || !item.OpenAmount.IsPositive():
				return httpx.NewProblem(http.StatusConflict, CodeNotDebit, "open item "+item.DocumentNumber+" is not an open debit")
			}
			m := move{item: item, amount: d.Amount, itemAmount: d.Amount}
			if item.Currency != credit.Currency {
				if fxSet == nil {
					set, err := loadFXSetting(ctx, tx, credit.LegalEntityID)
					if err != nil {
						return err
					}
					fxSet = &set
				}
				if !fxSet.CrossCurrencyEnabled {
					return settleInvalid("open item " + item.DocumentNumber + " is in " + item.Currency + ", the credit in " + credit.Currency + "; cross-currency settlement is not enabled")
				}
				price, itemAmount, err := s.priceCreditSettlement(ctx, book, *fxSet, credit, item, d)
				if err != nil {
					return err
				}
				m.itemAmount, m.price = itemAmount, &price
			} else if d.ItemAmount.IsPositive() {
				return settleInvalid("open item " + item.DocumentNumber + " is in the credit's currency; itemAmount is only for a debit in another currency")
			}
			if m.itemAmount.GreaterThan(item.OpenAmount) {
				return settleInvalid(fmt.Sprintf("%s is more than open item %s has open (%s)", m.itemAmount, item.DocumentNumber, item.OpenAmount))
			}
			total = total.Add(d.Amount)
			moves = append(moves, m)
		}
		if total.GreaterThan(room) {
			return settleInvalid(fmt.Sprintf("the debits total %s, more than credit %s holds open (%s)", total, credit.DocumentNumber, room))
		}
		var diffs []decimal.Decimal
		for _, m := range moves {
			newOpen := m.item.OpenAmount.Sub(m.itemAmount)
			status := domain.ApplyStatus(newOpen)
			if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, m.item.ID, newOpen.String(), status); err != nil {
				return err
			}
			line := CreditSettlementLine{OpenItemID: m.item.ID, DocumentNumber: m.item.DocumentNumber, Amount: m.amount, OpenAfter: newOpen, ItemStatus: status}
			if m.price == nil {
				if _, err := tx.Exec(ctx, `insert into credit_settlement (tenant_id, id, legal_entity_id, batch_id, customer_id, currency, credit_item_id,
					debit_item_id, amount, settled_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10)`,
					p.TenantID, ids.New(), credit.LegalEntityID, out.BatchID, credit.CustomerID, credit.Currency, credit.ID, m.item.ID,
					m.amount.String(), p.UserID); err != nil {
					return err
				}
			} else {
				if _, err := tx.Exec(ctx, `insert into credit_settlement (tenant_id, id, legal_entity_id, batch_id, customer_id, currency, credit_item_id,
					debit_item_id, amount, settled_by, debit_currency, debit_amount, credit_rate, debit_rate, fx_gain_loss)
					values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, $11, $12::numeric, $13::numeric, $14::numeric, $15::numeric)`,
					p.TenantID, ids.New(), credit.LegalEntityID, out.BatchID, credit.CustomerID, credit.Currency, credit.ID, m.item.ID,
					m.amount.String(), p.UserID, m.item.Currency, m.itemAmount.String(), m.price.paymentRate.String(), m.price.itemRate.String(),
					m.price.fx.Difference.String()); err != nil {
					return err
				}
				ia, fx := m.itemAmount, m.price.fx.Difference
				line.ItemCurrency, line.ItemAmount, line.RealizedFX = m.item.Currency, &ia, &fx
				diffs = append(diffs, fx)
			}
			out.Settlements = append(out.Settlements, line)
		}
		if len(diffs) > 0 {
			if err := fxJournal(ctx, tx, p.TenantID, *fxSet, credit.LegalEntityID, credit.ID, "credit-fx:"+out.BatchID, "CS-"+out.BatchID, diffs); err != nil {
				return err
			}
		}
		creditOpen := credit.OpenAmount.Add(total)
		out.Total, out.CreditOpenAfter, out.CreditStatus = total, creditOpen, domain.ApplyStatus(creditOpen)
		if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, credit.ID, creditOpen.String(), out.CreditStatus); err != nil {
			return err
		}
		event := rcvevents.CreditSettledData{BatchID: out.BatchID, LegalEntityID: credit.LegalEntityID, CustomerID: credit.CustomerID,
			Currency: credit.Currency, CreditItemID: credit.ID, Total: total, Debits: make([]rcvevents.CreditSettledDebit, 0, len(moves))}
		for _, m := range moves {
			cd := rcvevents.CreditSettledDebit{OpenItemID: m.item.ID, Amount: m.amount}
			if m.price != nil {
				cd.ItemCurrency, cd.ItemAmount = m.item.Currency, m.itemAmount
			}
			event.Debits = append(event.Debits, cd)
		}
		if err := enqueue(ctx, tx, "creditsettlement", out.BatchID, rcvevents.CreditSettled, p.TenantID, event); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, mapWriteErr(err)
}

// originalDebits proposes the settlements of a credit note against the open items of the invoice it credits: the items
// whose billing document is the note's original, oldest due date and installment first, each up to what it has open, until
// the credit is used up. A credit with no original document is refused.
func originalDebits(ctx context.Context, tx pgx.Tx, tenantID string, credit OpenItem, room decimal.Decimal) ([]CreditDebit, error) {
	var original *string
	if err := tx.QueryRow(ctx, `select original_doc_id::text from open_item where tenant_id = $1 and id = $2`, tenantID, credit.ID).Scan(&original); err != nil {
		return nil, err
	}
	if original == nil {
		return nil, settleInvalid("credit " + credit.DocumentNumber + " does not credit a billing invoice; name the debit items to settle")
	}
	rows, err := tx.Query(ctx, `select id::text, open_amount::text from open_item
		where tenant_id = $1 and source_doc_id = $2 and source_type = 'INVOICE' and customer_id = $3 and currency = $4 and open_amount > 0
		order by due_date, installment_no, id`, tenantID, *original, credit.CustomerID, credit.Currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CreditDebit
	left := room
	for rows.Next() && left.IsPositive() {
		var id, open string
		if err := rows.Scan(&id, &open); err != nil {
			return nil, err
		}
		take := decimal.Min(left, decimal.RequireFromString(open))
		out = append(out, CreditDebit{OpenItemID: id, Amount: take})
		left = left.Sub(take)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// prefetchCreditRates reads, before the settlement takes its locks, the rates a cross-currency credit settlement needs: the credit's and
// each foreign debit's currency into the functional currency at their document dates, and the credit's currency into the debit's on the
// settlement date. Items it cannot read are left for the transaction to refuse.
func (s *Postgres) prefetchCreditRates(ctx context.Context, req CreditSettlementRequest) (rateBook, error) {
	book := rateBook{}
	type ref struct{ currency, docDate string }
	var credit ref
	var set FXSetting
	debits := map[string]ref{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select currency::text, document_date::text from open_item where id = $1`, req.CreditItemID).Scan(&credit.currency, &credit.docDate); err != nil {
			return nil
		}
		var err error
		if set, err = loadFXSetting(ctx, tx, req.LegalEntityID); err != nil {
			return err
		}
		for _, d := range req.Debits {
			var r ref
			if err := tx.QueryRow(ctx, `select currency::text, document_date::text from open_item where id = $1`, d.OpenItemID).Scan(&r.currency, &r.docDate); err == nil {
				debits[d.OpenItemID] = r
			}
		}
		return nil
	})
	if err != nil || credit.currency == "" || !set.CrossCurrencyEnabled {
		return book, err
	}
	on := settlementDay()
	for _, r := range debits {
		if r.currency == credit.currency {
			continue
		}
		for _, q := range []struct {
			from, to string
			date     time.Time
		}{{credit.currency, set.FunctionalCurrency, parseDay(credit.docDate)}, {r.currency, set.FunctionalCurrency, parseDay(r.docDate)}, {credit.currency, r.currency, on}} {
			if _, done := book[rateKey(q.from, q.to, q.date)]; done {
				continue
			}
			rate, err := s.rate(ctx, req.LegalEntityID, q.from, q.to, q.date)
			if err != nil {
				return nil, err
			}
			book[rateKey(q.from, q.to, q.date)] = rate
		}
	}
	return book, nil
}

// settlementDay is the date a credit settlement is priced on: today, UTC.
func settlementDay() time.Time { return parseDay(today()) }

// priceCreditSettlement prices one cross-currency credit settlement: the debit amount (given, or what the credit amount is worth in the
// debit's currency on the settlement date), checked against that worth with the entity's FX tolerance, and the realized difference of
// the credit at its document-date rate against the debit at its document-date rate, in the functional currency.
func (s *Postgres) priceCreditSettlement(ctx context.Context, book rateBook, set FXSetting, credit, item OpenItem, d CreditDebit) (settlementPricing, decimal.Decimal, error) {
	le, on := credit.LegalEntityID, settlementDay()
	cross, err := s.rateFor(ctx, book, le, credit.Currency, item.Currency, on)
	if err != nil {
		return settlementPricing{}, decimal.Zero, err
	}
	mu := money.MinorUnits(item.Currency)
	worth := d.Amount.Mul(cross).Round(mu)
	itemAmount := d.ItemAmount
	if !itemAmount.IsPositive() {
		itemAmount = worth
	}
	switch {
	case !itemAmount.IsPositive():
		return settlementPricing{}, decimal.Zero, settleInvalid("the credit applied to open item " + item.DocumentNumber + " is worth nothing in " + item.Currency + "; give itemAmount")
	case !itemAmount.Equal(itemAmount.Round(mu)):
		return settlementPricing{}, decimal.Zero, settleInvalid("itemAmount for open item " + item.DocumentNumber + " has more decimals than " + item.Currency + " has")
	}
	if gap, tol := itemAmount.Sub(worth).Abs(), set.Tolerance(itemAmount); gap.GreaterThan(tol) {
		return settlementPricing{}, decimal.Zero, httpx.NewProblem(http.StatusUnprocessableEntity, CodeFXTolerance,
			"the credit of "+d.Amount.String()+" "+credit.Currency+" is worth about "+worth.String()+" "+item.Currency+" today, but "+itemAmount.String()+" is taken off the item; the difference is more than the "+tol.String()+" allowed")
	}
	cr, err := s.rateFor(ctx, book, le, credit.Currency, set.FunctionalCurrency, parseDay(credit.DocumentDate))
	if err != nil {
		return settlementPricing{}, decimal.Zero, err
	}
	ir, err := s.rateFor(ctx, book, le, item.Currency, set.FunctionalCurrency, parseDay(item.DocumentDate))
	if err != nil {
		return settlementPricing{}, decimal.Zero, err
	}
	return settlementPricing{itemCurrency: item.Currency, paymentRate: cr, itemRate: ir,
		fx: domain.ComputeRealizedFX(set.FunctionalCurrency, d.Amount, cr, itemAmount, ir)}, itemAmount, nil
}
