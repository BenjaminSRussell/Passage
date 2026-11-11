// Package ledgerrecon reconciles intercompany balances from the ledgers (FR-40.6). For each pair of the stored accounting matrix it
// reads the due-from balance of the first entity and the due-to balance of the second from their own ledgers, up to the end of the
// period and for the period after it, converts both to one currency at the rate tables' closing rate and classifies the pair. The
// same code runs behind the API and the scheduled job. It also books the FX difference of an item to the entity's FX-difference
// account.
package ledgerrecon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/fxclient"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/posting"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
)

// DefaultRateType is the rate type of the fx rate tables both sides of a pair are converted at.
const DefaultRateType = "closing"

// Registry is what the reconciler reads of the stored setup.
type Registry interface {
	ListEntities(ctx context.Context) ([]domain.EntityRecord, error)
	GetMatrix(ctx context.Context) (domain.Matrix, error)
	GetSettings(ctx context.Context) (domain.Settings, error)
}

// Runs stores a classified run and its items.
type Runs interface {
	CreateReconRun(ctx context.Context, run domain.ReconRun, items []domain.ReconItem, key, hash string) (domain.ReconRun, bool, error)
}

// Adjustments stores the FX adjustment of an item.
type Adjustments interface {
	GetReconItem(ctx context.Context, itemID string) (domain.ReconItem, domain.ReconRun, error)
	GetFXAdjustment(ctx context.Context, itemID string) (domain.FXAdjustment, bool, error)
	BeginFXAdjustment(ctx context.Context, a domain.FXAdjustment) (domain.FXAdjustment, bool, error)
	FinishFXAdjustment(ctx context.Context, id, ledgerEntryID string, postedOn time.Time, note string) (domain.FXAdjustment, error)
}

// Reconciler reads pair amounts from the ledgers.
type Reconciler struct {
	reg      Registry
	runs     Runs
	adj      Adjustments
	ledger   ledgerclient.Ledger
	poster   *posting.Poster
	rates    fxclient.Rater
	rateType string
}

// New returns a reconciler. rateType is the fx rate type both sides are converted at; empty means DefaultRateType. poster may be nil
// when FX adjustments are not offered.
func New(reg Registry, runs Runs, adj Adjustments, l ledgerclient.Ledger, poster *posting.Poster, rates fxclient.Rater, rateType string) *Reconciler {
	if rateType == "" {
		rateType = DefaultRateType
	}
	return &Reconciler{reg: reg, runs: runs, adj: adj, ledger: l, poster: poster, rates: rates, rateType: rateType}
}

func unavailable(what string, err error) error {
	return &domain.Error{Code: domain.CodeLedgerUnavailable, Detail: fmt.Sprintf("%s could not be read: %v", what, err)}
}

func monthEnd(period string) (time.Time, error) {
	first, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, fmt.Errorf("period %q is not YYYY-MM", period)
	}
	return first.AddDate(0, 1, -1), nil
}

func (r *Reconciler) convert(ctx context.Context, legalEntityID string, amount decimal.Decimal, from, to string, on time.Time) (decimal.Decimal, error) {
	if from == to {
		return amount.Round(int32(money.MinorUnits(to))), nil
	}
	if r.rates == nil {
		return decimal.Zero, &domain.Error{Code: domain.CodeNotConfigured, Detail: "the rate tables are not configured, so pairs in different currencies cannot be compared"}
	}
	rate, err := r.rates.Rate(ctx, legalEntityID, r.rateType, from, to, on)
	if errors.Is(err, fxclient.ErrRateMissing) {
		return decimal.Zero, &domain.Error{Code: domain.CodeRateUnavailable, Detail: fmt.Sprintf("the rate tables have no %s rate from %s to %s on %s", r.rateType, from, to, on.Format(time.DateOnly))}
	}
	if err != nil {
		return decimal.Zero, unavailable("the "+r.rateType+" rate from "+from+" to "+to, err)
	}
	return amount.Mul(rate).Round(int32(money.MinorUnits(to))), nil
}

// side reads one account of one entity: the net balance up to the period and the movement in the next period, each positive on the
// side the account normally holds (debit for due-from, credit for due-to), in the ledger's currency.
func (r *Reconciler) side(ctx context.Context, e domain.EntityRecord, account, period, next string, debitSide bool) (closing, nextMove decimal.Decimal, currency string, err error) {
	sign := func(b ledgerclient.Balance) decimal.Decimal {
		if debitSide {
			return b.Net()
		}
		return b.Net().Neg()
	}
	bal, err := r.ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: e.LegalEntityID, LedgerID: e.LedgerID, Account: account, PeriodTo: period})
	if err != nil {
		return decimal.Zero, decimal.Zero, "", unavailable("the ledger of "+e.Entity, err)
	}
	mv, err := r.ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: e.LegalEntityID, LedgerID: e.LedgerID, Account: account, PeriodFrom: next, PeriodTo: next})
	if err != nil {
		return decimal.Zero, decimal.Zero, "", unavailable("the ledger of "+e.Entity, err)
	}
	cur := bal.Currency
	if cur == "" {
		cur = e.Currency
	}
	if cur != e.Currency {
		return decimal.Zero, decimal.Zero, "", &domain.Error{Code: domain.CodeLedgerMismatch, Detail: fmt.Sprintf("entity %s is registered in %s but its ledger is in %s", e.Entity, e.Currency, cur)}
	}
	return sign(bal), sign(mv), cur, nil
}

// Items reads the pair amounts of every pair of the stored matrix for the period, converted to currency.
func (r *Reconciler) Items(ctx context.Context, period, currency string) ([]domain.ReconItem, error) {
	end, err := monthEnd(period)
	if err != nil {
		return nil, &domain.Error{Code: domain.CodeInvalid, Detail: err.Error()}
	}
	next := end.AddDate(0, 0, 1).Format("2006-01")
	entities, err := r.reg.ListEntities(ctx)
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]domain.EntityRecord, len(entities))
	for _, e := range entities {
		byCode[e.Entity] = e
	}
	matrix, err := r.reg.GetMatrix(ctx)
	if err != nil {
		return nil, err
	}
	if len(matrix) == 0 {
		return nil, &domain.Error{Code: domain.CodeNotConfigured, Detail: "the intercompany accounting matrix is empty; store it first"}
	}
	items := make([]domain.ReconItem, 0, len(matrix))
	for _, row := range matrix {
		a, b := byCode[row.EntityA], byCode[row.EntityB]
		if a.Entity == "" || b.Entity == "" {
			return nil, &domain.Error{Code: domain.CodeEntityUnknown, Detail: fmt.Sprintf("matrix pair %s to %s names an entity that is not in the registry", row.EntityA, row.EntityB)}
		}
		aClose, aNext, aCur, err := r.side(ctx, a, row.DueFrom, period, next, true)
		if err != nil {
			return nil, err
		}
		bClose, bNext, bCur, err := r.side(ctx, b, row.DueTo, period, next, false)
		if err != nil {
			return nil, err
		}
		it := domain.ReconItem{Entity: a.Entity, Counterparty: b.Entity, Ref: a.Entity + ">" + b.Entity, FXApplies: aCur != bCur}
		for _, c := range []struct {
			dst    *decimal.Decimal
			amount decimal.Decimal
			e      domain.EntityRecord
			cur    string
		}{{&it.AmountA, aClose, a, aCur}, {&it.NextPeriodA, aNext, a, aCur}, {&it.AmountB, bClose, b, bCur}, {&it.NextPeriodB, bNext, b, bCur}} {
			if *c.dst, err = r.convert(ctx, c.e.LegalEntityID, c.amount, c.cur, currency, end); err != nil {
				return nil, err
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// Run reads the pairs, classifies them and stores the run under key. A retry with the key replays the stored run.
func (r *Reconciler) Run(ctx context.Context, period, currency string, tolerance decimal.Decimal, key string) (domain.ReconRun, bool, error) {
	items, err := r.Items(ctx, period, currency)
	if err != nil {
		return domain.ReconRun{}, false, err
	}
	run, classified := domain.ClassifyRun(domain.ReconRun{Period: period, Currency: currency, FXTolerancePct: tolerance, Source: "ledger"}, items)
	hash, err := store.HashOf("recon-ledger", period, currency, tolerance.String())
	if err != nil {
		return domain.ReconRun{}, false, err
	}
	return r.runs.CreateReconRun(ctx, run, classified, key, hash)
}

// AdjustFX books the FX difference of an open item: the entity that holds the due-from side moves its balance to the other side's
// converted amount against its FX-difference account, in its own currency, on the last day of the period (or in the first open period
// after it). The item is resolved with a note naming the entry. A second call for the same item returns the stored adjustment.
func (r *Reconciler) AdjustFX(ctx context.Context, itemID string) (domain.FXAdjustment, error) {
	if r.poster == nil || r.adj == nil {
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeNotConfigured, Detail: "posting is not configured: the ledger is not set for this service"}
	}
	it, run, err := r.adj.GetReconItem(ctx, itemID)
	if err != nil {
		return domain.FXAdjustment{}, err
	}
	switch {
	case it.Resolution == domain.ResolutionResolved:
		if stored, found, gerr := r.adj.GetFXAdjustment(ctx, itemID); gerr == nil && found && stored.Status == "posted" {
			return stored, nil
		}
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeReconNotOpen, Detail: "the item is resolved and has nothing left to adjust"}
	case it.Resolution != domain.ResolutionOpen:
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeReconNotOpen, Detail: "the item matched and has nothing to adjust"}
	case it.Status == domain.ReconTiming:
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeNothingToAdjust, Detail: "the pair is a timing item: the other side's booking in the next period closes it, so there is nothing to adjust"}
	case !it.FXApplies || it.Difference.IsZero():
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeNothingToAdjust, Detail: "only a pair in two currencies with a difference has an FX difference to book"}
	}
	settings, err := r.reg.GetSettings(ctx)
	if err != nil {
		return domain.FXAdjustment{}, err
	}
	larger := decimal.Max(it.AmountA.Abs(), it.AmountB.Abs())
	limit := larger.Mul(settings.FXAdjustMaxPct).Div(decimal.NewFromInt(100))
	if it.Difference.Abs().GreaterThan(limit) {
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeBeyondFX, Detail: fmt.Sprintf("the difference of %s %s is more than %s percent of the larger side, which is the most an exchange difference can explain here; find the missing or wrong entry and post a correcting entry instead",
			it.Difference.Abs().StringFixed(int32(money.MinorUnits(run.Currency))), run.Currency, settings.FXAdjustMaxPct.String())}
	}
	entities, err := r.reg.ListEntities(ctx)
	if err != nil {
		return domain.FXAdjustment{}, err
	}
	var a domain.EntityRecord
	for _, e := range entities {
		if e.Entity == it.Entity {
			a = e
		}
	}
	if a.Entity == "" {
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeEntityUnknown, Detail: "entity " + it.Entity + " is not in the registry"}
	}
	if a.FXDifferenceAccount == "" {
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeNoFXAccount, Detail: "entity " + a.Entity + " has no FX-difference account; set it in the registry"}
	}
	matrix, err := r.reg.GetMatrix(ctx)
	if err != nil {
		return domain.FXAdjustment{}, err
	}
	row, err := matrix.Lookup(it.Entity, it.Counterparty)
	if err != nil {
		return domain.FXAdjustment{}, err
	}
	end, err := monthEnd(run.Period)
	if err != nil {
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeInvalid, Detail: err.Error()}
	}
	// The difference is A minus B in the run currency; the entity books it in its own currency.
	own, err := r.convert(ctx, a.LegalEntityID, it.Difference.Abs(), run.Currency, a.Currency, end)
	if err != nil {
		return domain.FXAdjustment{}, err
	}
	if !own.IsPositive() {
		return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeNothingToAdjust, Detail: "the difference rounds to nothing in " + a.Currency}
	}
	adj, _, err := r.adj.BeginFXAdjustment(ctx, domain.FXAdjustment{ItemID: itemID, Entity: a.Entity, Target: domain.Target{LegalEntityID: a.LegalEntityID, LedgerID: a.LedgerID},
		Currency: a.Currency, Amount: own, IncreasesBalance: it.Difference.IsNegative(), BalanceAccount: row.DueFrom, FXAccount: a.FXDifferenceAccount, PeriodEnd: end})
	if err != nil {
		return domain.FXAdjustment{}, err
	}
	if adj.Status == "posted" {
		return adj, nil
	}
	build := func(date time.Time) ledgerclient.Entry {
		text := fmt.Sprintf("Intercompany FX difference %s to %s, %s", adj.Entity, it.Counterparty, run.Period)
		if !date.Equal(end) {
			text += fmt.Sprintf(" (dated %s, posted %s because that period is closed)", end.Format(time.DateOnly), date.Format(time.DateOnly))
		}
		// An increase debits the due-from account and credits the FX account; a decrease does the opposite.
		return ledgerclient.Entry{LegalEntityID: adj.Target.LegalEntityID, LedgerID: adj.Target.LedgerID, DocumentID: adj.ID, PostingDate: date.Format(time.DateOnly), Currency: adj.Currency,
			IdempotencyKey: "ic-fx:" + adj.ID, SourceType: posting.SourceType, Lines: []ledgerclient.Line{
				{Account: adj.BalanceAccount, Credit: !adj.IncreasesBalance, Amount: adj.Amount, Text: text},
				{Account: adj.FXAccount, Credit: adj.IncreasesBalance, Amount: adj.Amount, Text: text},
			}}
	}
	id, on, err := r.poster.PostShifting(ctx, adj.Target.LegalEntityID, end, build)
	if err != nil {
		if ledgerclient.Rejected(err) {
			return domain.FXAdjustment{}, &domain.Error{Code: domain.CodeSecondSideFailed, Detail: fmt.Sprintf("the ledger of %s refused the FX difference entry (%s); nothing was posted", adj.Entity, ledgerclient.CodeOf(err))}
		}
		return domain.FXAdjustment{}, unavailable("the ledger of "+adj.Entity, err)
	}
	note := fmt.Sprintf("FX difference of %s %s booked to account %s (ledger entry %s, dated %s)", adj.Amount.StringFixed(int32(money.MinorUnits(adj.Currency))), adj.Currency, adj.FXAccount, id, on.Format(time.DateOnly))
	return r.adj.FinishFXAdjustment(ctx, adj.ID, id, on, note)
}
