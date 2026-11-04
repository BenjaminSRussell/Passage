package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// campaignState is the fake's campaigns and procedure versions. It applies the same
// rules as the Postgres store: version and state checks on the kill switch, the next
// version number per legal entity and name, PRC-0008 on non-DRAFT versions, supersede
// on activation and Idempotency-Key replay.
type campaignState struct {
	campaigns map[string]domain.Campaign
	procs     map[string]domain.Procedure
	replays   map[string]any
}

func (f *fakeStore) campaignData() *campaignState {
	if f.camp.campaigns == nil {
		f.camp = campaignState{campaigns: map[string]domain.Campaign{}, procs: map[string]domain.Procedure{}, replays: map[string]any{}}
	}
	return &f.camp
}

func notFoundCampaign(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeCampaignNotFound, "no campaign "+id)
}

func notFoundProcedure(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeProcedureNotFound, "no pricing procedure "+id)
}

func immutable(p domain.Procedure) error {
	return store.RuleProblem(domain.ProcedureImmutable(p))
}

// checkCampaign mirrors the store's record check: the campaign exists and is of the record's legal entity.
func (f *fakeStore) checkCampaign(in store.Entry) error {
	c, ok := f.campaignData().campaigns[in.CampaignID]
	if !ok {
		return notFoundCampaign(in.CampaignID)
	}
	if c.LegalEntityID != in.LegalEntityID {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0001", "request is invalid")
		p.Errors = map[string]string{"campaignId": "belongs to another legal entity"}
		return p
	}
	return nil
}

// campaignUsable mirrors the store's resolve filter: live on the day and targeting the group.
func (f *fakeStore) campaignUsable(id string, q store.ActiveQuery) bool {
	c, ok := f.campaignData().campaigns[id]
	return ok && domain.CampaignLive(c, q.AsOf) && domain.CampaignTargets(c, q.CustomerGroupID)
}

func (f *fakeStore) CreateCampaign(_ context.Context, c domain.Campaign, key string) (domain.Campaign, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.campaignData()
	if prev, ok := st.replays["campaign:"+key].(domain.Campaign); ok && key != "" {
		return prev, nil
	}
	c.ID, c.Version = f.nextID("0011"), 1
	st.campaigns[c.ID] = c
	if key != "" {
		st.replays["campaign:"+key] = c
	}
	return c, nil
}

func (f *fakeStore) GetCampaign(_ context.Context, id string) (domain.Campaign, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.campaignData().campaigns[id]
	if !ok {
		return domain.Campaign{}, notFoundCampaign(id)
	}
	return c, nil
}

func (f *fakeStore) ListCampaigns(_ context.Context, le string, _ int) ([]domain.Campaign, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Campaign{}
	for _, c := range f.campaignData().campaigns {
		if le == "" || c.LegalEntityID == le {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) SetCampaignKilled(_ context.Context, id string, version int, killed bool, key string) (domain.Campaign, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.campaignData()
	if prev, ok := st.replays["campaign-switch:"+key].(domain.Campaign); ok && key != "" {
		return prev, nil
	}
	c, ok := st.campaigns[id]
	if !ok {
		return domain.Campaign{}, notFoundCampaign(id)
	}
	if c.Version != version {
		return domain.Campaign{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "campaign version does not match")
	}
	if c.Killed == killed {
		return domain.Campaign{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "campaign is already in that state")
	}
	c.Killed = killed
	c.Version++
	st.campaigns[id] = c
	if key != "" {
		st.replays["campaign-switch:"+key] = c
	}
	return c, nil
}

func (f *fakeStore) CreateProcedure(_ context.Context, p domain.Procedure, key string) (domain.Procedure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.campaignData()
	if prev, ok := st.replays["procedure:"+key].(domain.Procedure); ok && key != "" {
		a, _ := json.Marshal(prev.Steps)
		b, _ := json.Marshal(p.Steps)
		if prev.Name != p.Name || string(a) != string(b) {
			return domain.Procedure{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
		}
		return prev, nil
	}
	next := 1
	for _, q := range st.procs {
		if q.LegalEntityID == p.LegalEntityID && q.Name == p.Name && q.Version >= next {
			next = q.Version + 1
		}
	}
	p.ID, p.Version, p.Status, p.Revision = f.nextID("0012"), next, domain.ProcedureDraft, 1
	st.procs[p.ID] = p
	if key != "" {
		st.replays["procedure:"+key] = p
	}
	return p, nil
}

func (f *fakeStore) GetProcedure(_ context.Context, id string) (domain.Procedure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.campaignData().procs[id]
	if !ok {
		return domain.Procedure{}, notFoundProcedure(id)
	}
	return p, nil
}

func (f *fakeStore) ListProcedures(_ context.Context, le string, _ int) ([]domain.Procedure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Procedure{}
	for _, p := range f.campaignData().procs {
		if le == "" || p.LegalEntityID == le {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (f *fakeStore) ReviseProcedure(_ context.Context, id string, revision int, steps []domain.ProcedureStep, _ string) (domain.Procedure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.campaignData()
	p, ok := st.procs[id]
	if !ok {
		return domain.Procedure{}, notFoundProcedure(id)
	}
	if p.Status != domain.ProcedureDraft {
		return domain.Procedure{}, immutable(p)
	}
	if p.Revision != revision {
		return domain.Procedure{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "procedure revision does not match")
	}
	p.Steps, p.Revision = steps, p.Revision+1
	st.procs[id] = p
	return p, nil
}

func (f *fakeStore) ActivateProcedure(_ context.Context, id string, key string) (domain.Procedure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	st := f.campaignData()
	if prev, ok := st.replays["procedure-activate:"+key].(domain.Procedure); ok && key != "" {
		return prev, nil
	}
	p, ok := st.procs[id]
	if !ok {
		return domain.Procedure{}, notFoundProcedure(id)
	}
	if p.Status != domain.ProcedureDraft {
		return domain.Procedure{}, immutable(p)
	}
	for k, q := range st.procs {
		if q.LegalEntityID == p.LegalEntityID && q.Name == p.Name && q.Status == domain.ProcedureActive {
			q.Status = domain.ProcedureSuperseded
			st.procs[k] = q
		}
	}
	p.Status = domain.ProcedureActive
	st.procs[id] = p
	if key != "" {
		st.replays["procedure-activate:"+key] = p
	}
	return p, nil
}

func (f *fakeStore) ReviseCampaign(_ context.Context, rev domain.CampaignRevision, _ string) (domain.Campaign, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.campaignData()
	c, ok := st.campaigns[rev.ID]
	if !ok {
		return domain.Campaign{}, notFoundCampaign(rev.ID)
	}
	if c.Version != rev.Version {
		return domain.Campaign{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "campaign version does not match")
	}
	next := rev.Apply(c)
	if fields := domain.ValidateCampaign(next); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "request is invalid")
		p.Errors = fields
		return domain.Campaign{}, p
	}
	next.Version++
	st.campaigns[rev.ID] = next
	return next, nil
}
