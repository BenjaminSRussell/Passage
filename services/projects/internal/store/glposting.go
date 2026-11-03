package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

const (
	// MaxGLItemsPerRun bounds the transactions and the project estimates one posting run books.
	MaxGLItemsPerRun = 200
	// MaxGLAttempts is how many failed runs park a transaction until a run asks to retry parked rows.
	MaxGLAttempts = 5

	statusPending = "PENDING"
	statusPosted  = "POSTED"
	sourceTxn     = "project-transaction"
	sourceWIP     = "project-estimate"
)

// GL books project transactions and estimates in the general ledger and reconciles them to its balances.
type GL interface {
	PostGL(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error)
	ReconcileGL(ctx context.Context, ledger ledgerclient.Ledger, in GLReconcileRequest) (GLReconciliation, error)
}

// GLPostRequest selects what a run books. Period (YYYY-MM) also books the estimate of fixed-price projects at that month end.
// RetryParked puts the entity's parked transactions back in the queue first.
type GLPostRequest struct {
	LegalEntityID string
	LedgerID      string
	Currency      string
	Period        string
	ProjectID     string
	RetryParked   bool
}

type GLBooked struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	EntryID string `json:"entryId"`
	Lines   int    `json:"lines"`
}

type GLFailed struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Code string `json:"code"`
}

type GLPostResult struct {
	Booked []GLBooked `json:"booked"`
	Failed []GLFailed `json:"failed"`
	More   bool       `json:"more"`
}

type booking struct {
	id, kind, ref, projectID, legalEntityID, ledgerID, currency, date, key, status string
	txnID, period                                                                  *string
	position                                                                       *domain.Position
	lines                                                                          []domain.GLLine
}

func (b booking) entry() ledgerclient.Entry {
	e := ledgerclient.Entry{LegalEntityID: b.legalEntityID, LedgerID: b.ledgerID, DocumentID: b.ref, PostingDate: b.date, Currency: b.currency, IdempotencyKey: b.key, SourceType: sourceWIP}
	if b.kind == domain.GLKindTxn {
		e.SourceType = sourceTxn
	}
	for _, l := range b.lines {
		e.Lines = append(e.Lines, ledgerclient.Line{Account: l.Account, Credit: l.Credit, Amount: decimal.RequireFromString(l.Amount)})
	}
	return e
}

const bookingColumns = `id::text, kind, coalesce(txn_id::text, project_id || ':' || period), project_id, legal_entity_id::text, ledger_id::text, currency, posting_date::text, idempotency_key, status, lines`

func scanBooking(row pgx.Row) (booking, error) {
	var b booking
	err := row.Scan(&b.id, &b.kind, &b.ref, &b.projectID, &b.legalEntityID, &b.ledgerID, &b.currency, &b.date, &b.key, &b.status, &b.lines)
	return b, err
}

func loadBooking(ctx context.Context, tx pgx.Tx, tenantID, key string) (booking, error) {
	return scanBooking(tx.QueryRow(ctx, `select `+bookingColumns+` from project_gl_booking where tenant_id = $1 and idempotency_key = $2`, tenantID, key))
}

// store writes the booking under its idempotency key and returns the row that holds the key, which is the earlier one when
// another run got there first, so every run resends the same payload.
func (b booking) store(ctx context.Context, tx pgx.Tx, tenantID string) (booking, error) {
	if b.lines == nil {
		b.lines = []domain.GLLine{}
	}
	raw, err := json.Marshal(b.lines)
	if err != nil {
		return booking{}, err
	}
	var rev, billed, prov, cost *string
	if b.position != nil {
		r, bl, pv, c := b.position.Revenue.String(), b.position.Billed.String(), b.position.Provision.String(), b.position.Cost.String()
		rev, billed, prov, cost = &r, &bl, &pv, &c
	}
	if _, err := tx.Exec(ctx, `insert into project_gl_booking (tenant_id, id, legal_entity_id, project_id, kind, txn_id, period, ledger_id, currency, posting_date,
			idempotency_key, lines, revenue_to_date, billed_to_date, provision_to_date, cost_to_date, status, posted_at)
		values ($1, $2, $3, $4, $5, $6::uuid, $7, $8, $9, $10::date, $11, $12, $13::numeric, $14::numeric, $15::numeric, $16::numeric, $17, case when $17 = 'POSTED' then now() end)
		on conflict (tenant_id, idempotency_key) do nothing`,
		tenantID, ids.New(), b.legalEntityID, b.projectID, b.kind, b.txnID, b.period, b.ledgerID, b.currency, b.date, b.key, raw, rev, billed, prov, cost, b.status); err != nil {
		return booking{}, err
	}
	return loadBooking(ctx, tx, tenantID, b.key)
}

func loadProfiles(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.PostingProfile, error) {
	rows, err := tx.Query(ctx, `select `+profileColumns+` from posting_profile where tenant_id = $1 order by project_group, category, line_property`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PostingProfile{}
	for rows.Next() {
		pr, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

func glProblem(status int, code, detail string) error { return httpx.NewProblem(status, code, detail) }

// checkLedgerOwner refuses a ledger that the legal entity does not own or that the ledger service cannot read for it.
func checkLedgerOwner(ctx context.Context, ledger ledgerclient.Ledger, entity, ledgerID string) error {
	owner, err := ledger.LegalEntity(ctx, entity, ledgerID)
	if err != nil {
		slog.ErrorContext(ctx, "projects cannot read the ledger", "ledger", ledgerID, "err", err, "correlationId", middleware.GetReqID(ctx))
		var le *ledgerclient.Error
		if errors.As(err, &le) && (le.Status == http.StatusNotFound || le.Status == http.StatusUnprocessableEntity) {
			return glProblem(http.StatusUnprocessableEntity, domain.CodeLedgerForeign, "the ledger is unknown or not readable for this legal entity ("+le.Code+")")
		}
		return glProblem(http.StatusBadGateway, domain.CodeLedgerForeign, "the general ledger did not answer the ledger lookup")
	}
	if owner != entity {
		return glProblem(http.StatusUnprocessableEntity, domain.CodeLedgerForeign, "the ledger belongs to another legal entity")
	}
	return nil
}

// pinLedger binds the legal entity's project postings to one ledger: the first posting (create) pins the ledger it names after the ledger
// service confirms the entity owns it, and any other ledger afterwards is 409, because the WIP position chain is per project and a second
// ledger would receive only the movement.
func (s *Postgres) pinLedger(ctx context.Context, ledger ledgerclient.Ledger, entity, ledgerID string, create bool) error {
	p, _ := tenant.FromContext(ctx)
	var pinned string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select ledger_id::text from project_gl_ledger where tenant_id = $1 and legal_entity_id = $2`, p.TenantID, entity).Scan(&pinned)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	mismatch := func(have string) error {
		return glProblem(http.StatusConflict, domain.CodeLedgerPinned, "the legal entity's project postings are pinned to ledger "+have)
	}
	if pinned != "" {
		if pinned != ledgerID {
			return mismatch(pinned)
		}
		return nil
	}
	if err := checkLedgerOwner(ctx, ledger, entity, ledgerID); err != nil || !create {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into project_gl_ledger (tenant_id, legal_entity_id, ledger_id) values ($1, $2, $3) on conflict do nothing`, p.TenantID, entity, ledgerID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select ledger_id::text from project_gl_ledger where tenant_id = $1 and legal_entity_id = $2`, p.TenantID, entity).Scan(&pinned); err != nil {
			return err
		}
		if pinned != ledgerID {
			return mismatch(pinned)
		}
		return nil
	})
}

type glTxnRow struct{ id, projectID, group, category, property, date, cost string }

type glProjectRow struct{ id, group, contract string }

const markFailedSQL = `update project_transaction set gl_attempts = gl_attempts + 1, gl_last_error = $3,
	gl_next_attempt_at = now() + least(power(2, gl_attempts) * interval '1 minute', interval '6 hours'),
	gl_parked_at = case when gl_attempts + 1 >= $4 then now() end
	where tenant_id = $1 and id = $2::uuid`

func glCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return domain.CodeGLFailed
}

// planGL stores the bookings of this run's due, unposted transactions and returns the estimate candidates. Amounts and accounts come from the
// stored transaction and the stored posting profile only. Rows that failed recently wait for their next attempt, and parked rows are skipped,
// so a backlog of failing rows cannot keep newer transactions from posting.
func (s *Postgres) planGL(ctx context.Context, in GLPostRequest, res *GLPostResult) (todo []booking, projects []glProjectRow, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if in.RetryParked {
			if _, err := tx.Exec(ctx, `update project_transaction t set gl_attempts = 0, gl_parked_at = null, gl_next_attempt_at = null
				from project p where p.tenant_id = t.tenant_id and p.id = t.project_id and t.tenant_id = $1 and p.legal_entity_id = $2 and t.gl_parked_at is not null`,
				p.TenantID, in.LegalEntityID); err != nil {
				return err
			}
		}
		profiles, err := loadProfiles(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select t.id::text, t.project_id, p.project_group_id, t.category_id, t.line_property, t.txn_date::text, t.cost_amount::text
			from project_transaction t join project p on p.tenant_id = t.tenant_id and p.id = t.project_id
			where t.tenant_id = $1 and p.legal_entity_id = $2 and t.currency = $3 and t.journal_id is null and t.cost_amount <> 0 and ($4 = '' or t.project_id = $4)
				and t.gl_parked_at is null and (t.gl_next_attempt_at is null or t.gl_next_attempt_at <= now())
			order by t.txn_date, t.id limit $5`, p.TenantID, in.LegalEntityID, in.Currency, in.ProjectID, MaxGLItemsPerRun+1)
		if err != nil {
			return err
		}
		var txns []glTxnRow
		for rows.Next() {
			var r glTxnRow
			if err := rows.Scan(&r.id, &r.projectID, &r.group, &r.category, &r.property, &r.date, &r.cost); err != nil {
				rows.Close()
				return err
			}
			txns = append(txns, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(txns) > MaxGLItemsPerRun {
			res.More, txns = true, txns[:MaxGLItemsPerRun]
		}
		for _, r := range txns {
			profile, err := domain.Resolve(profiles, domain.ProfileKey{ProjectGroup: r.group, Category: r.category, LineProperty: r.property})
			var lines []domain.GLLine
			if err == nil {
				lines, err = domain.TransactionLines(profile, decimal.RequireFromString(r.cost), in.Currency)
			}
			if err != nil {
				code := glCode(err)
				if _, err := tx.Exec(ctx, markFailedSQL, p.TenantID, r.id, code, MaxGLAttempts); err != nil {
					return err
				}
				res.Failed = append(res.Failed, GLFailed{Kind: domain.GLKindTxn, Ref: r.id, Code: code})
				continue
			}
			b, err := booking{kind: domain.GLKindTxn, projectID: r.projectID, legalEntityID: in.LegalEntityID, ledgerID: in.LedgerID, currency: in.Currency, date: r.date,
				key: fmt.Sprintf("projects:txn:%s:1", r.id), status: statusPending, txnID: &r.id, lines: lines}.store(ctx, tx, p.TenantID)
			if err != nil {
				return err
			}
			if b.status == statusPending {
				todo = append(todo, b)
			}
		}
		if in.Period == "" {
			return nil
		}
		prows, err := tx.Query(ctx, `select id, project_group_id, contract_value::text from project p
			where tenant_id = $1 and legal_entity_id = $2 and currency = $3 and project_type = 'FIXED_PRICE' and stage <> 'CREATED' and ($4 = '' or id = $4)
				and not exists (select 1 from project_gl_booking b where b.tenant_id = p.tenant_id and b.project_id = p.id and b.kind = 'WIP' and b.period = $6 and b.status = 'POSTED')
			order by id limit $5`, p.TenantID, in.LegalEntityID, in.Currency, in.ProjectID, MaxGLItemsPerRun+1, in.Period)
		if err != nil {
			return err
		}
		defer prows.Close()
		for prows.Next() {
			var r glProjectRow
			if err := prows.Scan(&r.id, &r.group, &r.contract); err != nil {
				return err
			}
			projects = append(projects, r)
		}
		return prows.Err()
	})
	return todo, projects, err
}

// planEstimate stores the booking of one fixed-price project's estimate for the period, under the project row lock so two runs cannot both derive
// the position from the same prior period. It returns a failure code instead of a booking when the estimate cannot be derived, and neither when
// the period is already booked.
func (s *Postgres) planEstimate(ctx context.Context, in GLPostRequest, pr glProjectRow) (*booking, string, error) {
	p, _ := tenant.FromContext(ctx)
	var out *booking
	code := ""
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select 1 from project where tenant_id = $1 and id = $2 for update`, p.TenantID, pr.id); err != nil {
			return err
		}
		key := fmt.Sprintf("projects:wip:%s:%s", pr.id, in.Period)
		if b, err := loadBooking(ctx, tx, p.TenantID, key); err == nil {
			if b.status == statusPending {
				out = &b
			}
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var later bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from project_gl_booking where tenant_id = $1 and project_id = $2 and kind = 'WIP' and period > $3)`,
			p.TenantID, pr.id, in.Period).Scan(&later); err != nil {
			return err
		}
		if later {
			code = domain.CodePeriodOrder
			return nil
		}
		var prior domain.Position
		var priorStatus *string
		var rev, billed, prov, priorCost *string
		err := tx.QueryRow(ctx, `select status, revenue_to_date::text, billed_to_date::text, provision_to_date::text, cost_to_date::text from project_gl_booking
			where tenant_id = $1 and project_id = $2 and kind = 'WIP' and period < $3 order by period desc limit 1`, p.TenantID, pr.id, in.Period).Scan(&priorStatus, &rev, &billed, &prov, &priorCost)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case *priorStatus != statusPosted:
			code = domain.CodePeriodOrder
			return nil
		default:
			prior = domain.Position{Revenue: decimal.RequireFromString(*rev), Billed: decimal.RequireFromString(*billed),
				Provision: decimal.RequireFromString(*prov), Cost: decimal.RequireFromString(*priorCost)}
		}
		monthStart, err := time.Parse("2006-01", in.Period)
		if err != nil {
			return err
		}
		next := monthStart.AddDate(0, 1, 0).Format(time.DateOnly)
		var modelID, modelCurrency string
		err = tx.QueryRow(ctx, `select id::text, currency from project_forecast_model where tenant_id = $1 and project_id = $2 and is_active`, p.TenantID, pr.id).Scan(&modelID, &modelCurrency)
		if errors.Is(err, pgx.ErrNoRows) {
			code = domain.CodeNoEstimate
			return nil
		}
		if err != nil {
			return err
		}
		if modelCurrency != in.Currency {
			code = domain.CodeEstimateCurrency
			return nil
		}
		var cost, etc, billedNow string
		if err := tx.QueryRow(ctx, `select coalesce(sum(cost_amount), 0)::text from project_transaction where tenant_id = $1 and project_id = $2 and txn_date < $3::date`,
			p.TenantID, pr.id, next).Scan(&cost); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select coalesce(sum(cost_amount), 0)::text from project_forecast_line where tenant_id = $1 and model_id = $2::uuid and period > $3`,
			p.TenantID, modelID, in.Period).Scan(&etc); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select coalesce(sum(l.amount), 0)::text from project_invoice_proposal_line l
			join project_invoice_proposal pp on pp.tenant_id = l.tenant_id and pp.id = l.proposal_id
			where l.tenant_id = $1 and pp.project_id = $2 and pp.status in ('APPROVED', 'POSTED') and pp.to_date < $3::date`, p.TenantID, pr.id, next).Scan(&billedNow); err != nil {
			return err
		}
		costNow, billedTotal := decimal.RequireFromString(cost), decimal.RequireFromString(billedNow)
		contract := decimal.RequireFromString(pr.contract)
		poc, err := domain.ComputePOC(domain.POCInput{Currency: in.Currency, ContractValue: contract, CostToDate: costNow,
			EstimatedToComplete: decimal.RequireFromString(etc), BilledToDate: billedTotal, PriorRevenueToDate: prior.Revenue})
		if err != nil {
			code = domain.CodeGLFailed
			return nil
		}
		profiles, err := loadProfiles(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		profile, err := domain.Resolve(profiles, domain.ProfileKey{ProjectGroup: pr.group, Category: domain.Wildcard, LineProperty: domain.Wildcard})
		if err != nil {
			code = domain.CodePostingProfile
			return nil
		}
		now := domain.Position{Revenue: poc.RevenueToDate, Billed: billedTotal, Cost: costNow,
			Provision: domain.RequiredProvision(poc.EAC, contract, costNow, poc.RevenueToDate)}
		lines, err := domain.EstimateLines(profile, prior, now, in.Currency)
		if err != nil {
			code = glCode(err)
			return nil
		}
		b := booking{kind: domain.GLKindWIP, projectID: pr.id, legalEntityID: in.LegalEntityID, ledgerID: in.LedgerID, currency: in.Currency, period: &in.Period,
			date: monthStart.AddDate(0, 1, -1).Format(time.DateOnly), key: key, status: statusPending, lines: lines, position: &now}
		if len(lines) == 0 {
			b.status = statusPosted
		}
		stored, err := b.store(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		if stored.status == statusPending {
			out = &stored
		}
		return nil
	})
	return out, code, err
}

// confirm marks a booking the ledger accepted as posted and links its transaction to the entry.
func (s *Postgres) confirm(ctx context.Context, b booking, entryID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update project_gl_booking set status = 'POSTED', journal_entry_id = $3::uuid, posted_at = now()
			where tenant_id = $1 and id = $2 and status = 'PENDING'`, p.TenantID, b.id, entryID); err != nil {
			return err
		}
		if b.kind != domain.GLKindTxn {
			return nil
		}
		_, err := tx.Exec(ctx, `update project_transaction set journal_id = $3::uuid, gl_last_error = null, gl_next_attempt_at = null
			where tenant_id = $1 and id = $2::uuid and journal_id is null`, p.TenantID, b.ref, entryID)
		return err
	})
}

func (s *Postgres) discard(ctx context.Context, b booking) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from project_gl_booking where tenant_id = $1 and id = $2 and status = 'PENDING'`, p.TenantID, b.id)
		return err
	})
}

func (s *Postgres) recordFailure(ctx context.Context, txnID, code string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, markFailedSQL, p.TenantID, txnID, code, MaxGLAttempts)
		return err
	})
}

// PostGL books the cost of due, unposted project transactions of the legal entity as Dr work in progress / Cr the profile's offset account, and,
// for a period, the movement of each fixed-price project's percentage-of-completion position (revenue, billing, cost of sales and loss provision).
// Each booking is stored before the ledger is called and resent unchanged until the ledger confirms it, so a retry returns the same entry. A booking
// the ledger refuses is reported and the rest go on; a refusal other than a closed period drops the stored booking so the next run rebuilds it from
// the current profile. A failing transaction backs off and is parked after MaxGLAttempts.
func (s *Postgres) PostGL(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error) {
	res := GLPostResult{Booked: []GLBooked{}, Failed: []GLFailed{}}
	if err := s.pinLedger(ctx, ledger, in.LegalEntityID, in.LedgerID, true); err != nil {
		return GLPostResult{}, err
	}
	todo, projects, err := s.planGL(ctx, in, &res)
	if err != nil {
		return GLPostResult{}, err
	}
	if len(projects) > MaxGLItemsPerRun {
		res.More, projects = true, projects[:MaxGLItemsPerRun]
	}
	for _, pr := range projects {
		b, code, err := s.planEstimate(ctx, in, pr)
		if err != nil {
			logGLFailure(ctx, domain.GLKindWIP, pr.id, "", domain.CodeGLFailed, err)
			code = domain.CodeGLFailed
		}
		switch {
		case code != "":
			res.Failed = append(res.Failed, GLFailed{Kind: domain.GLKindWIP, Ref: pr.id + ":" + in.Period, Code: code})
		case b != nil:
			todo = append(todo, *b)
		}
	}
	for _, b := range todo {
		entry := b.entry()
		entryID, err := ledger.Post(ctx, entry)
		if err != nil {
			code := ledgerclient.CodeOf(err)
			if code == "" {
				code = domain.CodeGLFailed
			}
			if ledgerclient.Rejected(err) && code != ledgerclient.CodePeriodClosed {
				if derr := s.discard(ctx, b); derr != nil {
					slog.ErrorContext(ctx, "projects pending booking not dropped", "ref", b.ref, "err", derr, "correlationId", middleware.GetReqID(ctx))
				}
			}
			s.failed(ctx, &res, b, code, err)
			continue
		}
		if err := s.confirm(ctx, b, entryID); err != nil {
			s.failed(ctx, &res, b, domain.CodeGLFailed, err)
			continue
		}
		res.Booked = append(res.Booked, GLBooked{Kind: b.kind, Ref: b.ref, EntryID: entryID, Lines: len(entry.Lines)})
	}
	return res, nil
}

func (s *Postgres) failed(ctx context.Context, res *GLPostResult, b booking, code string, err error) {
	logGLFailure(ctx, b.kind, b.ref, b.key, code, err)
	if b.kind == domain.GLKindTxn {
		if rerr := s.recordFailure(ctx, b.ref, code); rerr != nil {
			slog.ErrorContext(ctx, "projects transaction failure not recorded", "ref", b.ref, "err", rerr, "correlationId", middleware.GetReqID(ctx))
		}
	}
	res.Failed = append(res.Failed, GLFailed{Kind: b.kind, Ref: b.ref, Code: code})
}

func logGLFailure(ctx context.Context, kind, ref, key, code string, err error) {
	slog.ErrorContext(ctx, "projects GL booking failed", "kind", kind, "ref", ref, "key", key, "code", code, "err", err, "correlationId", middleware.GetReqID(ctx))
}

type GLReconcileRequest struct {
	LegalEntityID string
	LedgerID      string
	Currency      string
	PeriodTo      string
}

// GLAccountRecon is one account of the subledger. Only control accounts are compared with the ledger; the others are shared with other
// modules, so the ledger's balance of them says nothing about this subledger.
type GLAccountRecon struct {
	Account    string
	Control    bool
	Subledger  decimal.Decimal
	GL         decimal.Decimal
	Difference decimal.Decimal
}

type UnpostedTxn struct {
	ID       string `json:"id"`
	Reason   string `json:"reason,omitempty"`
	Attempts int    `json:"attempts"`
	Parked   bool   `json:"parked"`
}

// UnpostedList is a count of transactions whose cost no ledger entry holds and the first ones with the reason of their last failure.
type UnpostedList struct {
	Count int           `json:"count"`
	Items []UnpostedTxn `json:"items"`
}

type GLReconciliation struct {
	Currency string
	Accounts []GLAccountRecon
	Unposted UnpostedList
	// PendingEstimates are estimate bookings the ledger has not confirmed; UnbookedEstimates (with periodTo) are fixed-price projects with
	// cost whose estimate for that period or a later one is not booked.
	PendingEstimates  int
	UnbookedEstimates int
	Reconciled        bool
}

// ReconcileGL compares, per control account the service booked on, the net of its posted bookings with the ledger's balance of the account
// (difference = GL minus subledger) up to periodTo. Reconciled also needs no unposted transaction and no pending or unbooked estimate.
func (s *Postgres) ReconcileGL(ctx context.Context, ledger ledgerclient.Ledger, in GLReconcileRequest) (GLReconciliation, error) {
	if err := s.pinLedger(ctx, ledger, in.LegalEntityID, in.LedgerID, false); err != nil {
		return GLReconciliation{}, err
	}
	p, _ := tenant.FromContext(ctx)
	out := GLReconciliation{Currency: in.Currency, Accounts: []GLAccountRecon{}, Unposted: UnpostedList{Items: []UnpostedTxn{}}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select l.account, bool_or(coalesce(l.control, false)), sum(case when l.credit then -l.amount::numeric else l.amount::numeric end)::text
			from project_gl_booking b, jsonb_to_recordset(b.lines) as l(account text, credit boolean, amount text, control boolean)
			where b.tenant_id = $1 and b.legal_entity_id = $2 and b.ledger_id = $3 and b.currency = $4 and b.status = 'POSTED'
				and ($5 = '' or to_char(b.posting_date, 'YYYY-MM') <= $5)
			group by l.account order by l.account`, p.TenantID, in.LegalEntityID, in.LedgerID, in.Currency, in.PeriodTo)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a GLAccountRecon
			var net string
			if err := rows.Scan(&a.Account, &a.Control, &net); err != nil {
				rows.Close()
				return err
			}
			a.Subledger = decimal.RequireFromString(net)
			out.Accounts = append(out.Accounts, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		const unposted = `from project_transaction t join project p on p.tenant_id = t.tenant_id and p.id = t.project_id
			where t.tenant_id = $1 and p.legal_entity_id = $2 and t.currency = $3 and t.journal_id is null and t.cost_amount <> 0
				and ($4 = '' or to_char(t.txn_date, 'YYYY-MM') <= $4)`
		if err := tx.QueryRow(ctx, `select count(*) `+unposted, p.TenantID, in.LegalEntityID, in.Currency, in.PeriodTo).Scan(&out.Unposted.Count); err != nil {
			return err
		}
		irows, err := tx.Query(ctx, `select t.id::text, coalesce(t.gl_last_error, ''), t.gl_attempts, t.gl_parked_at is not null `+unposted+` order by t.txn_date, t.id limit 100`,
			p.TenantID, in.LegalEntityID, in.Currency, in.PeriodTo)
		if err != nil {
			return err
		}
		for irows.Next() {
			var u UnpostedTxn
			if err := irows.Scan(&u.ID, &u.Reason, &u.Attempts, &u.Parked); err != nil {
				irows.Close()
				return err
			}
			out.Unposted.Items = append(out.Unposted.Items, u)
		}
		irows.Close()
		if err := irows.Err(); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) from project_gl_booking where tenant_id = $1 and legal_entity_id = $2 and ledger_id = $3 and currency = $4
			and kind = 'WIP' and status = 'PENDING' and ($5 = '' or period <= $5)`, p.TenantID, in.LegalEntityID, in.LedgerID, in.Currency, in.PeriodTo).Scan(&out.PendingEstimates); err != nil {
			return err
		}
		if in.PeriodTo == "" {
			return nil
		}
		return tx.QueryRow(ctx, `select count(*) from project p where p.tenant_id = $1 and p.legal_entity_id = $2 and p.currency = $3 and p.project_type = 'FIXED_PRICE' and p.stage <> 'CREATED'
			and exists (select 1 from project_transaction t where t.tenant_id = p.tenant_id and t.project_id = p.id and t.cost_amount <> 0 and t.txn_date < ($4 || '-01')::date + interval '1 month')
			and not exists (select 1 from project_gl_booking b where b.tenant_id = p.tenant_id and b.project_id = p.id and b.kind = 'WIP' and b.period >= $4)`,
			p.TenantID, in.LegalEntityID, in.Currency, in.PeriodTo).Scan(&out.UnbookedEstimates)
	})
	if err != nil {
		return GLReconciliation{}, err
	}
	out.Reconciled = out.Unposted.Count == 0 && out.PendingEstimates == 0 && out.UnbookedEstimates == 0
	for i := range out.Accounts {
		a := &out.Accounts[i]
		if !a.Control {
			continue
		}
		bal, err := ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, Account: a.Account, PeriodTo: in.PeriodTo})
		if err != nil {
			return GLReconciliation{}, fmt.Errorf("general ledger balance of %s: %w", a.Account, err)
		}
		if bal.Currency != in.Currency && !bal.Net().IsZero() {
			return GLReconciliation{}, glProblem(http.StatusUnprocessableEntity, domain.CodeBalanceCurrency, fmt.Sprintf("the general ledger balance of %s is in %s, not %s", a.Account, bal.Currency, in.Currency))
		}
		a.GL = bal.Net()
		a.Difference = a.GL.Sub(a.Subledger)
		out.Reconciled = out.Reconciled && a.Difference.IsZero()
	}
	return out, nil
}
