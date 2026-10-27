package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	qevents "github.com/BenjaminSRussell/passage/services/quality/internal/events"
)

// StudyFilter narrows the stability study list.
type StudyFilter struct {
	Status string
	ItemID string
	Limit  int
}

const (
	studyColumns = "id, item_id, batch_id, protocol, condition, start_date, test_group_id, lead_days, window_days, status, version, created_at"
	dateLayout   = "2006-01-02"
)

func scanStudy(row pgx.Row) (domain.Study, error) {
	var st domain.Study
	err := row.Scan(&st.ID, &st.ItemID, &st.BatchID, &st.Protocol, &st.Condition, &st.StartDate, &st.TestGroupID,
		&st.LeadDays, &st.WindowDays, &st.Status, &st.Version, &st.CreatedAt)
	return st, err
}

// loadPulls reads the pull points of one study in month order and restores the
// per-pull test group overrides.
func loadPulls(ctx context.Context, tx pgx.Tx, st *domain.Study) error {
	rows, err := tx.Query(ctx, `select months, pull_date, order_date, test_group_id, status from quality_pull_point
		where `+ownTenant+` and study_id = $1 order by months`, st.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	st.Pulls = []domain.Pull{}
	st.PullMonths = []int{}
	for rows.Next() {
		var pl domain.Pull
		if err := rows.Scan(&pl.Months, &pl.PullDate, &pl.OrderDate, &pl.TestGroupID, &pl.Status); err != nil {
			return err
		}
		st.Pulls = append(st.Pulls, pl)
		st.PullMonths = append(st.PullMonths, pl.Months)
		if pl.TestGroupID != st.TestGroupID {
			if st.PullTestGroups == nil {
				st.PullTestGroups = map[int]string{}
			}
			st.PullTestGroups[pl.Months] = pl.TestGroupID
		}
	}
	return rows.Err()
}

func studyTx(ctx context.Context, tx pgx.Tx, id string) (domain.Study, error) {
	st, err := scanStudy(tx.QueryRow(ctx, `select `+studyColumns+` from quality_stability_study where `+ownTenant+` and id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return st, notFound(domain.CodeStudyMissing, "stability study not found")
	}
	if err != nil {
		return st, err
	}
	return st, loadPulls(ctx, tx, &st)
}

// CreateStudy stores a study with its generated pull schedule and writes
// stabilitystudy.created to the outbox, from which the scheduling workflow starts
// (BR-33.14). A replayed key returns the stored study.
func (s *Postgres) CreateStudy(ctx context.Context, st domain.Study, key string) (domain.Study, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Study
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			var id string
			err := tx.QueryRow(ctx, `select id from quality_stability_study where `+ownTenant+` and idempotency_key = $1`, key).Scan(&id)
			if err == nil {
				out, err = studyTx(ctx, tx, id)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		_, err := tx.Exec(ctx, `insert into quality_stability_study
			(tenant_id, id, item_id, batch_id, protocol, condition, start_date, test_group_id, lead_days, window_days, status, version, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10, $11, 1, $12)`,
			p.TenantID, st.ID, st.ItemID, st.BatchID, st.Protocol, st.Condition, st.StartDate.Format(dateLayout), st.TestGroupID,
			st.LeadDays, st.WindowDays, st.Status, keyArg(key))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeStudyRace, "a study with this Idempotency-Key is being created; retry")
		}
		if pgCode(err) == fkViolation {
			return notFound(domain.CodeTestGroupMissing, "test group not found")
		}
		if err != nil {
			return err
		}
		pulls := make([]qevents.StabilityPull, len(st.Pulls))
		for i, pl := range st.Pulls {
			_, err := tx.Exec(ctx, `insert into quality_pull_point (tenant_id, study_id, months, pull_date, order_date, test_group_id, status)
				values ($1, $2, $3, $4::date, $5::date, $6, $7)`, p.TenantID, st.ID, pl.Months, pl.PullDate.Format(dateLayout),
				pl.OrderDate.Format(dateLayout), pl.TestGroupID, pl.Status)
			if pgCode(err) == fkViolation {
				return notFound(domain.CodeTestGroupMissing, "test group not found")
			}
			if err != nil {
				return err
			}
			pulls[i] = qevents.StabilityPull{Months: pl.Months, PullDate: pl.PullDate.Format(dateLayout),
				OrderDate: pl.OrderDate.Format(dateLayout), TestGroupID: pl.TestGroupID}
		}
		if err := enqueue(ctx, tx, "stabilitystudy", st.ID, qevents.StabilityStudyCreated, p.TenantID, qevents.StabilityStudyCreatedData{
			StudyID: st.ID, ItemID: st.ItemID, BatchID: st.BatchID, Condition: st.Condition,
			StartDate: st.StartDate.Format(dateLayout), Pulls: pulls,
		}); err != nil {
			return err
		}
		created, err := studyTx(ctx, tx, st.ID)
		out, inserted = created, true
		return err
	})
	return out, inserted, err
}

// GetStudy reads one study with its pull points.
func (s *Postgres) GetStudy(ctx context.Context, id string) (domain.Study, error) {
	var out domain.Study
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = studyTx(ctx, tx, id)
		return err
	})
	return out, err
}

// ListStudies returns studies, newest first, each with its pull points.
func (s *Postgres) ListStudies(ctx context.Context, f StudyFilter) ([]domain.Study, error) {
	out := []domain.Study{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+studyColumns+` from quality_stability_study where `+ownTenant+`
			and ($1 = '' or status = $1) and ($2 = '' or item_id::text = $2)
			order by created_at desc, id limit $3`, f.Status, f.ItemID, f.Limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			st, err := scanStudy(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, st)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			if err := loadPulls(ctx, tx, &out[i]); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
