package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/optimizer"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) CampaignSetupMatrix(context.Context, string, time.Time) (domain.SetupMatrix, error) {
	return f.matrix, f.matrixErr
}

// fakeSequencer answers for scheduler-opt and records what it was sent.
type fakeSequencer struct {
	res    optimizer.CampaignResult
	err    error
	calls  int
	matrix [][]int
	limit  time.Duration
}

func (f *fakeSequencer) CampaignSequence(_ context.Context, m [][]int, limit time.Duration) (optimizer.CampaignResult, error) {
	f.calls++
	f.matrix, f.limit = m, limit
	return f.res, f.err
}

// cpsatCampaign is the BR-23.12 example as a cp-sat campaign at version 3:
// order C was added before order B, current group A.
func cpsatCampaign() store.Campaign {
	current := "A"
	return store.Campaign{ID: campID, ResourceID: resID, Status: domain.CampaignProposed, Version: 3, SequenceRule: domain.SequenceCPSAT,
		CurrentSetupGroup: &current, PlannedStart: at0, Orders: []store.CampaignOrder{
			{OrderID: ordB, SetupGroup: "B", Quantity: decimal.NewFromInt(1), RunMinutes: 60, Position: 2},
			{OrderID: ordC, SetupGroup: "C", Quantity: decimal.NewFromInt(1), RunMinutes: 60, Position: 1},
		}}
}

func brSetupMatrix(t *testing.T) domain.SetupMatrix {
	t.Helper()
	m, err := domain.NewSetupMatrix([]domain.SetupEntry{
		{FromGroup: "A", ToGroup: "B", Minutes: 30}, {FromGroup: "A", ToGroup: "C", Minutes: 90},
		{FromGroup: "B", ToGroup: "C", Minutes: 20}, {FromGroup: "C", ToGroup: "B", Minutes: 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func sequenceWith(t *testing.T, f *fakeStore, seq CampaignSequencer, ifMatch string) int {
	t.Helper()
	r := httpx.NewRouter()
	a := New(f, DefaultConfig)
	if seq != nil {
		a.WithSequencer(seq)
	}
	a.Register(r)
	rec := doWith(r, http.MethodPost, "/v1/scheduling/campaigns/"+campID+":sequence", "", allDuties,
		map[string]string{"If-Match": ifMatch, "Idempotency-Key": "seq-1"})
	return rec.Code
}

// The solver gets the changeover matrix in position order with the current
// group at index 0 and a time limit inside the request budget; its indices
// come back to the store as order ids.
func TestSequenceCampaign_CPSATAsksTheSolver(t *testing.T) {
	f := &fakeStore{campaign: cpsatCampaign(), matrix: brSetupMatrix(t)}
	seq := &fakeSequencer{res: optimizer.CampaignResult{Status: "OPTIMAL", Sequence: []int{1, 0}, ChangeoverMinutes: 50}}
	if code := sequenceWith(t, f, seq, `"3"`); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	// Index 1 is order C (position 1), index 2 is order B (position 2).
	want := [][]int{{0, 90, 30}, {0, 0, 60}, {0, 20, 0}}
	for a := range want {
		for b := range want[a] {
			if seq.matrix[a][b] != want[a][b] {
				t.Fatalf("matrix = %v, want %v", seq.matrix, want)
			}
		}
	}
	if seq.limit != campaignSolverLimit || campaignSolverLimit+5*time.Second >= 30*time.Second {
		t.Fatalf("time limit %s must leave room inside the 30 s request timeout", seq.limit)
	}
	p := f.lastCmd.Proposal
	if p == nil || p.Status != "OPTIMAL" || len(p.OrderIDs) != 2 || p.OrderIDs[0] != ordB || p.OrderIDs[1] != ordC {
		t.Fatalf("proposal = %+v, want B then C", p)
	}
}

// Solver trouble never fails the command; the store gets a status that keeps
// the heuristic, and campaigns that are not cp-sat or will be refused do not
// call the solver at all.
func TestSequenceCampaign_CPSATFallbacks(t *testing.T) {
	big := cpsatCampaign()
	big.Orders = nil
	for i := 0; i <= domain.MaxSolverCampaignOrders; i++ {
		big.Orders = append(big.Orders, store.CampaignOrder{OrderID: ordB, SetupGroup: "B", Position: i + 1})
	}
	nearest := cpsatCampaign()
	nearest.SequenceRule = domain.SequenceNearestNeighbor
	started := cpsatCampaign()
	started.Status = domain.CampaignStarted
	huge, err := domain.NewSetupMatrix([]domain.SetupEntry{{FromGroup: "A", ToGroup: "C", Minutes: 20000}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		campaign   store.Campaign
		matrix     domain.SetupMatrix
		matrixErr  error
		seq        *fakeSequencer
		ifMatch    string
		wantStatus string // "" means no proposal
		wantCalls  int
	}{
		{"no sequencer configured", cpsatCampaign(), brSetupMatrix(t), nil, nil, `"3"`, domain.SolverUnavailable, 0},
		{"solver down", cpsatCampaign(), brSetupMatrix(t), nil, &fakeSequencer{err: errors.New("connection refused")}, `"3"`, domain.SolverUnavailable, 1},
		{"index out of range", cpsatCampaign(), brSetupMatrix(t), nil, &fakeSequencer{res: optimizer.CampaignResult{Status: "OPTIMAL", Sequence: []int{0, 7}}}, `"3"`, domain.SolverUnavailable, 1},
		{"no status", cpsatCampaign(), brSetupMatrix(t), nil, &fakeSequencer{res: optimizer.CampaignResult{Sequence: []int{0, 1}}}, `"3"`, domain.SolverUnavailable, 1},
		{"solver unknown", cpsatCampaign(), brSetupMatrix(t), nil, &fakeSequencer{res: optimizer.CampaignResult{Status: "UNKNOWN"}}, `"3"`, domain.SolverUnknown, 1},
		{"matrix read fails", cpsatCampaign(), domain.SetupMatrix{}, errors.New("db down"), &fakeSequencer{}, `"3"`, domain.SolverUnavailable, 0},
		{"over 50 orders", big, brSetupMatrix(t), nil, &fakeSequencer{}, `"3"`, domain.SolverTooLarge, 0},
		{"entry over a week", cpsatCampaign(), huge, nil, &fakeSequencer{}, `"3"`, domain.SolverTooLarge, 0},
		{"nearest-neighbour campaign", nearest, brSetupMatrix(t), nil, &fakeSequencer{}, `"3"`, "", 0},
		{"stale version", cpsatCampaign(), brSetupMatrix(t), nil, &fakeSequencer{}, `"2"`, "", 0},
		{"frozen campaign", started, brSetupMatrix(t), nil, &fakeSequencer{}, `"3"`, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{campaign: tc.campaign, matrix: tc.matrix, matrixErr: tc.matrixErr}
			var seq CampaignSequencer
			if tc.seq != nil {
				seq = tc.seq
			}
			if code := sequenceWith(t, f, seq, tc.ifMatch); code != http.StatusOK {
				t.Fatalf("status %d", code)
			}
			p := f.lastCmd.Proposal
			switch {
			case tc.wantStatus == "" && p != nil:
				t.Fatalf("proposal %+v, want none", p)
			case tc.wantStatus != "" && (p == nil || p.Status != tc.wantStatus || len(p.OrderIDs) != 0):
				t.Fatalf("proposal %+v, want status %s with no order", p, tc.wantStatus)
			}
			if tc.seq != nil && tc.seq.calls != tc.wantCalls {
				t.Fatalf("solver calls %d, want %d", tc.seq.calls, tc.wantCalls)
			}
		})
	}
}

func TestCreateCampaign_AcceptsCPSAT(t *testing.T) {
	f := &fakeStore{}
	body := `{"resourceId":"` + resID + `","campaignKey":"K","plannedStart":"2027-01-04T06:00:00Z","sequenceRule":"cp-sat"}`
	if rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/campaigns", body, allDuties, "k1"); rec.Code != http.StatusCreated ||
		f.lastCampaign.SequenceRule != domain.SequenceCPSAT {
		t.Fatalf("status %d rule %q: %s", rec.Code, f.lastCampaign.SequenceRule, rec.Body)
	}
}
