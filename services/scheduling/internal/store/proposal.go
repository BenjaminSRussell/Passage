package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// ProposalFilter is the campaign-proposal inquiry: jobs starting in
// [From, From+WindowDays), optionally on one resource.
type ProposalFilter struct {
	From       time.Time
	WindowDays int
	ResourceID string
}

// ProposeCampaigns proposes campaigns from the orders already scheduled
// (planned or firmed jobs) that belong to no campaign (FR-23.14). It reads the
// first segment of every job starting in the window whose operation has a
// setup group, and groups them with domain.ProposeCampaigns. It writes
// nothing: a planner creates a campaign from a proposal with
// POST /campaigns, which is where quantities are entered.
func (s *Postgres) ProposeCampaigns(ctx context.Context, f ProposalFilter) ([]domain.ProposedCampaign, error) {
	var cands []domain.ProposalCandidate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		cands, err = proposalCandidates(ctx, tx, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	return domain.ProposeCampaigns(cands, f.From, f.WindowDays), nil
}

func proposalCandidates(ctx context.Context, tx pgx.Tx, f ProposalFilter) ([]domain.ProposalCandidate, error) {
	to := f.From.AddDate(0, 0, f.WindowDays)
	rows, err := tx.Query(ctx, `select o.order_id::text, o.id::text, o.oper_number, j.resource_id::text, o.setup_group, o.attributes,
			o.minutes, o.setup_minutes, j.start_at, o.due_at, j.firmed
		from scheduled_job j join scheduling_operation o on o.tenant_id = j.tenant_id and o.id = j.job_id
		where j.segment_no = 1 and j.start_at >= $1 and j.start_at < $2 and o.setup_group is not null
			and ($3 = '' or j.resource_id::text = $3)
			and not exists (select 1 from scheduling_campaign_order co where co.tenant_id = o.tenant_id and co.order_id = o.order_id)
		order by j.start_at, o.order_id, o.oper_number`, f.From, to, f.ResourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ProposalCandidate
	for rows.Next() {
		var c domain.ProposalCandidate
		var attrs []byte
		if err := rows.Scan(&c.OrderID, &c.OperationID, &c.OperNumber, &c.ResourceID, &c.SetupGroup, &attrs,
			&c.RunMinutes, &c.RouteSetupMinutes, &c.Start, &c.DueAt, &c.Firmed); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(attrs, &c.Attributes); err != nil {
			return nil, err
		}
		c.Start = c.Start.UTC()
		if c.DueAt != nil {
			d := c.DueAt.UTC()
			c.DueAt = &d
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
