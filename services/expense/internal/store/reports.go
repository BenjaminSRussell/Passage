// Package store persists expense reports and lines. Every call runs in a tenant
// transaction (dbx.WithTenantTx), so row-level security applies, and every query
// also names the tenant explicitly. The outbox row for a state change is
// committed in the same transaction as the change.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	kevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/events"
)

// Problem codes raised by the report store (listed in the service README).
const (
	CodeReportNotFound   = "EXP-REP-001"
	CodeReportState      = "EXP-REP-002"
	CodeReportEmpty      = "EXP-REP-003"
	CodeVersionConflict  = "EXP-REP-004"
	CodeDeciderUnknown   = "EXP-REP-005"
	CodeLineExists       = "EXP-LINE-001"
	CodeLineNotFound     = "EXP-LINE-002"
	CodeLineLimit        = "EXP-LINE-003"
	CodeIdempotencyReuse = "EXP-IDEM-001"
	CodeOutOfScope       = "AUTH-0004"
)

// MaxLines bounds the lines of one report (the evaluation preview has the same limit).
const MaxLines = 500

const (
	outboxAggregate = "expense_report"
	uniqueViolation = "23505"
	rlsViolation    = "42501"
)

// Report is a persisted expense report with its lines (lines are empty in list pages).
type Report struct {
	ID            string
	LegalEntityID string
	WorkerID      string
	Purpose       string
	Currency      string
	Status        string
	Total         decimal.Decimal
	Reimbursable  decimal.Decimal
	CompanyPaid   decimal.Decimal
	Version       int
	CreatedAt     time.Time
	SubmittedAt   *time.Time
	CreatedBy     string
	SubmittedBy   string
	APOpenItemID  string
	ReimbursedAt  *time.Time
	Lines         []ReportLine
}

// ReportLine is one persisted line with the policy results of its last evaluation.
type ReportLine struct {
	domain.Line
	ReportID string
	LineNo   int
	Results  []domain.Result
}

// NewReport is the input to CreateReport.
type NewReport struct {
	LegalEntityID string
	WorkerID      string
	Purpose       string
	Currency      string
}

// ReportFilter narrows ListReports. Empty fields do not filter.
type ReportFilter struct {
	WorkerID string
	Status   string
}

// Store persists reports and lines.
type Store struct{ pool *pgxpool.Pool }

// New returns a Store over pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const reportColumns = `id::text, legal_entity_id::text, worker_id::text, purpose, currency::text, status,
  total_amount::text, reimbursable_amount::text, company_paid_amount::text, version, created_at, submitted_at,
  created_by, submitted_by, coalesce(ap_open_item_id::text, ''), reimbursed_at`

const lineColumns = `id::text, report_id::text, line_no, category, txn_date, amount::text, merchant,
  attendees, has_receipt, justification, paid_by, policy_results`

// CreateReport inserts a DRAFT report. A replayed key with the same request hash
// returns the original report with replayed=true; the same key with another body
// is refused with EXP-IDEM-001.
func (s *Store) CreateReport(ctx context.Context, key, hash string, in NewReport) (Report, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Report{}, false, err
	}
	if !tenant.InLegalEntityScope(p, in.LegalEntityID) {
		return Report{}, false, outOfScope()
	}
	var out Report
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `insert into expense_report(tenant_id, id, legal_entity_id, worker_id, purpose, currency, create_key, request_hash, created_by)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9) on conflict (tenant_id, create_key) do nothing returning id::text`,
			p.TenantID, ids.New(), in.LegalEntityID, in.WorkerID, in.Purpose, in.Currency, key, hash, p.UserID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			var stored string
			if err := tx.QueryRow(ctx, `select id::text, request_hash from expense_report where tenant_id=$1 and create_key=$2`,
				p.TenantID, key).Scan(&id, &stored); err != nil {
				return notFoundOr(err)
			}
			if stored != hash {
				return reuse()
			}
			replayed = true
		} else if err != nil {
			return translate(err, CodeIdempotencyReuse)
		}
		out, err = loadReport(ctx, tx, p.TenantID, id)
		return err
	})
	if err != nil {
		return Report{}, false, err
	}
	return out, replayed, nil
}

// GetReport loads one report with its lines.
func (s *Store) GetReport(ctx context.Context, id string) (Report, error) {
	p, err := principal(ctx)
	if err != nil {
		return Report{}, err
	}
	var out Report
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadReport(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ListReports returns up to limit reports after the cursor (a report id), newest
// id last, without lines. The next cursor is empty on the last page.
func (s *Store) ListReports(ctx context.Context, f ReportFilter, after string, limit int) ([]Report, string, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, "", err
	}
	if after == "" {
		after = ids.Nil
	}
	out := []Report{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+reportColumns+` from expense_report
			where tenant_id=$1 and id > $2::uuid and ($3 = '' or worker_id::text = $3) and ($4 = '' or status = $4)
			order by id limit $5`, p.TenantID, after, f.WorkerID, f.Status, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReport(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// AddLine appends a line to a DRAFT report and re-evaluates the report's lines at
// commit (FR-43.12): the per-day meal rule and duplicate detection look across the
// report, so an earlier line's results can change too. Commit records the results
// and the stored totals; it does not refuse a line for its policy outcome. Submit
// is where unsatisfied outcomes stop the report.
func (s *Store) AddLine(ctx context.Context, reportID, key, hash string, l domain.Line, commitDate time.Time) (Report, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Report{}, false, err
	}
	var out Report
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		status, _, err := lockReport(ctx, tx, p.TenantID, reportID)
		if err != nil {
			return err
		}
		var stored string
		err = tx.QueryRow(ctx, `select request_hash from expense_line where tenant_id=$1 and report_id=$2 and create_key=$3`,
			p.TenantID, reportID, key).Scan(&stored)
		if err == nil {
			if stored != hash {
				return reuse()
			}
			replayed = true
			out, err = loadReport(ctx, tx, p.TenantID, reportID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !domain.CanAddLine(domain.ReportStatus(status)) {
			return state(status)
		}
		current, err := loadReport(ctx, tx, p.TenantID, reportID)
		if err != nil {
			return err
		}
		if len(current.Lines) >= MaxLines {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeLineLimit, "a report holds at most "+strconv.Itoa(MaxLines)+" lines")
		}
		if l.PaidBy == "" {
			l.PaidBy = domain.PaidByEmployee
		}
		next := 1
		if n := len(current.Lines); n > 0 {
			next = current.Lines[n-1].LineNo + 1
		}
		_, err = tx.Exec(ctx, `insert into expense_line(tenant_id, id, report_id, line_no, category, txn_date, amount, merchant,
			attendees, has_receipt, justification, paid_by, create_key, request_hash)
			values($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$11,$12,$13,$14)`,
			p.TenantID, l.ID, reportID, next, l.Category, l.TxnDate, l.Amount.String(), l.Merchant,
			l.Attendees, l.HasReceipt, l.Justification, l.PaidBy, key, hash)
		if err != nil {
			return translate(err, CodeLineExists)
		}
		lines := append(current.Lines, ReportLine{Line: l, ReportID: reportID, LineNo: next})
		if _, err := storeResults(ctx, tx, p.TenantID, reportID, lines, commitDate); err != nil {
			return err
		}
		if err := storeTotals(ctx, tx, p.TenantID, reportID, lines); err != nil {
			return err
		}
		out, err = loadReport(ctx, tx, p.TenantID, reportID)
		return err
	})
	if err != nil {
		return Report{}, false, err
	}
	return out, replayed, nil
}

// DeleteLine removes a line from a DRAFT report and re-evaluates the remaining
// lines at commitDate. It is how a worker corrects a line that a refused submit
// flagged. expectedVersion, when positive, must equal the report version.
func (s *Store) DeleteLine(ctx context.Context, reportID, lineID string, expectedVersion int, commitDate time.Time) (Report, error) {
	p, err := principal(ctx)
	if err != nil {
		return Report{}, err
	}
	var out Report
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		status, version, err := lockReport(ctx, tx, p.TenantID, reportID)
		if err != nil {
			return err
		}
		if expectedVersion > 0 && expectedVersion != version {
			return versionConflict(version)
		}
		if !domain.CanAddLine(domain.ReportStatus(status)) {
			return state(status)
		}
		tag, err := tx.Exec(ctx, `delete from expense_line where tenant_id=$1 and report_id=$2 and id=$3`, p.TenantID, reportID, lineID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeLineNotFound, "expense line not found on this report")
		}
		current, err := loadReport(ctx, tx, p.TenantID, reportID)
		if err != nil {
			return err
		}
		if _, err := storeResults(ctx, tx, p.TenantID, reportID, current.Lines, commitDate); err != nil {
			return err
		}
		if err := storeTotals(ctx, tx, p.TenantID, reportID, current.Lines); err != nil {
			return err
		}
		out, err = loadReport(ctx, tx, p.TenantID, reportID)
		return err
	})
	return out, err
}

// SubmitReport moves a DRAFT report to SUBMITTED (8.43.5: every REQUIRE_* satisfied
// and no BLOCK). It re-evaluates every line at submitDate and stores the results.
// When any result is unsatisfied the report stays DRAFT, the refusal is returned as
// a 422 problem, and each BLOCK result is still committed to the outbox as
// policy.violated.v1 (the transaction commits before the refusal is returned).
//
// The outcome, success or refusal, is stored against the Idempotency-Key, so a
// retry with the same key replays it and never publishes the violations twice.
// expectedVersion, when positive, must equal the report version (If-Match).
func (s *Store) SubmitReport(ctx context.Context, reportID, key, hash string, expectedVersion int, submitDate time.Time) (Report, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Report{}, false, err
	}
	var out Report
	replayed := false
	var refused *httpx.Problem
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		status, version, err := lockReport(ctx, tx, p.TenantID, reportID)
		if err != nil {
			return err
		}
		var storedHash string
		var storedStatus int
		var storedBody []byte
		err = tx.QueryRow(ctx, `select request_hash, response_status, response_body from idempotency_keys where tenant_id=$1 and key=$2`,
			p.TenantID, key).Scan(&storedHash, &storedStatus, &storedBody)
		if err == nil {
			if storedHash != hash {
				return reuse()
			}
			if storedStatus != http.StatusOK {
				refused = &httpx.Problem{}
				if err := json.Unmarshal(storedBody, refused); err != nil {
					return err
				}
				return nil
			}
			replayed = true
			out, err = loadReport(ctx, tx, p.TenantID, reportID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if expectedVersion > 0 && expectedVersion != version {
			return versionConflict(version)
		}
		if !domain.CanSubmit(domain.ReportStatus(status)) {
			return state(status)
		}
		current, err := loadReport(ctx, tx, p.TenantID, reportID)
		if err != nil {
			return err
		}
		if len(current.Lines) == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeReportEmpty, "a report needs at least one line to submit")
		}
		results, err := storeResults(ctx, tx, p.TenantID, reportID, current.Lines, submitDate)
		if err != nil {
			return err
		}
		if blockers := domain.Blockers(results); len(blockers) > 0 {
			for _, b := range blockers {
				if b.Outcome != domain.OutcomeBlock {
					continue
				}
				evt, err := kevents.New(events.PolicyViolated, events.Source, reportID, p.TenantID, events.PolicyViolatedData{
					ReportID: reportID, LineID: b.LineID, RuleID: b.RuleID, Outcome: string(b.Outcome),
				})
				if err != nil {
					return err
				}
				if err := outbox.Enqueue(ctx, tx, outboxAggregate, reportID, evt); err != nil {
					return err
				}
			}
			refused = refusal(blockers)
			return remember(ctx, tx, p.TenantID, key, hash, refused.Status, refused)
		}
		if _, err := tx.Exec(ctx, `update expense_report set status=$3, submitted_at=now(), submitted_by=$4,
			updated_at=now(), version=version+1 where tenant_id=$1 and id=$2`, p.TenantID, reportID, string(domain.ReportSubmitted), p.UserID); err != nil {
			return err
		}
		if err := remember(ctx, tx, p.TenantID, key, hash, http.StatusOK, map[string]string{"reportId": reportID}); err != nil {
			return err
		}
		out, err = loadReport(ctx, tx, p.TenantID, reportID)
		return err
	})
	if err != nil {
		return Report{}, false, err
	}
	if refused != nil {
		return Report{}, false, refused
	}
	return out, replayed, nil
}

// lockReport locks the report row for the rest of tx and returns its status and version.
func lockReport(ctx context.Context, tx pgx.Tx, tenantID, id string) (string, int, error) {
	var status string
	var version int
	err := tx.QueryRow(ctx, `select status, version from expense_report where tenant_id=$1 and id=$2 for update`, tenantID, id).Scan(&status, &version)
	return status, version, notFoundOr(err)
}

// storeResults evaluates the report's lines on date and stores each line's results.
func storeResults(ctx context.Context, tx pgx.Tx, tenantID, reportID string, lines []ReportLine, date time.Time) ([]domain.Result, error) {
	results := domain.EvaluateReport(domain.DefaultRules(), domainLines(lines), domain.Context{SubmitDate: date})
	for _, l := range lines {
		payload, err := json.Marshal(nonNil(domain.ForLine(results, l.ID)))
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `update expense_line set policy_results=$4 where tenant_id=$1 and report_id=$2 and id=$3`,
			tenantID, reportID, l.ID, payload); err != nil {
			return nil, err
		}
	}
	return results, nil
}

// storeTotals writes the report totals from its lines and bumps the version.
func storeTotals(ctx context.Context, tx pgx.Tx, tenantID, reportID string, lines []ReportLine) error {
	t := domain.SumLines(domainLines(lines))
	_, err := tx.Exec(ctx, `update expense_report set total_amount=$3::numeric, reimbursable_amount=$4::numeric,
		company_paid_amount=$5::numeric, version=version+1, updated_at=now() where tenant_id=$1 and id=$2`,
		tenantID, reportID, t.Total.String(), t.Reimbursable.String(), t.CompanyPaid.String())
	return err
}

// remember stores a command outcome against its Idempotency-Key.
func remember(ctx context.Context, tx pgx.Tx, tenantID, key, hash string, status int, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys(tenant_id, key, request_hash, response_status, response_body) values($1,$2,$3,$4,$5)`,
		tenantID, key, hash, status, payload)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return reuse()
	}
	return err
}

// loadReport reads a report and its lines inside tx.
func loadReport(ctx context.Context, tx pgx.Tx, tenantID, id string) (Report, error) {
	rep, err := scanReport(tx.QueryRow(ctx, `select `+reportColumns+` from expense_report where tenant_id=$1 and id=$2`, tenantID, id))
	if err != nil {
		return Report{}, notFoundOr(err)
	}
	rows, err := tx.Query(ctx, `select `+lineColumns+` from expense_line where tenant_id=$1 and report_id=$2 order by line_no`, tenantID, id)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return Report{}, err
		}
		rep.Lines = append(rep.Lines, l)
	}
	return rep, rows.Err()
}

func scanReport(r pgx.Row) (Report, error) {
	var rep Report
	var total, reimbursable, companyPaid string
	if err := r.Scan(&rep.ID, &rep.LegalEntityID, &rep.WorkerID, &rep.Purpose, &rep.Currency, &rep.Status,
		&total, &reimbursable, &companyPaid, &rep.Version, &rep.CreatedAt, &rep.SubmittedAt,
		&rep.CreatedBy, &rep.SubmittedBy, &rep.APOpenItemID, &rep.ReimbursedAt); err != nil {
		return Report{}, err
	}
	var err error
	if rep.Total, err = decimal.NewFromString(total); err != nil {
		return Report{}, err
	}
	if rep.Reimbursable, err = decimal.NewFromString(reimbursable); err != nil {
		return Report{}, err
	}
	if rep.CompanyPaid, err = decimal.NewFromString(companyPaid); err != nil {
		return Report{}, err
	}
	return rep, nil
}

func scanLine(r pgx.Row) (ReportLine, error) {
	var l ReportLine
	var amount string
	var results []byte
	if err := r.Scan(&l.ID, &l.ReportID, &l.LineNo, &l.Category, &l.TxnDate, &amount, &l.Merchant,
		&l.Attendees, &l.HasReceipt, &l.Justification, &l.PaidBy, &results); err != nil {
		return ReportLine{}, err
	}
	var err error
	if l.Amount, err = decimal.NewFromString(amount); err != nil {
		return ReportLine{}, err
	}
	l.TxnDate = time.Date(l.TxnDate.Year(), l.TxnDate.Month(), l.TxnDate.Day(), 0, 0, 0, 0, time.UTC)
	if err := json.Unmarshal(results, &l.Results); err != nil {
		return ReportLine{}, err
	}
	return l, nil
}

func domainLines(ls []ReportLine) []domain.Line {
	out := make([]domain.Line, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Line)
	}
	return out
}

func nonNil(rs []domain.Result) []domain.Result {
	if rs == nil {
		return []domain.Result{}
	}
	return rs
}

func principal(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok || !ids.Valid(p.TenantID) {
		return tenant.Principal{}, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant")
	}
	return p, nil
}

func notFoundOr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusNotFound, CodeReportNotFound, "expense report not found")
	}
	return err
}

func reuse() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
}

func outOfScope() error {
	return httpx.NewProblem(http.StatusForbidden, CodeOutOfScope, "legal entity is outside the caller's scope")
}

func state(status string) error {
	return httpx.NewProblem(http.StatusConflict, CodeReportState, "report is "+status+" and this action needs a DRAFT report")
}

func versionConflict(current int) error {
	return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
		"report changed since it was read; it is now at version "+strconv.Itoa(current))
}

// refusal is the 422 for a refused submit: the first unsatisfied result's code, and
// one error per result keyed "<lineId>/<ruleId>".
func refusal(blockers []domain.Result) *httpx.Problem {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, blockers[0].Code, "report has unsatisfied policy results")
	p.Errors = map[string]string{}
	for _, b := range blockers {
		p.Errors[b.LineID+"/"+b.RuleID] = b.Message
	}
	return p
}

// translate maps Postgres errors: a unique violation becomes dupCode (409), and an
// RLS rejection means the row is outside the caller's legal-entity scope (403).
func translate(err error, dupCode string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case uniqueViolation:
			return httpx.NewProblem(http.StatusConflict, dupCode, "a record with this id or key already exists")
		case rlsViolation:
			return outOfScope()
		}
	}
	return err
}
