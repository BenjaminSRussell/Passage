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
	return store.DefaultParameters(legalEntityID), nil
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

func (f *fakeStore) GetTenantParameters(context.Context) (store.TenantParameters, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tenantParams != nil {
		return *f.tenantParams, nil
	}
	return store.DefaultTenantParameters(), nil
}

func (f *fakeStore) SetTenantParameters(_ context.Context, in store.TenantParameters, version int, _ string) (store.TenantParameters, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := store.DefaultTenantParameters()
	if f.tenantParams != nil {
		cur = *f.tenantParams
	}
	if cur.Version != version {
		return store.TenantParameters{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "tenant parameters version does not match")
	}
	in.Version = version + 1
	f.tenantParams = &in
	return in, nil
}
