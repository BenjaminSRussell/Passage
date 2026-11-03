package api_test

import (
	"context"
	"net/http"
	"sort"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func (f *fakeStore) activeProcedure(le, name string) (*domain.Procedure, error) {
	for _, p := range f.campaignData().procs {
		if p.LegalEntityID == le && p.Name == name && p.Status == domain.ProcedureActive {
			cp := p
			return &cp, nil
		}
	}
	return nil, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeProcedureNotActive, "pricing procedure "+name+" has no active version")
}

func (f *fakeStore) SetProcedureAssignment(_ context.Context, in store.ProcedureAssignment, version int, _ string) (store.ProcedureAssignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.assigns == nil {
		f.assigns = map[string]store.ProcedureAssignment{}
	}
	if _, err := f.activeProcedure(in.LegalEntityID, in.ProcedureName); err != nil {
		return store.ProcedureAssignment{}, err
	}
	key := in.LegalEntityID + "|" + in.SalesChannel
	if f.assigns[key].Version != version {
		return store.ProcedureAssignment{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "procedure assignment version does not match")
	}
	in.Version = version + 1
	f.assigns[key] = in
	return in, nil
}

func (f *fakeStore) ListProcedureAssignments(_ context.Context, le string) ([]store.ProcedureAssignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ProcedureAssignment{}
	for _, a := range f.assigns {
		if a.LegalEntityID == le {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SalesChannel < out[j].SalesChannel })
	return out, nil
}

func (f *fakeStore) ClearProcedureAssignment(_ context.Context, le, channel string, version int, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := le + "|" + channel
	if a, ok := f.assigns[key]; !ok || a.Version != version {
		return httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "procedure assignment version does not match")
	}
	delete(f.assigns, key)
	return nil
}

func (f *fakeStore) DetermineProcedure(_ context.Context, le, channel, name string) (*domain.Procedure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" {
		a, ok := f.assigns[le+"|"+channel]
		if !ok {
			a, ok = f.assigns[le+"|"]
		}
		if !ok {
			return nil, nil
		}
		name = a.ProcedureName
	}
	return f.activeProcedure(le, name)
}
