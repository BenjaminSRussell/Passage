package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

const clearingAggregate = "clearing"

// ClearingInput selects the open items of one ledger to clear. Key and Hash are
// the Idempotency-Key and the hash of the request body; they are used by
// ClearItems only.
type ClearingInput struct {
	LedgerID       string
	ItemIDs        []string
	ClearingDate   time.Time
	ToleranceGroup string
	ResidualItem   bool
	Key            string
	Hash           []byte
	TraceID        string
	Today          time.Time
}

// ClearingPreview is the stateless result of a simulation (POST /clearings:simulate)
// and is embedded in nothing else. Amounts are in the ledger currency; Net is debit
// minus credit. Outcome is balanced, difference, residual or unbalanced.
type ClearingPreview struct {
	LedgerID         string `json:"ledgerId"`
	Account          string `json:"account"`
	Currency         string `json:"currency"`
	ItemCount        int    `json:"itemCount"`
	Debit            string `json:"debit"`
	Credit           string `json:"credit"`
	Net              string `json:"net"`
	ToleranceGroup   string `json:"toleranceGroup,omitempty"`
	Outcome          string `json:"outcome"`
	DifferenceAmount string `json:"differenceAmount"`
	ResidualAmount   string `json:"residualAmount"`
}

// ClearingDocument groups the items one clearing cleared. Items is set on single reads.
type ClearingDocument struct {
	ID                string     `json:"id"`
	LedgerID          string     `json:"ledgerId"`
	Account           string     `json:"account"`
	ClearingDate      string     `json:"clearingDate"`
	Mode              string     `json:"mode"`
	ItemIDs           []string   `json:"itemIds"`
	ToleranceGroup    string     `json:"toleranceGroup,omitempty"`
	DifferenceAmount  string     `json:"differenceAmount"`
	DifferenceEntryID *string    `json:"differenceEntryId,omitempty"`
	ResidualItemID    *string    `json:"residualItemId,omitempty"`
	Status            string     `json:"status"`
	ResetReasonCode   *string    `json:"resetReasonCode,omitempty"`
	ResetComment      *string    `json:"resetComment,omitempty"`
	ResetBy           *string    `json:"resetBy,omitempty"`
	ResetAt           *time.Time `json:"resetAt,omitempty"`
	CreatedBy         string     `json:"createdBy"`
	CreatedAt         time.Time  `json:"createdAt"`
	Items             []OpenItem `json:"items,omitempty"`

	currency string
	hash     []byte
	resetKey string
}

// ResetInput resets a clearing document as a whole (BR-11.13).
type ResetInput struct {
	ID         string
	Key        string
	ReasonCode string
	Comment    string
	TraceID    string
	Today      time.Time
}

const clearingSelect = `select d.id::text, d.ledger_id::text, a.account_number, d.clearing_date::text, d.mode, d.item_ids::text[],
		coalesce(d.tolerance_group_code, ''), d.difference_amount::text, d.difference_entry_id::text, d.residual_item_id::text, d.status,
		d.reset_reason_code, d.reset_comment, d.reset_by, d.reset_at, d.created_by, d.created_at, l.currency::text, d.payload_hash,
		coalesce(d.reset_key, '')
	from clearing_document d
	join main_account a on a.tenant_id = d.tenant_id and a.id = d.main_account_id
	join ledger l on l.tenant_id = d.tenant_id and l.id = d.ledger_id`

func scanClearing(row pgx.Row) (ClearingDocument, error) {
	var d ClearingDocument
	var diff string
	err := row.Scan(&d.ID, &d.LedgerID, &d.Account, &d.ClearingDate, &d.Mode, &d.ItemIDs, &d.ToleranceGroup, &diff, &d.DifferenceEntryID,
		&d.ResidualItemID, &d.Status, &d.ResetReasonCode, &d.ResetComment, &d.ResetBy, &d.ResetAt, &d.CreatedBy, &d.CreatedAt,
		&d.currency, &d.hash, &d.resetKey)
	if err != nil {
		return d, err
	}
	amount, err := decimal.NewFromString(diff)
	if err != nil {
		return d, err
	}
	d.DifferenceAmount = amount.StringFixed(money.MinorUnits(d.currency))
	return d, nil
}

func clearingNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeClearingNotFound, "clearing document not found")
}

func clearingViolation(code, field, message string) error { return oiViolation(code, field, message) }

// selection is the evaluated item selection of a clearing.
type selection struct {
	ledger    Ledger
	items     []selectedItem
	accountID string
	account   string
	debit     decimal.Decimal
	credit    decimal.Decimal
	net       decimal.Decimal
	tolerance *toleranceRow
	outcome   string
	largest   selectedItem
}

type selectedItem struct {
	id, status, postingDate string
	clearingDoc             *string
	amount                  decimal.Decimal
	combination             []byte
}

type toleranceRow struct {
	code, differenceAccount string
	differenceManaged       bool
	limit                   domain.Tolerance
}

// evaluate reads the selected items (locking them when lock is set) and decides
// how they clear. Every item must belong to the ledger, sit on one account and be
// clearable; the clearing date may not precede any item's posting date (BR-11.13).
func evaluate(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, in ClearingInput, lock bool) (selection, error) {
	sel := selection{ledger: ledger}
	if n := len(in.ItemIDs); n < domain.MinClearingItems || n > domain.MaxClearingItems {
		return sel, clearingViolation("COM-0001", "itemIds",
			fmt.Sprintf("select %d to %d open items", domain.MinClearingItems, domain.MaxClearingItems))
	}
	q := `select i.id::text, i.main_account_id::text, a.account_number, i.amount::text, i.posting_date::text, i.status,
			i.clearing_document_id::text, i.dimension_combination_id
		from open_item i join main_account a on a.tenant_id = i.tenant_id and a.id = i.main_account_id
		where i.tenant_id = $1 and i.ledger_id = $2 and i.id = any($3::uuid[]) order by i.seq`
	if lock {
		q += ` for update of i`
	}
	rows, err := tx.Query(ctx, q, tenantID, ledger.ID, in.ItemIDs)
	if err != nil {
		return sel, err
	}
	defer rows.Close()
	accounts := map[string]bool{}
	var latest string
	amounts := []decimal.Decimal{}
	for rows.Next() {
		var it selectedItem
		var accountID, number, amount string
		if err := rows.Scan(&it.id, &accountID, &number, &amount, &it.postingDate, &it.status, &it.clearingDoc, &it.combination); err != nil {
			return sel, err
		}
		if it.amount, err = decimal.NewFromString(amount); err != nil {
			return sel, err
		}
		accounts[accountID] = true
		sel.accountID, sel.account = accountID, number
		if it.postingDate > latest {
			latest = it.postingDate
		}
		if sel.largest.id == "" || it.amount.Abs().GreaterThan(sel.largest.amount.Abs()) {
			sel.largest = it
		}
		sel.items = append(sel.items, it)
		amounts = append(amounts, it.amount)
	}
	if err := rows.Err(); err != nil {
		return sel, err
	}
	rows.Close()
	if len(sel.items) != len(in.ItemIDs) {
		return sel, httpx.NewProblem(http.StatusNotFound, CodeOpenItemNotFound, "one or more open items do not exist in this ledger")
	}
	for _, it := range sel.items {
		if it.status == ItemCleared || it.status == ItemVoided {
			doc := ""
			if it.clearingDoc != nil {
				doc = *it.clearingDoc
			}
			return sel, httpx.NewProblem(http.StatusConflict, CodeOpenItemCleared,
				fmt.Sprintf("item %s is already cleared (document %s); reset the earlier clearing first", it.id, doc))
		}
	}
	if len(accounts) > 1 {
		return sel, clearingViolation(CodeClearingAccountMixed, "itemIds", "all selected items must be on one account")
	}
	if dateText(in.ClearingDate) < latest {
		return sel, clearingViolation(CodeClearingDateInvalid, "clearingDate",
			"the clearing date may not precede the latest posting date of the items, "+latest)
	}
	var enabled bool
	if err := tx.QueryRow(ctx, `select coalesce((select enabled from open_item_setting where tenant_id = $1 and ledger_id = $2 and main_account_id = $3), false)`,
		tenantID, ledger.ID, sel.accountID).Scan(&enabled); err != nil {
		return sel, err
	}
	if !enabled {
		return sel, clearingViolation(CodeOpenItemAccountRequired, "itemIds", "account "+sel.account+" is not open-item managed")
	}
	sel.debit, sel.credit, sel.net = domain.ClearingTotals(amounts)
	var tol *domain.Tolerance
	if in.ToleranceGroup != "" {
		var row toleranceRow
		var debitLimit, creditLimit, pct string
		var active bool
		err := tx.QueryRow(ctx, `select t.code, a.account_number, t.debit_limit::text, t.credit_limit::text, t.percent_limit::text, t.active,
				coalesce((select s.enabled from open_item_setting s where s.tenant_id = t.tenant_id and s.ledger_id = t.ledger_id
					and s.main_account_id = t.difference_account_id), false)
			from tolerance_group t join main_account a on a.tenant_id = t.tenant_id and a.id = t.difference_account_id
			where t.tenant_id = $1 and t.ledger_id = $2 and t.code = $3`, tenantID, ledger.ID, in.ToleranceGroup).
			Scan(&row.code, &row.differenceAccount, &debitLimit, &creditLimit, &pct, &active, &row.differenceManaged)
		if errors.Is(err, pgx.ErrNoRows) {
			return sel, toleranceNotFound()
		}
		if err != nil {
			return sel, err
		}
		if !active {
			return sel, clearingViolation(CodeToleranceInactive, "toleranceGroup", "tolerance group "+in.ToleranceGroup+" is not active")
		}
		row.limit = domain.Tolerance{Debit: decimal.RequireFromString(debitLimit), Credit: decimal.RequireFromString(creditLimit),
			Percent: decimal.RequireFromString(pct)}
		tol = &row.limit
		sel.tolerance = &row
	}
	sel.outcome = domain.ClearingOutcomeOf(sel.net, sel.debit, sel.credit, tol, in.ResidualItem)
	if sel.outcome == domain.OutcomeDifference && sel.tolerance.differenceManaged {
		// The write-off line would open an item on the difference account that no
		// clearing tracks, and a reset could not void it.
		return sel, clearingViolation(CodeClearingDifferenceAccount, "toleranceGroup", "the difference account "+
			sel.tolerance.differenceAccount+" of tolerance group "+sel.tolerance.code+" is open-item managed; use a group whose difference account is not")
	}
	return sel, nil
}

func (sel selection) preview(in ClearingInput) ClearingPreview {
	minor := money.MinorUnits(sel.ledger.Currency)
	zero := decimal.Zero.StringFixed(minor)
	pv := ClearingPreview{LedgerID: sel.ledger.ID, Account: sel.account, Currency: sel.ledger.Currency, ItemCount: len(sel.items),
		Debit: sel.debit.StringFixed(minor), Credit: sel.credit.StringFixed(minor), Net: sel.net.StringFixed(minor),
		ToleranceGroup: in.ToleranceGroup, Outcome: sel.outcome, DifferenceAmount: zero, ResidualAmount: zero}
	switch sel.outcome {
	case domain.OutcomeDifference:
		pv.DifferenceAmount = sel.net.StringFixed(minor)
	case domain.OutcomeResidual:
		pv.ResidualAmount = sel.net.StringFixed(minor)
	}
	return pv
}

func (sel selection) unbalanced() error {
	minor := money.MinorUnits(sel.ledger.Currency)
	limit := "no tolerance group"
	if sel.tolerance != nil {
		side := sel.tolerance.limit.Debit
		if sel.net.IsNegative() {
			side = sel.tolerance.limit.Credit
		}
		limit = "the tolerance " + side.StringFixed(minor)
		if sel.tolerance.limit.Percent.IsPositive() {
			limit += " or " + sel.tolerance.limit.Percent.String() + "%"
		}
	}
	return clearingViolation(CodeClearingUnbalanced, "itemIds", fmt.Sprintf(
		"Selected items leave a residual of %s %s, above %s. Add items, create a residual item, or adjust the selection.",
		sel.net.Abs().StringFixed(minor), sel.ledger.Currency, limit))
}

// SimulateClearing previews a clearing without writing anything.
func (s *Postgres) SimulateClearing(ctx context.Context, in ClearingInput) (ClearingPreview, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return ClearingPreview{}, err
	}
	var out ClearingPreview
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, false)
		if err != nil {
			return err
		}
		sel, err := evaluate(ctx, tx, p.TenantID, ledger, in, false)
		if err != nil {
			return err
		}
		out = sel.preview(in)
		return nil
	})
	return out, err
}

// ClearItems clears the selected items in one transaction (F-03 semantics): a
// zero net clears them; a residual inside the named tolerance group is posted to
// its difference account and cleared with them; a residual outside it becomes a
// new open item when ResidualItem is set and is refused otherwise
// (LEDGER_CLEARING_UNBALANCED). A repeated Idempotency-Key on the ledger returns
// the same document (created = false); the same key with another body is a 409.
func (s *Postgres) ClearItems(ctx context.Context, in ClearingInput) (ClearingDocument, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return ClearingDocument{}, false, err
	}
	var out ClearingDocument
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, created, err = clearInTx(ctx, tx, p, in, "manual")
		return err
	})
	return out, created, err
}

// clearInTx is the body of a clearing inside the caller's transaction; mode is
// manual or auto.
func clearInTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in ClearingInput, mode string) (ClearingDocument, bool, error) {
	var out ClearingDocument
	ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, true)
	if err != nil {
		return out, false, err
	}
	existing, err := scanClearing(tx.QueryRow(ctx, clearingSelect+` where d.tenant_id = $1 and d.ledger_id = $2 and d.idempotency_key = $3`,
		p.TenantID, ledger.ID, in.Key))
	if err == nil {
		if !bytes.Equal(existing.hash, in.Hash) {
			return out, false, httpx.NewProblem(http.StatusConflict, CodeIdempotencyConflict, "Idempotency-Key was already used with a different request body")
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, false, err
	}
	sel, err := evaluate(ctx, tx, p.TenantID, ledger, in, true)
	if err != nil {
		return out, false, err
	}
	if sel.outcome == domain.OutcomeUnbalanced {
		return out, false, sel.unbalanced()
	}
	docID := ids.New()
	clearedItems := make([]string, 0, len(sel.items)+1)
	clearedItems = append(clearedItems, in.ItemIDs...)
	var diffEntryID, residualID *string
	diffAmount := decimal.Zero
	switch sel.outcome {
	case domain.OutcomeDifference:
		entryID, itemID, err := postClearingDifference(ctx, tx, p, ledger, sel, docID, in)
		if err != nil {
			return out, false, err
		}
		diffEntryID, diffAmount = &entryID, sel.net
		clearedItems = append(clearedItems, itemID)
	case domain.OutcomeResidual:
		id, err := insertResidual(ctx, tx, p.TenantID, ledger, sel, in.ClearingDate)
		if err != nil {
			return out, false, err
		}
		residualID = &id
	}
	var tol *string
	if in.ToleranceGroup != "" {
		tol = &in.ToleranceGroup
	}
	if _, err := tx.Exec(ctx, `insert into clearing_document (tenant_id, id, legal_entity_id, ledger_id, main_account_id, clearing_date, mode,
			item_ids, tolerance_group_code, difference_amount, difference_entry_id, residual_item_id, status, idempotency_key, payload_hash, created_by)
		values ($1, $2, $3, $4, $5, $6::text::date, $15, $7::uuid[], $8, $9::text::numeric, $10, $11, 'cleared', $12, $13, $14)`,
		p.TenantID, docID, ledger.LegalEntityID, ledger.ID, sel.accountID, dateText(in.ClearingDate), in.ItemIDs, tol,
		diffAmount.String(), diffEntryID, residualID, in.Key, in.Hash, p.UserID, mode); err != nil {
		return out, false, err
	}
	if _, err := tx.Exec(ctx, `update open_item set status = 'cleared', clearing_document_id = $3
		where tenant_id = $1 and id = any($2::uuid[])`, p.TenantID, clearedItems, docID); err != nil {
		return out, false, err
	}
	if err := enqueueFor(ctx, tx, clearingAggregate, docID, ledgerevents.OpenItemCleared, p.TenantID, ledgerevents.OpenItemClearedData{
		ClearingDocumentID: docID, LedgerID: ledger.ID, Account: sel.account, Mode: mode, ItemIDs: in.ItemIDs,
		DifferenceAmount: diffAmount.StringFixed(money.MinorUnits(ledger.Currency)), DifferenceEntryID: deref(diffEntryID),
		ResidualItemID: deref(residualID), Status: "cleared", Actor: p.UserID,
	}); err != nil {
		return out, false, err
	}
	out, err = loadClearing(ctx, tx, p.TenantID, docID, false)
	return out, true, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// postClearingDifference posts the write-off of a residual inside tolerance: a
// line on the open-item account that brings the selection to zero, against a line
// on the tolerance group's difference account. It returns the entry id and the
// open item the account line created, which is cleared with the selection.
func postClearingDifference(ctx context.Context, tx pgx.Tx, p tenant.Principal, ledger Ledger, sel selection, docID string, in ClearingInput) (string, string, error) {
	var dims map[string]string
	if err := tx.QueryRow(ctx, `select dimensions from dimension_combination where tenant_id = $1 and id = $2`,
		p.TenantID, sel.largest.combination).Scan(&dims); err != nil {
		return "", "", err
	}
	itemSide, diffSide := domain.SideCredit, domain.SideDebit // debits exceed credits: credit the item account
	if sel.net.IsNegative() {
		itemSide, diffSide = domain.SideDebit, domain.SideCredit
	}
	amount := sel.net.Abs()
	key := "clearing:" + docID
	hash := sha256.Sum256([]byte(key))
	entry, _, err := postToLedger(ctx, tx, p.TenantID, ledger, PostInput{
		LedgerID: ledger.ID, DocumentID: ids.New(), PostingDate: in.ClearingDate, Description: "Clearing difference",
		Source: ledgerevents.Source{System: "ledger", Type: "clearing_difference", ID: docID, URI: "passage://ledger/clearings/" + docID},
		Lines: []domain.LineInput{
			{Account: sel.account, Dimensions: dims, Side: itemSide, Amount: amount, Currency: ledger.Currency, Text: "Clearing difference", SourceLineRef: "clearing:" + docID},
			{Account: sel.tolerance.differenceAccount, Side: diffSide, Amount: amount, Currency: ledger.Currency, Text: "Clearing difference"},
		},
		Key: key, Hash: hash[:], Actor: p.UserID, TraceID: in.TraceID, Today: in.Today,
	})
	if err != nil {
		return "", "", err
	}
	itemID, err := entryItem(ctx, tx, p.TenantID, entry.ID, sel.accountID)
	return entry.ID, itemID, err
}

// entryItem returns the open item made by the line of an entry on an account.
func entryItem(ctx context.Context, tx pgx.Tx, tenantID, entryID, accountID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `select i.id::text from open_item i join journal_line l on l.tenant_id = i.tenant_id and l.id = i.journal_line_id
		where i.tenant_id = $1 and l.entry_id = $2 and l.main_account_id = $3`, tenantID, entryID, accountID).Scan(&id)
	return id, err
}

// insertResidual carries a residual outside tolerance as a new open item that
// points at the largest cleared item (BR-11.13: the chain is at most 20 deep).
func insertResidual(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, sel selection, date time.Time) (string, error) {
	var depth int
	if err := tx.QueryRow(ctx, `with recursive chain as (
			select id, residual_of_item_id, 1 as depth from open_item where tenant_id = $1 and id = $2
			union all
			select o.id, o.residual_of_item_id, c.depth + 1 from open_item o
			join chain c on o.tenant_id = $1 and o.id = c.residual_of_item_id)
		select max(depth) from chain`, tenantID, sel.largest.id).Scan(&depth); err != nil {
		return "", err
	}
	if depth+1 > domain.MaxResidualDepth {
		return "", clearingViolation(CodeClearingResidualDepth, "residualItem",
			fmt.Sprintf("the residual chain would be deeper than %d items", domain.MaxResidualDepth))
	}
	id := ids.New()
	_, err := tx.Exec(ctx, `insert into open_item (tenant_id, id, legal_entity_id, ledger_id, main_account_id, dimension_combination_id,
			source_type, amount, currency, posting_date, residual_of_item_id)
		values ($1, $2, $3, $4, $5, $6, 'residual', $7::text::numeric, $8, $9::text::date, $10)`,
		tenantID, id, ledger.LegalEntityID, ledger.ID, sel.accountID, sel.largest.combination, sel.net.String(), ledger.Currency,
		dateText(date), sel.largest.id)
	return id, err
}

func loadClearing(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (ClearingDocument, error) {
	q := clearingSelect + ` where d.tenant_id = $1 and d.id = $2`
	if lock {
		q += ` for update of d`
	}
	d, err := scanClearing(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, clearingNotFound()
	}
	return d, err
}

// GetClearing returns one clearing document with the items it holds.
func (s *Postgres) GetClearing(ctx context.Context, id string) (ClearingDocument, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return ClearingDocument{}, err
	}
	var out ClearingDocument
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if out, err = loadClearing(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, openItemSelect+` where i.tenant_id = $1 and i.id = any($2::uuid[]) order by i.seq`, p.TenantID, out.ItemIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Items = []OpenItem{}
		for rows.Next() {
			it, err := scanOpenItem(rows, nil)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, it)
		}
		return rows.Err()
	})
	return out, err
}

// ListClearings returns the clearing documents of a ledger, newest first, one keyset page (200 rows when
// no limit is asked; Limit+1 rows are returned so the caller can see another page exists).
func (s *Postgres) ListClearings(ctx context.Context, ledgerID, status string, page Page) ([]ClearingDocument, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []ClearingDocument{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, clearingSelect+` where d.tenant_id = $1 and d.ledger_id = $2 and ($3 = '' or d.status = $3)
			and (cardinality($5::text[]) = 0 or (d.created_at, d.id) < ($5[1]::timestamptz, $5[2]::uuid))
			order by d.created_at desc, d.id desc limit $4`, p.TenantID, ledgerID, status, page.pageLimit(200), page.keys())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanClearing(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// ResetClearing resets a clearing document as a whole (BR-11.13) with a required
// reason code: its items are open again (reset_open), the difference entry is
// reversed with the reason code
// and its two open items are voided, and a residual item is voided, which needs it
// to be still open. Resetting a reset document with the same Idempotency-Key
// returns it; with another key it is 409 LEDGER_CLEARING_ALREADY_RESET.
func (s *Postgres) ResetClearing(ctx context.Context, in ResetInput) (ClearingDocument, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return ClearingDocument{}, err
	}
	var out ClearingDocument
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		doc, err := loadClearing(ctx, tx, p.TenantID, in.ID, false)
		if err != nil {
			return err
		}
		ledger, err := loadLedger(ctx, tx, p.TenantID, doc.LedgerID, true)
		if err != nil {
			return err
		}
		if doc, err = loadClearing(ctx, tx, p.TenantID, in.ID, true); err != nil {
			return err
		}
		if doc.Status == "reset" {
			if doc.resetKey == in.Key {
				out = doc
				return nil
			}
			return httpx.NewProblem(http.StatusConflict, CodeClearingAlreadyReset, "the clearing document was already reset")
		}
		// Every reset names an active ledger reason code (FR-11.19, FR-11.15), whether or
		// not it reverses a difference entry; the reversal applies the ledger's own rules too.
		reason, err := loadReason(ctx, tx, p.TenantID, in.ReasonCode)
		if err != nil {
			return err
		}
		if _, v := domain.CheckReversal(domain.ReversalPolicy{ReasonRequired: true, NegativeEnabled: true}, reason,
			domain.ReversalRequest{ReasonCode: in.ReasonCode, ReasonComment: in.Comment, OriginalDate: in.Today, ReversalDate: in.Today}); len(v) > 0 {
			return &domain.RejectedError{Violations: v}
		}
		if doc.ResidualItemID != nil {
			var status string
			if err := tx.QueryRow(ctx, `select status from open_item where tenant_id = $1 and id = $2 for update`, p.TenantID, *doc.ResidualItemID).Scan(&status); err != nil {
				return err
			}
			if status != ItemOpen && status != ItemResetOpen {
				return httpx.NewProblem(http.StatusConflict, CodeClearingResidualUsed,
					"the residual item of this clearing is already cleared; reset that clearing first")
			}
		}
		var accountID string
		if err := tx.QueryRow(ctx, `select main_account_id::text from clearing_document where tenant_id = $1 and id = $2`, p.TenantID, doc.ID).Scan(&accountID); err != nil {
			return err
		}
		voided := []string{}
		if doc.DifferenceEntryID != nil {
			diffItem, err := entryItem(ctx, tx, p.TenantID, *doc.DifferenceEntryID, accountID)
			if err != nil {
				return err
			}
			key := "clearing-reset:" + doc.ID
			hash := sha256.Sum256([]byte(key))
			res, err := reverseEntryTx(ctx, tx, p.TenantID, ReverseInput{EntryID: *doc.DifferenceEntryID, ReasonCode: in.ReasonCode,
				ReasonComment: in.Comment, Key: key, Hash: hash[:], Actor: p.UserID, TraceID: in.TraceID, Today: in.Today, CanPostLedgerSpecific: true,
				clearingReset: true})
			if err != nil {
				return err
			}
			reversalItem, err := entryItem(ctx, tx, p.TenantID, res.Entries[0].ID, accountID)
			if err != nil {
				return err
			}
			voided = append(voided, diffItem, reversalItem)
		}
		if doc.ResidualItemID != nil {
			voided = append(voided, *doc.ResidualItemID)
		}
		if len(voided) > 0 {
			if _, err := tx.Exec(ctx, `update open_item set status = 'voided' where tenant_id = $1 and id = any($2::uuid[])`, p.TenantID, voided); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update open_item set status = 'reset_open', clearing_document_id = null
			where tenant_id = $1 and id = any($2::uuid[])`, p.TenantID, doc.ItemIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update clearing_document set status = 'reset', reset_reason_code = $3, reset_comment = nullif($4, ''),
				reset_key = $5, reset_by = $6, reset_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, doc.ID, in.ReasonCode, in.Comment, in.Key, p.UserID); err != nil {
			return err
		}
		if err := enqueueFor(ctx, tx, clearingAggregate, doc.ID, ledgerevents.OpenItemCleared, p.TenantID, ledgerevents.OpenItemClearedData{
			ClearingDocumentID: doc.ID, LedgerID: ledger.ID, Account: doc.Account, Mode: doc.Mode, ItemIDs: doc.ItemIDs,
			DifferenceAmount: doc.DifferenceAmount, DifferenceEntryID: deref(doc.DifferenceEntryID), ResidualItemID: deref(doc.ResidualItemID),
			Status: "reset", ReasonCode: in.ReasonCode, Actor: p.UserID,
		}); err != nil {
			return err
		}
		out, err = loadClearing(ctx, tx, p.TenantID, doc.ID, false)
		return err
	})
	return out, idempotencyRace(err)
}
