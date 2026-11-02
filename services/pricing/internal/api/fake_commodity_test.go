package api_test

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// commodityState is the fake's commodity pricing data; it applies the store's rules: unique
// basis code, revisions per day, withdrawn calculations refusing runs, and key replay.
type commodityState struct {
	bases   map[string]domain.CommodityBasis
	values  map[string][]domain.IndexValue
	calcs   map[string]domain.CommodityCalc
	runs    []store.CommodityRun
	replays map[string]store.CommodityRun
}

func (f *fakeStore) commodity() *commodityState {
	if f.comd.bases == nil {
		f.comd = commodityState{bases: map[string]domain.CommodityBasis{}, values: map[string][]domain.IndexValue{},
			calcs: map[string]domain.CommodityCalc{}, replays: map[string]store.CommodityRun{}}
	}
	return &f.comd
}

func (f *fakeStore) CreateCommodityBasis(_ context.Context, in domain.CommodityBasis, _ string) (domain.CommodityBasis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.commodity()
	for _, b := range st.bases {
		if b.LegalEntityID == in.LegalEntityID && b.Code == in.Code {
			return domain.CommodityBasis{}, httpx.NewProblem(http.StatusConflict, domain.CodeBasisCodeTaken, "code taken")
		}
	}
	in.ID, in.Version = f.nextID("0006"), 1
	st.bases[in.ID] = in
	return in, nil
}

func (f *fakeStore) GetCommodityBasis(_ context.Context, id string) (domain.CommodityBasis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.commodity().bases[id]
	if !ok {
		return b, httpx.NewProblem(http.StatusNotFound, domain.CodeBasisNotFound, "no basis "+id)
	}
	return b, nil
}

func (f *fakeStore) ListCommodityBases(_ context.Context, le string, _ int) ([]domain.CommodityBasis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.CommodityBasis{}
	for _, b := range f.commodity().bases {
		if le == "" || b.LegalEntityID == le {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (f *fakeStore) RecordIndexValue(_ context.Context, in domain.IndexValue, _ string) (domain.IndexValue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.commodity()
	if _, ok := st.bases[in.BasisID]; !ok {
		return in, httpx.NewProblem(http.StatusNotFound, domain.CodeBasisNotFound, "no basis "+in.BasisID)
	}
	in.Revision = 1
	for _, v := range st.values[in.BasisID] {
		if v.AsOf.Equal(in.AsOf) && v.Revision >= in.Revision {
			in.Revision = v.Revision + 1
		}
	}
	st.values[in.BasisID] = append(st.values[in.BasisID], in)
	return in, nil
}

func (f *fakeStore) ListIndexValues(_ context.Context, basisID string, _ int) ([]domain.IndexValue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.commodity()
	if _, ok := st.bases[basisID]; !ok {
		return nil, httpx.NewProblem(http.StatusNotFound, domain.CodeBasisNotFound, "no basis "+basisID)
	}
	out := append([]domain.IndexValue{}, st.values[basisID]...)
	sort.Slice(out, func(i, j int) bool {
		if !out[i].AsOf.Equal(out[j].AsOf) {
			return out[i].AsOf.After(out[j].AsOf)
		}
		return out[i].Revision > out[j].Revision
	})
	return out, nil
}

func (f *fakeStore) CreateCommodityCalc(_ context.Context, in domain.CommodityCalc, _ string) (domain.CommodityCalc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.commodity()
	if b, ok := st.bases[in.BasisID]; !ok || b.LegalEntityID != in.LegalEntityID {
		return in, httpx.NewProblem(http.StatusNotFound, domain.CodeBasisNotFound, "no basis "+in.BasisID)
	}
	in.ID, in.Version = f.nextID("0007"), 1
	st.calcs[in.ID] = in
	return in, nil
}

func (f *fakeStore) GetCommodityCalc(_ context.Context, id string) (domain.CommodityCalc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalc(id)
}

func (f *fakeStore) getCalc(id string) (domain.CommodityCalc, error) {
	c, ok := f.commodity().calcs[id]
	if !ok {
		return c, httpx.NewProblem(http.StatusNotFound, domain.CodeCalculationNotFound, "no calculation "+id)
	}
	return c, nil
}

func (f *fakeStore) ListCommodityCalcs(_ context.Context, le, basis string, _ int) ([]domain.CommodityCalc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.CommodityCalc{}
	for _, c := range f.commodity().calcs {
		if (le == "" || c.LegalEntityID == le) && (basis == "" || c.BasisID == basis) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) WithdrawCommodityCalc(_ context.Context, id string, version int, _ string) (domain.CommodityCalc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.getCalc(id)
	if err != nil {
		return c, err
	}
	if c.Version != version {
		return c, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "version")
	}
	if c.Withdrawn {
		return c, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "withdrawn")
	}
	c.Withdrawn, c.Version = true, c.Version+1
	f.commodity().calcs[id] = c
	return c, nil
}

func (f *fakeStore) plan(id string, asOf time.Time) (domain.CommodityCalc, domain.RunPlan, error) {
	c, err := f.getCalc(id)
	if err != nil {
		return c, domain.RunPlan{}, err
	}
	if c.Withdrawn {
		return c, domain.RunPlan{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "calculation is withdrawn")
	}
	var idx *domain.IndexValue
	for _, v := range f.commodity().values[c.BasisID] {
		v := v
		if v.AsOf.After(asOf) {
			continue
		}
		if idx == nil || v.AsOf.After(idx.AsOf) || (v.AsOf.Equal(idx.AsOf) && v.Revision > idx.Revision) {
			idx = &v
		}
	}
	if idx == nil {
		return c, domain.RunPlan{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNoIndexValue, "no index value")
	}
	var recs []domain.Record
	for _, e := range f.records {
		if e.LegalEntityID == c.LegalEntityID {
			recs = append(recs, e.Record)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	plan, perr := domain.PlanRun(c, *idx, asOf, recs)
	if perr != nil {
		return c, plan, httpx.NewProblem(http.StatusUnprocessableEntity, perr.Code, perr.Detail)
	}
	return c, plan, nil
}

func (f *fakeStore) PreviewCommodityRun(_ context.Context, id string, asOf time.Time) (domain.CommodityCalc, domain.RunPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plan(id, asOf)
}

func (f *fakeStore) RunCommodityCalc(_ context.Context, id string, asOf time.Time, key string) (store.CommodityRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.commodity()
	if prev, ok := st.replays[key]; ok && key != "" {
		return prev, nil
	}
	c, plan, err := f.plan(id, asOf)
	if err != nil {
		return store.CommodityRun{}, err
	}
	run := store.CommodityRun{ID: f.nextID("0008"), LegalEntityID: c.LegalEntityID, CalculationID: id, AsOf: asOf, IndexAsOf: plan.Index.AsOf,
		IndexRevision: plan.Index.Revision, IndexValue: plan.Index.Value, Price: plan.Price, ChangedCount: len(plan.Changes),
		UnchangedCount: plan.Unchanged, Stale: plan.Stale, CreatedAt: time.Now()}
	if len(plan.Changes) > 0 {
		j := &store.Journal{Journal: domain.Journal{ID: f.nextID("0003"), LegalEntityID: c.LegalEntityID,
			Name: fmt.Sprintf("Commodity %s", c.Name), Lines: plan.JournalLines()}, Status: domain.JournalValidated, LineCount: len(plan.Changes)}
		f.journals[j.ID] = j
		run.JournalID = j.ID
	}
	st.runs = append([]store.CommodityRun{run}, st.runs...)
	if key != "" {
		st.replays[key] = run
	}
	return run, nil
}

func (f *fakeStore) ListCommodityRuns(_ context.Context, id string, _ int) ([]store.CommodityRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.getCalc(id); err != nil {
		return nil, err
	}
	out := []store.CommodityRun{}
	for _, r := range f.commodity().runs {
		if r.CalculationID == id {
			out = append(out, r)
		}
	}
	return out, nil
}
