package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/fxclient"
)

const (
	// CodeFXUnavailable: a cross-currency settlement could not be priced (no rate source, no rate, or the fx service is down).
	CodeFXUnavailable = "AR-FX-UNAVAILABLE"
	// JournalRealizedFX is the source_type of the journal that books the realized exchange difference of a settlement.
	JournalRealizedFX = "realized_fx"
	// JournalReceiptReversal is the source_type of the journal that undoes a payment's cash receipt.
	JournalReceiptReversal = "payment_reversal"
)

// FXSetting is the realized-FX profile of one legal entity: whether it settles across currencies, its functional currency and
// the accounts a gain and a loss are booked to.
type FXSetting struct {
	LegalEntityID        string `json:"legalEntityId"`
	CrossCurrencyEnabled bool   `json:"crossCurrencyEnabled"`
	FunctionalCurrency   string `json:"functionalCurrency"`
	GainAccount          string `json:"gainAccount"`
	LossAccount          string `json:"lossAccount"`
	// ToleranceAmount and TolerancePercent are the entity's FX tolerance (domain.FXTolerance); both empty keep BR-30.4's 5.00 and 0.5%.
	ToleranceAmount  *decimal.Decimal `json:"toleranceAmount,omitempty"`
	TolerancePercent *decimal.Decimal `json:"tolerancePercent,omitempty"`
}

// Tolerance is the entity's FX tolerance for an item amount.
func (f FXSetting) Tolerance(itemAmount decimal.Decimal) decimal.Decimal {
	return domain.FXTolerance(itemAmount, f.ToleranceAmount, f.TolerancePercent)
}

// DefaultFXSetting is what an entity with no stored setting has: no cross-currency settlement, accounts 7100 and 7200.
func DefaultFXSetting(legalEntityID string) FXSetting {
	return FXSetting{LegalEntityID: legalEntityID, GainAccount: domain.AccountFXGain, LossAccount: domain.AccountFXLoss}
}

// ValidateFXSetting checks a setting, filling the default accounts.
func ValidateFXSetting(f *FXSetting) error {
	if f.GainAccount == "" {
		f.GainAccount = domain.AccountFXGain
	}
	if f.LossAccount == "" {
		f.LossAccount = domain.AccountFXLoss
	}
	switch {
	case f.FunctionalCurrency != "" && !currencyRE.MatchString(f.FunctionalCurrency):
		return fxFieldError("functionalCurrency", "must be an ISO 4217 code such as USD")
	case f.CrossCurrencyEnabled && f.FunctionalCurrency == "":
		return fxFieldError("functionalCurrency", "is required to settle across currencies")
	case !glAccountPattern.MatchString(f.GainAccount):
		return fxFieldError("gainAccount", "must be a ledger account number of at most 20 letters, digits, dots, dashes or underscores")
	case !glAccountPattern.MatchString(f.LossAccount):
		return fxFieldError("lossAccount", "must be a ledger account number of at most 20 letters, digits, dots, dashes or underscores")
	case f.ToleranceAmount != nil && (f.ToleranceAmount.IsNegative() || f.ToleranceAmount.Exponent() < -6 || f.ToleranceAmount.GreaterThan(decimal.NewFromInt(1_000_000))):
		return fxFieldError("toleranceAmount", "must be an amount from 0 to 1000000 with at most 6 decimals")
	case f.TolerancePercent != nil && (f.TolerancePercent.IsNegative() || f.TolerancePercent.Exponent() < -4 || f.TolerancePercent.GreaterThan(decimal.NewFromInt(100))):
		return fxFieldError("tolerancePercent", "must be a percentage from 0 to 100 with at most 4 decimals")
	}
	return nil
}

func fxFieldError(field, rule string) error {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "invalid FX setting")
	p.Errors = map[string]string{field: rule}
	return p
}

// WithRates sets where the rates of a cross-currency settlement come from (the fx service). Without it such a settlement is refused.
func (s *Postgres) WithRates(r fxclient.Rates) *Postgres {
	s.rates = r
	return s
}

func loadFXSetting(ctx context.Context, tx pgx.Tx, legalEntityID string) (FXSetting, error) {
	f := DefaultFXSetting(legalEntityID)
	var fc, tolAmount, tolPct *string
	err := tx.QueryRow(ctx, `select cross_currency_enabled, functional_currency::text, gain_account, loss_account, tolerance_amount::text,
		tolerance_percent::text from ar_fx_setting where legal_entity_id = $1`, legalEntityID).
		Scan(&f.CrossCurrencyEnabled, &fc, &f.GainAccount, &f.LossAccount, &tolAmount, &tolPct)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	f.FunctionalCurrency = deref(fc)
	for dst, src := range map[**decimal.Decimal]*string{&f.ToleranceAmount: tolAmount, &f.TolerancePercent: tolPct} {
		if src != nil {
			d, err := decimal.NewFromString(*src)
			if err != nil {
				return f, err
			}
			*dst = &d
		}
	}
	return f, nil
}

// GetFXSetting returns the setting of a legal entity (the default when none is stored).
func (s *Postgres) GetFXSetting(ctx context.Context, legalEntityID string) (FXSetting, error) {
	var out FXSetting
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadFXSetting(ctx, tx, legalEntityID)
		return err
	})
	return out, err
}

// PutFXSetting stores the setting of a legal entity.
func (s *Postgres) PutFXSetting(ctx context.Context, f FXSetting) (FXSetting, error) {
	p, _ := tenant.FromContext(ctx)
	if err := ValidateFXSetting(&f); err != nil {
		return FXSetting{}, err
	}
	var fc *string
	if f.FunctionalCurrency != "" {
		fc = &f.FunctionalCurrency
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var tolAmount, tolPct any
		if f.ToleranceAmount != nil {
			tolAmount = f.ToleranceAmount.String()
		}
		if f.TolerancePercent != nil {
			tolPct = f.TolerancePercent.String()
		}
		_, err := tx.Exec(ctx, `insert into ar_fx_setting (tenant_id, legal_entity_id, cross_currency_enabled, functional_currency, gain_account, loss_account,
			  tolerance_amount, tolerance_percent)
			values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric)
			on conflict (tenant_id, legal_entity_id) do update set cross_currency_enabled = excluded.cross_currency_enabled,
			  functional_currency = excluded.functional_currency, gain_account = excluded.gain_account, loss_account = excluded.loss_account,
			  tolerance_amount = excluded.tolerance_amount, tolerance_percent = excluded.tolerance_percent, updated_at = now()`,
			p.TenantID, f.LegalEntityID, f.CrossCurrencyEnabled, fc, f.GainAccount, f.LossAccount, tolAmount, tolPct)
		return err
	})
	return f, mapWriteErr(err)
}

// settlementPricing is what a cross-currency allocation was priced at.
type settlementPricing struct {
	itemCurrency          string
	paymentRate, itemRate decimal.Decimal
	fx                    domain.RealizedFX
}

// rate reads one rate, treating the functional currency as 1 without a call.
func (s *Postgres) rate(ctx context.Context, legalEntityID, from, to string, date time.Time) (decimal.Decimal, error) {
	if from == to {
		return decimal.NewFromInt(1), nil
	}
	if s.rates == nil {
		return decimal.Decimal{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeFXUnavailable, "the fx service is not configured; a settlement across currencies cannot be priced")
	}
	r, err := s.rates.Rate(ctx, legalEntityID, from, to, date)
	switch {
	case err == nil:
		return r, nil
	case errors.Is(err, fxclient.ErrNoRate):
		return decimal.Decimal{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeFXUnavailable, "there is no "+from+" to "+to+" rate for "+date.Format(dateLayout))
	default:
		return decimal.Decimal{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeFXUnavailable, "the exchange rate could not be read; retry later")
	}
}

// CodeFXTolerance: the item amount of a cross-currency settlement is not what the cash is worth in the item's currency.
const CodeFXTolerance = "AR-FX-TOLERANCE"

// rateBook holds the rates an apply needs, read before any lock is taken.
type rateBook map[string]decimal.Decimal

func rateKey(from, to string, date time.Time) string {
	return from + ">" + to + "@" + date.Format(dateLayout)
}

// rateFor returns a prefetched rate, reading it now when it was not prefetched.
func (s *Postgres) rateFor(ctx context.Context, book rateBook, legalEntityID, from, to string, date time.Time) (decimal.Decimal, error) {
	if r, ok := book[rateKey(from, to, date)]; ok {
		return r, nil
	}
	return s.rate(ctx, legalEntityID, from, to, date)
}

// prefetchRates reads, before the apply transaction takes its locks, the rates a cross-currency apply will need: the payment currency and
// each foreign item's currency into the functional currency, and the payment currency into the item's at the value date. It reads the
// payment and items without locking them; anything it cannot know (a missing item, a disabled entity) is left for the transaction to
// refuse. A rate it cannot read is the apply's answer.
func (s *Postgres) prefetchRates(ctx context.Context, id string, req ApplyRequest) (rateBook, error) {
	book := rateBook{}
	var pay Payment
	var set FXSetting
	type itemRef struct{ currency, docDate string }
	items := map[string]itemRef{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if len(req.Allocations) == 0 {
			return nil
		}
		if pay, err = scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1`, id)); err != nil {
			return nil
		}
		if set, err = loadFXSetting(ctx, tx, pay.LegalEntityID); err != nil {
			return err
		}
		for _, a := range req.Allocations {
			var it itemRef
			if err := tx.QueryRow(ctx, `select currency::text, document_date::text from open_item where id = $1`, a.OpenItemID).Scan(&it.currency, &it.docDate); err == nil {
				items[a.OpenItemID] = it
			}
		}
		return nil
	})
	if err != nil || pay.ID == "" || !set.CrossCurrencyEnabled {
		return book, err
	}
	for _, a := range req.Allocations {
		it, ok := items[a.OpenItemID]
		if !ok || it.currency == pay.Currency {
			continue
		}
		value := parseDay(pay.ValueDate)
		for _, q := range []struct {
			from, to string
			date     time.Time
		}{{pay.Currency, set.FunctionalCurrency, value}, {it.currency, set.FunctionalCurrency, parseDay(it.docDate)}, {pay.Currency, it.currency, value}} {
			if _, done := book[rateKey(q.from, q.to, q.date)]; done {
				continue
			}
			r, err := s.rate(ctx, pay.LegalEntityID, q.from, q.to, q.date)
			if err != nil {
				return nil, err
			}
			book[rateKey(q.from, q.to, q.date)] = r
		}
	}
	return book, nil
}

// priceCrossCurrency prices one cross-currency allocation: the cash at the rate of the payment's value date and the settled part of the
// item at the rate of its document date, both into the functional currency. It first checks the item amount against what the cash is
// worth in the item's currency at the value date: a difference above the entity's FX tolerance (FXSetting.Tolerance; by default BR-30.4's
// larger of 5.00 and 0.5% of the item amount, in the item's currency) is refused, so a small payment cannot close a large invoice into a
// realized loss.
func (s *Postgres) priceCrossCurrency(ctx context.Context, book rateBook, set FXSetting, pay Payment, item OpenItem, a domain.Allocation) (settlementPricing, error) {
	value, le := parseDay(pay.ValueDate), pay.LegalEntityID
	pr, err := s.rateFor(ctx, book, le, pay.Currency, set.FunctionalCurrency, value)
	if err != nil {
		return settlementPricing{}, err
	}
	ir, err := s.rateFor(ctx, book, le, item.Currency, set.FunctionalCurrency, parseDay(item.DocumentDate))
	if err != nil {
		return settlementPricing{}, err
	}
	cross, err := s.rateFor(ctx, book, le, pay.Currency, item.Currency, value)
	if err != nil {
		return settlementPricing{}, err
	}
	worth := a.Amount.Mul(cross).Round(money.MinorUnits(item.Currency))
	if gap, tol := a.ItemCurrencyAmount.Sub(worth).Abs(), set.Tolerance(a.ItemCurrencyAmount); gap.GreaterThan(tol) {
		return settlementPricing{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeFXTolerance,
			"the cash of "+a.Amount.String()+" "+pay.Currency+" is worth about "+worth.String()+" "+item.Currency+" on the value date, but "+a.ItemCurrencyAmount.String()+" is taken off the item; the difference is more than the "+tol.String()+" allowed")
	}
	return settlementPricing{itemCurrency: item.Currency, paymentRate: pr, itemRate: ir,
		fx: domain.ComputeRealizedFX(set.FunctionalCurrency, a.Amount, pr, a.ItemCurrencyAmount, ir)}, nil
}

// realizedFXJournal owes the ledger the realized difference of a payment's cross-currency settlements, as one journal in the
// functional currency: gains Dr 1200 / Cr the gain account, losses Dr the loss account / Cr 1200. A payment whose settlements
// net to nothing owes none.
func realizedFXJournal(ctx context.Context, tx pgx.Tx, tenantID string, set FXSetting, pay Payment, prices []settlementPricing) error {
	diffs := make([]decimal.Decimal, len(prices))
	for i, p := range prices {
		diffs[i] = p.fx.Difference
	}
	return fxJournal(ctx, tx, tenantID, set, pay.LegalEntityID, pay.ID, "fx:"+pay.ID+":"+strconv.Itoa(pay.Version), "PAY-"+pay.ID, diffs)
}

// fxJournal owes the ledger realized exchange differences (positive a gain) as one journal in the functional currency.
func fxJournal(ctx context.Context, tx pgx.Tx, tenantID string, set FXSetting, legalEntityID, sourceID, sourceKey, documentID string, diffs []decimal.Decimal) error {
	gain, loss := decimal.Zero, decimal.Zero
	for _, d := range diffs {
		if d.IsPositive() {
			gain = gain.Add(d)
		} else {
			loss = loss.Add(d.Neg())
		}
	}
	var lines []journalLine
	if gain.IsPositive() {
		lines = append(lines, debit(domain.AccountReceivable, gain, "Realized exchange gain"), credit(set.GainAccount, gain, "Realized exchange gain"))
	}
	if loss.IsPositive() {
		lines = append(lines, debit(set.LossAccount, loss, "Realized exchange loss"), credit(domain.AccountReceivable, loss, "Realized exchange loss"))
	}
	return enqueueJournal(ctx, tx, tenantID, journalSpec{LegalEntityID: legalEntityID, SourceType: JournalRealizedFX, SourceID: sourceID,
		SourceKey: sourceKey, DocumentID: documentID, PostingDate: today(), Currency: set.FunctionalCurrency, Lines: lines})
}

// ReversePayment undoes the cash receipt of a payment that was never applied: the payment becomes REVERSED and the ledger is owed
// the receipt journal's mirror image, built from the lines stored on the original receipt journal, so the credit goes back to the
// bank account the receipt was booked to even if the entity's bank account mapping has changed since. A payment with an
// application is refused: reverse the settlement first. A payment whose receipt was never journalled (it predates the ledger
// postings and was not backfilled) owes nothing.
func (s *Postgres) ReversePayment(ctx context.Context, id string, version int) (Payment, error) {
	p, _ := tenant.FromContext(ctx)
	var out Payment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pay, err := scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1 for update`, id))
		if err != nil {
			return notFound(err, CodePaymentNotFound, "payment not found")
		}
		if pay.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "payment changed since it was read")
		}
		var settlements int
		if err := tx.QueryRow(ctx, `select count(*) from settlement s where s.tenant_id = $1 and s.payment_id = $2 and s.reversal_of_id is null
			and not exists (select 1 from settlement r where r.tenant_id = s.tenant_id and r.reversal_of_id = s.id)`, p.TenantID, pay.ID).Scan(&settlements); err != nil {
			return err
		}
		if !appliable(pay.Status) || settlements > 0 || !pay.UnappliedAmount.Equal(pay.Amount) {
			return httpx.NewProblem(http.StatusConflict, CodePaymentState, "only a payment that was never applied can be reversed (status "+pay.Status+")")
		}
		var confirmed int
		if err := tx.QueryRow(ctx, `select count(*) from payment where tenant_id = $1 and confirmed_by_payment_id = $2`, p.TenantID, pay.ID).Scan(&confirmed); err != nil {
			return err
		}
		if confirmed > 0 {
			return httpx.NewProblem(http.StatusConflict, CodePaymentState, "this payment confirmed a remittance advice; reversing it would leave the advice confirmed by cash that is gone")
		}
		out, err = scanPayment(tx.QueryRow(ctx, `update payment set status = 'REVERSED', unapplied_amount = 0, version = version + 1
			where tenant_id = $1 and id = $2 returning `+paymentColumns, p.TenantID, pay.ID))
		if err != nil {
			return err
		}
		var raw []byte
		err = tx.QueryRow(ctx, `select lines from gl_journal where tenant_id = $1 and legal_entity_id = $2 and source_key = $3`,
			p.TenantID, pay.LegalEntityID, "receipt:"+pay.ID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var lines []journalLine
		if err := json.Unmarshal(raw, &lines); err != nil {
			return err
		}
		for i := range lines {
			lines[i].Credit = !lines[i].Credit
			lines[i].Text = "Reversal: " + lines[i].Text
		}
		return insertJournal(ctx, tx, p.TenantID, journalSpec{LegalEntityID: pay.LegalEntityID, SourceType: JournalReceiptReversal, SourceID: pay.ID,
			SourceKey: "receipt-reversal:" + pay.ID, DocumentID: "PAY-" + pay.ID + "-REV", PostingDate: today(), Currency: pay.Currency}, lines)
	})
	return out, mapWriteErr(err)
}
