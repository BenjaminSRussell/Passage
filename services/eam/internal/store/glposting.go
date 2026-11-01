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

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	MaxGLOrdersPerRun = 200
	CodeGLFailed      = "EAM-GL-001"
	CodeNoEntity      = "EAM-GL-004"
	CodeEntityLocked  = "EAM-GL-005"
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

type pendingLine struct {
	Account string `json:"account"`
	Credit  bool   `json:"credit"`
	Amount  string `json:"amount"`
}

// pendingBooking is the exact payload of one ledger entry, stored before the ledger is called.
type pendingBooking struct {
	seq                    int
	legalEntityID          string
	ledgerID, currency     string
	date, key              string
	lines                  []pendingLine
	partsBasis, laborBasis decimal.Decimal
}

func (p pendingBooking) entry(number string) ledgerclient.Entry {
	e := ledgerclient.Entry{LegalEntityID: p.legalEntityID, LedgerID: p.ledgerID, DocumentID: number, PostingDate: p.date, Currency: p.currency, IdempotencyKey: p.key, SourceType: glSourceType}
	for _, l := range p.lines {
		e.Lines = append(e.Lines, ledgerclient.Line{Account: l.Account, Credit: l.Credit, Amount: decimal.RequireFromString(l.Amount)})
	}
	return e
}

func (o glOrder) deltas(currency string) (parts, labor decimal.Decimal) {
	mu := money.MinorUnits(currency)
	return o.parts.Round(mu).Sub(o.partsBase.Round(mu)), o.labor.Round(mu).Sub(o.laborBase.Round(mu))
}

const glOrderColumns = `w.id::text, w.number, w.work_order_type, w.actual_end, w.parts_cost::text, w.labor_cost::text, w.gl_parts_basis::text, w.gl_labor_basis::text, w.gl_post_count`

func scanGLOrder(row pgx.Row) (glOrder, error) {
	var o glOrder
	var parts, labor, pb, lb string
	if err := row.Scan(&o.id, &o.number, &o.typ, &o.end, &parts, &labor, &pb, &lb, &o.count); err != nil {
		return o, err
	}
	o.parts, o.labor, o.partsBase, o.laborBase = decimal.RequireFromString(parts), decimal.RequireFromString(labor), decimal.RequireFromString(pb), decimal.RequireFromString(lb)
	return o, nil
}

func (s *Postgres) unpostedOrders(ctx context.Context, tx pgx.Tx, t string, in GLPostRequest) ([]glOrder, error) {
	rows, err := tx.Query(ctx, `select `+glOrderColumns+`
		from work_order w join asset a on a.tenant_id = w.tenant_id and a.id = w.asset_id
		where w.tenant_id = $1 and w.legal_entity_id = $6::uuid and w.status in ('COMPLETED', 'CLOSED') and w.currency = $2 and ($3 = '' or a.site_id = $3::uuid)
			and ($4 = '' or w.id = $4::uuid) and (w.parts_cost <> w.gl_parts_basis or w.labor_cost <> w.gl_labor_basis)
		order by w.actual_end, w.id limit $5`, t, in.Currency, in.SiteID, in.WorkOrderID, MaxGLOrdersPerRun+1, in.LegalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []glOrder
	for rows.Next() {
		o, err := scanGLOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (o glOrder) lines(p PostingProfile, currency string) []pendingLine {
	var out []pendingLine
	parts, labor := o.deltas(currency)
	for _, l := range []struct {
		delta           decimal.Decimal
		expense, offset string
	}{{parts, p.PartsExpense, p.PartsOffset}, {labor, p.LaborExpense, p.LaborOffset}} {
		if l.delta.IsZero() {
			continue
		}
		mu := money.MinorUnits(currency)
		amount := l.delta.Abs().StringFixed(mu)
		out = append(out,
			pendingLine{Account: l.expense, Credit: l.delta.IsNegative(), Amount: amount},
			pendingLine{Account: l.offset, Credit: l.delta.IsPositive(), Amount: amount})
	}
	return out
}

// decide returns the booking to send for the order. A pending booking left by an earlier attempt is returned unchanged; otherwise the cost not yet
// booked becomes one, stored in this transaction under the order's row lock before the ledger is called. Cost that rounds to nothing only moves the basis.
func (s *Postgres) decide(ctx context.Context, id string, in GLPostRequest, profiles []PostingProfile) (glOrder, *pendingBooking, error) {
	var o glOrder
	var pb *pendingBooking
	err := s.withTx(ctx, func(tx pgx.Tx, t string) (err error) {
		if o, err = scanGLOrder(tx.QueryRow(ctx, `select `+glOrderColumns+` from work_order w where w.tenant_id = $1 and w.id = $2 for update`, t, id)); err != nil {
			return err
		}
		var p pendingBooking
		var pbase, lbase string
		err = tx.QueryRow(ctx, `select seq, legal_entity_id::text, ledger_id::text, currency, posting_date::text, idempotency_key, lines, parts_basis::text, labor_basis::text
			from eam_pending_booking where tenant_id = $1 and work_order_id = $2`, t, id).Scan(&p.seq, &p.legalEntityID, &p.ledgerID, &p.currency, &p.date, &p.key, &p.lines, &pbase, &lbase)
		if err == nil {
			p.partsBasis, p.laborBasis = decimal.RequireFromString(pbase), decimal.RequireFromString(lbase)
			pb = &p
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		date := s.now()
		if o.count == 0 && o.end != nil {
			date = *o.end
		}
		p = pendingBooking{seq: o.count + 1, legalEntityID: in.LegalEntityID, ledgerID: in.LedgerID, currency: in.Currency, date: date.UTC().Format(time.DateOnly),
			key: fmt.Sprintf("eam-work-order:%s:%d", o.id, o.count+1), lines: o.lines(resolveProfile(profiles, o.typ), in.Currency), partsBasis: o.parts, laborBasis: o.labor}
		if len(p.lines) == 0 {
			_, err = tx.Exec(ctx, `update work_order set gl_parts_basis = $3::numeric, gl_labor_basis = $4::numeric where tenant_id = $1 and id = $2`, t, id, o.parts.String(), o.labor.String())
			return err
		}
		raw, err := json.Marshal(p.lines)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `insert into eam_pending_booking (tenant_id, work_order_id, seq, legal_entity_id, ledger_id, currency, posting_date, idempotency_key, lines, parts_basis, labor_basis)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10::numeric, $11::numeric)`, t, id, p.seq, p.legalEntityID, p.ledgerID, p.currency, p.date, p.key, raw, p.partsBasis.String(), p.laborBasis.String()); err != nil {
			return err
		}
		pb = &p
		return nil
	})
	return o, pb, err
}

// confirm records a booking the ledger accepted: its lines enter the GL subledger, the basis moves and the pending row goes, in one transaction.
func (s *Postgres) confirm(ctx context.Context, o glOrder, p pendingBooking, entryID string) error {
	return s.withTx(ctx, func(tx pgx.Tx, t string) error {
		tag, err := tx.Exec(ctx, `delete from eam_pending_booking where tenant_id = $1 and work_order_id = $2 and seq = $3`, t, o.id, p.seq)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		for _, l := range p.lines {
			dr, cr := l.Amount, "0"
			if l.Credit {
				dr, cr = cr, dr
			}
			if _, err := tx.Exec(ctx, `insert into eam_gl_posting (tenant_id, id, legal_entity_id, work_order_id, seq, account, amount_dr, amount_cr, currency, posting_date, journal_entry_id)
				values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9, $10::date, $11)`, t, ids.New(), p.legalEntityID, o.id, p.seq, l.Account, dr, cr, p.currency, p.date, entryID); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `update work_order set gl_parts_basis = $3::numeric, gl_labor_basis = $4::numeric, gl_post_count = $5 where tenant_id = $1 and id = $2`,
			t, o.id, p.partsBasis.String(), p.laborBasis.String(), p.seq)
		return err
	})
}

// discard drops a pending booking the ledger rejected for good, so the next run derives it again from the current profile.
func (s *Postgres) discard(ctx context.Context, id string, seq int) error {
	return s.withTx(ctx, func(tx pgx.Tx, t string) error {
		_, err := tx.Exec(ctx, `delete from eam_pending_booking where tenant_id = $1 and work_order_id = $2 and seq = $3`, t, id, seq)
		return err
	})
}

// PostWorkOrderCosts books the parts and labor cost of completed and closed work orders of the legal entity (FR-44.11): per order one balanced entry
// for the cost not yet booked, stored as a pending booking before the ledger is called and resent unchanged until the ledger confirms it, so a retry
// returns the same entry even after the profile or the cost changed. A cost that changes after booking is booked as a delta. An order the ledger
// refuses is reported and the rest go on; a refusal other than a closed period drops the pending booking so the next run rebuilds it.
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
	fail := func(ctx context.Context, number, key, code string, err error) {
		slog.ErrorContext(ctx, "eam GL booking failed", "workOrder", number, "key", key, "code", code, "err", err, "correlationId", middleware.GetReqID(ctx))
		res.Failed = append(res.Failed, GLFailed{WorkOrder: number, Code: code})
	}
	for _, c := range orders {
		o, pb, err := s.decide(ctx, c.id, in, profiles)
		if err != nil {
			fail(ctx, c.number, "", CodeGLFailed, err)
			continue
		}
		if pb == nil {
			continue
		}
		entry := pb.entry(o.number)
		entryID, err := ledger.Post(ctx, entry)
		if err != nil {
			code := ledgerclient.CodeOf(err)
			if code == "" {
				code = CodeGLFailed
			}
			if ledgerclient.Rejected(err) && code != ledgerclient.CodePeriodClosed {
				if derr := s.discard(ctx, o.id, pb.seq); derr != nil {
					slog.ErrorContext(ctx, "eam pending booking not dropped", "workOrder", o.number, "err", derr, "correlationId", middleware.GetReqID(ctx))
				}
			}
			fail(ctx, o.number, entry.IdempotencyKey, code, err)
			continue
		}
		if err := s.confirm(ctx, o, *pb, entryID); err != nil {
			fail(ctx, o.number, entry.IdempotencyKey, CodeGLFailed, err)
			continue
		}
		res.Booked = append(res.Booked, GLBooked{WorkOrder: o.number, EntryID: entryID, Lines: len(entry.Lines), Placeholder: resolveProfile(profiles, o.typ).Placeholder})
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

// OrderList is a count of work orders and the numbers of the first ones.
type OrderList struct {
	Count   int      `json:"count"`
	Numbers []string `json:"numbers"`
}

type GLReconciliation struct {
	Currency string           `json:"currency"`
	Accounts []GLAccountRecon `json:"accounts"`
	// Unbooked are completed or closed orders of the legal entity whose cost is not (fully) in the ledger; NoEntity are completed or closed orders of the
	// tenant with no legal entity, which no ledger will ever receive. Reconciled needs both empty.
	Unbooked OrderList `json:"unbookedWorkOrders"`
	NoEntity OrderList `json:"workOrdersWithoutEntity"`
	// AssetsNoEntity are the tenant's assets with no legal entity (their orders are refused or skipped); set with PUT /v1/eam/assets/{id}/legal-entity.
	AssetsNoEntity OrderList `json:"assetsWithoutEntity"`
	Reconciled     bool      `json:"reconciled"`
}

func orderList(ctx context.Context, tx pgx.Tx, where string, args ...any) (OrderList, error) {
	out := OrderList{Numbers: []string{}}
	base := `from work_order w where w.tenant_id = $1 and w.status in ('COMPLETED', 'CLOSED') and w.currency = $2 and ` + where
	if err := tx.QueryRow(ctx, `select count(*) `+base, args...).Scan(&out.Count); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `select w.number `+base+` order by w.number limit 100`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return out, err
		}
		out.Numbers = append(out.Numbers, n)
	}
	return out, rows.Err()
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
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if out.Unbooked, err = orderList(ctx, tx, `w.legal_entity_id = $3::uuid and (w.parts_cost <> w.gl_parts_basis or w.labor_cost <> w.gl_labor_basis)`, t, in.Currency, in.LegalEntityID); err != nil {
			return err
		}
		if out.NoEntity, err = orderList(ctx, tx, `w.legal_entity_id is null and (w.parts_cost <> 0 or w.labor_cost <> 0)`, t, in.Currency); err != nil {
			return err
		}
		out.AssetsNoEntity = OrderList{Numbers: []string{}}
		if err := tx.QueryRow(ctx, `select count(*) from asset where tenant_id = $1 and legal_entity_id is null`, t).Scan(&out.AssetsNoEntity.Count); err != nil {
			return err
		}
		arows, err := tx.Query(ctx, `select asset_number from asset where tenant_id = $1 and legal_entity_id is null order by asset_number limit 100`, t)
		if err != nil {
			return err
		}
		defer arows.Close()
		for arows.Next() {
			var n string
			if err := arows.Scan(&n); err != nil {
				return err
			}
			out.AssetsNoEntity.Numbers = append(out.AssetsNoEntity.Numbers, n)
		}
		return arows.Err()
	})
	if err != nil {
		return GLReconciliation{}, err
	}
	out.Reconciled = out.Unbooked.Count == 0 && out.NoEntity.Count == 0
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
