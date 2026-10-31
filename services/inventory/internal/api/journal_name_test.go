package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// journalNameFake records what the journal name handlers pass to the store.
type journalNameFake struct {
	key      string
	created  domain.JournalName
	replay   bool
	version  int
	patch    store.JournalNamePatch
	filter   store.JournalNameFilter
	returned domain.JournalName
}

func (f *fakeStore) CreateJournalName(_ context.Context, key string, n domain.JournalName) (domain.JournalName, bool, error) {
	if f.err != nil {
		return domain.JournalName{}, false, f.err
	}
	f.jn.key, f.jn.created = key, n
	n.ID, n.Active, n.Version = ids.New(), true, 1
	return n, f.jn.replay, nil
}

func (f *fakeStore) GetJournalName(_ context.Context, id string) (domain.JournalName, error) {
	if f.err != nil {
		return domain.JournalName{}, f.err
	}
	return domain.JournalName{ID: id, Code: "ADJ", Type: domain.JournalAdjustment, Active: true, Version: 3}, nil
}

func (f *fakeStore) ListJournalNames(_ context.Context, flt store.JournalNameFilter) ([]domain.JournalName, error) {
	f.jn.filter = flt
	if f.err != nil {
		return nil, f.err
	}
	return []domain.JournalName{{ID: ids.New(), Code: "ADJ", Type: domain.JournalAdjustment, Active: true, Version: 1}}, nil
}

func (f *fakeStore) UpdateJournalName(_ context.Context, id string, version int, p store.JournalNamePatch) (domain.JournalName, error) {
	f.jn.version, f.jn.patch = version, p
	if f.err != nil {
		return domain.JournalName{}, f.err
	}
	return domain.JournalName{ID: id, Code: "ADJ", Type: domain.JournalAdjustment, Active: true, Version: version + 1}, nil
}

var jnWrite = []string{"inventory.journal-name.write"}

func TestCreateJournalNameNormalizesAndRequiresKey(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := `{"legalEntityId":"` + ids.New() + `","code":" adj-1 ","description":" Adjustments ","journalType":"ADJUSTMENT","requiresApproval":true}`
	rec := do(t, h, http.MethodPost, "/v1/inventory/journal-names", body, jnWrite, map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d etag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if fs.jn.created.Code != "ADJ-1" || fs.jn.created.Description != "Adjustments" || !fs.jn.created.RequiresApproval || fs.jn.key != "k1" {
		t.Fatalf("store got %+v key %q", fs.jn.created, fs.jn.key)
	}
	rec = do(t, h, http.MethodPost, "/v1/inventory/journal-names", body, jnWrite, nil)
	if rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["Idempotency-Key"] == "" {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateJournalNameValidatesFields(t *testing.T) {
	h := newServer(t, &fakeStore{})
	cases := map[string]string{
		"legalEntityId":   `{"legalEntityId":"x","code":"A","journalType":"ADJUSTMENT"}`,
		"journalType":     `{"legalEntityId":"` + ids.New() + `","code":"A","journalType":"COUNT"}`,
		"code":            `{"legalEntityId":"` + ids.New() + `","code":"bad code","journalType":"ADJUSTMENT"}`,
		"offsetAccountId": `{"legalEntityId":"` + ids.New() + `","code":"A","journalType":"ADJUSTMENT","offsetAccountId":"5000"}`,
		"description":     `{"legalEntityId":"` + ids.New() + `","code":"A","journalType":"ADJUSTMENT","description":"` + strings.Repeat("x", 201) + `"}`,
	}
	for field, body := range cases {
		rec := do(t, h, http.MethodPost, "/v1/inventory/journal-names", body, jnWrite, map[string]string{"Idempotency-Key": "k"})
		if rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors[field] == "" {
			t.Errorf("%s: status %d %s", field, rec.Code, rec.Body.String())
		}
	}
}

func TestJournalNameWritesNeedTheDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	id := ids.New()
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/inventory/journal-names"},
		{http.MethodPatch, "/v1/inventory/journal-names/" + id},
	} {
		if rec := do(t, h, c.method, c.path, `{}`, []string{"inventory.read"}, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without duty: %d", c.method, c.path, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/journal-names", "", nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("list without read duty: %d", rec.Code)
	}
}

func TestListAndGetJournalNames(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	le := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/inventory/journal-names?legalEntityId="+le+"&active=true&journalType=ADJUSTMENT", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK || fs.jn.filter.LegalEntityID != le || fs.jn.filter.Active == nil || !*fs.jn.filter.Active || fs.jn.filter.JournalType != "ADJUSTMENT" {
		t.Fatalf("list: %d %+v %s", rec.Code, fs.jn.filter, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/journal-names?active=maybe", "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad active: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/journal-names/not-a-uuid", "", []string{"inventory.read"}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/journal-names/"+ids.New(), "", []string{"inventory.read"}, nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Errorf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestUpdateJournalNameUsesIfMatchAndPatchFields(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/inventory/journal-names/" + ids.New()
	if rec := do(t, h, http.MethodPatch, path, `{"active":false}`, jnWrite, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPatch, path, `{}`, jnWrite, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPatch, path, `{"active":false,"requiresApproval":true}`, jnWrite, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || fs.jn.version != 2 || fs.jn.patch.Active == nil || *fs.jn.patch.Active || fs.jn.patch.RequiresApproval == nil || !*fs.jn.patch.RequiresApproval {
		t.Fatalf("patch: %d version %d %+v", rec.Code, fs.jn.version, fs.jn.patch)
	}
}

func TestCreateJournalAcceptsNameInsteadOfOffsetAccount(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	name := ids.New()
	body := `{"journalType":"MOVEMENT","legalEntityId":"` + ids.New() + `","postingDate":"2026-10-01","currency":"USD","journalNameId":"` + name + `"}`
	rec := do(t, h, http.MethodPost, "/v1/inventory/journals", body, []string{"inventory.journal.write"}, map[string]string{"Idempotency-Key": "k"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/v1/inventory/journals", strings.Replace(body, name, "nope", 1), []string{"inventory.journal.write"}, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name id: %d", rec.Code)
	}
}
