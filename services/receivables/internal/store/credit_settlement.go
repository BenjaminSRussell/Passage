package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
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

// CreditDebit is one debit item to reduce and by how much (a positive magnitude).
type CreditDebit struct {
	OpenItemID string          `json:"openItemId"`
	Amount     decimal.Decimal `json:"amount"`
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
		text += "|" + d.OpenItemID + "=" + d.Amount.String()
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
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
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
		}
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
			case item.Currency != credit.Currency:
				return settleInvalid("open item " + item.DocumentNumber + " is in " + item.Currency + ", the credit in " + credit.Currency)
			case !item.Amount.IsPositive() || !item.OpenAmount.IsPositive():
				return httpx.NewProblem(http.StatusConflict, CodeNotDebit, "open item "+item.DocumentNumber+" is not an open debit")
			case d.Amount.GreaterThan(item.OpenAmount):
				return settleInvalid(fmt.Sprintf("%s is more than open item %s has open (%s)", d.Amount, item.DocumentNumber, item.OpenAmount))
			}
			total = total.Add(d.Amount)
			moves = append(moves, move{item, d.Amount})
		}
		if total.GreaterThan(room) {
			return settleInvalid(fmt.Sprintf("the debits total %s, more than credit %s holds open (%s)", total, credit.DocumentNumber, room))
		}
		for _, m := range moves {
			newOpen := m.item.OpenAmount.Sub(m.amount)
			status := domain.ApplyStatus(newOpen)
			if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, m.item.ID, newOpen.String(), status); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into credit_settlement (tenant_id, id, legal_entity_id, batch_id, customer_id, currency, credit_item_id,
				debit_item_id, amount, settled_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10)`,
				p.TenantID, ids.New(), credit.LegalEntityID, out.BatchID, credit.CustomerID, credit.Currency, credit.ID, m.item.ID,
				m.amount.String(), p.UserID); err != nil {
				return err
			}
			out.Settlements = append(out.Settlements, CreditSettlementLine{OpenItemID: m.item.ID, DocumentNumber: m.item.DocumentNumber,
				Amount: m.amount, OpenAfter: newOpen, ItemStatus: status})
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
			event.Debits = append(event.Debits, rcvevents.CreditSettledDebit{OpenItemID: m.item.ID, Amount: m.amount})
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
