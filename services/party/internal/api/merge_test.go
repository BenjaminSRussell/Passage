package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const loserID = "00000000-0000-0000-0000-0000000000cc"

type fakeMerges struct {
	got    []store.MergeRequest
	merged map[string]bool
	err    error
}

func (f *fakeMerges) Merge(_ context.Context, req store.MergeRequest) (store.MergeResult, error) {
	f.got = append(f.got, req)
	if f.err != nil {
		return store.MergeResult{}, f.err
	}
	if f.merged[req.LoserID] {
		return store.MergeResult{}, store.ErrNotMergeable
	}
	if f.merged == nil {
		f.merged = map[string]bool{}
	}
	f.merged[req.LoserID] = true
	return store.MergeResult{MergeID: "m1", RolesMoved: 2, ReferencesRewritten: 1, Resolved: map[string]string{"name": "Ada L"}}, nil
}

func mergeRouter(f *fakeMerges) http.Handler {
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithMerge(f).Register(r)
	return r
}

func mergeCall(r http.Handler, id, body, duties string) (int, string) {
	rec := do(r, http.MethodPost, "/v1/party/parties/"+id+":merge", body, duties, true)
	return rec.Code, rec.Body.String()
}

func TestMergeRoute_Merges(t *testing.T) {
	f := &fakeMerges{}
	code, body := mergeCall(mergeRouter(f), knownID, `{"mergedPartyId":"`+loserID+`","rules":{"name":"newest"}}`, "party.merge")
	if code != http.StatusOK || !strings.Contains(body, `"mergeId":"m1"`) || !strings.Contains(body, `"survivorPartyId":"`+knownID+`"`) || !strings.Contains(body, `"rolesMoved":2`) {
		t.Fatalf("got %d %s", code, body)
	}
	if len(f.got) != 1 || f.got[0].WinnerID != knownID || f.got[0].LoserID != loserID || f.got[0].Rules["name"] != domain.Newest {
		t.Fatalf("store got %+v", f.got)
	}
}

func TestMergeRoute_Rejections(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		body     string
		duties   string
		storeErr error
		want     int
		wantCode string
	}{
		{"self merge", knownID, `{"mergedPartyId":"` + knownID + `"}`, "party.merge", nil, 422, "PTY-PARTY-INVALID"},
		{"missing merged id", knownID, `{}`, "party.merge", nil, 422, "PTY-PARTY-INVALID"},
		{"malformed merged id", knownID, `{"mergedPartyId":"nope"}`, "party.merge", nil, 422, "PTY-PARTY-INVALID"},
		{"unknown field in body", knownID, `{"mergedPartyId":"` + loserID + `","tenantId":"x"}`, "party.merge", nil, 400, ""},
		{"duty missing", knownID, `{"mergedPartyId":"` + loserID + `"}`, "party.write", nil, 403, "AUTH-0003"},
		{"unknown survivor", knownID, `{"mergedPartyId":"` + loserID + `"}`, "party.merge", store.ErrNotFound, 404, "PTY-PARTY-NOT-FOUND"},
		{"malformed survivor id", "nope", `{"mergedPartyId":"` + loserID + `"}`, "party.merge", nil, 404, "PTY-PARTY-NOT-FOUND"},
		{"unmergeable", knownID, `{"mergedPartyId":"` + loserID + `"}`, "party.merge", store.ErrNotMergeable, 409, "PTY-MERGE-CONFLICT"},
		{"role conflict", knownID, `{"mergedPartyId":"` + loserID + `"}`, "party.merge", store.ErrRoleConflict, 409, "PTY-MERGE-ROLE-CONFLICT"},
		{"unknown rule", knownID, `{"mergedPartyId":"` + loserID + `","rules":{"name":"bogus"}}`, "party.merge", &domain.RuleError{Field: "name", Reason: "unknown rule"}, 422, "PTY-PARTY-INVALID"},
		{"legal-entity scoped caller", knownID, `{"mergedPartyId":"` + loserID + `"}`, "party.merge", store.ErrScopedPrincipal, 403, "AUTH-0003"},
		{"not tenant wide", knownID, `{"mergedPartyId":"` + loserID + `"}`, "party.merge", dbx.ErrNotTenantWide, 403, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMerges{err: tc.storeErr}
			code, body := mergeCall(mergeRouter(f), tc.id, tc.body, tc.duties)
			if code != tc.want || !strings.Contains(body, tc.wantCode) {
				t.Fatalf("got %d %s, want %d %s", code, body, tc.want, tc.wantCode)
			}
			if tc.storeErr == nil && len(f.got) != 0 {
				t.Fatalf("store reached on a request that must fail first: %+v", f.got)
			}
		})
	}
}

func TestMergeRoute_RepeatAfterMergeIsConflict(t *testing.T) {
	r := mergeRouter(&fakeMerges{})
	body := `{"mergedPartyId":"` + loserID + `"}`
	if code, out := mergeCall(r, knownID, body, "party.merge"); code != http.StatusOK {
		t.Fatalf("first: %d %s", code, out)
	}
	if code, out := mergeCall(r, knownID, body, "party.merge"); code != http.StatusConflict || !strings.Contains(out, "PTY-MERGE-CONFLICT") {
		t.Fatalf("repeat: %d %s", code, out)
	}
}

func TestMergeRoute_NotRegisteredWithoutStore(t *testing.T) {
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).Register(r)
	if code, _ := mergeCall(r, knownID, `{"mergedPartyId":"`+loserID+`"}`, "party.merge"); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d", code)
	}
}
