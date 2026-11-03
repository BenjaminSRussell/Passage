package store_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutyPerfManage = "hr.performance.manage"
	dutyPerfAdmin  = "hr.performance.admin"
	dutyPerfSelf   = "hr.performance.self"
)

func TestPerformanceReviewOverHTTPConfinesManagersAndBlocksSelfSignOff(t *testing.T) {
	o := newOrg(t)
	rep, peer := o.workers["rep"].ID, o.workers["peer"].ID
	period := `{"periodStart":"2026-01-01","periodEnd":"2026-12-31"}`

	opened := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/workers/"+rep+"/reviews", period, dutyPerfManage)
	if opened.Code != http.StatusCreated {
		t.Fatalf("manager opens a report's review: status %d body %s", opened.Code, opened.Body.String())
	}
	review := decodeBody[store.Review](t, opened)

	for name, rec := range map[string]func() int{
		"manager opening a peer's review": func() int {
			return o.as(o.users["mgr"], http.MethodPost, "/v1/hr/workers/"+peer+"/reviews", period, dutyPerfManage).Code
		},
		"manager listing a peer's goals": func() int {
			return o.as(o.users["mgr"], http.MethodGet, "/v1/hr/workers/"+peer+"/goals", "", dutyPerfManage).Code
		},
		"a peer reading the review": func() int {
			return o.as(o.users["peer"], http.MethodGet, "/v1/hr/reviews/"+review.ID, "", dutyPerfManage).Code
		},
		"the skip-level manager reading the review": func() int {
			return o.as(o.users["ceo"], http.MethodGet, "/v1/hr/reviews/"+review.ID, "", dutyPerfManage).Code
		},
		"a peer signing off": func() int {
			return o.as(o.users["peer"], http.MethodPost, "/v1/hr/reviews/"+review.ID+"/sign-off", `{}`, dutyPerfManage).Code
		},
	} {
		if code := rec(); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want the uniform 404", name, code)
		}
	}
	if rec := o.as(o.users["mgr"], http.MethodGet, "/v1/hr/reviews/"+ids.New(), "", dutyPerfManage); rec.Code != http.StatusNotFound {
		t.Errorf("unknown review: status %d, want 404", rec.Code)
	}

	early := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/reviews/"+review.ID+"/sign-off", `{"overallRating":4}`, dutyPerfManage)
	if early.Code != http.StatusConflict || !strings.Contains(early.Body.String(), store.CodeReviewState) {
		t.Fatalf("sign-off before the self-assessment: status %d body %s, want 409 %s", early.Code, early.Body.String(), store.CodeReviewState)
	}
	if rec := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/me/reviews/"+review.ID+"/self-assessment", `{"achievements":"x"}`, dutyPerfSelf); rec.Code != http.StatusNotFound {
		t.Fatalf("manager writing a report's self-assessment: status %d, want 404", rec.Code)
	}
	submitted := o.as(o.users["rep"], http.MethodPost, "/v1/hr/me/reviews/"+review.ID+"/self-assessment", `{"achievements":"Closed 40 tickets","development":"Lead a team"}`, dutyPerfSelf)
	if submitted.Code != http.StatusOK || decodeBody[store.Review](t, submitted).Status != "self_submitted" {
		t.Fatalf("self-assessment: status %d body %s", submitted.Code, submitted.Body.String())
	}

	own := o.as(o.users["rep"], http.MethodPost, "/v1/hr/reviews/"+review.ID+"/sign-off", `{"overallRating":5}`, dutyPerfManage)
	if own.Code != http.StatusForbidden || !strings.Contains(own.Body.String(), store.CodeReviewSelf) {
		t.Fatalf("worker signing off their own review: status %d body %s, want 403 %s", own.Code, own.Body.String(), store.CodeReviewSelf)
	}
	adminOwn := o.as(o.users["rep"], http.MethodPost, "/v1/hr/reviews/"+review.ID+"/sign-off", `{"overallRating":5}`, dutyPerfAdmin)
	if adminOwn.Code != http.StatusForbidden {
		t.Fatalf("an admin signing off their own review: status %d, want 403", adminOwn.Code)
	}
	if rec := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/reviews/"+review.ID+"/sign-off", `{"overallRating":9}`, dutyPerfManage); rec.Code != http.StatusBadRequest {
		t.Fatalf("rating 9: status %d, want 400", rec.Code)
	}
	signed := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/reviews/"+review.ID+"/sign-off", `{"managerComments":"Solid","overallRating":4}`, dutyPerfManage)
	if got := decodeBody[store.Review](t, signed); signed.Code != http.StatusOK || got.Status != "signed_off" || got.SignedOffBy == nil {
		t.Fatalf("sign-off: status %d body %s", signed.Code, signed.Body.String())
	}

	mine := o.as(o.users["rep"], http.MethodGet, "/v1/hr/me/reviews", "", dutyPerfSelf)
	if list := decodeBody[listOf[store.Review]](t, mine); mine.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].Status != "signed_off" {
		t.Fatalf("my reviews: status %d body %s", mine.Code, mine.Body.String())
	}
	peers := o.as(o.users["peer"], http.MethodGet, "/v1/hr/me/reviews", "", dutyPerfSelf)
	if len(decodeBody[listOf[store.Review]](t, peers).Items) != 0 {
		t.Fatal("a peer must not see the rep's reviews through /me/reviews")
	}
	if rec := o.as(o.users["peer"], http.MethodGet, "/v1/hr/reviews/"+review.ID, "", dutyPerfAdmin); rec.Code != http.StatusOK {
		t.Fatalf("hr.performance.admin reads any review: status %d", rec.Code)
	}
}

func TestPerformanceGoalsOverHTTP(t *testing.T) {
	o := newOrg(t)
	rep, peer := o.workers["rep"].ID, o.workers["peer"].ID
	created := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/workers/"+rep+"/goals", `{"title":"Reduce backlog","dueOn":"2026-12-31"}`, dutyPerfManage)
	if created.Code != http.StatusCreated {
		t.Fatalf("create goal: status %d body %s", created.Code, created.Body.String())
	}
	goal := decodeBody[store.Goal](t, created)
	if rec := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/workers/"+peer+"/goals", `{"title":"Not mine"}`, dutyPerfManage); rec.Code != http.StatusNotFound {
		t.Fatalf("goal for a peer: status %d, want 404", rec.Code)
	}
	if rec := o.as(o.users["peer"], http.MethodPost, "/v1/hr/goals/"+goal.ID+"/progress", `{"progressPercent":50}`, dutyPerfManage); rec.Code != http.StatusNotFound {
		t.Fatalf("a peer updating the goal: status %d, want 404", rec.Code)
	}
	updated := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/goals/"+goal.ID+"/progress", `{"status":"ACHIEVED"}`, dutyPerfManage)
	if got := decodeBody[store.Goal](t, updated); updated.Code != http.StatusOK || got.ProgressPct != 100 || got.Status != "ACHIEVED" {
		t.Fatalf("achieve: status %d body %s", updated.Code, updated.Body.String())
	}
	mine := o.as(o.users["rep"], http.MethodGet, "/v1/hr/me/goals", "", dutyPerfSelf)
	if list := decodeBody[listOf[store.Goal]](t, mine); mine.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != goal.ID {
		t.Fatalf("my goals: status %d body %s", mine.Code, mine.Body.String())
	}
	if rec := o.as(ids.New(), http.MethodGet, "/v1/hr/me/goals", "", dutyPerfSelf); rec.Code != http.StatusNotFound {
		t.Fatalf("unlinked sign-in: status %d, want 404", rec.Code)
	}
}

func TestPerformanceManagerCannotSetTheirOwnGoals(t *testing.T) {
	o := newOrg(t)
	mgr := o.workers["mgr"].ID
	if rec := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/workers/"+mgr+"/goals", `{"title":"Give myself a goal"}`, dutyPerfManage); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), store.CodeGoalSelf) {
		t.Fatalf("own goal: status %d body %s, want 403 %s", rec.Code, rec.Body.String(), store.CodeGoalSelf)
	}
	if rec := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/workers/"+o.workers["rep"].ID+"/goals", `{"title":"x","legalEntityId":"nope"}`, dutyPerfManage); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad legalEntityId: status %d, want 400", rec.Code)
	}
	created := o.as(o.users["ceo"], http.MethodPost, "/v1/hr/workers/"+mgr+"/goals", `{"title":"From the boss"}`, dutyPerfManage)
	if created.Code != http.StatusCreated {
		t.Fatalf("boss sets the manager's goal: status %d body %s", created.Code, created.Body.String())
	}
	goal := decodeBody[store.Goal](t, created)
	if rec := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/goals/"+goal.ID+"/progress", `{"progressPercent":100}`, dutyPerfManage); rec.Code != http.StatusForbidden {
		t.Fatalf("own goal progress: status %d, want 403", rec.Code)
	}
}

func TestPerformanceEmployeeUpdatesOnlyTheirOwnGoal(t *testing.T) {
	o := newOrg(t)
	created := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/workers/"+o.workers["rep"].ID+"/goals", `{"title":"Reduce backlog"}`, dutyPerfManage)
	if created.Code != http.StatusCreated {
		t.Fatalf("create goal: status %d body %s", created.Code, created.Body.String())
	}
	goal := decodeBody[store.Goal](t, created)
	path := "/v1/hr/me/goals/" + goal.ID + "/progress"

	if rec := o.as(o.users["peer"], http.MethodPost, path, `{"progressPercent":50}`, dutyPerfSelf); rec.Code != http.StatusNotFound {
		t.Fatalf("a peer updating the rep's goal: status %d, want 404", rec.Code)
	}
	if rec := o.as(o.users["mgr"], http.MethodPost, path, `{"progressPercent":50}`, dutyPerfSelf); rec.Code != http.StatusNotFound {
		t.Fatalf("the manager through the self route: status %d, want 404", rec.Code)
	}
	if rec := o.as(o.users["rep"], http.MethodPost, "/v1/hr/me/goals/"+ids.New()+"/progress", `{"progressPercent":50}`, dutyPerfSelf); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown goal: status %d, want 404", rec.Code)
	}
	if rec := o.as(o.users["rep"], http.MethodPost, path, `{"status":"CANCELLED"}`, dutyPerfSelf); rec.Code != http.StatusBadRequest {
		t.Fatalf("employee cancelling: status %d, want 400", rec.Code)
	}
	half := o.as(o.users["rep"], http.MethodPost, path, `{"progressPercent":50}`, dutyPerfSelf)
	if got := decodeBody[store.Goal](t, half); half.Code != http.StatusOK || got.ProgressPct != 50 || got.Status != "OPEN" {
		t.Fatalf("progress: status %d body %s", half.Code, half.Body.String())
	}
	if rec := o.as(o.users["rep"], http.MethodPost, path, `{"status":"ACHIEVED","progressPercent":60}`, dutyPerfSelf); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("achieved below 100: status %d, want 422", rec.Code)
	}
	done := o.as(o.users["rep"], http.MethodPost, path, `{"status":"ACHIEVED"}`, dutyPerfSelf)
	if got := decodeBody[store.Goal](t, done); done.Code != http.StatusOK || got.ProgressPct != 100 || got.Status != "ACHIEVED" {
		t.Fatalf("achieve: status %d body %s", done.Code, done.Body.String())
	}
	if again := o.as(o.users["rep"], http.MethodPost, path, `{"status":"ACHIEVED"}`, dutyPerfSelf); again.Code != http.StatusOK {
		t.Fatalf("the same update again: status %d body %s, want 200", again.Code, again.Body.String())
	}

	if rec := o.as(o.users["mgr"], http.MethodPost, "/v1/hr/goals/"+goal.ID+"/progress", `{"status":"CANCELLED"}`, dutyPerfManage); rec.Code != http.StatusOK {
		t.Fatalf("manager cancelling: status %d body %s", rec.Code, rec.Body.String())
	}
	cancelled := o.as(o.users["rep"], http.MethodPost, path, `{"progressPercent":10}`, dutyPerfSelf)
	if cancelled.Code != http.StatusConflict || !strings.Contains(cancelled.Body.String(), store.CodeGoalState) {
		t.Fatalf("progress on a cancelled goal: status %d body %s, want 409 %s", cancelled.Code, cancelled.Body.String(), store.CodeGoalState)
	}
}
