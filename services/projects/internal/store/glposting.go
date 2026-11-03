package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// MaxGLItemsPerRun bounds the transactions and the project estimates one posting run books.
const MaxGLItemsPerRun = 200

const (
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
type GLPostRequest struct {
	LegalEntityID string
	LedgerID      string
	Currency      string
	Period        string
	ProjectID     string
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
	position                                                                       *[3]decimal.Decimal
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
	var rev, billed, prov *string
	if b.position != nil {
		r, bl, pv := b.position[0].String(), b.position[1].String(), b.position[2].String()
		rev, billed, prov = &r, &bl, &pv
	}
	if _, err := tx.Exec(ctx, `insert into project_gl_booking (tenant_id, id, legal_entity_id, project_id, kind, txn_id, period, ledger_id, currency, posting_date,
			idempotency_key, lines, revenue_to_date, billed_to_date, provision_to_date, status, posted_at)
		values ($1, $2, $3, $4, $5, $6::uuid, $7, $8, $9, $10::date, $11, $12, $13::numeric, $14::numeric, $15::numeric, $16, case when $16 = 'POSTED' then now() end)
		on conflict (tenant_id, idempotency_key) do nothing`,
		tenantID, ids.New(), b.legalEntityID, b.projectID, b.kind, b.txnID, b.period, b.ledgerID, b.currency, b.date, b.key, raw, rev, billed, prov, b.status); err != nil {
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

type glTxnRow struct{ id, projectID, group, category, property, date, cost string }

type glProjectRow struct{ id, group, contract string }

// planGL stores the bookings of this run's unposted transactions and returns the estimate candidates. Amounts and accounts come from the stored
// transaction and the stored posting profile only.
func (s *Postgres) planGL(ctx context.Context, in GLPostRequest, res *GLPostResult) (todo []booking, projects []glProjectRow, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		profiles, err := loadProfiles(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select t.id::text, t.project_id, p.project_group_id, t.category_id, t.line_property, t.txn_date::text, t.cost_amount::text
			from project_transaction t join project p on p.tenant_id = t.tenant_id and p.id = t.project_id
			where t.tenant_id = $1 and p.legal_entity_id = $2 and t.currency = $3 and t.journal_id is null and t.cost_amount <> 0 and ($4 = '' or t.project_id = $4)
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
			if err != nil {
				res.Failed = append(res.Failed, GLFailed{Kind: domain.GLKindTxn, Ref: r.id, Code: domain.CodePostingProfile})
				continue
			}
			b, err := booking{kind: domain.GLKindTxn, projectID: r.projectID, legalEntityID: in.LegalEntityID, ledgerID: in.LedgerID, currency: in.Currency, date: r.date,
				key: fmt.Sprintf("projects:txn:%s:1", r.id), status: statusPending, txnID: &r.id,
				lines: domain.TransactionLines(profile, decimal.RequireFromString(r.cost), in.Currency)}.store(ctx, tx, p.TenantID)
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
		prows, err := tx.Query(ctx, `select id, project_group_id, contract_value::text from project
			where tenant_id = $1 and legal_entity_id = $2 and currency = $3 and project_type = 'FIXED_PRICE' and stage <> 'CREATED' and ($4 = '' or id = $4)
			order by id limit $5`, p.TenantID, in.LegalEntityID, in.Currency, in.ProjectID, MaxGLItemsPerRun+1)
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

type estimatePosition struct{ revenue, billed, provision decimal.Decimal }

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
		var prior estimatePosition
		var priorStatus *string
		var rev, billed, prov *string
		err := tx.QueryRow(ctx, `select status, revenue_to_date::text, billed_to_date::text, provision_to_date::text from project_gl_booking
			where tenant_id = $1 and project_id = $2 and kind = 'WIP' and period < $3 order by period desc limit 1`, p.TenantID, pr.id, in.Period).Scan(&priorStatus, &rev, &billed, &prov)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case *priorStatus != statusPosted:
			code = domain.CodePeriodOrder
			return nil
		default:
			prior = estimatePosition{decimal.RequireFromString(*rev), decimal.RequireFromString(*billed), decimal.RequireFromString(*prov)}
		}
		monthStart, err := time.Parse("2006-01", in.Period)
		if err != nil {
			return err
		}
		next := monthStart.AddDate(0, 1, 0).Format(time.DateOnly)
		var cost, etc, billedNow string
		if err := tx.QueryRow(ctx, `select coalesce(sum(cost_amount), 0)::text from project_transaction where tenant_id = $1 and project_id = $2 and txn_date < $3::date`,
			p.TenantID, pr.id, next).Scan(&cost); err != nil {
			return err
		}
		var modelID string
		err = tx.QueryRow(ctx, `select id::text from project_forecast_model where tenant_id = $1 and project_id = $2 order by created_at desc, id desc limit 1`, p.TenantID, pr.id).Scan(&modelID)
		if errors.Is(err, pgx.ErrNoRows) {
			code = domain.CodeNoEstimate
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select coalesce(sum(cost_amount), 0)::text from project_forecast_line where tenant_id = $1 and model_id = $2 and period > $3`,
			p.TenantID, modelID, in.Period).Scan(&etc); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select coalesce(sum(l.amount), 0)::text from project_invoice_proposal_line l
			join project_invoice_proposal pp on pp.tenant_id = l.tenant_id and pp.id = l.proposal_id
			where l.tenant_id = $1 and pp.project_id = $2 and pp.status in ('APPROVED', 'POSTED') and pp.to_date < $3::date`, p.TenantID, pr.id, next).Scan(&billedNow); err != nil {
			return err
		}
		poc, err := domain.ComputePOC(domain.POCInput{Currency: in.Currency, ContractValue: decimal.RequireFromString(pr.contract), CostToDate: decimal.RequireFromString(cost),
			EstimatedToComplete: decimal.RequireFromString(etc), BilledToDate: decimal.RequireFromString(billedNow), PriorRevenueToDate: prior.revenue})
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
		billedTotal := decimal.RequireFromString(billedNow)
		lines := domain.EstimateLines(profile, poc.RevenueToDate.Sub(prior.revenue), billedTotal.Sub(prior.billed), domain.ProvisionMovement(prior.provision, poc.LossProvision), in.Currency)
		b := booking{kind: domain.GLKindWIP, projectID: pr.id, legalEntityID: in.LegalEntityID, ledgerID: in.LedgerID, currency: in.Currency, period: &in.Period,
			date: monthStart.AddDate(0, 1, -1).Format(time.DateOnly), key: key, status: statusPending, lines: lines,
			position: &[3]decimal.Decimal{poc.RevenueToDate, billedTotal, poc.LossProvision}}
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
		_, err := tx.Exec(ctx, `update project_transaction set journal_id = $3::uuid where tenant_id = $1 and id = $2::uuid and journal_id is null`, p.TenantID, b.ref, entryID)
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

// PostGL books the cost of unposted project transactions of the legal entity as Dr work in progress / Cr cost account, and, for a period, the
// movement of each fixed-price project's percentage-of-completion position (revenue, billing and loss provision). Each booking is stored before the
// ledger is called and resent unchanged until the ledger confirms it, so a retry returns the same entry. A booking the ledger refuses is reported and
// the rest go on; a refusal other than a closed period drops the stored booking so the next run rebuilds it from the current profile.
func (s *Postgres) PostGL(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error) {
	res := GLPostResult{Booked: []GLBooked{}, Failed: []GLFailed{}}
	todo, projects, err := s.planGL(ctx, in, &res)
	if err != nil {
		return GLPostResult{}, err
	}
	if len(projects) > MaxGLItemsPerRun {
		res.More, projects = true, projects[:MaxGLItemsPerRun]
	}
	for _, pr := range projects {
		b, code, err := s.planEstimate(ctx, in, pr)
		switch {
		case err != nil:
			logGLFailure(ctx, domain.GLKindWIP, pr.id, "", domain.CodeGLFailed, err)
			res.Failed = append(res.Failed, GLFailed{Kind: domain.GLKindWIP, Ref: pr.id + ":" + in.Period, Code: domain.CodeGLFailed})
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
			logGLFailure(ctx, b.kind, b.ref, b.key, code, err)
			res.Failed = append(res.Failed, GLFailed{Kind: b.kind, Ref: b.ref, Code: code})
			continue
		}
		if err := s.confirm(ctx, b, entryID); err != nil {
			logGLFailure(ctx, b.kind, b.ref, b.key, domain.CodeGLFailed, err)
			res.Failed = append(res.Failed, GLFailed{Kind: b.kind, Ref: b.ref, Code: domain.CodeGLFailed})
			continue
		}
		res.Booked = append(res.Booked, GLBooked{Kind: b.kind, Ref: b.ref, EntryID: entryID, Lines: len(entry.Lines)})
	}
	return res, nil
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

type GLAccountRecon struct {
	Account    string
	Subledger  decimal.Decimal
	GL         decimal.Decimal
	Difference decimal.Decimal
}

// TxnList is a count of project transactions and the ids of the first ones.
type TxnList struct {
	Count int      `json:"count"`
	Ids   []string `json:"ids"`
}

type GLReconciliation struct {
	Currency string
	Accounts []GLAccountRecon
	// Unposted are the entity's transactions with a cost that no ledger entry holds yet; Reconciled needs it empty.
	Unposted   TxnList
	Reconciled bool
}

// ReconcileGL compares, per account the service booked on, the net of its posted bookings with the ledger's balance of the account
// (difference = GL minus subledger) up to periodTo.
func (s *Postgres) ReconcileGL(ctx context.Context, ledger ledgerclient.Ledger, in GLReconcileRequest) (GLReconciliation, error) {
	p, _ := tenant.FromContext(ctx)
	out := GLReconciliation{Currency: in.Currency, Accounts: []GLAccountRecon{}, Unposted: TxnList{Ids: []string{}}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select l.account, sum(case when l.credit then -l.amount::numeric else l.amount::numeric end)::text
			from project_gl_booking b, jsonb_to_recordset(b.lines) as l(account text, credit boolean, amount text)
			where b.tenant_id = $1 and b.legal_entity_id = $2 and b.ledger_id = $3 and b.currency = $4 and b.status = 'POSTED'
				and ($5 = '' or to_char(b.posting_date, 'YYYY-MM') <= $5)
			group by l.account order by l.account`, p.TenantID, in.LegalEntityID, in.LedgerID, in.Currency, in.PeriodTo)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a GLAccountRecon
			var net string
			if err := rows.Scan(&a.Account, &net); err != nil {
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
		irows, err := tx.Query(ctx, `select t.id::text `+unposted+` order by t.txn_date, t.id limit 100`, p.TenantID, in.LegalEntityID, in.Currency, in.PeriodTo)
		if err != nil {
			return err
		}
		defer irows.Close()
		for irows.Next() {
			var id string
			if err := irows.Scan(&id); err != nil {
				return err
			}
			out.Unposted.Ids = append(out.Unposted.Ids, id)
		}
		return irows.Err()
	})
	if err != nil {
		return GLReconciliation{}, err
	}
	out.Reconciled = out.Unposted.Count == 0
	for i := range out.Accounts {
		a := &out.Accounts[i]
		bal, err := ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, Account: a.Account, PeriodTo: in.PeriodTo})
		if err != nil {
			return GLReconciliation{}, fmt.Errorf("general ledger balance of %s: %w", a.Account, err)
		}
		if bal.Currency != in.Currency && !bal.Net().IsZero() {
			return GLReconciliation{}, fmt.Errorf("general ledger balance of %s is in %s, not %s", a.Account, bal.Currency, in.Currency)
		}
		a.GL = bal.Net()
		a.Difference = a.GL.Sub(a.Subledger)
		out.Reconciled = out.Reconciled && a.Difference.IsZero()
	}
	return out, nil
}
