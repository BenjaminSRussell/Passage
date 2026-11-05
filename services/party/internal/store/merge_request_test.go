package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

// mergeApprover is the principal a verified workflow-ts tenant-level token maps to:
// no legal entity, the service-only merge duty, and the approver as the actor.
func mergeApprover(user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: user, Service: store.MergeExecutor,
		Duties: map[string]struct{}{store.DutyMergeExecute: {}}})
}

func requestMerge(t *testing.T, f mergeFixture, s *store.Postgres) domain.MergeRequest {
	t.Helper()
	m, created, err := s.RequestMerge(f.merger, store.MergeRequestInput{SurvivorID: f.winner, MergedID: f.loser, Rules: map[string]domain.Rule{"time_zone": domain.KeepLoser}})
	if err != nil || !created || m.Status != domain.MergePending || m.RequestedBy != "merger" {
		t.Fatalf("request = %+v created %v err %v", m, created, err)
	}
	return m
}

func TestMergeRequest_RequestMergesNothingUntilAnotherUserApproves(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	m := requestMerge(t, f, s)
	if got := scalar(t, f, `select status || '/' || (select count(*) from party_merge)::text from party where id = $1`, f.loser); got != "active/0" {
		t.Fatalf("after request: loser %s, want active and no merge", got)
	}
	var evt struct {
		MergeRequestID string `json:"merge_request_id"`
		RequestedBy    string `json:"requested_by"`
	}
	if err := json.Unmarshal([]byte(scalar(t, f, `select (payload->'data')::text from outbox where type = 'dev.passage.party.merge.requested.v1'`)), &evt); err != nil || evt.MergeRequestID != m.ID || evt.RequestedBy != "merger" {
		t.Fatalf("merge.requested = %+v %v", evt, err)
	}

	approved, err := s.DecideMerge(mergeApprover("ada"), m.ID, true, "")
	if err != nil || approved.Status != domain.MergeApproved || approved.DecidedBy == nil || *approved.DecidedBy != "ada" || approved.MergeID == nil || approved.Replayed {
		t.Fatalf("approve = %+v %v", approved, err)
	}
	got := scalar(t, f, `select l.status || ':' || (l.merged_into_id = w.id)::text || ':' || w.time_zone
		|| ':' || (select count(*) from customer_role where party_id = l.id)::text
		|| ':' || (select count(*) from outbox where type = 'dev.passage.party.party.merged.v1' and aggregate_id = w.id::text)::text
		from party l, party w where l.id = $1 and w.id = $2`, f.loser, f.winner)
	if want := "merged:true:CET:0:1"; got != want {
		t.Errorf("after approval (status:redirect:survivor time zone:loser roles:merged events) = %q, want %q", got, want)
	}
	if got := scalar(t, f, `select id::text from party_merge where merged_party_id = $1`, f.loser); got != *approved.MergeID {
		t.Errorf("merge_id %s, party_merge %s", *approved.MergeID, got)
	}

	replay, err := s.DecideMerge(mergeApprover("ada"), m.ID, true, "")
	if err != nil || !replay.Replayed || *replay.MergeID != *approved.MergeID {
		t.Fatalf("replayed approval = %+v %v", replay, err)
	}
	if got := scalar(t, f, `select count(*)::text from party_merge`); got != "1" {
		t.Fatalf("a replay merged again: %s merges", got)
	}
	for name, ctx := range map[string]context.Context{"another approver": mergeApprover("bob"), "the approver rejecting": mergeApprover("ada")} {
		_, err := s.DecideMerge(ctx, m.ID, name == "another approver", "")
		wantProblem(t, err, http.StatusConflict, store.CodeMergeDecided)
	}
	reader := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityA, UserID: "reader", Duties: testkit.ScopeDuties(legalEntityA)})
	read, err := s.GetMergeRequest(reader, m.ID)
	if err != nil || read.Status != domain.MergeApproved || read.Rules["time_zone"] != domain.KeepLoser {
		t.Fatalf("read back = %+v %v", read, err)
	}
}

func TestMergeRequest_RequesterCannotDecideTheirOwnMerge(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	m := requestMerge(t, f, s)
	for _, approve := range []bool{true, false} {
		_, err := s.DecideMerge(mergeApprover("merger"), m.ID, approve, "")
		wantProblem(t, err, http.StatusForbidden, store.CodeMergeSelfApproval)
	}
	if got := scalar(t, f, `select status from merge_request where id = $1`, m.ID); got != "pending" {
		t.Fatalf("request %s, want pending", got)
	}
	if got := scalar(t, f, `select status from party where id = $1`, f.loser); got != "active" {
		t.Fatalf("loser %s, want active", got)
	}
	// The table refuses it too, whoever writes it.
	err := dbx.WithTenantTx(f.ctx, f.app, func(tx pgx.Tx) error {
		_, err := tx.Exec(f.ctx, `update merge_request set status = 'rejected', decided_by = requested_by, decided_at = now() where id = $1`, m.ID)
		return err
	})
	if err == nil {
		t.Fatal("merge_request accepted decided_by = requested_by")
	}
}

func TestMergeRequest_RejectionMergesNothingAndReplays(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	m := requestMerge(t, f, s)
	rejected, err := s.DecideMerge(mergeApprover("ada"), m.ID, false, "not the same supplier")
	if err != nil || rejected.Status != domain.MergeRejected || rejected.MergeID != nil || rejected.Reason == nil || *rejected.Reason != "not the same supplier" {
		t.Fatalf("reject = %+v %v", rejected, err)
	}
	if got := scalar(t, f, `select status || '/' || (select count(*) from party_merge)::text from party where id = $1`, f.loser); got != "active/0" {
		t.Fatalf("after reject: %s, want active and no merge", got)
	}
	if got := scalar(t, f, `select payload->'data'->>'decided_by' from outbox where type = 'dev.passage.party.merge.rejected.v1'`); got != "ada" {
		t.Fatalf("merge.rejected decided_by %q", got)
	}
	if replay, err := s.DecideMerge(mergeApprover("ada"), m.ID, false, "again"); err != nil || !replay.Replayed {
		t.Fatalf("replayed reject = %+v %v", replay, err)
	}
	_, err = s.DecideMerge(mergeApprover("ada"), m.ID, true, "")
	wantProblem(t, err, http.StatusConflict, store.CodeMergeDecided)
	// A rejected request no longer blocks a new one.
	requestMerge(t, f, s)
}

func TestMergeRequest_OnlyTheWorkflowExecutorDecides(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	m := requestMerge(t, f, s)
	for name, p := range map[string]tenant.Principal{
		"a tenant admin user":          {TenantID: testkit.TenantID(1), UserID: "ada", Duties: map[string]struct{}{store.DutyMergeExecute: {}, store.DutyMerge: {}, tenant.DutyTenantAdmin: {}}},
		"an entity-scoped token":       {TenantID: testkit.TenantID(1), UserID: "ada", Service: store.MergeExecutor, LegalEntityID: legalEntityA, Duties: map[string]struct{}{store.DutyMergeExecute: {}}},
		"another service":              {TenantID: testkit.TenantID(1), UserID: "ada", Service: "payables", Duties: map[string]struct{}{store.DutyMergeExecute: {}}},
		"workflow-ts without the duty": {TenantID: testkit.TenantID(1), UserID: "ada", Service: store.MergeExecutor, Duties: map[string]struct{}{"party.approve": {}}},
	} {
		_, err := s.DecideMerge(tenant.With(context.Background(), p), m.ID, true, "")
		wantProblem(t, err, http.StatusForbidden, "AUTH-0003")
		if t.Failed() {
			t.Fatalf("%s decided a merge", name)
		}
	}
	_, err := s.DecideMerge(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "ada", Service: store.MergeExecutor, Duties: map[string]struct{}{store.DutyMergeExecute: {}}}), m.ID, true, "")
	wantProblem(t, err, http.StatusNotFound, store.CodeMergeRequestNotFound)
}

func TestMergeRequest_IdempotencyAndOneOpenRequest(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	in := store.MergeRequestInput{SurvivorID: f.winner, MergedID: f.loser, Key: "k1", Hash: "h1"}
	first, created, err := s.RequestMerge(f.merger, in)
	if err != nil || !created {
		t.Fatalf("first = %+v %v %v", first, created, err)
	}
	again, created, err := s.RequestMerge(f.merger, in)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay = %+v created %v err %v", again, created, err)
	}
	in.Hash = "h2"
	_, _, err = s.RequestMerge(f.merger, in)
	wantProblem(t, err, http.StatusConflict, store.CodeIdempotencyReuse)
	_, _, err = s.RequestMerge(f.merger, store.MergeRequestInput{SurvivorID: f.winner, MergedID: f.loser})
	wantProblem(t, err, http.StatusConflict, store.CodeMergePending)
	if got := scalar(t, f, `select count(*)::text from outbox where type = 'dev.passage.party.merge.requested.v1'`); got != "1" {
		t.Fatalf("merge.requested events = %s, want 1", got)
	}
}

func TestMergeRequest_RefusedRequests(t *testing.T) {
	f := seedMerge(t, legalEntityA)
	s := store.New(f.app)
	if _, _, err := s.RequestMerge(f.merger, store.MergeRequestInput{SurvivorID: f.winner, MergedID: f.loser}); !errors.Is(err, store.ErrRoleConflict) {
		t.Errorf("same-entity roles = %v, want ErrRoleConflict", err)
	}
	if _, _, err := s.RequestMerge(f.merger, store.MergeRequestInput{SurvivorID: f.winner, MergedID: f.payer, Rules: map[string]domain.Rule{"name": "bogus"}}); err == nil {
		t.Error("an unknown rule was accepted")
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityA, UserID: "merger", Duties: map[string]struct{}{store.DutyMerge: {}, tenant.DutyTenantAdmin: {}}})
	if _, _, err := s.RequestMerge(scoped, store.MergeRequestInput{SurvivorID: f.winner, MergedID: f.payer}); !errors.Is(err, store.ErrScopedPrincipal) {
		t.Errorf("scoped requester = %v, want ErrScopedPrincipal", err)
	}
	if got := scalar(t, f, `select count(*)::text from merge_request`); got != "0" {
		t.Fatalf("merge requests = %s, want 0", got)
	}
}

func TestMergeRequest_ApprovalOfAStaleRequestMergesNothing(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	m := requestMerge(t, f, s)
	if err := insertCustomerRole(f.ctx, f.app, testkit.TenantID(1), f.winner, legalEntityB, "CUST-W2"); err != nil {
		t.Fatal(err)
	}
	_, err := s.DecideMerge(mergeApprover("ada"), m.ID, true, "")
	wantProblem(t, err, http.StatusConflict, store.CodeMergeStale)
	if got := scalar(t, f, `select (select status from merge_request where id = $1) || '/' || (select status from party where id = $2)`, m.ID, f.loser); got != "pending/active" {
		t.Fatalf("after a stale approval: %s, want pending/active", got)
	}
	if rejected, err := s.DecideMerge(mergeApprover("ada"), m.ID, false, "stale"); err != nil || rejected.Status != domain.MergeRejected {
		t.Fatalf("reject after stale = %+v %v", rejected, err)
	}
}
