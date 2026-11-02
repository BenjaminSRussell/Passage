package domain_test

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

func newEcr() domain.Ecr {
	return domain.Ecr{ID: "e1", Number: "ECR-1", Title: "Swap the wheel", Priority: "high", State: domain.EcrDraft, Products: []string{"BIKE", "WHEEL"}, RequestedBy: "rita"}
}

func TestEcr_Validate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.Ecr)
		field  string
	}{
		{"valid", func(*domain.Ecr) {}, ""},
		{"no number", func(e *domain.Ecr) { e.Number = " " }, "number"},
		{"no title", func(e *domain.Ecr) { e.Title = "" }, "title"},
		{"long title", func(e *domain.Ecr) { e.Title = strings.Repeat("x", domain.MaxEcrTitle+1) }, "title"},
		{"long reason", func(e *domain.Ecr) { e.Reason = strings.Repeat("x", domain.MaxEcrReason+1) }, "reason"},
		{"bad priority", func(e *domain.Ecr) { e.Priority = "urgent" }, "priority"},
		{"no products", func(e *domain.Ecr) { e.Products = nil }, "products"},
		{"duplicate product", func(e *domain.Ecr) { e.Products = []string{"BIKE", "BIKE"} }, "products"},
		{"blank product", func(e *domain.Ecr) { e.Products = []string{"  "} }, "products"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEcr()
			tc.mutate(&e)
			fields := e.Validate()
			if tc.field == "" && len(fields) != 0 || tc.field != "" && fields[tc.field] == "" {
				t.Fatalf("fields = %v; want problem on %q", fields, tc.field)
			}
		})
	}
}

func TestEcr_WorkflowReachesApprovedOnlyThroughReview(t *testing.T) {
	e := newEcr()
	if err := e.Decide(domain.EcrApproved, "carol", ""); !domain.HasCode(err, domain.CodeEcrState) {
		t.Fatalf("approving a draft = %v; want %s", err, domain.CodeEcrState)
	}
	if err := e.Submit(); err != nil {
		t.Fatal(err)
	}
	if err := e.Decide(domain.EcrApproved, "carol", ""); !domain.HasCode(err, domain.CodeEcrState) {
		t.Fatalf("approving a submitted ECR = %v; want %s", err, domain.CodeEcrState)
	}
	if err := e.StartReview("carol"); err != nil || e.State != domain.EcrUnderReview {
		t.Fatalf("review = %v, state %s", err, e.State)
	}
	if err := e.Decide(domain.EcrApproved, "carol", "fine"); err != nil || e.State != domain.EcrApproved || e.DecidedBy != "carol" || e.DecisionReason != "fine" {
		t.Fatalf("approve = %v, %+v", err, e)
	}
	if err := e.Cancel("rita", ""); !domain.HasCode(err, domain.CodeEcrState) {
		t.Fatalf("cancelling an approved ECR = %v; want %s", err, domain.CodeEcrState)
	}
}

func TestEcr_RequesterCannotReviewOrDecide(t *testing.T) {
	e := newEcr()
	e.State = domain.EcrSubmitted
	if err := e.StartReview("rita"); !domain.HasCode(err, domain.CodeEcrSelfApproval) {
		t.Fatalf("self review = %v", err)
	}
	if err := e.StartReview(""); !domain.HasCode(err, domain.CodeEcrActorRequired) {
		t.Fatalf("anonymous review = %v", err)
	}
	e.State = domain.EcrUnderReview
	for _, to := range []domain.EcrState{domain.EcrApproved, domain.EcrRejected} {
		if err := e.Decide(to, "rita", ""); !domain.HasCode(err, domain.CodeEcrSelfApproval) {
			t.Fatalf("self %s = %v", to, err)
		}
	}
	if err := e.Decide(domain.EcrCancelled, "carol", ""); !domain.HasCode(err, domain.CodeEcrState) {
		t.Fatalf("decide as cancelled = %v", err)
	}
	if err := e.Decide(domain.EcrRejected, "carol", "no"); err != nil || e.State != domain.EcrRejected {
		t.Fatalf("reject = %v, %s", err, e.State)
	}
}

func TestEcr_RequesterMayWithdraw(t *testing.T) {
	for _, from := range []domain.EcrState{domain.EcrDraft, domain.EcrSubmitted, domain.EcrUnderReview} {
		e := newEcr()
		e.State = from
		if err := e.Cancel("carol", "not needed"); !domain.HasCode(err, domain.CodeEcrNotRequester) || e.State != from {
			t.Fatalf("withdraw by another user from %s = %v, state %s; want %s", from, err, e.State, domain.CodeEcrNotRequester)
		}
		if err := e.Cancel("", ""); !domain.HasCode(err, domain.CodeEcrActorRequired) {
			t.Fatalf("anonymous withdraw = %v", err)
		}
		if err := e.Cancel("rita", "changed my mind"); err != nil || e.State != domain.EcrCancelled || e.DecisionReason != "changed my mind" {
			t.Fatalf("cancel from %s = %v", from, err)
		}
	}
}

func TestEcr_CheckEcoSource(t *testing.T) {
	e := newEcr()
	if err := e.CheckEcoSource([]string{"BIKE"}); !domain.HasCode(err, domain.CodeEcrNotApproved) {
		t.Fatalf("draft source = %v", err)
	}
	e.State = domain.EcrApproved
	if err := e.CheckEcoSource([]string{"BIKE", "WHEEL"}); err != nil {
		t.Fatalf("approved source = %v", err)
	}
	if err := e.CheckEcoSource([]string{"BIKE", "FRAME"}); !domain.HasCode(err, domain.CodeEcrProduct) {
		t.Fatalf("unlisted product = %v", err)
	}
	e.EcoID = "eco-1"
	if err := e.CheckEcoSource([]string{"BIKE"}); !domain.HasCode(err, domain.CodeEcrConverted) {
		t.Fatalf("second ECO = %v", err)
	}
}

func TestEcr_EventData(t *testing.T) {
	e := newEcr()
	data := e.EventData()
	e.Products[0] = "X"
	if data.EcrNumber != "ECR-1" || data.Products[0] != "BIKE" || data.RequestedBy != "rita" {
		t.Fatalf("event data = %+v", data)
	}
}
