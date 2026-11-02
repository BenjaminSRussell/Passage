package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
)

// MaxJobGapWorkers bounds the workers one job gap report lists.
const MaxJobGapWorkers = 500

// JobRequirement is one skill a job needs at a minimum rating (FR-42.18).
type JobRequirement struct {
	JobID     string `json:"jobId"`
	SkillID   string `json:"skillId"`
	SkillCode string `json:"skillCode"`
	SkillName string `json:"skillName"`
	ScaleMax  int    `json:"scaleMax"`
	MinRating int    `json:"minRating"`
}

// JobWorkerGap is one worker in a job's gap report; Gaps holds only unmet rows.
type JobWorkerGap struct {
	WorkerID        string          `json:"workerId"`
	PersonnelNumber string          `json:"personnelNumber"`
	LegalName       string          `json:"legalName"`
	Met             bool            `json:"met"`
	Gaps            []domain.GapRow `json:"gaps"`
}

// JobSkillGaps compares every worker whose primary position on AsOf carries
// the job's code with the job's skill profile. Truncated is true when more than
// MaxJobGapWorkers workers hold the job.
type JobSkillGaps struct {
	JobID     string         `json:"jobId"`
	JobCode   string         `json:"jobCode"`
	AsOf      string         `json:"asOf"`
	Items     []JobWorkerGap `json:"items"`
	Truncated bool           `json:"truncated"`
}

func jobCodeOf(ctx context.Context, tx pgx.Tx, tenantID, jobID string) (string, error) {
	var code string
	err := tx.QueryRow(ctx, `select code from hr_job where tenant_id = $1 and id = $2`, tenantID, jobID).Scan(&code)
	if err == pgx.ErrNoRows {
		return "", problem(http.StatusNotFound, CodeJobNotFound, "job not found")
	}
	return code, err
}

func jobRequirements(ctx context.Context, tx pgx.Tx, tenantID, jobID string) ([]domain.Requirement, error) {
	reqs, err := sklCodeRatings(ctx, tx, `select k.code, r.min_rating from job_skill_requirement r
		join hr_skill k on k.tenant_id = r.tenant_id and k.id = r.skill_id
		where r.tenant_id = $1 and r.job_id = $2`, tenantID, jobID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Requirement, 0, len(reqs))
	for code, min := range reqs {
		out = append(out, domain.Requirement{Skill: code, MinRating: min})
	}
	return out, nil
}

// SetJobRequirement sets the minimum rating a job needs in a skill, replacing an
// earlier minimum for that skill.
func (s *Postgres) SetJobRequirement(ctx context.Context, jobID, skillCode string, minRating int) (JobRequirement, error) {
	p := principal(ctx)
	var out JobRequirement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := jobCodeOf(ctx, tx, p.TenantID, jobID); err != nil {
			return err
		}
		skill, err := sklByCode(ctx, tx, p.TenantID, skillCode)
		if err != nil {
			return err
		}
		if err := domain.ValidateRating(minRating, skill.ScaleMax); err != nil {
			return sklProblem(err)
		}
		if _, err := tx.Exec(ctx, `insert into job_skill_requirement (tenant_id, job_id, skill_id, min_rating)
			values ($1, $2, $3, $4)
			on conflict (tenant_id, job_id, skill_id) do update set min_rating = excluded.min_rating, updated_at = now()`,
			p.TenantID, jobID, skill.ID, minRating); err != nil {
			return err
		}
		out = JobRequirement{JobID: jobID, SkillID: skill.ID, SkillCode: skill.Code, SkillName: skill.Name, ScaleMax: skill.ScaleMax, MinRating: minRating}
		return nil
	})
	return out, err
}

// RemoveJobRequirement drops a skill from a job's profile.
func (s *Postgres) RemoveJobRequirement(ctx context.Context, jobID, skillCode string) error {
	p := principal(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := jobCodeOf(ctx, tx, p.TenantID, jobID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from job_skill_requirement r using hr_skill k
			where r.tenant_id = $1 and r.job_id = $2 and k.tenant_id = r.tenant_id and k.id = r.skill_id and k.code = $3`,
			p.TenantID, jobID, skillCode)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(http.StatusNotFound, CodeRequirementNotFound, "the job does not require skill "+skillCode)
		}
		return nil
	})
}

// ListJobRequirements returns a job's skill profile ordered by skill code.
func (s *Postgres) ListJobRequirements(ctx context.Context, jobID string) ([]JobRequirement, error) {
	p := principal(ctx)
	out := []JobRequirement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := jobCodeOf(ctx, tx, p.TenantID, jobID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select r.job_id::text, k.id::text, k.code, k.name, k.scale_max, r.min_rating
			from job_skill_requirement r join hr_skill k on k.tenant_id = r.tenant_id and k.id = r.skill_id
			where r.tenant_id = $1 and r.job_id = $2 order by k.code`, p.TenantID, jobID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r JobRequirement
			if err := rows.Scan(&r.JobID, &r.SkillID, &r.SkillCode, &r.SkillName, &r.ScaleMax, &r.MinRating); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// SkillGapForJob compares a worker's ratings with a job's skill profile. A
// skill the worker is not rated on counts as 0.
func (s *Postgres) SkillGapForJob(ctx context.Context, workerID, jobID string) (SkillGap, error) {
	p := principal(ctx)
	out := SkillGap{WorkerID: workerID, JobID: jobID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := sklRequireWorker(ctx, tx, p.TenantID, workerID); err != nil {
			return err
		}
		if _, err := jobCodeOf(ctx, tx, p.TenantID, jobID); err != nil {
			return err
		}
		required, err := jobRequirements(ctx, tx, p.TenantID, jobID)
		if err != nil {
			return err
		}
		actual, err := sklCodeRatings(ctx, tx, `select k.code, ws.rating from worker_skill ws
			join hr_skill k on k.tenant_id = ws.tenant_id and k.id = ws.skill_id
			where ws.tenant_id = $1 and ws.worker_id = $2`, p.TenantID, workerID)
		if err != nil {
			return err
		}
		out.Comparison = domain.Compare(required, actual)
		out.Gaps = domain.Gap(required, actual)
		out.Met = len(out.Gaps) == 0
		return nil
	})
	return out, err
}

// JobGaps lists the gaps of every worker holding the job on asOf, ordered by
// personnel number. Workers follow the caller's legal-entity scope through the
// positions they can see.
func (s *Postgres) JobGaps(ctx context.Context, jobID string, asOf time.Time) (JobSkillGaps, error) {
	p := principal(ctx)
	out := JobSkillGaps{JobID: jobID, AsOf: asOf.Format(absDateLayout), Items: []JobWorkerGap{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		code, err := jobCodeOf(ctx, tx, p.TenantID, jobID)
		if err != nil {
			return err
		}
		out.JobCode = code
		required, err := jobRequirements(ctx, tx, p.TenantID, jobID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select w.id::text, w.personnel_number, w.legal_name
			from worker w
			where w.tenant_id = $1 and exists (
				select 1 from position_assignment a join hr_position pos on pos.tenant_id = a.tenant_id and pos.id = a.position_id
				where a.tenant_id = $1 and a.worker_id = w.id and a.is_primary and a.validity @> $3::timestamptz and pos.job_code = $2)
			order by w.personnel_number limit $4`, p.TenantID, code, domain.AsOfDay(asOf), MaxJobGapWorkers+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var g JobWorkerGap
			if err := rows.Scan(&g.WorkerID, &g.PersonnelNumber, &g.LegalName); err != nil {
				rows.Close()
				return err
			}
			out.Items = append(out.Items, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > MaxJobGapWorkers {
			out.Items, out.Truncated = out.Items[:MaxJobGapWorkers], true
		}
		if len(out.Items) == 0 {
			return nil
		}
		workerIDs := make([]string, len(out.Items))
		for i, g := range out.Items {
			workerIDs[i] = g.WorkerID
		}
		ratings := map[string]map[string]int{}
		rrows, err := tx.Query(ctx, `select ws.worker_id::text, k.code, ws.rating from worker_skill ws
			join hr_skill k on k.tenant_id = ws.tenant_id and k.id = ws.skill_id
			where ws.tenant_id = $1 and ws.worker_id = any($2::uuid[])`, p.TenantID, workerIDs)
		if err != nil {
			return err
		}
		defer rrows.Close()
		for rrows.Next() {
			var worker, skill string
			var rating int
			if err := rrows.Scan(&worker, &skill, &rating); err != nil {
				return err
			}
			if ratings[worker] == nil {
				ratings[worker] = map[string]int{}
			}
			ratings[worker][skill] = rating
		}
		if err := rrows.Err(); err != nil {
			return err
		}
		for i := range out.Items {
			out.Items[i].Gaps = domain.Gap(required, ratings[out.Items[i].WorkerID])
			out.Items[i].Met = len(out.Items[i].Gaps) == 0
		}
		return nil
	})
	return out, err
}
