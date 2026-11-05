package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/assets/internal/api"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

type leaseGLStub struct {
	glStub
	leasePost store.LeaseGLPostRequest
	profile   store.LeasePostingProfile
	profileIf int
}

func (g *leaseGLStub) PostLeasesToGL(_ context.Context, _ ledgerclient.Ledger, in store.LeaseGLPostRequest) (store.LeaseGLPostResult, error) {
	g.leasePost = in
	return store.LeaseGLPostResult{Booked: []store.LeaseGLBooked{{LeaseID: "l1", LeaseNumber: "L-1", Period: 0, EntryID: "e1", Lines: 2}},
		Failed: []store.LeaseGLFailed{{LeaseID: "l2", LeaseNumber: "L-2", Period: 3, Code: "LEDGER_PERIOD_CLOSED"}}}, nil
}

func (g *leaseGLStub) GetLeasePostings(_ context.Context, id string) (store.LeasePostings, error) {
	e := "entry-1"
	return store.LeasePostings{LeaseID: id, RecognitionJournalID: &e, Periods: []store.LeasePostedPeriod{{PeriodNo: 1, PeriodEnd: "2026-12-31", PostedJournalID: &e}, {PeriodNo: 2, PeriodEnd: "2027-12-31"}}}, nil
}

func (g *leaseGLStub) GetLeasePostingProfile(_ context.Context, le string) (store.LeasePostingProfile, error) {
	p := store.PlaceholderLeaseProfile
	p.LegalEntityID = le
	return p, nil
}

func (g *leaseGLStub) SaveLeasePostingProfile(_ context.Context, in store.LeasePostingProfile, ifMatch int, _ string) (store.LeasePostingProfile, bool, error) {
	g.profile, g.profileIf = in, ifMatch
	in.Version = ifMatch + 1
	return in, ifMatch == 0, nil
}

func TestLeaseGL_PostingNeedsTheJournalDutyAndValidInput(t *testing.T) {
	stub := &leaseGLStub{}
	h := glServer(stub)
	body := `{"legalEntityId":"` + testLE + `","ledgerId":"` + ids.New() + `","postThrough":"2026-12-31"}`
	if rec := call(h, http.MethodPost, "/v1/leases/gl-postings", body, "assets.lease.maintain", false); rec.Code != http.StatusForbidden {
		t.Fatalf("lease maintenance must not book: %d", rec.Code)
	}
	rec := call(h, http.MethodPost, "/v1/leases/gl-postings", body, "assets.journal.post", false)
	var got store.LeaseGLPostResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got.Booked) != 1 || got.Failed[0].Code != "LEDGER_PERIOD_CLOSED" || stub.leasePost.PostThrough.String() != "2026-12-31" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for name, bad := range map[string]string{
		"no ledger":         `{"legalEntityId":"` + testLE + `","postThrough":"2026-12-31"}`,
		"no date":           `{"legalEntityId":"` + testLE + `","ledgerId":"` + ids.New() + `"}`,
		"bad date":          `{"legalEntityId":"` + testLE + `","ledgerId":"` + ids.New() + `","postThrough":"31/12/2026"}`,
		"bad lease":         `{"legalEntityId":"` + testLE + `","ledgerId":"` + ids.New() + `","postThrough":"2026-12-31","leaseId":"x"}`,
		"another legal ent": `{"legalEntityId":"` + ids.New() + `","ledgerId":"` + ids.New() + `","postThrough":"2026-12-31"}`,
	} {
		if rec := call(h, http.MethodPost, "/v1/leases/gl-postings", bad, "assets.journal.post", false); rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestLeaseGL_PostingsOfALeaseAndTheProfile(t *testing.T) {
	stub := &leaseGLStub{}
	h := glServer(stub)
	id := ids.New()
	rec := call(h, http.MethodGet, "/v1/leases/"+id+"/gl-postings", "", "assets.lease.maintain", false)
	var got store.LeasePostings
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.LeaseID != id || got.Periods[1].PostedJournalID != nil || got.Periods[0].PostedJournalID == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, http.MethodGet, "/v1/leases/not-a-uuid/gl-postings", "", "assets.lease.maintain", false); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/leases/"+id+"/gl-postings", "", "assets.asset.view", false); rec.Code != http.StatusForbidden {
		t.Fatalf("view duty must not read lease postings: %d", rec.Code)
	}

	read := call(h, http.MethodGet, "/v1/leases/posting-profile?legalEntityId="+testLE, "", "assets.asset.configure", false)
	var prof store.LeasePostingProfile
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &prof) != nil || !prof.Placeholder || prof.ROUAsset != "1700" || read.Header().Get("ETag") != "" {
		t.Fatalf("placeholder read: %d %s etag %q", read.Code, read.Body, read.Header().Get("ETag"))
	}
	if rec := call(h, http.MethodGet, "/v1/leases/posting-profile", "", "assets.lease.maintain", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing legal entity: %d", rec.Code)
	}
	profile := `{"legalEntityId":"` + testLE + `","rouAssetAccount":"1750","rouAccumulatedDepreciationAccount":"1795","liabilityAccount":"2450","interestExpenseAccount":"7250",` +
		`"depreciationExpenseAccount":"6850","paymentClearingAccount":"2150","remeasurementGainAccount":"7150"}`
	if rec := call(h, http.MethodPut, "/v1/leases/posting-profile", profile, "assets.lease.maintain", false); rec.Code != http.StatusForbidden {
		t.Fatalf("profile needs assets.asset.configure: %d", rec.Code)
	}
	rec = call(h, http.MethodPut, "/v1/leases/posting-profile", profile, "assets.asset.configure", false)
	if rec.Code != http.StatusCreated || stub.profile.Liability != "2450" || stub.profileIf != 0 || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if stub.profile.TerminationGainLoss != "7150" || stub.profile.ExemptLeaseExpense != "6850" {
		t.Fatalf("omitted accounts default to the remeasurement gain and depreciation expense accounts: %+v", stub.profile)
	}
	rec = do(h, req{method: http.MethodPut, path: "/v1/leases/posting-profile", body: profile, duties: "assets.asset.configure", headers: map[string]string{"If-Match": `"1"`}})
	if rec.Code != http.StatusOK || stub.profileIf != 1 {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	explicit := strings.Replace(profile, `"remeasurementGainAccount":"7150"`, `"remeasurementGainAccount":"7150","terminationGainLossAccount":"7160","exemptLeaseExpenseAccount":"6860"`, 1)
	if rec := call(h, http.MethodPut, "/v1/leases/posting-profile", explicit, "assets.asset.configure", false); rec.Code != http.StatusCreated || stub.profile.TerminationGainLoss != "7160" || stub.profile.ExemptLeaseExpense != "6860" {
		t.Fatalf("explicit accounts: %d %s %+v", rec.Code, rec.Body, stub.profile)
	}
	if rec := call(h, http.MethodPut, "/v1/leases/posting-profile", strings.Replace(explicit, `"7160"`, `"a b"`, 1), "assets.asset.configure", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid termination account: %d", rec.Code)
	}
	if bad := call(h, http.MethodPut, "/v1/leases/posting-profile", `{"legalEntityId":"`+testLE+`","liabilityAccount":"a b"}`, "assets.asset.configure", false); bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid accounts: %d", bad.Code)
	}
}

func TestLeaseGL_NotConfiguredAnswers503(t *testing.T) {
	plain := server(newFake())
	for _, c := range []struct{ method, path, body, duties string }{
		{http.MethodPost, "/v1/leases/gl-postings", `{}`, "assets.journal.post"},
		{http.MethodGet, "/v1/leases/" + ids.New() + "/gl-postings", ``, "assets.lease.maintain"},
		{http.MethodGet, "/v1/leases/posting-profile?legalEntityId=" + testLE, ``, "assets.lease.maintain"},
	} {
		if rec := call(plain, c.method, c.path, c.body, c.duties, false); rec.Code != http.StatusServiceUnavailable || problem(t, rec).Code != api.CodeGLUnavailable {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}
