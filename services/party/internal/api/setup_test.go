package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

type fakeSetup struct {
	group  domain.NewCustomerGroup
	key    string
	le     string
	notFnd bool
}

func (f *fakeSetup) ListPaymentTerms(context.Context) ([]store.PaymentTerm, error) {
	days := 30
	return []store.PaymentTerm{{ID: knownID, Code: "NET30", Name: "Net 30 days", NetDays: &days, Version: 1}}, nil
}

func (f *fakeSetup) GetPaymentTerm(_ context.Context, id string) (store.PaymentTerm, error) {
	if f.notFnd {
		return store.PaymentTerm{}, httpx.NewProblem(http.StatusNotFound, store.CodePaymentTermNotFound, "not found")
	}
	return store.PaymentTerm{ID: id, Code: "NET30", Name: "Net 30 days"}, nil
}

func (f *fakeSetup) ListPostingProfiles(_ context.Context, le string) ([]store.PostingProfile, error) {
	f.le = le
	return []store.PostingProfile{{ID: knownID, LegalEntityID: le, Code: "DOM", Name: "Domestic"}}, nil
}

func (f *fakeSetup) GetPostingProfile(_ context.Context, id string) (store.PostingProfile, error) {
	return store.PostingProfile{ID: id, Code: "DOM", Name: "Domestic"}, nil
}

func (f *fakeSetup) CreateCustomerGroup(_ context.Context, in store.NewCustomerGroupRequest) (domain.CustomerGroup, bool, error) {
	f.group, f.key = in.In, in.Key
	return domain.CustomerGroup{ID: knownID, Code: in.In.Code, Name: in.In.Name, Version: 1}, true, nil
}

func (f *fakeSetup) ListCustomerGroups(context.Context) ([]domain.CustomerGroup, error) {
	return []domain.CustomerGroup{{ID: knownID, Code: "KEY", Name: "Key accounts", Version: 1}}, nil
}

func (f *fakeSetup) GetCustomerGroup(_ context.Context, id string) (domain.CustomerGroup, error) {
	return domain.CustomerGroup{ID: id, Code: "KEY", Name: "Key accounts", Version: 1}, nil
}

func TestSetupRoutes(t *testing.T) {
	f := &fakeSetup{}
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithSetup(f).Register(r)

	for path, want := range map[string]string{
		"/v1/party/payment-terms":               `"code":"NET30"`,
		"/v1/party/payment-terms/" + knownID:    `"name":"Net 30 days"`,
		"/v1/party/posting-profiles":            `"code":"DOM"`,
		"/v1/party/posting-profiles/" + knownID: `"name":"Domestic"`,
		"/v1/party/customer-groups":             `"code":"KEY"`,
		"/v1/party/customer-groups/" + knownID:  `"name":"Key accounts"`,
	} {
		if rec := do(r, http.MethodGet, path, "", "party.read", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
		}
		if rec := do(r, http.MethodGet, path, "", "party.write", true); rec.Code != http.StatusForbidden {
			t.Fatalf("GET %s without party.read = %d, want 403", path, rec.Code)
		}
	}
	le := "00000000-0000-0000-0000-0000000000a1"
	if rec := do(r, http.MethodGet, "/v1/party/posting-profiles?legalEntityId="+le, "", "party.read", true); rec.Code != http.StatusOK || f.le != le {
		t.Fatalf("profiles of one entity = %d, filter %q", rec.Code, f.le)
	}
	if rec := do(r, http.MethodGet, "/v1/party/posting-profiles?legalEntityId=x", "", "party.read", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed entity = %d, want 400", rec.Code)
	}
	f.notFnd = true
	if rec := do(r, http.MethodGet, "/v1/party/payment-terms/"+knownID, "", "party.read", true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodePaymentTermNotFound) {
		t.Fatalf("unknown term = %d %s", rec.Code, rec.Body)
	}

	const groups = "/v1/party/customer-groups"
	if rec := do(r, http.MethodPost, groups, `{"code":" key ","name":" Key accounts "}`, "party.write", true); rec.Code != http.StatusCreated || f.group.Code != "KEY" || f.group.Name != "Key accounts" {
		t.Fatalf("create = %d %s group=%+v", rec.Code, rec.Body, f.group)
	}
	for _, body := range []string{`{"code":"","name":"x"}`, `{"code":"TOOLONGCODE1","name":"x"}`, `{"code":"A","name":""}`, `{"id":"x","code":"A","name":"x"}`} {
		if rec := do(r, http.MethodPost, groups, body, "party.write", true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeCustomerGroupInvalid) {
			t.Fatalf("create %s = %d %s; want 422", body, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPost, groups, `{"code":"A","name":"x"}`, "party.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("create without party.write = %d, want 403", rec.Code)
	}
}
