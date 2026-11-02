package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

const (
	ecrReviewDuty = "engineering.ecr.review"
	ecrs          = "/v1/engineering/ecrs"
	ecrID         = "00000000-0000-0000-0000-0000000000c1"
)

type ecrState struct {
	list   []domain.Ecr
	filter store.EcrFilter
	saved  []domain.Ecr
	prev   []domain.EcrState
	err    error
}

func (f *fakeStore) CreateEcr(_ context.Context, e domain.Ecr, _ store.Idem) (domain.Ecr, error) {
	f.ecrs.list = append(f.ecrs.list, e)
	return e, f.ecrs.err
}

func (f *fakeStore) GetEcr(_ context.Context, id string) (domain.Ecr, error) {
	for _, e := range f.ecrs.list {
		if e.ID == id {
			return e, nil
		}
	}
	return domain.Ecr{}, f.ecrs.err
}

func (f *fakeStore) ListEcrs(_ context.Context, flt store.EcrFilter, _ string, limit int) (store.Page[domain.Ecr], error) {
	f.ecrs.filter = flt
	page := store.Page[domain.Ecr]{Items: f.ecrs.list}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextID = page.Items[limit-1].ID
	}
	return page, f.ecrs.err
}

func (f *fakeStore) SaveEcr(_ context.Context, e domain.Ecr, prev domain.EcrState) error {
	f.ecrs.saved = append(f.ecrs.saved, e)
	f.ecrs.prev = append(f.ecrs.prev, prev)
	return f.ecrs.err
}

func ecrIn(state domain.EcrState) *fakeStore {
	return &fakeStore{ecrs: &ecrState{list: []domain.Ecr{{ID: ecrID, Number: "ECR-1", Title: "Swap", Priority: "high", State: state, Products: []string{"BIKE"}, RequestedBy: "rita"}}}}
}

func ecrAct(h http.Handler, action, duties, user, body string) (int, string) {
	rec := do(h, http.MethodPost, ecrs+"/"+ecrID+":"+action, body, duties, true, "X-Passage-User", user)
	return rec.Code, rec.Body.String()
}

const validEcr = `{"number":"ECR-9","title":"Swap the wheel","reason":"fatigue crack","priority":"high","products":["BIKE","WHEEL"]}`

func TestCreateEcr(t *testing.T) {
	f := &fakeStore{ecrs: &ecrState{}}
	r, _ := newRouter(f, approves)
	rec := do(r, http.MethodPost, ecrs, validEcr, allDuties, true, "X-Passage-User", "rita")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"state":"draft"`) || !strings.Contains(rec.Body.String(), `"requestedBy":"rita"`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if len(f.ecrs.list) != 1 || f.ecrs.list[0].Number != "ECR-9" {
		t.Fatalf("stored = %+v", f.ecrs.list)
	}
	for name, body := range map[string]string{
		"no title":   `{"number":"E","priority":"low","products":["A"]}`,
		"bad prio":   `{"number":"E","title":"t","priority":"now","products":["A"]}`,
		"no product": `{"number":"E","title":"t","priority":"low","products":[]}`,
		"unknown":    `{"number":"E","title":"t","priority":"low","products":["A"],"state":"approved","extra":1}`,
	} {
		if rec := do(r, http.MethodPost, ecrs, body, allDuties, true, "X-Passage-User", "rita"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s; want 400", name, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPost, ecrs, validEcr, allDuties, true); rec.Code != http.StatusUnauthorized {
		t.Errorf("no user = %d; want 401", rec.Code)
	}
	if rec := do(r, http.MethodPost, ecrs, validEcr, "engineering.read", true, "X-Passage-User", "rita"); rec.Code != http.StatusForbidden {
		t.Errorf("read-only = %d; want 403", rec.Code)
	}
}

func TestCreateEcr_StateIsNotCallerSupplied(t *testing.T) {
	f := &fakeStore{ecrs: &ecrState{}}
	r, _ := newRouter(f, approves)
	rec := do(r, http.MethodPost, ecrs, strings.Replace(validEcr, `"products"`, `"state":"approved","requestedBy":"mallory","products"`, 1), allDuties, true, "X-Passage-User", "rita")
	if rec.Code != http.StatusCreated || f.ecrs.list[0].State != domain.EcrDraft || f.ecrs.list[0].RequestedBy != "rita" {
		t.Fatalf("create = %d %s %+v", rec.Code, rec.Body, f.ecrs.list)
	}
}

func TestListAndGetEcr(t *testing.T) {
	f := ecrIn(domain.EcrApproved)
	f.ecrs.list = append(f.ecrs.list, domain.Ecr{ID: "00000000-0000-0000-0000-0000000000c2", Number: "ECR-2", State: domain.EcrDraft})
	r, _ := newRouter(f, approves)
	rec := do(r, http.MethodGet, ecrs+"?limit=1&state=approved&productId=BIKE", "", "engineering.read", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nextCursor"`) || f.ecrs.filter.State != "approved" || f.ecrs.filter.ProductID != "BIKE" {
		t.Fatalf("list = %d %s %+v", rec.Code, rec.Body, f.ecrs.filter)
	}
	if rec := do(r, http.MethodGet, ecrs+"?state=bogus", "", "engineering.read", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad state = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, ecrs+"?productId="+strings.Repeat("x", 65), "", "engineering.read", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("long product = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, ecrs+"/"+ecrID, "", "engineering.read", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ECR-1"`) {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, ecrs+"/nope", "", "engineering.read", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, ecrs, "", "", true); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty = %d", rec.Code)
	}
}

func TestEcrWorkflow(t *testing.T) {
	tests := []struct {
		action string
		from   domain.EcrState
		to     domain.EcrState
		duties string
		user   string
	}{
		{"submit", domain.EcrDraft, domain.EcrSubmitted, "engineering.bom.write", "rita"},
		{"review", domain.EcrSubmitted, domain.EcrUnderReview, ecrReviewDuty, "carol"},
		{"approve", domain.EcrUnderReview, domain.EcrApproved, ecrReviewDuty, "carol"},
		{"reject", domain.EcrUnderReview, domain.EcrRejected, ecrReviewDuty, "carol"},
		{"cancel", domain.EcrSubmitted, domain.EcrCancelled, "engineering.bom.write", "rita"},
	}
	for _, tc := range tests {
		t.Run(tc.action, func(t *testing.T) {
			f := ecrIn(tc.from)
			r, _ := newRouter(f, approves)
			code, body := ecrAct(r, tc.action, tc.duties, tc.user, `{"reason":"because"}`)
			if code != http.StatusOK || !strings.Contains(body, `"state":"`+string(tc.to)+`"`) {
				t.Fatalf("%s = %d %s", tc.action, code, body)
			}
			if len(f.ecrs.saved) != 1 || f.ecrs.prev[0] != tc.from || f.ecrs.saved[0].State != tc.to {
				t.Fatalf("saved = %+v prev %v", f.ecrs.saved, f.ecrs.prev)
			}
			if code, _ := ecrAct(r, tc.action, "engineering.read", tc.user, ""); code != http.StatusForbidden {
				t.Fatalf("%s without its duty = %d; want 403", tc.action, code)
			}
		})
	}
}

func TestEcrWorkflow_RepeatIsIdempotentAndWrongStateConflicts(t *testing.T) {
	f := ecrIn(domain.EcrApproved)
	r, _ := newRouter(f, approves)
	if code, body := ecrAct(r, "approve", ecrReviewDuty, "carol", ""); code != http.StatusOK || len(f.ecrs.saved) != 0 {
		t.Fatalf("repeat approve = %d %s saved %d", code, body, len(f.ecrs.saved))
	}
	if code, body := ecrAct(r, "reject", ecrReviewDuty, "carol", ""); code != http.StatusConflict || !strings.Contains(body, domain.CodeEcrState) {
		t.Fatalf("reject approved = %d %s", code, body)
	}
	if code, body := ecrAct(r, "cancel", "engineering.bom.write", "rita", ""); code != http.StatusConflict || !strings.Contains(body, domain.CodeEcrState) {
		t.Fatalf("cancel approved = %d %s", code, body)
	}
}

func TestEcrWorkflow_SeparationOfDuties(t *testing.T) {
	for _, action := range []string{"review", "approve", "reject"} {
		from := domain.EcrUnderReview
		if action == "review" {
			from = domain.EcrSubmitted
		}
		f := ecrIn(from)
		r, _ := newRouter(f, approves)
		code, body := ecrAct(r, action, ecrReviewDuty, "rita", "")
		if code != http.StatusForbidden || !strings.Contains(body, domain.CodeEcrSelfApproval) || len(f.ecrs.saved) != 0 {
			t.Fatalf("requester %s = %d %s", action, code, body)
		}
		if code, body := ecrAct(r, action, ecrReviewDuty, "", ""); code != http.StatusUnprocessableEntity || !strings.Contains(body, domain.CodeEcrActorRequired) {
			t.Fatalf("anonymous %s = %d %s", action, code, body)
		}
	}
}

func TestEcrWorkflow_OnlyTheRequesterWithdraws(t *testing.T) {
	f := ecrIn(domain.EcrUnderReview)
	r, _ := newRouter(f, approves)
	if code, body := ecrAct(r, "cancel", "engineering.bom.write", "carol", ""); code != http.StatusForbidden || !strings.Contains(body, domain.CodeEcrNotRequester) || len(f.ecrs.saved) != 0 {
		t.Fatalf("withdraw by another user = %d %s saved %d", code, body, len(f.ecrs.saved))
	}
}

func TestEcrWorkflow_ReasonTooLong(t *testing.T) {
	r, _ := newRouter(ecrIn(domain.EcrUnderReview), approves)
	if code, body := ecrAct(r, "reject", ecrReviewDuty, "carol", `{"reason":"`+strings.Repeat("x", 201)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("long reason = %d %s", code, body)
	}
}

func TestEcrWorkflow_StoreFailures(t *testing.T) {
	f := ecrIn(domain.EcrDraft)
	f.ecrs.err = context.DeadlineExceeded
	r, _ := newRouter(f, approves)
	if code, _ := ecrAct(r, "submit", "engineering.bom.write", "rita", ""); code != http.StatusInternalServerError {
		t.Fatalf("store failure = %d; want 500", code)
	}
}

func TestCreateEco_FromEcr(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	body := `{"number":"ECO-1","ecrId":"` + ecrID + `","replaces":[{"oldVersionId":"` + oldVersion + `","newVersionId":"` + newVersion + `"}]}`
	rec := do(r, http.MethodPost, ecos, body, allDuties, true, "X-Passage-User", "rita")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"ecrId":"`+ecrID+`"`) {
		t.Fatalf("create from ecr = %d %s", rec.Code, rec.Body)
	}
	bad := strings.Replace(body, ecrID, "not-a-uuid", 1)
	if rec := do(r, http.MethodPost, ecos, bad, allDuties, true, "X-Passage-User", "rita"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ecrId") {
		t.Fatalf("bad ecr id = %d %s", rec.Code, rec.Body)
	}
}
