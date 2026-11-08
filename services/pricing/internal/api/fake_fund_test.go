package api_test

import (
	"context"
	"net/http"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

type fakeUsage struct {
	fundID, period, source string
	amount                 decimal.Decimal
}

// fundState is the fake's trade fund and promotion data. It applies the same domain
// rules (CheckAttribution, CheckRevision, CheckPromotionFund) the Postgres store calls.
type fundState struct {
	funds   map[string]domain.Fund
	usage   []fakeUsage
	promos  map[string]domain.PromotionPlan
	replays map[string]any // Idempotency-Key -> first response
}

func (f *fakeStore) fundData() *fundState {
	if f.fund.funds == nil {
		f.fund = fundState{funds: map[string]domain.Fund{}, promos: map[string]domain.PromotionPlan{}, replays: map[string]any{}}
	}
	return &f.fund
}

func notFoundFund(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeFundNotFound, "no trade fund "+id)
}

func notFoundPromo(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodePromoNotFound, "no promotion plan "+id)
}

func (f *fakeStore) CreateFund(_ context.Context, fund domain.Fund, key string) (domain.Fund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fundData()
	if prev, ok := st.replays["fund:"+key].(domain.Fund); ok && key != "" {
		return prev, nil
	}
	fund.ID, fund.Version = f.nextID("0009"), 1
	st.funds[fund.ID] = fund
	if key != "" {
		st.replays["fund:"+key] = fund
	}
	return fund, nil
}

func (f *fakeStore) GetFund(_ context.Context, id string) (domain.Fund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fund, ok := f.fundData().funds[id]
	if !ok {
		return domain.Fund{}, notFoundFund(id)
	}
	return fund, nil
}

func (f *fakeStore) ListFunds(_ context.Context, le string, _ int) ([]domain.Fund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Fund{}
	for _, fund := range f.fundData().funds {
		if le == "" || fund.LegalEntityID == le {
			out = append(out, fund)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (st *fundState) used(fundID, period string) decimal.Decimal {
	used := decimal.Zero
	for _, u := range st.usage {
		if u.fundID == fundID && u.period == period {
			used = used.Add(u.amount)
		}
	}
	return used
}

func (f *fakeStore) AttributeFund(_ context.Context, a store.FundAttribution, key string) (store.FundPosition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fundData()
	fund, ok := st.funds[a.FundID]
	if !ok {
		return store.FundPosition{}, notFoundFund(a.FundID)
	}
	if prev, ok := st.replays["use:"+key].(store.FundPosition); ok && key != "" {
		return prev, nil
	}
	used := st.used(a.FundID, a.Period)
	if err := domain.CheckAttribution(fund, used, a.Amount); err != nil {
		return store.FundPosition{}, store.RuleProblem(err)
	}
	st.usage = append(st.usage, fakeUsage{fundID: a.FundID, period: a.Period, source: a.Source, amount: a.Amount})
	pos := store.FundPosition{Fund: fund, Period: a.Period, Used: used.Add(a.Amount)}
	if key != "" {
		st.replays["use:"+key] = pos
	}
	return pos, nil
}

func versionConflict() error {
	return httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "fund version does not match")
}

func (f *fakeStore) ReviseFund(_ context.Context, rev store.FundRevision, _ string) (domain.Fund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fundData()
	fund, ok := st.funds[rev.ID]
	if !ok {
		return domain.Fund{}, notFoundFund(rev.ID)
	}
	if fund.Version != rev.Version {
		return domain.Fund{}, versionConflict()
	}
	peak := decimal.Zero
	for _, u := range st.usage {
		if u.fundID == rev.ID {
			if p := st.used(rev.ID, u.period); p.GreaterThan(peak) {
				peak = p
			}
		}
	}
	if err := domain.CheckRevision(fund, rev.BudgetPerPeriod, rev.AllowOverspend, peak); err != nil {
		return domain.Fund{}, store.RuleProblem(err)
	}
	fund.BudgetPerPeriod, fund.AllowOverspend, fund.Version = rev.BudgetPerPeriod, rev.AllowOverspend, fund.Version+1
	st.funds[rev.ID] = fund
	return fund, nil
}

func (f *fakeStore) setClosed(id string, version int, closed bool) (domain.Fund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fundData()
	fund, ok := st.funds[id]
	if !ok {
		return domain.Fund{}, notFoundFund(id)
	}
	if fund.Version != version {
		return domain.Fund{}, versionConflict()
	}
	if fund.Closed == closed {
		return domain.Fund{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "fund state unchanged")
	}
	fund.Closed, fund.Version = closed, fund.Version+1
	st.funds[id] = fund
	return fund, nil
}

func (f *fakeStore) CloseFund(_ context.Context, id string, version int, _ string) (domain.Fund, error) {
	return f.setClosed(id, version, true)
}

func (f *fakeStore) ReopenFund(_ context.Context, id string, version int, _ string) (domain.Fund, error) {
	return f.setClosed(id, version, false)
}

func (f *fakeStore) FundUsage(_ context.Context, id string) (domain.Fund, []store.FundUsageLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fundData()
	fund, ok := st.funds[id]
	if !ok {
		return domain.Fund{}, nil, notFoundFund(id)
	}
	sums := map[string]store.FundUsageLine{}
	keys := []string{}
	for _, u := range st.usage {
		if u.fundID != id {
			continue
		}
		k := u.period + "|" + u.source
		l, seen := sums[k]
		if !seen {
			l = store.FundUsageLine{Period: u.period, Source: u.source, Amount: decimal.Zero}
			keys = append(keys, k)
		}
		l.Amount = l.Amount.Add(u.amount)
		sums[k] = l
	}
	sort.Strings(keys)
	out := []store.FundUsageLine{}
	for _, k := range keys {
		out = append(out, sums[k])
	}
	return fund, out, nil
}

func (f *fakeStore) CreatePromotion(_ context.Context, p domain.PromotionPlan, key string) (domain.PromotionPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fundData()
	if prev, ok := st.replays["promo:"+key].(domain.PromotionPlan); ok && key != "" {
		return prev, nil
	}
	if p.FundID != "" {
		fund, ok := st.funds[p.FundID]
		if !ok {
			return domain.PromotionPlan{}, notFoundFund(p.FundID)
		}
		fields, err := domain.CheckPromotionFund(fund, p)
		if len(fields) > 0 {
			prob := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "promotion cannot draw on this fund")
			prob.Errors = fields
			return domain.PromotionPlan{}, prob
		}
		if err != nil {
			return domain.PromotionPlan{}, store.RuleProblem(err)
		}
	}
	if p.CampaignID != "" {
		c, ok := f.campaignData().campaigns[p.CampaignID]
		if !ok {
			return domain.PromotionPlan{}, httpx.NewProblem(http.StatusNotFound, domain.CodeCampaignNotFound, "no campaign "+p.CampaignID)
		}
		fields, err := domain.CheckPromotionCampaign(c, p)
		if len(fields) > 0 {
			prob := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "promotion cannot belong to this campaign")
			prob.Errors = fields
			return domain.PromotionPlan{}, prob
		}
		if err != nil {
			return domain.PromotionPlan{}, store.RuleProblem(err)
		}
	}
	p.ID = f.nextID("0010")
	st.promos[p.ID] = p
	if key != "" {
		st.replays["promo:"+key] = p
	}
	return p, nil
}

func (f *fakeStore) GetPromotion(_ context.Context, id string) (domain.PromotionPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.fundData().promos[id]
	if !ok {
		return domain.PromotionPlan{}, notFoundPromo(id)
	}
	return p, nil
}

func (f *fakeStore) ListPromotions(_ context.Context, le, campaign string, _ int) ([]domain.PromotionPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.PromotionPlan{}
	for _, p := range f.fundData().promos {
		if (le == "" || p.LegalEntityID == le) && (campaign == "" || p.CampaignID == campaign) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) RecordPromotionActuals(_ context.Context, id string, a domain.PromotionActuals) (domain.PromotionPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fundData()
	p, ok := st.promos[id]
	if !ok {
		return domain.PromotionPlan{}, notFoundPromo(id)
	}
	p.Actuals = &a
	st.promos[id] = p
	return p, nil
}
