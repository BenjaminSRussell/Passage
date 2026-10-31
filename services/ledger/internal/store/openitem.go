package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// Open-item error codes (FR-11.19, BR-11.12, BR-11.13, BR-11.18).
const (
	CodeAccountNotFound           = "LEDGER_ACCOUNT_NOT_FOUND"
	CodeOpenItemNotFound          = "LEDGER_OPEN_ITEM_NOT_FOUND"
	CodeOpenItemCleared           = "LEDGER_OPEN_ITEM_ALREADY_CLEARED"
	CodeOIAccountInvalid          = "LEDGER_OI_ACCOUNT_INVALID"
	CodeOIActivationBalance       = "LEDGER_OI_ACTIVATION_BALANCE"
	CodeOIItemsExist              = "LEDGER_OI_OPEN_ITEMS_EXIST"
	CodeClearingUnbalanced        = "LEDGER_CLEARING_UNBALANCED"
	CodeClearingAccountMixed      = "LEDGER_CLEARING_ACCOUNT_MIXED"
	CodeClearingDateInvalid       = "LEDGER_CLEARING_DATE_INVALID"
	CodeClearingNotFound          = "LEDGER_CLEARING_NOT_FOUND"
	CodeClearingAlreadyReset      = "LEDGER_CLEARING_ALREADY_RESET"
	CodeClearingResidualUsed      = "LEDGER_CLEARING_RESIDUAL_USED"
	CodeClearingResidualDepth     = "LEDGER_CLEARING_RESIDUAL_DEPTH"
	CodeClearingDifferenceAccount = "LEDGER_CLEARING_DIFFERENCE_ACCOUNT"
	CodeOpenItemAccountRequired   = "LEDGER_OPEN_ITEM_ACCOUNT_REQUIRED"
	CodeToleranceNotFound         = "LEDGER_TOLERANCE_GROUP_NOT_FOUND"
	CodeToleranceInactive         = "LEDGER_TOLERANCE_GROUP_INACTIVE"
	CodeIdempotencyConflict       = "LEDGER_IDEMPOTENCY_CONFLICT"

	// Open item statuses (BR-11.13). reset_open items are clearable again.
	ItemOpen      = "open"
	ItemCleared   = "cleared"
	ItemResetOpen = "reset_open"
	ItemVoided    = "voided"
)

// OpenItemAccount is the open-item setting of an account in one ledger.
type OpenItemAccount struct {
	LedgerID  string    `json:"ledgerId"`
	Account   string    `json:"account"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	OpenItems int       `json:"openItems"`
	ChangedBy string    `json:"changedBy"`
	ChangedAt time.Time `json:"changedAt"`
}

// ListOpenItemAccounts returns every account that was ever switched on or off in the ledger.
func (s *Postgres) ListOpenItemAccounts(ctx context.Context, ledgerID string) ([]OpenItemAccount, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []OpenItemAccount{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select a.account_number, a.name, s.enabled, s.changed_by, s.changed_at,
				(select count(*)::int from open_item i where i.tenant_id = s.tenant_id and i.ledger_id = s.ledger_id
				   and i.main_account_id = s.main_account_id and i.status in ('open', 'reset_open'))
			from open_item_setting s
			join main_account a on a.tenant_id = s.tenant_id and a.id = s.main_account_id
			where s.tenant_id = $1 and s.ledger_id = $2 order by a.account_number`, p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a := OpenItemAccount{LedgerID: ledgerID}
			if err := rows.Scan(&a.Account, &a.Name, &a.Enabled, &a.ChangedBy, &a.ChangedAt, &a.OpenItems); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

func oiViolation(code, field, message string) error {
	return &domain.RejectedError{Violations: []domain.Violation{{Code: code, Field: field, Message: message}}}
}

// PutOpenItemAccount switches open-item management of an account on or off in a
// ledger (BR-11.18). Only balance-sheet, postable accounts that are not customer
// or vendor reconciliation accounts qualify. Switching on is refused for an
// account with a non-zero balance in the ledger: that needs an activation run,
// which is not built yet. Switching off needs zero open items. created reports a
// first setting for the account.
func (s *Postgres) PutOpenItemAccount(ctx context.Context, ledgerID, number string, enabled bool) (OpenItemAccount, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return OpenItemAccount{}, false, err
	}
	var out OpenItemAccount
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The ledger row lock keeps postings from creating items while the setting flips.
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, true)
		if err != nil {
			return err
		}
		var accountID, name, accountType, recon string
		var postable bool
		err = tx.QueryRow(ctx, `select id::text, name, account_type, is_postable, reconciliation_account_type
			from main_account where tenant_id = $1 and chart_code = $2 and account_number = $3`,
			p.TenantID, ledger.ChartCode, number).Scan(&accountID, &name, &accountType, &postable, &recon)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeAccountNotFound, "account "+number+" does not exist in chart "+ledger.ChartCode)
		}
		if err != nil {
			return err
		}
		var current bool
		err = tx.QueryRow(ctx, `select enabled from open_item_setting where tenant_id = $1 and ledger_id = $2 and main_account_id = $3 for update`,
			p.TenantID, ledgerID, accountID).Scan(&current)
		known := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		created = !known
		if enabled && !(known && current) {
			if accountType != "asset" && accountType != "liability" && accountType != "equity" || !postable || recon == "customer" || recon == "vendor" {
				return oiViolation(CodeOIAccountInvalid, "account",
					"open-item management needs a postable balance-sheet account that is not a customer or vendor reconciliation account")
			}
			var history bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from balance where tenant_id = $1 and ledger_id = $2 and main_account_id = $3
					group by dimension_combination_id having sum(net) <> 0)`, p.TenantID, ledgerID, accountID).Scan(&history); err != nil {
				return err
			}
			if history {
				return oiViolation(CodeOIActivationBalance, "account",
					"open-item activation needs an activation run because account "+number+" has a balance")
			}
		}
		if !enabled && known && current {
			var open int
			if err := tx.QueryRow(ctx, `select count(*) from open_item where tenant_id = $1 and ledger_id = $2 and main_account_id = $3
				and status in ('open', 'reset_open')`, p.TenantID, ledgerID, accountID).Scan(&open); err != nil {
				return err
			}
			if open > 0 {
				return httpx.NewProblem(http.StatusConflict, CodeOIItemsExist,
					"account "+number+" still has "+strconv.Itoa(open)+" open items; clear them first")
			}
		}
		if _, err := tx.Exec(ctx, `insert into open_item_setting (tenant_id, legal_entity_id, ledger_id, main_account_id, enabled, changed_by)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, ledger_id, main_account_id) do update set enabled = excluded.enabled,
				changed_by = excluded.changed_by, changed_at = now()`,
			p.TenantID, ledger.LegalEntityID, ledgerID, accountID, enabled, p.UserID); err != nil {
			return err
		}
		out = OpenItemAccount{LedgerID: ledgerID, Account: number, Name: name, Enabled: enabled}
		return tx.QueryRow(ctx, `select changed_by, changed_at, (select count(*)::int from open_item i where i.tenant_id = $1 and i.ledger_id = $2
				and i.main_account_id = $3 and i.status in ('open', 'reset_open'))
			from open_item_setting where tenant_id = $1 and ledger_id = $2 and main_account_id = $3`,
			p.TenantID, ledgerID, accountID).Scan(&out.ChangedBy, &out.ChangedAt, &out.OpenItems)
	})
	return out, created, err
}

// createOpenItems makes one open item for every non-zero line of an entry that
// sits on an open-item managed account of the ledger (FR-11.19). The item is
// signed, debit positive, in the ledger currency, and its assignment starts as
// the line's source reference (first 50 characters). It runs in writeEntry's
// transaction, after the lines exist.
func createOpenItems(ctx context.Context, tx pgx.Tx, tenantID string, l Ledger, entryID, postingDate string) error {
	_, err := tx.Exec(ctx, `insert into open_item (tenant_id, id, legal_entity_id, ledger_id, main_account_id, dimension_combination_id,
			journal_line_id, source_type, amount, currency, assignment, posting_date)
		select jl.tenant_id, gen_random_uuid(), $3, $4, jl.main_account_id, jl.dimension_combination_id, jl.id, 'gl_line',
			case when jl.debit_credit = 'D' then jl.amount_acct else -jl.amount_acct end, $5, left(jl.source_line_ref, 50), $6::text::date
		from journal_line jl
		join open_item_setting s on s.tenant_id = jl.tenant_id and s.ledger_id = $4 and s.main_account_id = jl.main_account_id and s.enabled
		where jl.tenant_id = $1 and jl.entry_id = $2 and jl.amount_acct <> 0`,
		tenantID, entryID, l.LegalEntityID, l.ID, l.Currency, postingDate)
	return err
}

// OpenItem is one open item with its source line.
type OpenItem struct {
	ID                   string            `json:"id"`
	LedgerID             string            `json:"ledgerId"`
	Account              string            `json:"account"`
	AccountName          string            `json:"accountName"`
	Dimensions           map[string]string `json:"dimensions"`
	SourceType           string            `json:"sourceType"`
	Amount               string            `json:"amount"`
	DebitCredit          string            `json:"debitCredit"`
	Currency             string            `json:"currency"`
	Assignment           string            `json:"assignment,omitempty"`
	PostingDate          string            `json:"postingDate"`
	Status               string            `json:"status"`
	ClearingDocumentID   *string           `json:"clearingDocumentId,omitempty"`
	ResidualOfItemID     *string           `json:"residualOfItemId,omitempty"`
	EntryID              *string           `json:"entryId,omitempty"`
	Voucher              *string           `json:"voucher,omitempty"`
	LineNumber           *int              `json:"lineNumber,omitempty"`
	dimensionCombination []byte
}

// OpenItemFilter selects open items of one ledger. Status open also matches
// reset_open. Dimensions are exact matches on attribute values. Cursor is the
// NextCursor of the previous page.
type OpenItemFilter struct {
	LedgerID   string
	Account    string
	Status     string
	Assignment string
	Dimensions map[string]string
	Cursor     string
	Limit      int
}

// OpenItemPage is a keyset page ordered by creation.
type OpenItemPage struct {
	Items      []OpenItem `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

const openItemSelect = `select i.id::text, i.ledger_id::text, a.account_number, a.name, c.dimensions, i.source_type, i.amount::text, i.currency::text,
		coalesce(i.assignment, ''), i.posting_date::text, i.status, i.clearing_document_id::text, i.residual_of_item_id::text,
		e.id::text, e.voucher, jl.line_number, i.dimension_combination_id, i.seq
	from open_item i
	join main_account a on a.tenant_id = i.tenant_id and a.id = i.main_account_id
	join dimension_combination c on c.tenant_id = i.tenant_id and c.id = i.dimension_combination_id
	left join journal_line jl on jl.tenant_id = i.tenant_id and jl.id = i.journal_line_id
	left join journal_entry e on e.tenant_id = jl.tenant_id and e.id = jl.entry_id`

func scanOpenItem(row pgx.Row, seq *int64) (OpenItem, error) {
	var it OpenItem
	var amount string
	var s int64
	err := row.Scan(&it.ID, &it.LedgerID, &it.Account, &it.AccountName, &it.Dimensions, &it.SourceType, &amount, &it.Currency,
		&it.Assignment, &it.PostingDate, &it.Status, &it.ClearingDocumentID, &it.ResidualOfItemID, &it.EntryID, &it.Voucher,
		&it.LineNumber, &it.dimensionCombination, &s)
	if err != nil {
		return it, err
	}
	if seq != nil {
		*seq = s
	}
	a, err := decimal.NewFromString(amount)
	if err != nil {
		return it, err
	}
	it.DebitCredit = domain.SideDebit
	if a.IsNegative() {
		it.DebitCredit = domain.SideCredit
	}
	it.Amount = a.Abs().StringFixed(money.MinorUnits(it.Currency))
	if it.Dimensions == nil {
		it.Dimensions = map[string]string{}
	}
	return it, nil
}

// ListOpenItems returns open items of a ledger, oldest first, with keyset paging.
func (s *Postgres) ListOpenItems(ctx context.Context, f OpenItemFilter) (OpenItemPage, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return OpenItemPage{}, err
	}
	limit := f.Limit
	if limit <= 0 || limit > MaxPageSize {
		limit = 100
	}
	cursor := int64(0)
	if f.Cursor != "" {
		if cursor, err = strconv.ParseInt(f.Cursor, 10, 64); err != nil || cursor < 0 {
			return OpenItemPage{}, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "cursor is not valid")
		}
	}
	page := OpenItemPage{Items: []OpenItem{}}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, f.LedgerID, false); err != nil {
			return err
		}
		q := openItemSelect + ` where i.tenant_id = $1 and i.ledger_id = $2 and i.seq > $3`
		args := []any{p.TenantID, f.LedgerID, cursor}
		add := func(clause string, v any) {
			args = append(args, v)
			q += " and " + clause + "$" + strconv.Itoa(len(args))
		}
		if f.Account != "" {
			add("a.account_number = ", f.Account)
		}
		switch f.Status {
		case "":
		case ItemOpen:
			q += ` and i.status in ('open', 'reset_open')`
		default:
			add("i.status = ", f.Status)
		}
		if f.Assignment != "" {
			add("i.assignment = ", f.Assignment)
		}
		for attr, v := range f.Dimensions {
			args = append(args, attr, v)
			q += " and c.dimensions ->> $" + strconv.Itoa(len(args)-1) + " = $" + strconv.Itoa(len(args))
		}
		args = append(args, limit+1)
		q += ` order by i.seq limit $` + strconv.Itoa(len(args))
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var last int64
		for rows.Next() {
			var seq int64
			it, err := scanOpenItem(rows, &seq)
			if err != nil {
				return err
			}
			if len(page.Items) == limit {
				page.NextCursor = strconv.FormatInt(last, 10)
				break
			}
			last = seq
			page.Items = append(page.Items, it)
		}
		return rows.Err()
	})
	return page, err
}
