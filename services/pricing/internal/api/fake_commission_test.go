package api_test

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// commissionState is the fake's commission and retro data. It applies the same domain
// functions the Postgres store calls.
type commissionState struct {
	groups    map[string]store.SalesGroup
	rules     []store.CommissionRuleRow
	txns      []store.CommissionTxn
	activated map[string]time.Time // agreement id -> activation day (defaults to today)
	lastRetro store.RetroInput
}

func (f *fakeStore) commission() *commissionState {
	if f.comm.groups == nil {
		f.comm.groups = map[string]store.SalesGroup{}
		f.comm.activated = map[string]time.Time{}
	}
	return &f.comm
}

func (f *fakeStore) CreateSalesGroup(_ context.Context, g store.SalesGroup, _ string) (store.SalesGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g.ID = f.nextID("0007")
	f.commission().groups[g.ID] = g
	return g, nil
}

func (f *fakeStore) GetSalesGroup(_ context.Context, id string) (store.SalesGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.commission().groups[id]
	if !ok {
		return g, httpx.NewProblem(http.StatusNotFound, domain.CodeSalesGroupNotFound, "no sales group "+id)
	}
	return g, nil
}

func (f *fakeStore) ListSalesGroups(_ context.Context, le string, _ int) ([]store.SalesGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.SalesGroup{}
	for _, g := range f.commission().groups {
		if le == "" || g.LegalEntityID == le {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) CreateCommissionRule(_ context.Context, r domain.CommissionRule, _ string) (store.CommissionRuleRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.commission()
	g, ok := c.groups[r.SalesGroupID]
	if !ok {
		return store.CommissionRuleRow{}, httpx.NewProblem(http.StatusNotFound, domain.CodeSalesGroupNotFound, "no sales group "+r.SalesGroupID)
	}
	for _, row := range c.rules {
		if row.Rule.SalesGroupID == r.SalesGroupID && row.Rule.CustomerGroup == r.CustomerGroup && row.Rule.ItemGroup == r.ItemGroup {
			return store.CommissionRuleRow{}, httpx.NewProblem(http.StatusConflict, domain.CodeCommissionRuleExists, "rule exists")
		}
	}
	row := store.CommissionRuleRow{ID: f.nextID("0008"), LegalEntityID: g.LegalEntityID, Rule: r}
	c.rules = append(c.rules, row)
	return row, nil
}

func (f *fakeStore) ListCommissionRules(_ context.Context, sg string, _ int) ([]store.CommissionRuleRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.CommissionRuleRow{}
	for _, row := range f.commission().rules {
		if sg == "" || row.Rule.SalesGroupID == sg {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) linesTxns(line string) []store.CommissionTxn {
	var out []store.CommissionTxn
	for _, t := range f.commission().txns {
		if t.InvoiceLineID == line {
			out = append(out, t)
		}
	}
	return out
}

// PostCommission mirrors the store: redelivery, credit against the original, rule resolution and split.
func (f *fakeStore) PostCommission(_ context.Context, p store.CommissionPosting) ([]store.CommissionTxn, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, l := f.commission(), p.Line
	if existing := f.linesTxns(l.InvoiceLineID); len(existing) > 0 {
		if existing[0].SalesGroupID != p.SalesGroupID || !existing[0].LineNet.Equal(l.LineNet) {
			return nil, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyUse, "posted differently")
		}
		return existing, false, nil
	}
	g, ok := c.groups[p.SalesGroupID]
	if !ok {
		return nil, false, httpx.NewProblem(http.StatusNotFound, domain.CodeSalesGroupNotFound, "no sales group "+p.SalesGroupID)
	}
	var basis decimal.Decimal
	var splits []domain.Split
	var e *domain.Error
	kind, rate, shares := "", decimal.Zero, g.Reps
	if l.IsCredit() {
		orig := f.linesTxns(l.ReversesInvoiceLineID)
		if len(orig) == 0 {
			return nil, false, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCommissionCredit, "nothing to credit")
		}
		o := domain.CommissionOriginal{Basis: orig[0].Basis, RatePct: orig[0].RatePct, LineNet: orig[0].LineNet, Currency: orig[0].Currency, Credited: decimal.Zero}
		for _, t := range orig {
			o.Shares = append(o.Shares, domain.Share{RepID: t.RepID, SharePct: t.SharePct})
		}
		seen := map[string]bool{}
		for _, t := range c.txns {
			if t.ReversesInvoiceLineID == l.ReversesInvoiceLineID && !seen[t.InvoiceLineID] {
				seen[t.InvoiceLineID] = true
				o.Credited = o.Credited.Add(t.LineNet)
			}
		}
		basis, splits, e = domain.CreditCommission(o, l)
		kind, rate, shares = o.Basis, o.RatePct, o.Shares
	} else {
		var rules []domain.CommissionRule
		for _, row := range c.rules {
			rules = append(rules, row.Rule)
		}
		rule, ok := domain.ResolveCommissionRule(rules, p.SalesGroupID, l.CustomerGroup, l.ItemGroup)
		if !ok {
			return nil, false, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCommissionRuleMissing, "no rule")
		}
		basis, splits, e = domain.CalculateCommission(rule, g.Reps, l)
		kind, rate = rule.Basis, rule.RatePct
	}
	if e != nil {
		pr := httpx.NewProblem(http.StatusUnprocessableEntity, e.Code, e.Detail)
		pr.Errors = e.Fields
		return nil, false, pr
	}
	var out []store.CommissionTxn
	for i, sp := range splits {
		out = append(out, store.CommissionTxn{ID: f.nextID("0009"), LegalEntityID: p.LegalEntityID, InvoiceLineID: l.InvoiceLineID,
			ReversesInvoiceLineID: l.ReversesInvoiceLineID, InvoiceDate: l.InvoiceDate, PeriodKey: l.PeriodKey(), CustomerID: l.CustomerID,
			SalesGroupID: p.SalesGroupID, RepID: sp.RepID, SharePct: shares[i].SharePct, Basis: kind, LineNet: l.LineNet, BasisAmount: basis,
			RatePct: rate, Amount: sp.Amount, Currency: l.Currency, DebitAccount: "6200", CreditAccount: "2170", Status: "POSTED"})
	}
	c.txns = append(c.txns, out...)
	return out, true, nil
}

func (f *fakeStore) ListCommissionTransactions(_ context.Context, flt store.CommissionFilter) ([]store.CommissionTxn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.CommissionTxn{}
	for _, t := range f.commission().txns {
		if (flt.RepID == "" || t.RepID == flt.RepID) && (flt.PeriodKey == "" || t.PeriodKey == flt.PeriodKey) &&
			(flt.InvoiceLineID == "" || t.InvoiceLineID == flt.InvoiceLineID || t.ReversesInvoiceLineID == flt.InvoiceLineID) {
			out = append(out, t)
		}
	}
	return out, nil
}

// RecomputeRetro mirrors the store: replayed lines join their period, each period posts its delta once.
func (f *fakeStore) RecomputeRetro(_ context.Context, in store.RetroInput) (store.RetroResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.commission()
	c.lastRetro = in
	a, ok := f.agreements[in.AgreementID]
	if !ok {
		return store.RetroResult{}, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+in.AgreementID)
	}
	activated, ok := c.activated[a.ID]
	if !ok {
		activated = time.Now().UTC().Truncate(24 * time.Hour)
	}
	out := store.RetroResult{ActivatedOn: activated}
	prefix := "retro:" + in.RunKey + ":"
	for _, acc := range f.accruals {
		if acc.AgreementID == a.ID && strings.HasPrefix(acc.InvoiceLineID, prefix) {
			out.Replayed = true
			out.Deltas = append(out.Deltas, domain.RetroDelta{Period: acc.PeriodKey, Delta: acc.Accrued})
		}
	}
	if out.Replayed {
		return out, nil
	}
	if !domain.IsRetroactive(a, activated) {
		return store.RetroResult{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "agreement is not retroactive")
	}
	for _, l := range in.Lines {
		if l.Currency != a.Currency || !domain.InRetroScope(a, activated, l.InvoiceDate) {
			return store.RetroResult{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "invoice line "+l.InvoiceLineID+" is out of scope")
		}
	}
	for _, l := range in.Lines {
		dedupe := a.ID + "|" + l.InvoiceLineID
		if _, done := f.accruals[dedupe]; done {
			continue
		}
		key := domain.PeriodKey(a.Period, l.InvoiceDate)
		p := f.periods[a.ID+"|"+key]
		if p == nil {
			p = &fakePeriod{}
			f.periods[a.ID+"|"+key] = p
		}
		p.Value, p.Qty = p.Value.Add(l.Value), p.Qty.Add(l.Quantity)
		f.accruals[dedupe] = store.Accrual{ID: f.nextID("0010"), AgreementID: a.ID, PeriodKey: key, InvoiceLineID: l.InvoiceLineID,
			InvoiceDate: l.InvoiceDate, BasisValue: l.Value, BasisQty: l.Quantity, Currency: a.Currency, Status: "ACCRUED", Retroactive: true}
		out.ReaccruedLines++
	}
	var periods []domain.RetroPeriod
	for pk, p := range f.periods {
		if strings.HasPrefix(pk, a.ID+"|") {
			periods = append(periods, domain.RetroPeriod{Period: strings.TrimPrefix(pk, a.ID+"|"), CumValue: p.Value, CumQty: p.Qty, Posted: p.Accrued})
		}
	}
	out.Deltas = domain.RetroDeltas(a, periods)
	for _, d := range out.Deltas {
		f.periods[a.ID+"|"+d.Period].Accrued = d.Target
		f.accruals[a.ID+"|"+prefix+d.Period] = store.Accrual{ID: f.nextID("0011"), AgreementID: a.ID, PeriodKey: d.Period, InvoiceLineID: prefix + d.Period,
			InvoiceDate: activated, Accrued: d.Delta, Currency: a.Currency, Status: "ACCRUED", Retroactive: true}
	}
	return out, nil
}

// StreamCommissionStatement mirrors the store: the rep's lines of the month in invoice order and the totals per currency.
func (f *fakeStore) StreamCommissionStatement(_ context.Context, flt store.CommissionStatementFilter, start func([]store.CommissionStatementTotal) error, line func(store.CommissionTxn) error) error {
	f.mu.Lock()
	var rows []store.CommissionTxn
	for _, t := range f.commission().txns {
		if t.LegalEntityID == flt.LegalEntityID && t.RepID == flt.RepID && t.PeriodKey == flt.PeriodKey {
			rows = append(rows, t)
		}
	}
	f.mu.Unlock()
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].InvoiceDate.Before(rows[j].InvoiceDate) })
	byCur := map[string]*store.CommissionStatementTotal{}
	var order []string
	for _, t := range rows {
		c, ok := byCur[t.Currency]
		if !ok {
			c = &store.CommissionStatementTotal{Currency: t.Currency}
			byCur[t.Currency] = c
			order = append(order, t.Currency)
		}
		c.Lines++
		if t.Amount.Sign() < 0 {
			c.Credited = c.Credited.Add(t.Amount)
		} else {
			c.Earned = c.Earned.Add(t.Amount)
		}
		c.Net = c.Net.Add(t.Amount)
	}
	sort.Strings(order)
	totals := []store.CommissionStatementTotal{}
	for _, cur := range order {
		totals = append(totals, *byCur[cur])
	}
	if err := start(totals); err != nil {
		return err
	}
	for i, t := range rows {
		if i == 1 && f.streamErr != nil {
			return f.streamErr
		}
		if err := line(t); err != nil {
			return err
		}
	}
	return nil
}
