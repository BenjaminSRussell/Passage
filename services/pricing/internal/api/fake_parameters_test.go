package api_test

import (
	"context"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func (f *fakeStore) GetParameters(_ context.Context, legalEntityID string) (store.Parameters, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.params[legalEntityID]; ok {
		return p, nil
	}
	return store.Parameters{LegalEntityID: legalEntityID}, nil
}

func (f *fakeStore) SetParameters(_ context.Context, in store.Parameters, version int, _ string) (store.Parameters, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.params == nil {
		f.params = map[string]store.Parameters{}
	}
	if f.params[in.LegalEntityID].Version != version {
		return store.Parameters{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "parameters version does not match")
	}
	in.Version = version + 1
	f.params[in.LegalEntityID] = in
	return in, nil
}
