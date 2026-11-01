package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	MaxGLOrdersPerRun = 200
	CodeGLFailed      = "EAM-GL-001"
	CodeProfileState  = "EAM-GL-002"
	allTypes          = "ALL"
	glSourceType      = "work-order"
)

// PostingProfile maps a work order type to the accounts its cost is booked on: parts and labor go to their expense account
// against their offset (inventory or clearing, labor absorption). Placeholder marks the built-in default used when a legal entity has no profile.
type PostingProfile struct {
	LegalEntityID string    `json:"legalEntityId"`
	WorkOrderType string    `json:"workOrderType"`
	PartsExpense  string    `json:"partsExpenseAccount"`
	LaborExpense  string    `json:"laborExpenseAccount"`
	PartsOffset   string    `json:"partsOffsetAccount"`
	LaborOffset   string    `json:"laborOffsetAccount"`
	Version       int       `json:"version"`
	UpdatedBy     string    `json:"updatedBy,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt,omitempty"`
	Placeholder   bool      `json:"placeholder,omitempty"`
}

// PlaceholderProfile is the documented default when no profile is stored: accounts the accountant must replace (services/eam/README.md).
var PlaceholderProfile = PostingProfile{WorkOrderType: allTypes, PartsExpense: "6500", LaborExpense: "6510", PartsOffset: "1400", LaborOffset: "6590", Placeholder: true}

const profileColumns = `legal_entity_id::text, work_order_type, parts_expense_account, labor_expense_account, parts_offset_account, labor_offset_account, version, updated_by, updated_at`

func scanProfile(row pgx.Row) (PostingProfile, error) {
	var p PostingProfile
	err := row.Scan(&p.LegalEntityID, &p.WorkOrderType, &p.PartsExpense, &p.LaborExpense, &p.PartsOffset, &p.LaborOffset, &p.Version, &p.UpdatedBy, &p.UpdatedAt)
	return p, err
}

func (s *Postgres) ListPostingProfiles(ctx context.Context, legalEntityID string) ([]PostingProfile, error) {
	out := []PostingProfile{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select `+profileColumns+` from eam_posting_profile where tenant_id = $1 and legal_entity_id = $2 order by work_order_type`, t, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProfile(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// SavePostingProfile creates the profile of a legal entity and work order type (ifMatch 0) or replaces it under its version.
func (s *Postgres) SavePostingProfile(ctx context.Context, in PostingProfile, ifMatch int, actor string) (PostingProfile, bool, error) {
	var out PostingProfile
	created := false
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		var version int
		err := tx.QueryRow(ctx, `select version from eam_posting_profile where tenant_id = $1 and legal_entity_id = $2 and work_order_type = $3 for update`,
			t, in.LegalEntityID, in.WorkOrderType).Scan(&version)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if ifMatch != 0 {
				return problem(http.StatusPreconditionFailed, CodeVersion, "the posting profile does not exist; create it without If-Match")
			}
			created = true
			_, err = tx.Exec(ctx, `insert into eam_posting_profile (tenant_id, legal_entity_id, work_order_type, parts_expense_account, labor_expense_account,
				parts_offset_account, labor_offset_account, updated_by) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
				t, in.LegalEntityID, in.WorkOrderType, in.PartsExpense, in.LaborExpense, in.PartsOffset, in.LaborOffset, actor)
		case err != nil:
			return err
		case ifMatch == 0:
			return problem(http.StatusPreconditionRequired, "precondition_required", "If-Match with the profile version is required to replace it")
		case ifMatch != version:
			return problem(http.StatusPreconditionFailed, CodeVersion, fmt.Sprintf("the posting profile was changed by another user; current version is %d", version))
		default:
			_, err = tx.Exec(ctx, `update eam_posting_profile set parts_expense_account = $4, labor_expense_account = $5, parts_offset_account = $6,
				labor_offset_account = $7, updated_by = $8, updated_at = now(), version = version + 1
				where tenant_id = $1 and legal_entity_id = $2 and work_order_type = $3`,
				t, in.LegalEntityID, in.WorkOrderType, in.PartsExpense, in.LaborExpense, in.PartsOffset, in.LaborOffset, actor)
		}
		if err != nil {
			return err
		}
		out, err = scanProfile(tx.QueryRow(ctx, `select `+profileColumns+` from eam_posting_profile where tenant_id = $1 and legal_entity_id = $2 and work_order_type = $3`,
			t, in.LegalEntityID, in.WorkOrderType))
		return err
	})
	return out, created, err
}

// resolveProfile prefers the profile of the order's type, then ALL, then the placeholder.
func resolveProfile(profiles []PostingProfile, typ string) PostingProfile {
	var all *PostingProfile
	for i, p := range profiles {
		switch p.WorkOrderType {
		case typ:
			return p
		case allTypes:
			all = &profiles[i]
		}
	}
	if all != nil {
		return *all
	}
	return PlaceholderProfile
}

type GLPostRequest struct {
	LegalEntityID string
	LedgerID      string
	Currency      string
	SiteID        string
	WorkOrderID   string
}

type GLBooked struct {
	WorkOrder   string `json:"workOrder"`
	EntryID     string `json:"entryId"`
	Lines       int    `json:"lines"`
	Placeholder bool   `json:"placeholderProfile,omitempty"`
}

type GLFailed struct {
	WorkOrder string `json:"workOrder"`
	Code      string `json:"code"`
}

type GLPostResult struct {
	Booked []GLBooked `json:"booked"`
	Failed []GLFailed `json:"failed"`
	More   bool       `json:"more"`
}

type glOrder struct {
	id, number, typ                    string
	end                                *time.Time
	parts, labor, partsBase, laborBase decimal.Decimal
	count                              int
}

func (o glOrder) deltas(currency string) (parts, labor decimal.Decimal) {
	mu := money.MinorUnits(currency)
	return o.parts.Round(mu).Sub(o.partsBase.Round(mu)), o.labor.Round(mu).Sub(o.laborBase.Round(mu))
}

func (s *Postgres) unpostedOrders(ctx context.Context, tx pgx.Tx, t string, in GLPostRequest) ([]glOrder, error) {
	rows, err := tx.Query(ctx, `select w.id::text, w.number, w.work_order_type, w.actual_end, w.parts_cost::text, w.labor_cost::text,
			w.gl_parts_basis::text, w.gl_labor_basis::text, w.gl_post_count
		from work_order w join asset a on a.tenant_id = w.tenant_id and a.id = w.asset_id
		where w.tenant_id = $1 and w.status in ('COMPLETED', 'CLOSED') and w.currency = $2 and ($3 = '' or a.site_id = $3::uuid)
			and ($4 = '' or w.id = $4::uuid) and (w.parts_cost <> w.gl_parts_basis or w.labor_cost <> w.gl_labor_basis)
		order by w.actual_end, w.id limit $5`, t, in.Currency, in.SiteID, in.WorkOrderID, MaxGLOrdersPerRun+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []glOrder
	for rows.Next() {
		var o glOrder
		var parts, labor, pb, lb string
		if err := rows.Scan(&o.id, &o.number, &o.typ, &o.end, &parts, &labor, &pb, &lb, &o.count); err != nil {
			return nil, err
		}
		o.parts, o.labor, o.partsBase, o.laborBase = decimal.RequireFromString(parts), decimal.RequireFromString(labor), decimal.RequireFromString(pb), decimal.RequireFromString(lb)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (o glOrder) entry(in GLPostRequest, p PostingProfile, today time.Time) ledgerclient.Entry {
	date := today
	if o.count == 0 && o.end != nil {
		date = *o.end
	}
	e := ledgerclient.Entry{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, DocumentID: o.number, PostingDate: date.UTC().Format(time.DateOnly), Currency: in.Currency,
		IdempotencyKey: fmt.Sprintf("eam-work-order:%s:%d", o.id, o.count+1), SourceType: glSourceType}
	parts, labor := o.deltas(in.Currency)
	for _, l := range []struct {
		delta           decimal.Decimal
		expense, offset string
	}{{parts, p.PartsExpense, p.PartsOffset}, {labor, p.LaborExpense, p.LaborOffset}} {
		if l.delta.IsZero() {
			continue
		}
		e.Lines = append(e.Lines,
			ledgerclient.Line{Account: l.expense, Credit: l.delta.IsNegative(), Amount: l.delta.Abs()},
			ledgerclient.Line{Account: l.offset, Credit: l.delta.IsPositive(), Amount: l.delta.Abs()})
	}
	return e
}

func (s *Postgres) markPosted(ctx context.Context, o glOrder, in GLPostRequest, e ledgerclient.Entry, entryID string) error {
	return s.withTx(ctx, func(tx pgx.Tx, t string) error {
		var count int
		if err := tx.QueryRow(ctx, `select gl_post_count from work_order where tenant_id = $1 and id = $2 for update`, t, o.id).Scan(&count); err != nil {
			return err
		}
		if count != o.count {
			return nil
		}
		next := o.count
		if len(e.Lines) > 0 {
			next++
			for _, l := range e.Lines {
				dr, cr := l.Amount, decimal.Zero
				if l.Credit {
					dr, cr = cr, dr
				}
				if _, err := tx.Exec(ctx, `insert into eam_gl_posting (tenant_id, id, legal_entity_id, work_order_id, seq, account, amount_dr, amount_cr, currency, posting_date, journal_entry_id)
					values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::date, $11)`, t, ids.New(), in.LegalEntityID, o.id, next, l.Account, dr.String(), cr.String(), in.Currency, e.PostingDate, entryID); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `update work_order set gl_parts_basis = $3::numeric, gl_labor_basis = $4::numeric, gl_post_count = $5 where tenant_id = $1 and id = $2`,
			t, o.id, o.parts.String(), o.labor.String(), next)
		return err
	})
}

// PostWorkOrderCosts books the parts and labor cost of completed and closed work orders (FR-44.11): per order one balanced entry for the cost
// not yet booked, keyed by order and booking number so a retry returns the same entry. A cost that changes after booking is booked as a delta.
// An order the ledger refuses is reported and the rest go on.
func (s *Postgres) PostWorkOrderCosts(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error) {
	var orders []glOrder
	var profiles []PostingProfile
	err := s.withTx(ctx, func(tx pgx.Tx, t string) (err error) {
		if orders, err = s.unpostedOrders(ctx, tx, t, in); err != nil {
			return err
		}
		profiles = []PostingProfile{}
		rows, err := tx.Query(ctx, `select `+profileColumns+` from eam_posting_profile where tenant_id = $1 and legal_entity_id = $2`, t, in.LegalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProfile(rows)
			if err != nil {
				return err
			}
			profiles = append(profiles, p)
		}
		return rows.Err()
	})
	if err != nil {
		return GLPostResult{}, err
	}
	res := GLPostResult{Booked: []GLBooked{}, Failed: []GLFailed{}}
	if len(orders) > MaxGLOrdersPerRun {
		res.More, orders = true, orders[:MaxGLOrdersPerRun]
	}
	for _, o := range orders {
		profile := resolveProfile(profiles, o.typ)
		entry := o.entry(in, profile, s.now())
		entryID := ""
		if len(entry.Lines) > 0 {
			if entryID, err = ledger.Post(ctx, entry); err != nil {
				code := ledgerclient.CodeOf(err)
				if code == "" {
					code = CodeGLFailed
				}
				slog.ErrorContext(ctx, "eam GL booking failed", "workOrder", o.number, "key", entry.IdempotencyKey, "code", code, "err", err, "correlationId", middleware.GetReqID(ctx))
				res.Failed = append(res.Failed, GLFailed{WorkOrder: o.number, Code: code})
				continue
			}
		}
		if err := s.markPosted(ctx, o, in, entry, entryID); err != nil {
			slog.ErrorContext(ctx, "eam GL booking not recorded", "workOrder", o.number, "key", entry.IdempotencyKey, "entryId", entryID, "err", err, "correlationId", middleware.GetReqID(ctx))
			res.Failed = append(res.Failed, GLFailed{WorkOrder: o.number, Code: CodeGLFailed})
			continue
		}
		if entryID != "" {
			res.Booked = append(res.Booked, GLBooked{WorkOrder: o.number, EntryID: entryID, Lines: len(entry.Lines), Placeholder: profile.Placeholder})
		}
	}
	return res, nil
}

type GLReconcileRequest struct {
	LegalEntityID string
	LedgerID      string
	Currency      string
	PeriodTo      string
}

type GLAccountRecon struct {
	Account    string          `json:"account"`
	Subledger  decimal.Decimal `json:"subledger"`
	GL         decimal.Decimal `json:"gl"`
	Difference decimal.Decimal `json:"difference"`
}

type GLReconciliation struct {
	Currency   string           `json:"currency"`
	Accounts   []GLAccountRecon `json:"accounts"`
	Reconciled bool             `json:"reconciled"`
}

// ReconcileGL compares, per account the service booked on, the net of its GL subledger with the ledger's balance of the account
// (difference = GL minus subledger). Work orders whose cost is not booked yet are in neither side.
func (s *Postgres) ReconcileGL(ctx context.Context, ledger ledgerclient.Ledger, in GLReconcileRequest) (GLReconciliation, error) {
	out := GLReconciliation{Currency: in.Currency, Accounts: []GLAccountRecon{}, Reconciled: true}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select account, sum(amount_dr - amount_cr)::text from eam_gl_posting
			where tenant_id = $1 and legal_entity_id = $2 and currency = $3 and ($4 = '' or to_char(posting_date, 'YYYY-MM') <= $4)
			group by account order by account`, t, in.LegalEntityID, in.Currency, in.PeriodTo)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a GLAccountRecon
			var net string
			if err := rows.Scan(&a.Account, &net); err != nil {
				return err
			}
			a.Subledger = decimal.RequireFromString(net)
			out.Accounts = append(out.Accounts, a)
		}
		return rows.Err()
	})
	if err != nil {
		return GLReconciliation{}, err
	}
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
