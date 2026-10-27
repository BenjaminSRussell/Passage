package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/events"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// as returns a tenant-1 context for one user with no legal-entity scope.
func as(user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: user, Duties: map[string]struct{}{}})
}

// submittedCapex creates a request as "creator" for requester "plant.manager"
// with a 320,000 variant and submits it (InApproval, CFO, version 2).
func submittedCapex(t *testing.T, s *store.Postgres, number string) store.CapexRequest {
	t.Helper()
	ctx := as("creator")
	in := store.CapexRequest{LegalEntityID: ids.New(), Number: number, Title: "Press line", RequesterID: "plant.manager", Currency: "USD",
		Variants: []store.CapexVariant{
			{Description: "Full line", AmountsByYear: map[string]decimal.Decimal{"2026": d("200000"), "2027": d("120000")}, InServiceDate: day("2027-06-01"), ProfileAssumption: "SL-120"},
			{Description: "Refurbish", AmountsByYear: map[string]decimal.Decimal{"2026": d("40000")}, InServiceDate: day("2026-10-01"), ProfileAssumption: "SL-60"},
		}}
	req, err := s.CreateCapexRequest(ctx, in, "create-"+number)
	if err != nil {
		t.Fatal(err)
	}
	if req.CreatedBy == nil || *req.CreatedBy != "creator" {
		t.Fatalf("createdBy = %v", req.CreatedBy)
	}
	sub, err := s.SubmitCapexRequest(ctx, req.ID, store.SubmitInput{VariantID: req.Variants[0].ID, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// approvedPayloads returns the data of every capexrequest.approved event in
// the outbox.
func approvedPayloads(t *testing.T, pool *pgxpool.Pool, ctx context.Context) []events.CapexApprovedData {
	t.Helper()
	var out []events.CapexApprovedData
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select payload->'data' from outbox where type = $1 order by created_at`, events.CapexApproved)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			var data events.CapexApprovedData
			if err := json.Unmarshal(raw, &data); err != nil {
				return err
			}
			out = append(out, data)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestStore_CapexApproveReleaseClose covers the approve half of AC-37.11: the
// approved amount is the selected variant's total, capexrequest.approved is
// written to the outbox in the same transaction with the yearly amounts, and
// every decision lands in the trail.
func TestStore_CapexApproveReleaseClose(t *testing.T) {
	s, pool, _ := setup(t)
	req := submittedCapex(t, s, "CR-1")
	cfo := as("cfo")

	// The creator and the requester may not decide their own request.
	for _, user := range []string{"creator", "plant.manager"} {
		if _, err := s.DecideCapexRequest(as(user), req.ID, store.CapexDecisionInput{Decision: domain.DecisionApprove, Version: 2}); problemCode(err) != store.CodeCapexOwnDecision {
			t.Fatalf("%s approves own request: err = %v", user, err)
		}
	}
	if _, err := s.DecideCapexRequest(cfo, req.ID, store.CapexDecisionInput{Decision: domain.DecisionApprove, Version: 1}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale approve: err = %v", err)
	}
	if _, err := s.DecideCapexRequest(cfo, req.ID, store.CapexDecisionInput{Decision: domain.DecisionRelease, Version: 2}); problemCode(err) != domain.CodeCapexState {
		t.Fatalf("release before approval: err = %v", err)
	}
	if _, err := s.DecideCapexRequest(context.Background(), req.ID, store.CapexDecisionInput{Decision: domain.DecisionApprove, Version: 2}); problemCode(err) != "AUTH-0001" {
		t.Fatalf("anonymous approve: err = %v", err)
	}
	if got := approvedPayloads(t, pool, cfo); len(got) != 0 {
		t.Fatalf("refused decisions wrote events: %+v", got)
	}

	approved, err := s.DecideCapexRequest(cfo, req.ID, store.CapexDecisionInput{Decision: domain.DecisionApprove, Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "Approved" || approved.Version != 3 || approved.ApprovedAmount == nil || !approved.ApprovedAmount.Equal(d("320000")) {
		t.Fatalf("approved = %+v", approved)
	}
	if len(approved.Decisions) != 1 || approved.Decisions[0].DecidedBy != "cfo" || approved.Decisions[0].FromStatus != "InApproval" ||
		approved.Decisions[0].ToStatus != "Approved" || *approved.Decisions[0].ApprovalLevel != "CFO" || !approved.Decisions[0].Amount.Equal(d("320000")) {
		t.Fatalf("trail = %+v", approved.Decisions)
	}
	payloads := approvedPayloads(t, pool, cfo)
	if len(payloads) != 1 {
		t.Fatalf("approved events = %d, want 1", len(payloads))
	}
	ev := payloads[0]
	if ev.RequestID != req.ID || ev.VariantID != req.Variants[0].ID || ev.Currency != "USD" || ev.ApprovedAmount != "320000" ||
		ev.AmountsByYear["2026"] != "200000" || ev.AmountsByYear["2027"] != "120000" || len(ev.AmountsByYear) != 2 ||
		ev.InServiceDate != "2027-06-01" || ev.ProfileAssumption != "SL-120" || ev.ProgramPositionID != nil {
		t.Fatalf("event = %+v", ev)
	}

	comment := "Purchase order raised"
	released, err := s.DecideCapexRequest(cfo, req.ID, store.CapexDecisionInput{Decision: domain.DecisionRelease, Version: 3, Comment: &comment})
	if err != nil || released.Status != "Released" {
		t.Fatalf("release = %+v, %v", released, err)
	}
	closed, err := s.DecideCapexRequest(cfo, req.ID, store.CapexDecisionInput{Decision: domain.DecisionClose, Version: 4})
	if err != nil || closed.Status != "Closed" || closed.Version != 5 || !closed.ApprovedAmount.Equal(d("320000")) {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	read, err := s.GetCapexRequest(cfo, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Decisions) != 3 || read.Decisions[1].Comment == nil || *read.Decisions[1].Comment != comment || read.Decisions[2].Decision != "close" {
		t.Fatalf("read trail = %+v", read.Decisions)
	}
	if _, err := s.DecideCapexRequest(cfo, req.ID, store.CapexDecisionInput{Decision: domain.DecisionReopen, Version: 5}); problemCode(err) != domain.CodeCapexState {
		t.Fatalf("reopen a closed request: err = %v", err)
	}
	if got := approvedPayloads(t, pool, cfo); len(got) != 1 {
		t.Fatalf("only approve publishes; events = %d", len(got))
	}
	if _, err := s.DecideCapexRequest(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "cfo"}), req.ID,
		store.CapexDecisionInput{Decision: domain.DecisionClose, Version: 5}); problemCode(err) != store.CodeCapexNotFound {
		t.Fatalf("tenant 2 decides: err = %v", err)
	}
}

// TestStore_CapexPostponeReopenResubmit covers InApproval → Postponed → Draft
// (BR-37.12): reopening clears the routing, and the request can be submitted
// again with another variant; rejection is terminal.
func TestStore_CapexPostponeReopenResubmit(t *testing.T) {
	s, _, _ := setup(t)
	req := submittedCapex(t, s, "CR-2")
	cfo := as("cfo")
	reason := "Next budget year"
	postponed, err := s.DecideCapexRequest(cfo, req.ID, store.CapexDecisionInput{Decision: domain.DecisionPostpone, Version: 2, Comment: &reason})
	if err != nil || postponed.Status != "Postponed" || postponed.ApprovedAmount != nil {
		t.Fatalf("postpone = %+v, %v", postponed, err)
	}
	if _, err := s.DecideCapexRequest(cfo, req.ID, store.CapexDecisionInput{Decision: domain.DecisionApprove, Version: 3}); problemCode(err) != domain.CodeCapexState {
		t.Fatalf("approve a postponed request: err = %v", err)
	}
	// Reopen is the requester's action, so the creator may take it.
	reopened, err := s.DecideCapexRequest(as("creator"), req.ID, store.CapexDecisionInput{Decision: domain.DecisionReopen, Version: 3})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != "Draft" || reopened.SelectedVariantID != nil || reopened.ApprovalLevel != nil || reopened.RequestedAmount != nil || len(reopened.Decisions) != 2 {
		t.Fatalf("reopened = %+v", reopened)
	}
	again, err := s.SubmitCapexRequest(as("creator"), req.ID, store.SubmitInput{VariantID: req.Variants[1].ID, Version: 4})
	if err != nil || again.Status != "InApproval" || *again.ApprovalLevel != "PlantManager" || !again.RequestedAmount.Equal(d("40000")) {
		t.Fatalf("resubmit = %+v, %v", again, err)
	}
	rejected, err := s.DecideCapexRequest(cfo, req.ID, store.CapexDecisionInput{Decision: domain.DecisionReject, Version: 5, Comment: &reason})
	if err != nil || rejected.Status != "Rejected" {
		t.Fatalf("reject = %+v, %v", rejected, err)
	}
	if _, err := s.DecideCapexRequest(as("creator"), req.ID, store.CapexDecisionInput{Decision: domain.DecisionReopen, Version: 6}); problemCode(err) != domain.CodeCapexState {
		t.Fatalf("reopen a rejected request: err = %v", err)
	}
	read, err := s.GetCapexRequest(cfo, req.ID)
	if err != nil || len(read.Decisions) != 3 || read.Decisions[0].Decision != "postpone" || *read.Decisions[0].Comment != reason ||
		read.Decisions[1].DecidedBy != "creator" || read.Decisions[2].Decision != "reject" || *read.Decisions[2].ApprovalLevel != "PlantManager" {
		t.Fatalf("trail = %+v, %v", read.Decisions, err)
	}
}
