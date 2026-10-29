package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// KPIFilter bounds the service KPIs. From is inclusive and To exclusive. AsOf
// decides whether an unanswered case is already late. Territory, when set,
// narrows work orders to that territory and counts a case only when one of its
// work orders is there.
type KPIFilter struct {
	From      time.Time
	To        time.Time
	AsOf      time.Time
	Territory *string
}

// KPIs are the service measures over a window (FR-45.11, PRD 8.45.14 "SLA
// attainment" and "First-time fix"). Cases count by opening time, work orders by
// completion time. A rate is nil when its denominator is zero; rates are
// percentages rounded half-even to two places, means are wall-clock minutes.
type KPIs struct {
	From                    time.Time        `json:"from"`
	To                      time.Time        `json:"to"`
	Territory               *string          `json:"territory,omitempty"`
	CasesOpened             int64            `json:"casesOpened"`
	ResponseMet             int64            `json:"responseMet"`
	ResponseMissed          int64            `json:"responseMissed"`
	ResponseAttainmentPct   *decimal.Decimal `json:"responseAttainmentPct"`
	MeanResponseMinutes     *decimal.Decimal `json:"meanResponseMinutes"`
	ResolutionMet           int64            `json:"resolutionMet"`
	ResolutionBreached      int64            `json:"resolutionBreached"`
	ResolutionAttainmentPct *decimal.Decimal `json:"resolutionAttainmentPct"`
	MeanResolutionMinutes   *decimal.Decimal `json:"meanResolutionMinutes"`
	WorkOrdersCompleted     int64            `json:"workOrdersCompleted"`
	FirstTimeFix            int64            `json:"firstTimeFix"`
	FirstTimeFixPct         *decimal.Decimal `json:"firstTimeFixPct"`
}

// KPIs computes SLA attainment, mean time to respond and resolve, and the
// first-time-fix rate for the filter.
//
//   - Response is met when the case was answered by its response due; it is
//     missed when answered later, or still NEW and unanswered past the due at AsOf.
//   - Resolution counts MET and BREACHED clocks; a breach stays a miss even when
//     resolved later (BR-45.2). Open clocks within target are not counted yet.
//   - First-time fix counts original orders (not follow-ups) completed in the
//     window; a fix is one that never needed a follow-up (BR-45.5).
func (s *Postgres) KPIs(ctx context.Context, f KPIFilter) (KPIs, error) {
	tid := tenantOf(ctx)
	out := KPIs{From: f.From, To: f.To, Territory: f.Territory}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var meanResponse, meanResolution *string
		if err := tx.QueryRow(ctx, `select count(*),
			count(*) filter (where c.responded_at <= c.sla_response_due),
			count(*) filter (where c.responded_at > c.sla_response_due
				or (c.responded_at is null and c.status = 'NEW' and c.sla_response_due < $5)),
			round(avg(extract(epoch from (c.responded_at - c.opened_at))::numeric) / 60, 2)::text,
			count(*) filter (where c.sla_state = 'MET'),
			count(*) filter (where c.sla_state = 'BREACHED'),
			round(avg(extract(epoch from (c.resolved_at - c.opened_at))::numeric) / 60, 2)::text
			from service_case c where c.tenant_id = $1 and c.opened_at >= $2 and c.opened_at < $3
			and ($4::uuid is null or exists (select 1 from service_work_order w
				where w.tenant_id = c.tenant_id and w.case_id = c.id and w.territory_id = $4::uuid))`,
			tid, f.From, f.To, f.Territory, f.AsOf).Scan(&out.CasesOpened, &out.ResponseMet, &out.ResponseMissed, &meanResponse,
			&out.ResolutionMet, &out.ResolutionBreached, &meanResolution); err != nil {
			return err
		}
		var err error
		if out.MeanResponseMinutes, err = optionalDecimal(meanResponse); err != nil {
			return err
		}
		if out.MeanResolutionMinutes, err = optionalDecimal(meanResolution); err != nil {
			return err
		}
		out.ResponseAttainmentPct = domain.Percent(out.ResponseMet, out.ResponseMet+out.ResponseMissed)
		out.ResolutionAttainmentPct = domain.Percent(out.ResolutionMet, out.ResolutionMet+out.ResolutionBreached)

		if err := tx.QueryRow(ctx, `select count(*), count(*) filter (where w.first_time_fix)
			from service_work_order w where w.tenant_id = $1 and w.follow_up_of is null
			and w.completed_at >= $2 and w.completed_at < $3 and ($4::uuid is null or w.territory_id = $4::uuid)`,
			tid, f.From, f.To, f.Territory).Scan(&out.WorkOrdersCompleted, &out.FirstTimeFix); err != nil {
			return err
		}
		out.FirstTimeFixPct = domain.Percent(out.FirstTimeFix, out.WorkOrdersCompleted)
		return nil
	})
	return out, err
}

func optionalDecimal(s *string) (*decimal.Decimal, error) {
	if s == nil {
		return nil, nil
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}
