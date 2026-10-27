package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/optimizer"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// campaignSolverLimit is the CP-SAT time limit for one campaign. FR-23.14
// names 30 s, but every request is cut at 30 s by the platform router, so
// the solver gets 20 s and the client 5 s more for transport.
const campaignSolverLimit = 20 * time.Second

// CampaignSequencer is scheduler-opt's campaign endpoint (FR-23.14). The
// optimizer client implements it.
type CampaignSequencer interface {
	CampaignSequence(ctx context.Context, changeover [][]int, timeLimit time.Duration) (optimizer.CampaignResult, error)
}

// WithSequencer attaches the CP-SAT campaign sequencer. Without one, cp-sat
// campaigns keep the nearest-neighbour sequence with SCHED-0006 UNAVAILABLE.
func (a *API) WithSequencer(s CampaignSequencer) *API {
	a.seq = s
	return a
}

// proposeSequence asks scheduler-opt to order a cp-sat campaign before the
// sequencing transaction runs, so no row lock or connection is held while
// the solver works. It returns nil when the campaign does not use cp-sat or
// the command will be refused anyway (stale version, frozen, missing); the
// store then reports the refusal. Solver failures never fail the command:
// they come back as a status that keeps the heuristic (BR-23.9).
func (a *API) proposeSequence(ctx context.Context, cmd store.CampaignCommand) *domain.SolverProposal {
	c, err := a.store.GetCampaign(ctx, cmd.CampaignID)
	if err != nil || c.SequenceRule != domain.SequenceCPSAT || c.Version != cmd.Version || domain.CanJoin(c.Status) != nil || len(c.Orders) == 0 {
		return nil
	}
	if len(c.Orders) > domain.MaxSolverCampaignOrders {
		return &domain.SolverProposal{Status: domain.SolverTooLarge}
	}
	if a.seq == nil {
		return &domain.SolverProposal{Status: domain.SolverUnavailable}
	}
	matrix, err := a.store.CampaignSetupMatrix(ctx, c.ResourceID, c.PlannedStart)
	if err != nil {
		slog.Error("read setup matrix for cp-sat failed", "err", err, "campaignId", c.ID, "correlationId", middleware.GetReqID(ctx))
		return &domain.SolverProposal{Status: domain.SolverUnavailable}
	}
	current := ""
	if c.CurrentSetupGroup != nil {
		current = *c.CurrentSetupGroup
	}
	orders := c.SetupOrders()
	changeover := domain.ChangeoverMatrix(matrix, current, orders)
	for _, row := range changeover {
		for _, v := range row {
			if v > domain.MaxSolverChangeoverMinutes {
				return &domain.SolverProposal{Status: domain.SolverTooLarge}
			}
		}
	}
	res, err := a.seq.CampaignSequence(ctx, changeover, campaignSolverLimit)
	if err == nil {
		err = checkSolverAnswer(res, len(orders))
	}
	if err != nil {
		slog.Warn("cp-sat campaign sequencing unavailable", "err", err, "campaignId", c.ID, "correlationId", middleware.GetReqID(ctx))
		return &domain.SolverProposal{Status: domain.SolverUnavailable}
	}
	ids := make([]string, 0, len(res.Sequence))
	for _, i := range res.Sequence {
		ids = append(ids, orders[i].OrderID)
	}
	return &domain.SolverProposal{OrderIDs: ids, Status: res.Status}
}

// checkSolverAnswer refuses an answer whose indices do not name the orders.
func checkSolverAnswer(res optimizer.CampaignResult, n int) error {
	if res.Status == "" {
		return fmt.Errorf("scheduler-opt answered without a status")
	}
	for _, i := range res.Sequence {
		if i < 0 || i >= n {
			return fmt.Errorf("scheduler-opt returned order index %d outside 0..%d", i, n-1)
		}
	}
	return nil
}
