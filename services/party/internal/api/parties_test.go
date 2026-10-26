package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/numbering"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	partiesPath = "/v1/party/parties"
	knownID     = "00000000-0000-0000-0000-0000000000aa"
	mergedID    = "00000000-0000-0000-0000-0000000000bb"
)

type fakeSequencer struct {
	err   error
	calls []string
}

func (f *fakeSequencer) Next(_ context.Context, code, key string) (string, error) {
	f.calls = append(f.calls, code+"/"+key)
	return "PTY-000001", f.err
}

type fakeParties struct {
	created     store.NewParty
	updateID    string
	updateVer   int
	updatePatch store.PartyPatch
	err         error
	replay      bool
}

func (f *fakeParties) CreateParty(ctx context.Context, in store.NewParty, next func(context.Context) (string, error)) (domain.Party, bool, error) {
	f.created = in
	number, err := next(ctx)
	if err != nil {
		return domain.Party{}, false, err
	}
	name := domain.DisplayName(in.PartyType, in.Fields)
	return domain.Party{ID: knownID, PartyNumber: number, PartyType: in.PartyType, Name: name, SearchName: domain.Normalize(name), Status: "active", PartyFields: in.Fields, Version: 1}, !f.replay, nil
}

func (f *fakeParties) GetParty(_ context.Context, id string) (domain.Party, bool, error) {
	dob := "1990-02-03"
	switch id {
	case knownID:
		return domain.Party{ID: knownID, PartyType: "person", Name: "Ada L", Status: "active", PartyFields: domain.PartyFields{DOB: &dob}, Version: 4}, false, nil
	case mergedID:
		return domain.Party{ID: knownID, PartyType: "person", Name: "Ada L", Status: "active", Version: 4}, true, nil
	}
	return domain.Party{}, false, store.ErrNotFound
}

func (f *fakeParties) UpdateParty(_ context.Context, id string, version int, patch store.PartyPatch) (domain.Party, error) {
	f.updateID, f.updateVer, f.updatePatch = id, version, patch
	if f.err != nil {
		return domain.Party{}, f.err
	}
	return domain.Party{ID: id, Version: version + 1, Status: "active"}, nil
}

func partyRouter(parties *fakeParties, seq api.Sequencer) (*chi.Mux, *api.API) {
	a := api.New(&stubChecker{}, stubPinger{}, parties, seq)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return m
}

func TestCreateParty_OrganizationAndPerson(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantName string
	}{
		{"organization", `{"partyType":"organization","legalName":" Acme GmbH ","tradeName":"Acme"}`, "Acme GmbH"},
		{"person", `{"partyType":"person","givenName":"Ada","familyName":"Lovelace","dob":"1990-02-03"}`, "Ada Lovelace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seq := &fakeSequencer{}
			r, _ := partyRouter(&fakeParties{}, seq)
			rec := do(r, http.MethodPost, partiesPath, tc.body, "party.write", true)
			if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
				t.Fatalf("got %d etag=%q %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
			}
			got := decode(t, rec.Body.String())
			if got["partyNumber"] != "PTY-000001" || got["status"] != "active" || got["version"] != float64(1) || got["name"] != tc.wantName || got["searchName"] != domain.Normalize(tc.wantName) {
				t.Fatalf("got %v", got)
			}
			if dob, has := got["dob"]; has && dob != domain.MaskedDOB {
				t.Fatalf("dob leaked to a caller without pii.read: %v", dob)
			}
			if len(seq.calls) != 1 || !strings.HasPrefix(seq.calls[0], "PARTY/") {
				t.Fatalf("got %v", seq.calls)
			}
		})
	}
}

func TestCreateParty_UsesRequestKeyForNumberingAndStore(t *testing.T) {
	seq, parties := &fakeSequencer{}, &fakeParties{}
	r, _ := partyRouter(parties, seq)
	req := `{"partyType":"organization","legalName":"Acme"}`
	rec := do(r, http.MethodPost, partiesPath, req, "party.write", true)
	if rec.Code != http.StatusCreated || parties.created.Key != "" || strings.HasSuffix(seq.calls[0], "/") {
		t.Fatalf("no key: %d %+v %v", rec.Code, parties.created, seq.calls)
	}
	httpReq := newRequest(http.MethodPost, partiesPath, req, "party.write")
	httpReq.Header.Set("Idempotency-Key", "abc")
	rec = serve(r, httpReq)
	if rec.Code != http.StatusCreated || parties.created.Key != "abc" || seq.calls[1] != "PARTY/abc" || parties.created.Hash == "" {
		t.Fatalf("with key: %d %+v %v", rec.Code, parties.created, seq.calls)
	}
}

func TestCreateParty_ReplayIsNotCounted(t *testing.T) {
	r, a := partyRouter(&fakeParties{replay: true}, &fakeSequencer{})
	do(r, http.MethodPost, partiesPath, `{"partyType":"organization","legalName":"Acme"}`, "party.write", true)
	rec := httpRecorderFor(a.Metrics())
	if !strings.Contains(rec, "party_parties_created_total 0") {
		t.Fatalf("got %s", rec)
	}
	r, a = partyRouter(&fakeParties{}, &fakeSequencer{})
	do(r, http.MethodPost, partiesPath, `{"partyType":"organization","legalName":"Acme"}`, "party.write", true)
	if rec := httpRecorderFor(a.Metrics()); !strings.Contains(rec, "party_parties_created_total 1") {
		t.Fatalf("got %s", rec)
	}
}

func TestCreateParty_Rejections(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{"organization without legal name", `{"partyType":"organization"}`, "party.write", true, 422, "PTY-PARTY-INVALID", "legalName"},
		{"person without family name", `{"partyType":"person","givenName":"Ada"}`, "party.write", true, 422, "PTY-PARTY-INVALID", "familyName"},
		{"empty object", `{}`, "party.write", true, 422, "PTY-PARTY-INVALID", "partyType"},
		{"whitespace name", `{"partyType":"organization","legalName":"   "}`, "party.write", true, 422, "PTY-PARTY-INVALID", "legalName"},
		{"name over 200", `{"partyType":"organization","legalName":"` + strings.Repeat("a", 201) + `"}`, "party.write", true, 422, "PTY-PARTY-INVALID", "legalName"},
		{"future dob", `{"partyType":"person","givenName":"A","familyName":"B","dob":"2999-01-01"}`, "party.write", true, 422, "PTY-PARTY-INVALID", "dob"},
		{"owner not a uuid", `{"partyType":"organization","legalName":"A","ownerUserId":"x"}`, "party.write", true, 422, "PTY-PARTY-INVALID", "ownerUserId"},
		{"unknown field", `{"partyType":"organization","legalName":"A","bogus":1}`, "party.write", true, 400, "COM-0001", ""},
		{"server-owned field", `{"partyType":"organization","legalName":"A","status":"merged"}`, "party.write", true, 400, "COM-0001", ""},
		{"name as number", `{"partyType":"organization","legalName":5}`, "party.write", true, 400, "COM-0001", ""},
		{"malformed json", `{`, "party.write", true, 400, "COM-0001", ""},
		{"no tenant", `{}`, "", false, 401, "AUTH-0001", ""},
		{"missing duty", `{"partyType":"organization","legalName":"A"}`, "party.read", true, 403, "AUTH-0003", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seq := &fakeSequencer{}
			r, _ := partyRouter(&fakeParties{}, seq)
			rec := do(r, http.MethodPost, partiesPath, tc.body, tc.duties, tc.withTenant)
			got := decode(t, rec.Body.String())
			if rec.Code != tc.wantStatus || got["code"] != tc.wantCode {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			if tc.wantField != "" {
				if _, ok := got["errors"].(map[string]any)[tc.wantField]; !ok {
					t.Fatalf("errors do not name %s: %s", tc.wantField, rec.Body)
				}
			}
			if len(seq.calls) != 0 {
				t.Fatal("numbering was called for a rejected request")
			}
		})
	}
}

func TestCreateParty_NumberingUnavailable(t *testing.T) {
	for name, seq := range map[string]api.Sequencer{"empty url": numbering.Unavailable{}, "failing client": &fakeSequencer{err: httpx.NewProblem(503, numbering.CodeUnavailable, "down")}} {
		t.Run(name, func(t *testing.T) {
			r, _ := partyRouter(&fakeParties{}, seq)
			rec := do(r, http.MethodPost, partiesPath, `{"partyType":"organization","legalName":"Acme"}`, "party.write", true)
			if rec.Code != http.StatusServiceUnavailable || decode(t, rec.Body.String())["code"] != numbering.CodeUnavailable {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestGetParty(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		duties   string
		wantCode int
		wantDOB  any
		wantBody string
	}{
		{"dob masked without pii.read", knownID, "party.read", 200, "••••", ""},
		{"dob visible with pii.read", knownID, "party.read,pii.read", 200, "1990-02-03", ""},
		{"merged redirects", mergedID, "party.read", 200, nil, `"redirected":true`},
		{"unknown id", "00000000-0000-0000-0000-000000000000", "party.read", 404, nil, "PTY-PARTY-NOT-FOUND"},
		{"missing duty", knownID, "party.write", 403, nil, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := partyRouter(&fakeParties{}, &fakeSequencer{})
			rec := do(r, http.MethodGet, partiesPath+"/"+tc.id, "", tc.duties, true)
			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			if tc.wantDOB != nil && decode(t, rec.Body.String())["dob"] != tc.wantDOB {
				t.Fatalf("got %s", rec.Body)
			}
			if tc.wantCode == 200 && rec.Header().Get("ETag") != `"4"` {
				t.Fatalf("etag %q", rec.Header().Get("ETag"))
			}
			if tc.id == mergedID && !strings.Contains(rec.Body.String(), `"requestedId":"`+mergedID+`"`) {
				t.Fatalf("got %s", rec.Body)
			}
		})
	}
}

func TestGetParty_UnsetDOBStaysAbsent(t *testing.T) {
	r, _ := partyRouter(&fakeParties{}, &fakeSequencer{})
	rec := do(r, http.MethodGet, partiesPath+"/"+mergedID, "", "party.read", true)
	if _, has := decode(t, rec.Body.String())["dob"]; has {
		t.Fatalf("got %s", rec.Body)
	}
}

func TestPatchParty_VersionSources(t *testing.T) {
	tests := []struct {
		name       string
		ifMatch    string
		body       string
		wantStatus int
		wantVer    int
	}{
		{"if-match", `"3"`, `{"status":"inactive"}`, 200, 3},
		{"body version", "", `{"status":"inactive","version":5}`, 200, 5},
		{"if-match wins over body", `"2"`, `{"version":9}`, 200, 2},
		{"missing", "", `{"status":"inactive"}`, 428, 0},
		{"weak validator", `W/"3"`, `{}`, 428, 0},
		{"non numeric", `"abc"`, `{}`, 428, 0},
		{"zero", `"0"`, `{}`, 428, 0},
		{"negative body", "", `{"version":-1}`, 428, 0},
		{"unknown field", `"1"`, `{"id":"x"}`, 400, 0},
		{"status as number", `"1"`, `{"status":3}`, 400, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parties := &fakeParties{}
			r, _ := partyRouter(parties, &fakeSequencer{})
			req := newRequest(http.MethodPatch, partiesPath+"/"+knownID, tc.body, "party.write")
			if tc.ifMatch != "" {
				req.Header.Set("If-Match", tc.ifMatch)
			}
			rec := serve(r, req)
			if rec.Code != tc.wantStatus || parties.updateVer != tc.wantVer {
				t.Fatalf("got %d version=%d %s", rec.Code, parties.updateVer, rec.Body)
			}
			if tc.wantStatus == 428 && decode(t, rec.Body.String())["code"] != "PTY-VERSION-REQUIRED" {
				t.Fatalf("got %s", rec.Body)
			}
			if tc.wantStatus == 200 && rec.Header().Get("ETag") == "" {
				t.Fatal("missing ETag")
			}
		})
	}
}

func TestPatchParty_StoreProblemsPassThrough(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"stale version", httpx.NewProblem(409, store.CodeVersionConflict, "stale"), 409, store.CodeVersionConflict},
		{"transition", httpx.NewProblem(422, store.CodeStatusTransition, "no"), 422, store.CodeStatusTransition},
		{"not found", store.ErrNotFound, 404, "PTY-PARTY-NOT-FOUND"},
		{"bad id", store.ErrInvalidID, 404, "PTY-PARTY-NOT-FOUND"},
		{"database failure", context.DeadlineExceeded, 500, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := partyRouter(&fakeParties{err: tc.err}, &fakeSequencer{})
			req := newRequest(http.MethodPatch, partiesPath+"/"+knownID, `{"status":"inactive"}`, "party.write")
			req.Header.Set("If-Match", `"1"`)
			rec := serve(r, req)
			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestPatchParty_RequiresWriteDuty(t *testing.T) {
	r, _ := partyRouter(&fakeParties{}, &fakeSequencer{})
	if rec := do(r, http.MethodPatch, partiesPath+"/"+knownID, `{}`, "party.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}
}
