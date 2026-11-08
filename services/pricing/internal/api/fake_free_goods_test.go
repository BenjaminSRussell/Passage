package api_test

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// freeGoodsState is the fake's free goods rules; it applies the store's overlap, version
// and state rules and Idempotency-Key replay.
type freeGoodsState struct {
	rules   map[string]domain.FreeGoodsDef
	replays map[string]domain.FreeGoodsDef
}

func (f *fakeStore) freeData() *freeGoodsState {
	if f.free.rules == nil {
		f.free = freeGoodsState{rules: map[string]domain.FreeGoodsDef{}, replays: map[string]domain.FreeGoodsDef{}}
	}
	return &f.free
}

func (f *fakeStore) CreateFreeGoodsRule(_ context.Context, in domain.FreeGoodsDef, key string) (domain.FreeGoodsDef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.freeData()
	if prev, ok := st.replays[key]; ok && key != "" {
		return prev, nil
	}
	var live []domain.FreeGoodsDef
	for _, r := range st.rules {
		live = append(live, r)
	}
	if c, clash := domain.FreeGoodsOverlap(in, live); clash {
		return domain.FreeGoodsDef{}, httpx.NewProblem(http.StatusConflict, domain.CodeOverlap, "overlapping free goods rule "+c.ID)
	}
	f.seq++
	in.ID = "00000000-0000-4000-8000-" + padSeq(f.seq+500)
	in.Version = 1
	st.rules[in.ID] = in
	if key != "" {
		st.replays[key] = in
	}
	return in, nil
}

func padSeq(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 12 {
		s = "0" + s
	}
	return s
}

func (f *fakeStore) GetFreeGoodsRule(_ context.Context, id string) (domain.FreeGoodsDef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.freeData().rules[id]
	if !ok {
		return domain.FreeGoodsDef{}, httpx.NewProblem(http.StatusNotFound, domain.CodeFreeGoodsRuleNotFound, "no free goods rule "+id)
	}
	return r, nil
}

func (f *fakeStore) ListFreeGoodsRules(_ context.Context, le, product string, _ int) ([]domain.FreeGoodsDef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.FreeGoodsDef{}
	for _, r := range f.freeData().rules {
		if (le == "" || r.LegalEntityID == le) && (product == "" || r.ProductID == product) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) ActiveFreeGoodsRules(_ context.Context, le string, products []string, asOf time.Time) ([]domain.FreeGoodsDef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[string]bool{}
	for _, p := range products {
		want[p] = true
	}
	out := []domain.FreeGoodsDef{}
	for _, r := range f.freeData().rules {
		if r.LegalEntityID == le && want[r.ProductID] && !r.Withdrawn && !asOf.Before(r.ValidFrom) && !asOf.After(r.ValidTo) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) WithdrawFreeGoodsRule(_ context.Context, id string, version int, _ string) (domain.FreeGoodsDef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.freeData()
	r, ok := st.rules[id]
	if !ok {
		return domain.FreeGoodsDef{}, httpx.NewProblem(http.StatusNotFound, domain.CodeFreeGoodsRuleNotFound, "no free goods rule "+id)
	}
	if r.Version != version {
		return domain.FreeGoodsDef{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "free goods rule version does not match")
	}
	if r.Withdrawn {
		return domain.FreeGoodsDef{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "free goods rule is already withdrawn")
	}
	r.Withdrawn, r.Version = true, r.Version+1
	st.rules[id] = r
	return r, nil
}

func (f *fakeStore) ReviseFreeGoodsRule(_ context.Context, rev domain.FreeGoodsRevision, key string) (domain.FreeGoodsDef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.freeData()
	if prev, ok := st.replays["rev|"+key]; ok && key != "" {
		return prev, nil
	}
	r, ok := st.rules[rev.ID]
	if !ok {
		return domain.FreeGoodsDef{}, httpx.NewProblem(http.StatusNotFound, domain.CodeFreeGoodsRuleNotFound, "no free goods rule "+rev.ID)
	}
	if r.Version != rev.Version {
		return domain.FreeGoodsDef{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "free goods rule version does not match")
	}
	if r.Withdrawn {
		return domain.FreeGoodsDef{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "free goods rule is withdrawn and cannot be revised")
	}
	next := rev.Apply(r)
	if fields := domain.FreeGoodsPastChanges(r, next, time.Now().UTC()); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "free goods rule revision is invalid")
		p.Errors = fields
		return domain.FreeGoodsDef{}, p
	}
	var others []domain.FreeGoodsDef
	for _, o := range st.rules {
		others = append(others, o)
	}
	if c, clash := domain.FreeGoodsOverlap(next, others); clash {
		return domain.FreeGoodsDef{}, httpx.NewProblem(http.StatusConflict, domain.CodeOverlap, "overlapping free goods rule "+c.ID)
	}
	next.Version++
	st.rules[rev.ID] = next
	if key != "" {
		st.replays["rev|"+key] = next
	}
	return next, nil
}
