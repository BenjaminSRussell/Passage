package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
)

// RegularizationAlert is an emergency work order still unplanned after the
// window (BR-44.7), with the PRD warning EAM-WO-003.
type RegularizationAlert struct {
	WorkOrder
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// EmergencyOverdue lists the open priority 1 work orders that were never
// scheduled or released and were created at least window ago (BR-44.7),
// oldest first. It is the answer the hourly eam-emergency-regularize-check job
// and the KPI page share.
func (s *Postgres) EmergencyOverdue(ctx context.Context, window time.Duration) ([]RegularizationAlert, error) {
	out := []RegularizationAlert{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		now := s.now()
		rows, err := tx.Query(ctx, `select `+workOrderColumns+` from work_order w
		  where w.tenant_id = $1 and w.priority = 1 and w.status not in ('COMPLETED', 'CLOSED', 'CANCELED')
		    and w.created_at <= $2
		    and not exists(select 1 from work_order_transition h where h.tenant_id = w.tenant_id
		      and h.work_order_id = w.id and h.to_status in ('SCHEDULED', 'RELEASED'))
		  order by w.created_at, w.id`, t, now.Add(-window))
		if err != nil {
			return err
		}
		defer rows.Close()
		hours := int(window / time.Hour)
		for rows.Next() {
			w, err := scanWorkOrder(rows)
			if err != nil {
				return err
			}
			// The query keeps only orders never scheduled or released.
			if domain.RegularizationOverdue(w.Priority, domain.Status(w.Status), false, w.CreatedAt, now, window) {
				out = append(out, RegularizationAlert{WorkOrder: w, Code: domain.CodeRegularization,
					Detail: fmt.Sprintf("Emergency work order %s not regularized within %d h", w.Number, hours)})
			}
		}
		return rows.Err()
	})
	return out, err
}

// PlanReview is an asset whose maintenance plans are due for review (BR-44.15).
// The latest criticality assessment is the review record this service keeps.
type PlanReview struct {
	AssetID        string     `json:"assetId"`
	AssetNumber    string     `json:"assetNumber"`
	Name           string     `json:"name"`
	Criticality    int        `json:"criticality"`
	LastReviewedAt *time.Time `json:"lastReviewedAt,omitempty"`
	DueAt          *time.Time `json:"dueAt,omitempty"`
}

// PlanReviews lists the assets of criticality 4 or 5, not disposed, that were
// never assessed or whose latest assessment is 12 months old (BR-44.15),
// most overdue first.
func (s *Postgres) PlanReviews(ctx context.Context) ([]PlanReview, error) {
	out := []PlanReview{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		now := s.now()
		rows, err := tx.Query(ctx, `select a.id::text, a.asset_number, a.name, a.criticality,
		    (select max(c.assessed_at) from criticality_assessment c where c.tenant_id = a.tenant_id and c.asset_id = a.id) as last
		  from asset a
		  where a.tenant_id = $1 and a.criticality >= $2 and a.lifecycle_state <> 'DISPOSED'
		  order by last nulls first, a.asset_number`, t, domain.ReviewCriticality)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it PlanReview
			if err := rows.Scan(&it.AssetID, &it.AssetNumber, &it.Name, &it.Criticality, &it.LastReviewedAt); err != nil {
				return err
			}
			if !domain.ReviewDue(it.Criticality, it.LastReviewedAt, now) {
				continue
			}
			if it.LastReviewedAt != nil {
				due := it.LastReviewedAt.AddDate(0, domain.ReviewMonths, 0)
				it.DueAt = &due
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	return out, err
}
