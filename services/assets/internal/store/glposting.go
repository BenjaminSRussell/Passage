package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	MaxGLGroupsPerRun = 200
	CodeGLFailed      = "AST-0601"
	allClasses        = "ALL"
)

type GLBooks interface {
	PostToGL(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error)
	ReconcileGL(ctx context.Context, ledger ledgerclient.Ledger, in GLReconcileRequest) (GLReconciliation, error)
	ListPostingProfiles(ctx context.Context, legalEntityID string) ([]PostingProfile, error)
	SavePostingProfile(ctx context.Context, in PostingProfile, ifMatch int, actor string) (PostingProfile, bool, error)
}

var _ GLBooks = (*Postgres)(nil)

// PostingProfile maps an asset class to the accounts the journals of its books use. Placeholder marks the built-in default.
type PostingProfile struct {
	LegalEntityID       string    `json:"legalEntityId"`
	ClassCode           string    `json:"classCode"`
	Cost                string    `json:"costAccount"`
	Accumulated         string    `json:"accumulatedDepreciationAccount"`
	DepreciationExpense string    `json:"depreciationExpenseAccount"`
	Impairment          string    `json:"impairmentAccount"`
	Revaluation         string    `json:"revaluationAccount"`
	AcquisitionClearing string    `json:"acquisitionClearingAccount"`
	Proceeds            string    `json:"proceedsAccount"`
	Gain                string    `json:"gainAccount"`
	Loss                string    `json:"lossAccount"`
	TransferClearing    string    `json:"transferClearingAccount"`
	Version             int       `json:"version"`
	UpdatedBy           string    `json:"updatedBy,omitempty"`
	UpdatedAt           time.Time `json:"updatedAt,omitempty"`
	Placeholder         bool      `json:"placeholder,omitempty"`
}

// PlaceholderProfile applies when a legal entity has no profile; the accountant must replace these accounts (services/assets/README.md).
var PlaceholderProfile = PostingProfile{ClassCode: allClasses, Cost: "1500", Accumulated: "1590", DepreciationExpense: "6800", Impairment: "6810", Revaluation: "3300",
	AcquisitionClearing: "2190", Proceeds: "1190", Gain: "7100", Loss: "7110", TransferClearing: "1595", Placeholder: true}

const profileColumns = `legal_entity_id::text, class_code, cost_account, accumulated_account, depreciation_expense_account, impairment_account, revaluation_account,
	acquisition_clearing_account, proceeds_account, gain_account, loss_account, transfer_clearing_account, version, updated_by, updated_at`

func (p *PostingProfile) targets() []any {
	return []any{&p.LegalEntityID, &p.ClassCode, &p.Cost, &p.Accumulated, &p.DepreciationExpense, &p.Impairment, &p.Revaluation, &p.AcquisitionClearing,
		&p.Proceeds, &p.Gain, &p.Loss, &p.TransferClearing, &p.Version, &p.UpdatedBy, &p.UpdatedAt}
}

func loadProfiles(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) ([]PostingProfile, error) {
	rows, err := tx.Query(ctx, `select `+profileColumns+` from asset_posting_profile where tenant_id = $1 and legal_entity_id = $2 order by class_code`, tenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PostingProfile{}
	for rows.Next() {
		var p PostingProfile
		if err := rows.Scan(p.targets()...); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Postgres) ListPostingProfiles(ctx context.Context, legalEntityID string) ([]PostingProfile, error) {
	p := principal(ctx)
	var out []PostingProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadProfiles(ctx, tx, p.TenantID, legalEntityID)
		return err
	})
	return out, err
}

// SavePostingProfile creates the profile of a legal entity and class (ifMatch 0) or replaces it under its version.
func (s *Postgres) SavePostingProfile(ctx context.Context, in PostingProfile, ifMatch int, actor string) (PostingProfile, bool, error) {
	p := principal(ctx)
	var out PostingProfile
	created := false
	args := func() []any {
		return []any{p.TenantID, in.LegalEntityID, in.ClassCode, in.Cost, in.Accumulated, in.DepreciationExpense, in.Impairment, in.Revaluation,
			in.AcquisitionClearing, in.Proceeds, in.Gain, in.Loss, in.TransferClearing, actor}
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var version int
		err := tx.QueryRow(ctx, `select version from asset_posting_profile where tenant_id = $1 and legal_entity_id = $2 and class_code = $3 for update`,
			p.TenantID, in.LegalEntityID, in.ClassCode).Scan(&version)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if ifMatch != 0 {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the posting profile does not exist; create it without If-Match")
			}
			created = true
			_, err = tx.Exec(ctx, `insert into asset_posting_profile (tenant_id, legal_entity_id, class_code, cost_account, accumulated_account, depreciation_expense_account,
				impairment_account, revaluation_account, acquisition_clearing_account, proceeds_account, gain_account, loss_account, transfer_clearing_account, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`, args()...)
		case err != nil:
			return err
		case ifMatch == 0:
			return httpx.NewProblem(http.StatusPreconditionRequired, "precondition_required", "If-Match with the profile version is required to replace it")
		case ifMatch != version:
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("the posting profile was changed by another user; current version is %d", version))
		default:
			_, err = tx.Exec(ctx, `update asset_posting_profile set cost_account = $4, accumulated_account = $5, depreciation_expense_account = $6, impairment_account = $7,
				revaluation_account = $8, acquisition_clearing_account = $9, proceeds_account = $10, gain_account = $11, loss_account = $12, transfer_clearing_account = $13,
				updated_by = $14, updated_at = now(), version = version + 1 where tenant_id = $1 and legal_entity_id = $2 and class_code = $3`, args()...)
		}
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select `+profileColumns+` from asset_posting_profile where tenant_id = $1 and legal_entity_id = $2 and class_code = $3`,
			p.TenantID, in.LegalEntityID, in.ClassCode).Scan(out.targets()...)
	})
	return out, created, err
}

func resolveProfile(profiles []PostingProfile, class string) PostingProfile {
	var all *PostingProfile
	for i, p := range profiles {
		switch p.ClassCode {
		case class:
			return p
		case allClasses:
			all = &profiles[i]
		}
	}
	if all != nil {
		return *all
	}
	return PlaceholderProfile
}

// glNet is the net debit (credit when negative) per account of one journal: the cost and accumulated depreciation movements on the class accounts, and the
// counter account that balances them by transaction type. A disposal books the proceeds and takes the rest as gain or loss.
func glNet(p PostingProfile, typ string, cost, accumulated, proceeds decimal.Decimal, net map[string]decimal.Decimal) {
	add := func(account string, v decimal.Decimal) { net[account] = net[account].Add(v) }
	add(p.Cost, cost)
	add(p.Accumulated, accumulated.Neg())
	nbv := cost.Sub(accumulated)
	switch typ {
	case "DisposalSale", "DisposalScrap":
		add(p.Proceeds, proceeds)
		gl := nbv.Add(proceeds)
		if gl.IsPositive() {
			add(p.Gain, gl.Neg())
		} else {
			add(p.Loss, gl.Neg())
		}
	case "Acquisition", "AcquisitionAdj":
		add(p.AcquisitionClearing, nbv.Neg())
	case "Depreciation", "DepreciationAdj":
		add(p.DepreciationExpense, nbv.Neg())
	case "Impairment":
		add(p.Impairment, nbv.Neg())
	case "Revaluation":
		add(p.Revaluation, nbv.Neg())
	default:
		add(p.TransferClearing, nbv.Neg())
	}
}

type GLPostRequest struct {
	LegalEntityID string
	LedgerID      string
	BookCode      string
	Currency      string
}

type GLBooked struct {
	Document    string `json:"document"`
	EntryID     string `json:"entryId"`
	Lines       int    `json:"lines"`
	Transaction int    `json:"transactions"`
	Placeholder bool   `json:"placeholderProfile,omitempty"`
}

type GLFailed struct {
	Document string `json:"document"`
	Code     string `json:"code"`
}

type GLPostResult struct {
	Booked []GLBooked `json:"booked"`
	Failed []GLFailed `json:"failed"`
	More   bool       `json:"more"`
}

type glTx struct {
	id, typ, date, class string
	cost, accumulated    decimal.Decimal
	proceeds             decimal.Decimal
}

type glGroup struct {
	key    string
	source string
	txs    []glTx
}

func (s *Postgres) unbookedGroups(ctx context.Context, tx pgx.Tx, tenantID string, in GLPostRequest) ([]*glGroup, bool, error) {
	keys := []string{}
	krows, err := tx.Query(ctx, `select coalesce(t.run_id, t.id)::text from asset_transaction t
		join asset_book b on b.tenant_id = t.tenant_id and b.id = t.asset_book_id
		where t.tenant_id = $1 and t.legal_entity_id = $2 and b.book_code = $3 and b.currency = $4
			and not exists (select 1 from asset_gl_entry g where g.tenant_id = t.tenant_id and g.transaction_id = t.id)
		group by 1 order by min(t.created_at), 1 limit $5`, tenantID, in.LegalEntityID, in.BookCode, in.Currency, MaxGLGroupsPerRun+1)
	if err != nil {
		return nil, false, err
	}
	for krows.Next() {
		var k string
		if err := krows.Scan(&k); err != nil {
			krows.Close()
			return nil, false, err
		}
		keys = append(keys, k)
	}
	krows.Close()
	if err := krows.Err(); err != nil {
		return nil, false, err
	}
	more := len(keys) > MaxGLGroupsPerRun
	if more {
		keys = keys[:MaxGLGroupsPerRun]
	}
	rows, err := tx.Query(ctx, `select coalesce(t.run_id, t.id)::text, t.id::text, t.type, t.transaction_date::text, a.class_code, t.cost_amount::text, t.accumulated_amount::text,
			coalesce(t.proceeds_amount, 0)::text, t.run_id is not null
		from asset_transaction t join asset_book b on b.tenant_id = t.tenant_id and b.id = t.asset_book_id
		join asset a on a.tenant_id = b.tenant_id and a.id = b.asset_id
		where t.tenant_id = $1 and coalesce(t.run_id, t.id) = any($2::uuid[]) and b.book_code = $3 and b.currency = $4
			and not exists (select 1 from asset_gl_entry g where g.tenant_id = t.tenant_id and g.transaction_id = t.id)
		order by t.created_at, t.id`, tenantID, keys, in.BookCode, in.Currency)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	byKey := map[string]*glGroup{}
	var out []*glGroup
	for rows.Next() {
		var key, cost, acc, proceeds string
		var isRun bool
		var x glTx
		if err := rows.Scan(&key, &x.id, &x.typ, &x.date, &x.class, &cost, &acc, &proceeds, &isRun); err != nil {
			return nil, false, err
		}
		x.cost, x.accumulated, x.proceeds = decimal.RequireFromString(cost), decimal.RequireFromString(acc), decimal.RequireFromString(proceeds)
		g, ok := byKey[key]
		if !ok {
			g = &glGroup{key: key, source: "asset-transaction"}
			if isRun {
				g.source = "asset-depreciation-run"
			}
			byKey[key] = g
			out = append(out, g)
		}
		g.txs = append(g.txs, x)
	}
	return out, more, rows.Err()
}

func (g *glGroup) entry(in GLPostRequest, profiles []PostingProfile) (ledgerclient.Entry, bool) {
	mu := money.MinorUnits(in.Currency)
	net := map[string]decimal.Decimal{}
	placeholder := false
	for _, x := range g.txs {
		p := resolveProfile(profiles, x.class)
		placeholder = placeholder || p.Placeholder
		glNet(p, x.typ, x.cost.Round(mu), x.accumulated.Round(mu), x.proceeds.Round(mu), net)
	}
	e := ledgerclient.Entry{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, DocumentID: g.key, PostingDate: g.txs[0].date, Currency: in.Currency,
		IdempotencyKey: g.source + ":" + g.key, SourceType: g.source}
	accounts := make([]string, 0, len(net))
	for a := range net {
		accounts = append(accounts, a)
	}
	slices.Sort(accounts)
	for _, a := range accounts {
		if v := net[a]; !v.IsZero() {
			e.Lines = append(e.Lines, ledgerclient.Line{Account: a, Credit: v.IsNegative(), Amount: v.Abs()})
		}
	}
	return e, placeholder
}

func (s *Postgres) markBooked(ctx context.Context, tenantID string, in GLPostRequest, g *glGroup, entryID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, x := range g.txs {
			if _, err := tx.Exec(ctx, `insert into asset_gl_entry (tenant_id, transaction_id, legal_entity_id, journal_entry_id, posting_date)
				values ($1, $2, $3, nullif($4, '')::uuid, $5::date) on conflict do nothing`, tenantID, x.id, in.LegalEntityID, entryID, x.date); err != nil {
				return err
			}
		}
		return nil
	})
}

// PostToGL books the transactions of one book code that are not in the general ledger yet: one entry per depreciation run (all its books) and one per
// other transaction (acquisition, adjustment, revaluation, impairment, disposal, transfer), keyed by run or transaction so a retry returns the same entry.
// A group the ledger refuses is reported and the rest go on.
func (s *Postgres) PostToGL(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error) {
	p := principal(ctx)
	var groups []*glGroup
	var profiles []PostingProfile
	more := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if groups, more, err = s.unbookedGroups(ctx, tx, p.TenantID, in); err != nil {
			return err
		}
		profiles, err = loadProfiles(ctx, tx, p.TenantID, in.LegalEntityID)
		return err
	})
	if err != nil {
		return GLPostResult{}, err
	}
	res := GLPostResult{Booked: []GLBooked{}, Failed: []GLFailed{}, More: more}
	for _, g := range groups {
		entry, placeholder := g.entry(in, profiles)
		entryID := ""
		if len(entry.Lines) > 0 {
			if entryID, err = ledger.Post(ctx, entry); err != nil {
				code := ledgerclient.CodeOf(err)
				if code == "" {
					code = CodeGLFailed
				}
				slog.ErrorContext(ctx, "assets GL booking failed", "document", g.key, "key", entry.IdempotencyKey, "code", code, "err", err, "correlationId", middleware.GetReqID(ctx))
				res.Failed = append(res.Failed, GLFailed{Document: g.key, Code: code})
				continue
			}
		}
		if err := s.markBooked(ctx, p.TenantID, in, g, entryID); err != nil {
			slog.ErrorContext(ctx, "assets GL booking not recorded", "document", g.key, "entryId", entryID, "err", err, "correlationId", middleware.GetReqID(ctx))
			res.Failed = append(res.Failed, GLFailed{Document: g.key, Code: CodeGLFailed})
			continue
		}
		if entryID != "" {
			res.Booked = append(res.Booked, GLBooked{Document: g.key, EntryID: entryID, Lines: len(entry.Lines), Transaction: len(g.txs), Placeholder: placeholder})
		}
	}
	return res, nil
}

type GLReconcileRequest struct {
	LegalEntityID string
	LedgerID      string
	BookCode      string
	Currency      string
}

type GLAccountRecon struct {
	Account    string          `json:"account"`
	Subledger  decimal.Decimal `json:"subledger"`
	GL         decimal.Decimal `json:"gl"`
	Difference decimal.Decimal `json:"difference"`
}

type GLReconciliation struct {
	Currency   string           `json:"currency"`
	BookCode   string           `json:"bookCode"`
	Accounts   []GLAccountRecon `json:"accounts"`
	Unposted   int              `json:"unposted"`
	Reconciled bool             `json:"reconciled"`
}

// ReconcileGL compares the register's cost and accumulated depreciation of one book code, per class account, with the general ledger's balance of that
// account (difference = GL minus subledger; accumulated depreciation is a credit balance so it enters the subledger side negative). Unposted counts the
// transactions not booked yet, which show up as the difference.
func (s *Postgres) ReconcileGL(ctx context.Context, ledger ledgerclient.Ledger, in GLReconcileRequest) (GLReconciliation, error) {
	p := principal(ctx)
	out := GLReconciliation{Currency: in.Currency, BookCode: in.BookCode, Accounts: []GLAccountRecon{}, Reconciled: true}
	net := map[string]decimal.Decimal{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		profiles, err := loadProfiles(ctx, tx, p.TenantID, in.LegalEntityID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select a.class_code, sum(b.cost)::text, sum(b.accumulated_depreciation)::text from asset_book b
			join asset a on a.tenant_id = b.tenant_id and a.id = b.asset_id
			where b.tenant_id = $1 and b.legal_entity_id = $2 and b.book_code = $3 and b.currency = $4 group by a.class_code`, p.TenantID, in.LegalEntityID, in.BookCode, in.Currency)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var class, cost, acc string
			if err := rows.Scan(&class, &cost, &acc); err != nil {
				return err
			}
			pr := resolveProfile(profiles, class)
			net[pr.Cost] = net[pr.Cost].Add(decimal.RequireFromString(cost))
			net[pr.Accumulated] = net[pr.Accumulated].Sub(decimal.RequireFromString(acc))
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select count(*) from asset_transaction t join asset_book b on b.tenant_id = t.tenant_id and b.id = t.asset_book_id
			where t.tenant_id = $1 and t.legal_entity_id = $2 and b.book_code = $3 and b.currency = $4
				and not exists (select 1 from asset_gl_entry g where g.tenant_id = t.tenant_id and g.transaction_id = t.id)`,
			p.TenantID, in.LegalEntityID, in.BookCode, in.Currency).Scan(&out.Unposted)
	})
	if err != nil {
		return GLReconciliation{}, err
	}
	accounts := make([]string, 0, len(net))
	for a := range net {
		accounts = append(accounts, a)
	}
	slices.Sort(accounts)
	for _, account := range accounts {
		bal, err := ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, Account: account})
		if err != nil {
			return GLReconciliation{}, fmt.Errorf("general ledger balance of %s: %w", account, err)
		}
		if bal.Currency != in.Currency && !bal.Net().IsZero() {
			return GLReconciliation{}, fmt.Errorf("general ledger balance of %s is in %s, not %s", account, bal.Currency, in.Currency)
		}
		a := GLAccountRecon{Account: account, Subledger: net[account], GL: bal.Net()}
		a.Difference = a.GL.Sub(a.Subledger)
		out.Reconciled = out.Reconciled && a.Difference.IsZero()
		out.Accounts = append(out.Accounts, a)
	}
	return out, nil
}
