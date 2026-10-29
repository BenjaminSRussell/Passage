package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
)

// CriticalityAssessment is one versioned criticality assessment of an asset (FR-44.22).
type CriticalityAssessment struct {
	ID          string              `json:"id"`
	AssetID     string              `json:"assetId"`
	Version     int                 `json:"version"`
	Consequence domain.Consequences `json:"consequence"`
	Likelihood  int                 `json:"likelihood"`
	RiskScore   int                 `json:"riskScore"`
	Criticality int                 `json:"criticality"`
	AssessedBy  *string             `json:"assessedBy,omitempty"`
	AssessedAt  time.Time           `json:"assessedAt"`
}

const assessmentColumns = `id::text, asset_id::text, version, consequence_safety, consequence_environment,
  consequence_production, consequence_quality, consequence_cost, likelihood, risk_score, criticality,
  assessed_by, assessed_at`

func scanAssessment(row pgx.Row) (CriticalityAssessment, error) {
	var a CriticalityAssessment
	err := row.Scan(&a.ID, &a.AssetID, &a.Version, &a.Consequence.Safety, &a.Consequence.Environment,
		&a.Consequence.Production, &a.Consequence.Quality, &a.Consequence.Cost, &a.Likelihood,
		&a.RiskScore, &a.Criticality, &a.AssessedBy, &a.AssessedAt)
	return a, err
}

// AssessCriticality appends a new assessment version and replaces the asset's
// criticality with the derived value (BR-44.15). The asset row is locked so
// versions stay gap-free, and the asset's version moves on so a stale If-Match
// on the asset fails. A repeated key returns the stored assessment.
func (s *Postgres) AssessCriticality(ctx context.Context, assetID string, c domain.Consequences, likelihood int,
	assessedBy, key string) (CriticalityAssessment, error) {
	var out CriticalityAssessment
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "criticality_assessment", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = scanAssessment(tx.QueryRow(ctx, `select `+assessmentColumns+` from criticality_assessment
			  where tenant_id = $1 and id = $2`, t, id))
			if gerr == nil && out.AssetID != assetID {
				return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another asset's assessment")
			}
			return gerr
		}
		if _, err := getAsset(ctx, tx, t, assetID, true); err != nil {
			return err
		}
		risk, err := domain.RiskScore(c, likelihood)
		if err != nil {
			return problem(400, CodeValidation, err.Error())
		}
		crit := domain.CriticalityFromRisk(risk, c.Safety)
		out, err = scanAssessment(tx.QueryRow(ctx, `insert into criticality_assessment (tenant_id, id, asset_id, version,
		    consequence_safety, consequence_environment, consequence_production, consequence_quality, consequence_cost,
		    likelihood, risk_score, criticality, assessed_by, assessed_at, idempotency_key)
		  values ($1, $2, $3, (select coalesce(max(version), 0) + 1 from criticality_assessment
		      where tenant_id = $1 and asset_id = $3),
		    $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		  returning `+assessmentColumns,
			t, ids.New(), assetID, c.Safety, c.Environment, c.Production, c.Quality, c.Cost,
			likelihood, risk, crit, optStr(assessedBy), s.now(), optStr(key)))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update asset set criticality = $3, version = version + 1, updated_at = now()
		  where tenant_id = $1 and id = $2`, t, assetID, crit)
		return err
	})
	return out, err
}

// ListAssessments returns an asset's criticality assessments, newest version first.
func (s *Postgres) ListAssessments(ctx context.Context, assetID string) ([]CriticalityAssessment, error) {
	out := []CriticalityAssessment{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if _, err := getAsset(ctx, tx, t, assetID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+assessmentColumns+` from criticality_assessment
		  where tenant_id = $1 and asset_id = $2 order by version desc`, t, assetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAssessment(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// ChangeAssetLifecycle retires an asset to INACTIVE or DISPOSED (FR-44.2); the
// caller's version must match (If-Match). An asset with open work orders cannot
// be disposed, and disposal removes it from its functional location. Activation
// keeps its own action (ActivateAsset).
func (s *Postgres) ChangeAssetLifecycle(ctx context.Context, id, to string, version int) (Asset, error) {
	var out Asset
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		cur, err := getAsset(ctx, tx, t, id, true)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return problem(412, CodeVersion, "asset version changed")
		}
		if !domain.LifecycleAllowed(cur.LifecycleState, to) {
			return problem(422, CodeAssetState, "asset is "+cur.LifecycleState+"; it cannot move to "+to)
		}
		if to == "DISPOSED" {
			open, err := taken(ctx, tx, "work_order", `tenant_id = $1 and asset_id = $2
			    and status not in ('COMPLETED', 'CLOSED', 'CANCELED')`, t, id)
			if err != nil {
				return err
			}
			if open {
				return problem(422, CodeAssetState, "the asset has open work orders; complete or cancel them before disposal")
			}
		}
		// A disposed asset leaves its functional location (FR-44.2 remove history).
		leaving := to == "DISPOSED" && cur.FunctionalLocationID != nil
		out, err = scanAsset(tx.QueryRow(ctx, `update asset set lifecycle_state = $3, version = version + 1,
		    functional_location_id = case when $4 then null else functional_location_id end,
		    updated_at = now() where tenant_id = $1 and id = $2 returning `+assetColumns, t, id, to, leaving))
		if err != nil || !leaving {
			return err
		}
		return recordLocationChange(ctx, tx, t, id, domain.MoveRemove, *cur.FunctionalLocationID, "", actorOf(ctx), s.now(), "")
	})
	return out, err
}
