package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// Firing statuses (report_schedule_firing.status).
const (
	FiringCompleted = "completed"
	FiringFailed    = "failed"
)

// MaxFiringPage is the most firings one list call returns.
const MaxFiringPage = 200

// Firing is the record of one firing of a schedule for one ledger (FR-15.8).
type Firing struct {
	ID            string    `json:"id"`
	ScheduleCode  string    `json:"scheduleCode"`
	LedgerID      string    `json:"ledgerId"`
	LegalEntityID string    `json:"legalEntityId,omitempty"`
	FireTime      time.Time `json:"fireTime"`
	PeriodYear    int       `json:"periodYear"`
	PeriodNo      int       `json:"periodNo"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	RunID         string    `json:"runId,omitempty"`
	ErrorCode     string    `json:"errorCode,omitempty"`
	ErrorDetail   string    `json:"errorDetail,omitempty"`
	StartedAt     time.Time `json:"startedAt"`
	FinishedAt    time.Time `json:"finishedAt"`
}

// DueFiring is one schedule firing the job has to make: a schedule, one of its
// ledgers and the cron time that is due for it.
type DueFiring struct {
	Schedule domain.Schedule
	LedgerID string
	FireTime time.Time
}

// DueFirings lists the firings due at now: for every enabled schedule and each
// of its ledgers, the latest cron time after the later of the schedule's last
// save and its last recorded firing (completed or failed: a failed firing is
// reported, not retried at the next job run). A schedule that cannot be placed
// (an invalid body) is skipped, not fatal for the others.
func (s *Postgres) DueFirings(ctx context.Context, now time.Time) ([]DueFiring, error) {
	var out []DueFiring
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		last := map[string]time.Time{}
		rows, err := tx.Query(ctx, `select schedule_code, ledger_id::text, max(fire_time) from report_schedule_firing group by 1, 2`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var code, ledger string
			var at time.Time
			if err := rows.Scan(&code, &ledger, &at); err != nil {
				rows.Close()
				return err
			}
			last[code+"|"+ledger] = at
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		srows, err := tx.Query(ctx, `select body, updated_at from report_schedule where enabled order by code`)
		if err != nil {
			return err
		}
		defer srows.Close()
		for srows.Next() {
			var body []byte
			var saved time.Time
			if err := srows.Scan(&body, &saved); err != nil {
				return err
			}
			var sc domain.Schedule
			if err := json.Unmarshal(body, &sc); err != nil {
				continue
			}
			sc = sc.Normalize()
			for _, ledger := range sc.LedgerIDs {
				after := saved
				if l, ok := last[sc.Code+"|"+ledger]; ok && l.After(after) {
					after = l
				}
				fire, due, err := sc.DueFire(after, now)
				if err != nil || !due {
					continue
				}
				out = append(out, DueFiring{Schedule: sc, LedgerID: ledger, FireTime: fire})
			}
		}
		return srows.Err()
	})
	return out, err
}

// FiringOutput is one rendered file of a firing's run.
type FiringOutput struct {
	Format      string
	FileName    string
	ContentType string
	Content     []byte
}

// FiringResult is a firing that produced its statement: the run, the files
// rendered from it and how many attempts it took.
type FiringResult struct {
	Schedule    domain.Schedule
	LedgerID    string
	FireTime    time.Time
	Period      domain.FiringPeriod
	Run         Run
	Outputs     []FiringOutput
	RetainUntil time.Time
	Attempts    int
	StartedAt   time.Time
}

// RecordFiring stores a completed firing, its files in the report library and
// one distribution.sent.v1 per file in one transaction (FR-15.8). A firing
// already recorded (the job ran twice) is returned as it is, with no second
// copy of the files and no second event.
func (s *Postgres) RecordFiring(ctx context.Context, f FiringResult) (Firing, error) {
	var out Firing
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var entity string
		if err := tx.QueryRow(ctx, `select coalesce(legal_entity_id::text, '') from statement_run where id=$1::uuid`, f.Run.ID).Scan(&entity); err != nil {
			return err
		}
		id := ids.New()
		tag, err := tx.Exec(ctx, `insert into report_schedule_firing(tenant_id, id, schedule_code, ledger_id, legal_entity_id, fire_time,
				period_year, period_no, status, attempts, run_id, started_at)
			values($1,$2,$3,$4::uuid,nullif($5,'')::uuid,$6,$7,$8,'completed',$9,$10::uuid,$11)
			on conflict (tenant_id, schedule_code, ledger_id, fire_time) do nothing`,
			tenantID(ctx), id, f.Schedule.Code, f.LedgerID, entity, f.FireTime, f.Period.Year, f.Period.No, f.Attempts, f.Run.ID, f.StartedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var err error
			out, err = firingTx(ctx, tx, f.Schedule.Code, f.LedgerID, f.FireTime)
			return err
		}
		recipients := recipientsDigest(f.Schedule.Recipients)
		limit := int64(f.Schedule.AttachmentLimitMB) << 20
		for _, o := range f.Outputs {
			sum := sha256.Sum256(o.Content)
			digest := hex.EncodeToString(sum[:])
			libID := ids.New()
			if _, err := tx.Exec(ctx, `insert into report_library_entry(tenant_id, id, run_id, firing_id, schedule_code, definition_code,
					definition_version, ledger_id, legal_entity_id, period_year, period_no, currency_type, format, file_name,
					content_type, size_bytes, sha256, content, retain_until)
				values($1,$2,$3::uuid,$4,$5,$6,$7,$8::uuid,nullif($9,'')::uuid,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
				tenantID(ctx), libID, f.Run.ID, id, f.Schedule.Code, f.Run.DefinitionCode, f.Run.DefinitionVersion, f.LedgerID, entity,
				f.Run.PeriodYear, f.Run.PeriodNo, f.Run.CurrencyType, o.Format, o.FileName, o.ContentType, int64(len(o.Content)), digest,
				o.Content, f.RetainUntil); err != nil {
				return err
			}
			delivery := "attachment"
			if int64(len(o.Content)) > limit {
				delivery = "link"
			}
			if err := enqueue(ctx, tx, "distribution", libID, events.DistributionSent, tenantID(ctx), events.DistributionSentData{
				ScheduleCode: f.Schedule.Code, RunID: f.Run.ID, LibraryID: libID, LedgerID: f.LedgerID, LegalEntityID: entity,
				DefinitionCode: f.Run.DefinitionCode, Period: f.Period.Label(), Format: o.Format, Size: int64(len(o.Content)), SHA256: digest,
				Delivery: delivery, RecipientCount: len(f.Schedule.Recipients), RecipientsSHA256: recipients,
			}); err != nil {
				return err
			}
		}
		var err2 error
		out, err2 = firingTx(ctx, tx, f.Schedule.Code, f.LedgerID, f.FireTime)
		return err2
	})
	return out, err
}

// recipientsDigest is the SHA-256 of the sorted recipient addresses joined by a
// newline, the "recipients (hashed)" of distribution.sent.v1.
func recipientsDigest(recipients []string) string {
	sorted := append([]string(nil), recipients...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

// truncateRunes cuts s to at most n bytes without splitting a UTF-8 sequence
// (a text column refuses invalid UTF-8, which would lose the failure record).
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// FiringFailure is a firing that ended without a statement.
type FiringFailure struct {
	Schedule  domain.Schedule
	LedgerID  string
	FireTime  time.Time
	Period    domain.FiringPeriod
	RunID     string
	Stage     string
	Code      string
	Detail    string
	Attempts  int
	StartedAt time.Time
}

// RecordFailure stores a failed firing and writes statement.failed.v1 in the
// same transaction. Like RecordFiring it records a firing once.
func (s *Postgres) RecordFailure(ctx context.Context, f FiringFailure) (Firing, error) {
	var out Firing
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The ledger's entity is known only if the read model has the ledger; a
		// failure because it does not is recorded without one.
		entity, _, err := ledgerEntityTx(ctx, tx, f.LedgerID, "")
		if err != nil {
			entity = ""
		}
		id := ids.New()
		detail := truncateRunes(f.Detail, 2000)
		tag, err := tx.Exec(ctx, `insert into report_schedule_firing(tenant_id, id, schedule_code, ledger_id, legal_entity_id, fire_time,
				period_year, period_no, status, attempts, run_id, error_code, error_detail, started_at)
			values($1,$2,$3,$4::uuid,nullif($5,'')::uuid,$6,$7,$8,'failed',$9,nullif($10,'')::uuid,$11,$12,$13)
			on conflict (tenant_id, schedule_code, ledger_id, fire_time) do nothing`,
			tenantID(ctx), id, f.Schedule.Code, f.LedgerID, entity, f.FireTime, f.Period.Year, f.Period.No, f.Attempts, f.RunID, f.Code, detail, f.StartedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			if err := enqueue(ctx, tx, "statement", id, events.StatementFailed, tenantID(ctx), events.StatementFailedData{
				ScheduleCode: f.Schedule.Code, DefinitionCode: f.Schedule.Definition, LedgerID: f.LedgerID, LegalEntityID: entity,
				Period: f.Period.Label(), FireTime: f.FireTime.UTC().Format(time.RFC3339), RunID: f.RunID, Stage: f.Stage, Code: f.Code,
				Error: detail, Attempts: f.Attempts,
			}); err != nil {
				return err
			}
		}
		out, err = firingTx(ctx, tx, f.Schedule.Code, f.LedgerID, f.FireTime)
		return err
	})
	return out, err
}

const firingColumns = `id::text, schedule_code, ledger_id::text, coalesce(legal_entity_id::text, ''), fire_time, period_year, period_no,
	status, attempts, coalesce(run_id::text, ''), error_code, error_detail, started_at, finished_at`

func scanFiring(row pgx.Row) (Firing, error) {
	var f Firing
	err := row.Scan(&f.ID, &f.ScheduleCode, &f.LedgerID, &f.LegalEntityID, &f.FireTime, &f.PeriodYear, &f.PeriodNo,
		&f.Status, &f.Attempts, &f.RunID, &f.ErrorCode, &f.ErrorDetail, &f.StartedAt, &f.FinishedAt)
	return f, err
}

func firingTx(ctx context.Context, tx pgx.Tx, schedule, ledger string, fire time.Time) (Firing, error) {
	f, err := scanFiring(tx.QueryRow(ctx, `select `+firingColumns+` from report_schedule_firing
		where schedule_code=$1 and ledger_id=$2::uuid and fire_time=$3`, schedule, ledger, fire))
	if errors.Is(err, pgx.ErrNoRows) {
		return Firing{}, fmt.Errorf("firing %s/%s/%s was recorded but is not visible to the caller", schedule, ledger, fire)
	}
	return f, err
}

// ListFirings returns the firings of the schedule (all schedules when code is
// empty), newest first, at most limit. With latest, only the newest firing of
// each schedule and ledger is listed, so the last run of every schedule is
// there however many firings older ones have.
func (s *Postgres) ListFirings(ctx context.Context, schedule string, latest bool, limit int) ([]Firing, error) {
	if limit < 1 || limit > MaxFiringPage {
		limit = MaxFiringPage
	}
	out := []Firing{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+firingColumns+` from report_schedule_firing
			where ($1 = '' or schedule_code = $1)
			  and (not $3 or (schedule_code, ledger_id, fire_time) in (
			    select schedule_code, ledger_id, max(fire_time) from report_schedule_firing
			    where ($1 = '' or schedule_code = $1) group by schedule_code, ledger_id))
			order by fire_time desc, id desc limit $2`, schedule, limit, latest)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFiring(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}
