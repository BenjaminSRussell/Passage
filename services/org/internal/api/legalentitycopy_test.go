package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

func (f *fakeStore) CopyLegalEntity(_ context.Context, in store.LegalEntityCopyInput, _ string) (store.LegalEntityCopy, error) {
	if f.err != nil {
		return store.LegalEntityCopy{}, f.err
	}
	f.copies = append(f.copies, in)
	return store.LegalEntityCopy{LegalEntity: store.LegalEntity{ID: "new-1", Code: in.Code, Name: in.Name, Country: in.Country, Currency: in.Currency, Status: "active", Version: 1}, SourceID: in.SourceID, Sites: 2, Warehouses: 5}, nil
}

const copySource = "0190b6c0-0000-7000-8000-0000000000c1"

func postCopy(f *fakeStore, id, body, duties string) *httptest.ResponseRecorder {
	r, _ := newRouter(f)
	return do(r, http.MethodPost, legalEntities+"/"+id+":copy", body, duties, true)
}

func TestCopyLegalEntity_CreatesWithDefaultsAndUppercasesTheCode(t *testing.T) {
	f := &fakeStore{}
	rec := postCopy(f, copySource, `{"code":"camf","name":" Canada ","country":"CA","currency":"CAD"}`, allDuties)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"sites":2`) || !strings.Contains(rec.Body.String(), `"code":"CAMF"`) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	got := f.copies[0]
	if got.SourceID != copySource || got.Name != "Canada" || !got.Sites || !got.Locations || !got.RoundingRules {
		t.Fatalf("input = %+v; includes default to everything", got)
	}
}

func TestCopyLegalEntity_IncludeFlagsAreHonoured(t *testing.T) {
	f := &fakeStore{}
	rec := postCopy(f, copySource, `{"code":"CAMF","name":"Canada","country":"CA","currency":"CAD","include":{"sites":false,"roundingRules":false}}`, allDuties)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	if got := f.copies[0]; got.Sites || got.Locations || got.RoundingRules {
		t.Fatalf("input = %+v", got)
	}
}

func TestCopyLegalEntity_Validation(t *testing.T) {
	for name, body := range map[string]string{
		"bad code":            `{"code":"x","name":"Canada","country":"CA","currency":"CAD"}`,
		"reserved code":       `{"code":"SYS","name":"Canada","country":"CA","currency":"CAD"}`,
		"missing name":        `{"code":"CAMF","country":"CA","currency":"CAD"}`,
		"bad country":         `{"code":"CAMF","name":"Canada","country":"Canada","currency":"CAD"}`,
		"bad zone":            `{"code":"CAMF","name":"Canada","country":"CA","currency":"CAD","timeZone":"Mars/Base"}`,
		"locations w/o sites": `{"code":"CAMF","name":"Canada","country":"CA","currency":"CAD","include":{"sites":false,"locations":true}}`,
		"unknown field":       `{"code":"CAMF","name":"Canada","country":"CA","currency":"CAD","bogus":1}`,
	} {
		f := &fakeStore{}
		rec := postCopy(f, copySource, body, allDuties)
		if rec.Code != http.StatusBadRequest || len(f.copies) != 0 {
			t.Errorf("%s: status = %d body = %s copies = %d", name, rec.Code, rec.Body, len(f.copies))
		}
	}
}

func TestCopyLegalEntity_Refusals(t *testing.T) {
	body := `{"code":"CAMF","name":"Canada","country":"CA","currency":"CAD"}`
	f := &fakeStore{}
	if rec := postCopy(f, copySource, body, "org.read"); rec.Code != http.StatusForbidden {
		t.Errorf("without the duty: %d", rec.Code)
	}
	r, _ := newRouter(f)
	if rec := doWithoutTenantAdmin(r, http.MethodPost, legalEntities+"/"+copySource+":copy", body, allDuties, true); rec.Code != http.StatusForbidden {
		t.Errorf("without tenant.administer: %d", rec.Code)
	}
	if rec := postCopy(f, "not-a-uuid", body, allDuties); rec.Code != http.StatusNotFound {
		t.Errorf("malformed source id: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, legalEntities+"/"+copySource+":copy", body, allDuties, true, "Idempotency-Key", strings.Repeat("k", 201)); rec.Code != http.StatusBadRequest {
		t.Errorf("long idempotency key: %d", rec.Code)
	}
	if len(f.copies) != 0 {
		t.Errorf("a refused request reached the store")
	}
}

func TestCopyLegalEntity_StoreErrorsKeepTheirCode(t *testing.T) {
	f := &fakeStore{err: httpx.NewProblem(http.StatusConflict, store.CodeLegalEntityCode, "exists")}
	rec := postCopy(f, copySource, `{"code":"CAMF","name":"Canada","country":"CA","currency":"CAD"}`, allDuties)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeLegalEntityCode) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
}
