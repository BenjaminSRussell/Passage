package store

import (
	"context"
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

// JournalPaymentUnapply is the source_type of the journal that undoes a payment's applications.
const JournalPaymentUnapply = "payment_unapply"

// UnapplyResult is the payment after its applications were reversed and the items they reopened.
type UnapplyResult struct {
	Payment  Payment               `json:"payment"`
	Reversed []UnappliedSettlement `json:"reversed"`
}

// UnappliedSettlement is one reversed settlement: what goes back on the item and the item after it.
type UnappliedSettlement struct {
	SettlementID string          `json:"settlementId"`
	OpenItemID   string          `json:"openItemId"`
	Amount       decimal.Decimal `json:"amount"`
	ItemAmount   decimal.Decimal `json:"itemAmount"`
	OpenAfter    decimal.Decimal `json:"openAfter"`
	ItemStatus   string          `json:"itemStatus"`
}

type liveSettlement struct {
	id, openItemID             string
	amount, discount, writeOff decimal.Decimal
	itemCurrency               *string
	itemAmount, fxGainLoss     decimal.Decimal
	crossCurrency              bool
}

// UnapplyPayment reverses every live application of a payment (#30): each settlement not yet reversed gets a reversal row with its
// amounts negated (settlement.reversal_of_id), each item gets back what the settlement took off it (in the item's currency, so a
// cross-currency settlement restores its item amount), and the payment's cash is unapplied again (RECEIVED). The ledger is owed the
// mirror of the applications, Dr 1200 / Cr 1205, 4100 and 6100 in the payment currency, and of their realized exchange differences
// in the functional currency. An item that was settled by something else since (another payment, a credit) is reopened only by this
// payment's part. payment.unapplied.v1 is published. The cash receipt itself stays; POST :reverse undoes it afterwards.
func (s *Postgres) UnapplyPayment(ctx context.Context, id string, version int, reason string) (UnapplyResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out UnapplyResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pay, err := scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1 for update`, id))
		if err != nil {
			return notFound(err, CodePaymentNotFound, "payment not found")
		}
		if pay.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "payment changed since it was read")
		}
		rows, err := tx.Query(ctx, `select s.id::text, s.open_item_id::text, s.settled_amount::text, s.cash_discount_taken::text, s.write_off_amount::text,
				s.item_currency::text, coalesce(s.item_amount, s.settled_amount + s.cash_discount_taken + s.write_off_amount)::text, s.fx_gain_loss::text,
				s.item_amount is not null
			from settlement s where s.tenant_id = $1 and s.payment_id = $2 and s.reversal_of_id is null
			  and not exists (select 1 from settlement r where r.tenant_id = s.tenant_id and r.reversal_of_id = s.id)
			order by s.settled_at, s.id`, p.TenantID, pay.ID)
		if err != nil {
			return err
		}
		var live []liveSettlement
		for rows.Next() {
			var l liveSettlement
			var amount, discount, off, itemAmount, fx string
			if err := rows.Scan(&l.id, &l.openItemID, &amount, &discount, &off, &l.itemCurrency, &itemAmount, &fx, &l.crossCurrency); err != nil {
				rows.Close()
				return err
			}
			l.amount, l.discount, l.writeOff = decimal.RequireFromString(amount), decimal.RequireFromString(discount), decimal.RequireFromString(off)
			l.itemAmount, l.fxGainLoss = decimal.RequireFromString(itemAmount), decimal.RequireFromString(fx)
			live = append(live, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(live) == 0 {
			return httpx.NewProblem(http.StatusConflict, CodePaymentState, "the payment has no application to reverse (status "+pay.Status+")")
		}
		cash, disc, off := decimal.Zero, decimal.Zero, decimal.Zero
		gain, loss := decimal.Zero, decimal.Zero
		out.Reversed = []UnappliedSettlement{}
		settled := []rcvevents.SettlementData{}
		for _, l := range live {
			item, err := lockAnyOpenItem(ctx, tx, l.openItemID)
			if err != nil {
				return err
			}
			newOpen := item.OpenAmount.Add(l.itemAmount)
			if newOpen.GreaterThan(item.Amount) {
				return httpx.NewProblem(http.StatusConflict, CodePaymentState, fmt.Sprintf("open item %s would be open for more than its amount", item.DocumentNumber))
			}
			status := domain.ApplyStatus(newOpen)
			if newOpen.Equal(item.Amount) {
				status = "OPEN"
			}
			if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, item.ID, newOpen.String(), status); err != nil {
				return err
			}
			var negItem any
			if l.crossCurrency {
				negItem = l.itemAmount.Neg().String()
			}
			if _, err := tx.Exec(ctx, `insert into settlement (tenant_id, id, legal_entity_id, payment_id, open_item_id, settled_amount, cash_discount_taken,
					write_off_amount, item_currency, item_amount, fx_gain_loss, reversal_of_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::numeric, $11, $12)`, p.TenantID, ids.New(), pay.LegalEntityID, pay.ID, item.ID,
				l.amount.Neg().String(), l.discount.Neg().String(), l.writeOff.Neg().String(), l.itemCurrency, negItem, l.fxGainLoss.Neg().String(), l.id); err != nil {
				return err
			}
			cash, disc, off = cash.Add(l.amount), disc.Add(l.discount), off.Add(l.writeOff)
			if l.fxGainLoss.IsPositive() {
				gain = gain.Add(l.fxGainLoss)
			} else {
				loss = loss.Add(l.fxGainLoss.Neg())
			}
			out.Reversed = append(out.Reversed, UnappliedSettlement{SettlementID: l.id, OpenItemID: item.ID, Amount: l.amount, ItemAmount: l.itemAmount,
				OpenAfter: newOpen, ItemStatus: status})
			sd := rcvevents.SettlementData{OpenItemID: item.ID, Amount: l.amount, CashDiscount: l.discount, WriteOff: l.writeOff}
			if l.crossCurrency && l.itemCurrency != nil {
				sd.ItemCurrency, sd.ItemAmount = *l.itemCurrency, l.itemAmount
			}
			settled = append(settled, sd)
		}
		pay, err = scanPayment(tx.QueryRow(ctx, `update payment set unapplied_amount = unapplied_amount + $3::numeric, status = 'RECEIVED', version = version + 1
			where tenant_id = $1 and id = $2 returning `+paymentColumns, p.TenantID, pay.ID, cash.String()))
		if err != nil {
			return err
		}
		out.Payment = pay
		key := fmt.Sprintf("unapply:%s:%d", pay.ID, pay.Version)
		if err := enqueueJournal(ctx, tx, p.TenantID, journalSpec{LegalEntityID: pay.LegalEntityID, SourceType: JournalPaymentUnapply, SourceID: pay.ID,
			SourceKey: key, DocumentID: "PAY-" + pay.ID + "-UNAPPLY", PostingDate: today(), Currency: pay.Currency,
			Lines: []journalLine{
				debit(domain.AccountReceivable, cash.Add(disc).Add(off), "Application reversed"), credit(domain.AccountUnappliedCash, cash, "Payment unapplied"),
				credit(domain.AccountCashDiscount, disc, "Cash discount reversed"), credit(domain.AccountWriteOff, off, "Write-off reversed")}}); err != nil {
			return err
		}
		if gain.IsPositive() || loss.IsPositive() {
			set, err := loadFXSetting(ctx, tx, pay.LegalEntityID)
			if err != nil {
				return err
			}
			var lines []journalLine
			if gain.IsPositive() {
				lines = append(lines, debit(set.GainAccount, gain, "Realized exchange gain reversed"), credit(domain.AccountReceivable, gain, "Realized exchange gain reversed"))
			}
			if loss.IsPositive() {
				lines = append(lines, debit(domain.AccountReceivable, loss, "Realized exchange loss reversed"), credit(set.LossAccount, loss, "Realized exchange loss reversed"))
			}
			if err := enqueueJournal(ctx, tx, p.TenantID, journalSpec{LegalEntityID: pay.LegalEntityID, SourceType: JournalRealizedFX, SourceID: pay.ID,
				SourceKey: "fx-" + key, DocumentID: "PAY-" + pay.ID + "-UNAPPLY", PostingDate: today(), Currency: set.FunctionalCurrency, Lines: lines}); err != nil {
				return err
			}
		}
		return enqueue(ctx, tx, "payment", pay.ID, rcvevents.PaymentUnapplied, p.TenantID, rcvevents.PaymentUnappliedData{
			PaymentID: pay.ID, LegalEntityID: pay.LegalEntityID, CustomerID: pay.CustomerID, Currency: pay.Currency, Settlements: settled, Reason: reason,
		})
	})
	return out, mapWriteErr(err)
}
