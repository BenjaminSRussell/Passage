package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// Row outcomes of an opening-item import.
const (
	OutcomeCreated   = "CREATED"
	OutcomeUpdated   = "UPDATED"
	OutcomeUnchanged = "UNCHANGED"
	OutcomeRejected  = "REJECTED"
)

// OpeningItemInput is one row of an import. Reject is set by the caller for a row
// that failed validation, so it is reported at its index with the rest.
type OpeningItemInput struct {
	Row          domain.OpeningRow
	RejectCode   string
	RejectDetail string
}

// OpeningImport is a cutover snapshot of open items from one legacy system into
// one legal entity. Atomic stores nothing unless every row is accepted.
type OpeningImport struct {
	LegalEntityID string
	ImportSource  string
	Atomic        bool
	Items         []OpeningItemInput
}

// OpeningRowResult is the outcome of one row, in request order.
type OpeningRowResult struct {
	Index      int     `json:"index"`
	LegacyKey  string  `json:"legacyKey"`
	Outcome    string  `json:"outcome"`
	OpenItemID *string `json:"openItemId,omitempty"`
	Code       string  `json:"code,omitempty"`
	Detail     string  `json:"detail,omitempty"`
}

// OpeningImportResult reports every row. Committed is false when an atomic
// import was refused, in which case nothing was stored.
type OpeningImportResult struct {
	BatchID       string             `json:"batchId"`
	LegalEntityID string             `json:"legalEntityId"`
	ImportSource  string             `json:"importSource"`
	Atomic        bool               `json:"atomic"`
	Committed     bool               `json:"committed"`
	Created       int                `json:"created"`
	Updated       int                `json:"updated"`
	Unchanged     int                `json:"unchanged"`
	Rejected      int                `json:"rejected"`
	Results       []OpeningRowResult `json:"results"`
}

// OpeningImportSummary totals what one import source loaded into one legal
// entity and currency, for the reconciliation against the legacy ledger.
type OpeningImportSummary struct {
	ImportSource   string          `json:"importSource"`
	LegalEntityID  string          `json:"legalEntityId"`
	Currency       string          `json:"currency"`
	ItemCount      int             `json:"itemCount"`
	DebitCount     int             `json:"debitCount"`
	CreditCount    int             `json:"creditCount"`
	DebitOpen      decimal.Decimal `json:"debitOpen"`
	CreditOpen     decimal.Decimal `json:"creditOpen"`
	NetOpen        decimal.Decimal `json:"netOpen"`
	LastImportedAt *time.Time      `json:"lastImportedAt,omitempty"`
}

// rowReject is a per-row refusal that leaves the rest of the batch alone.
type rowReject struct{ code, detail string }

func (e *rowReject) Error() string { return e.code + ": " + e.detail }

func reject(code, format string, args ...any) error {
	return &rowReject{code: code, detail: fmt.Sprintf(format, args...)}
}

// ImportOpeningItems loads a cutover snapshot of open items. Each row is keyed by
// (import source, legacy key): a new key creates the item, a known key whose
// item is untouched in Passage is updated to the snapshot (an unchanged row
// writes nothing), and a known key whose item was settled, partly settled,
// cleared or deducted in Passage (ever, even if later reversed) is refused,
// because the snapshot's open amount no longer describes it, as is one with an
// active dispute or a promise to pay that still holds. A known key cannot change
// its legal entity, customer, currency, kind or side. Every row runs in its own savepoint, so a refused row
// never blocks the others, unless Atomic is set. One openitem.imported.v1 is
// written to the outbox in the same transaction when anything was stored.
func (s *Postgres) ImportOpeningItems(ctx context.Context, in OpeningImport) (OpeningImportResult, error) {
	p, _ := tenant.FromContext(ctx)
	out := OpeningImportResult{BatchID: ids.New(), LegalEntityID: in.LegalEntityID, ImportSource: in.ImportSource,
		Atomic: in.Atomic, Results: make([]OpeningRowResult, 0, len(in.Items))}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out.Created, out.Updated, out.Unchanged, out.Rejected, out.Results = 0, 0, 0, 0, out.Results[:0]
		var imported []rcvevents.OpenItemImportedRow
		for i, item := range in.Items {
			row := item.Row
			res := OpeningRowResult{Index: i, LegacyKey: row.LegacyKey}
			if item.RejectCode != "" {
				res.Outcome, res.Code, res.Detail = OutcomeRejected, item.RejectCode, item.RejectDetail
			} else {
				id, outcome, err := importOpeningRow(ctx, tx, p.TenantID, in, row)
				var rr *rowReject
				switch {
				case errors.As(err, &rr):
					res.Outcome, res.Code, res.Detail = OutcomeRejected, rr.code, rr.detail
				case err != nil:
					return err
				default:
					res.Outcome, res.OpenItemID = outcome, &id
					if outcome != OutcomeUnchanged {
						amount, open := row.Signed()
						imported = append(imported, rcvevents.OpenItemImportedRow{OpenItemID: id, LegacyKey: row.LegacyKey,
							Outcome: outcome, CustomerID: row.CustomerID, Currency: row.Currency, Amount: amount, OpenAmount: open})
					}
				}
			}
			switch res.Outcome {
			case OutcomeCreated:
				out.Created++
			case OutcomeUpdated:
				out.Updated++
			case OutcomeUnchanged:
				out.Unchanged++
			default:
				out.Rejected++
			}
			out.Results = append(out.Results, res)
		}
		if in.Atomic && out.Rejected > 0 {
			// Roll the whole batch back; the rows are still reported.
			return errAtomicRefused
		}
		out.Committed = true
		if len(imported) == 0 {
			return nil
		}
		return enqueue(ctx, tx, "openitem", out.BatchID, rcvevents.OpenItemsImported, p.TenantID, rcvevents.OpenItemsImportedData{
			BatchID: out.BatchID, LegalEntityID: in.LegalEntityID, ImportSource: in.ImportSource, Created: out.Created,
			Updated: out.Updated, Unchanged: out.Unchanged, Rejected: out.Rejected, Items: imported})
	})
	if errors.Is(err, errAtomicRefused) {
		out.Committed = false
		// Created/updated rows were rolled back with the rest.
		for i, r := range out.Results {
			if r.Outcome != OutcomeRejected {
				out.Results[i] = OpeningRowResult{Index: r.Index, LegacyKey: r.LegacyKey, Outcome: OutcomeRejected,
					Code: domain.CodeOpeningRejected, Detail: "not stored: another row of this atomic import was refused"}
			}
		}
		out.Created, out.Updated, out.Unchanged = 0, 0, 0
		out.Rejected = len(out.Results)
		return out, nil
	}
	if err != nil {
		return OpeningImportResult{}, mapWriteErr(err)
	}
	return out, nil
}

var errAtomicRefused = errors.New("atomic opening-item import refused")

// openItemImportKey is the unique (tenant, import source, legacy key) index of
// migration 0011.
const openItemImportKey = "open_item_import_key"

// importOpeningRow stores one row inside a savepoint and returns the item id and
// outcome, or a *rowReject. A database error other than a document-number or
// legacy-key clash aborts the batch.
func importOpeningRow(ctx context.Context, tx pgx.Tx, tenantID string, in OpeningImport, row domain.OpeningRow) (string, string, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	id, outcome, err := upsertOpening(ctx, sp, tenantID, in, row)
	if err != nil {
		_ = sp.Rollback(ctx)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			if pgErr.ConstraintName == openItemImportKey {
				// The key was loaded into a legal entity the caller cannot see
				// (row-level security hid it from the lookup): the legal entity
				// of a loaded item cannot change.
				return "", "", reject(domain.CodeOpeningIdentity,
					"legacy key %s of %s was already loaded into another legal entity; the identity of a loaded item cannot change",
					row.LegacyKey, in.ImportSource)
			}
			return "", "", reject(domain.CodeOpeningDocumentExists,
				"document number %s already exists in this legal entity under another item", row.DocumentNumber)
		}
		return "", "", err
	}
	if err := sp.Commit(ctx); err != nil {
		return "", "", err
	}
	return id, outcome, nil
}

func upsertOpening(ctx context.Context, tx pgx.Tx, tenantID string, in OpeningImport, row domain.OpeningRow) (string, string, error) {
	amount, open := row.Signed()
	sourceType := row.Kind
	status := ItemStatusFor(open, amount)
	var cur OpenItem
	var importedOpen string
	err := tx.QueryRow(ctx, `select `+openItemColumns+`, imported_open_amount::text from open_item
		where import_source = $1 and legacy_key = $2 for update`, in.ImportSource, row.LegacyKey).Scan(
		&cur.ID, &cur.LegalEntityID, &cur.CustomerID, &cur.SourceType, &cur.DocumentNumber, &cur.DocumentDate, &cur.DueDate,
		&cur.Currency, new(string), new(string), &cur.CashDiscountDate, new(string), &cur.DunningLevel, &cur.DunningBlock, &cur.Status,
		&cur.Version, &cur.ImportSource, &cur.LegacyKey, &importedOpen)
	if errors.Is(err, pgx.ErrNoRows) {
		id := ids.New()
		_, err := tx.Exec(ctx, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number,
			document_date, due_date, currency, amount, open_amount, cash_discount_date, cash_discount_amount, status,
			import_source, legacy_key, imported_open_amount, imported_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $11, now())`,
			tenantID, id, in.LegalEntityID, row.CustomerID, sourceType, row.DocumentNumber, row.DocumentDate, row.DueDate,
			row.Currency, amount.String(), open.String(), row.CashDiscountDate, row.CashDiscountAmount.String(), status,
			in.ImportSource, row.LegacyKey)
		return id, OutcomeCreated, err
	}
	if err != nil {
		return "", "", err
	}
	if cur.LegalEntityID != in.LegalEntityID || cur.CustomerID != row.CustomerID || cur.Currency != row.Currency || cur.SourceType != sourceType {
		return "", "", reject(domain.CodeOpeningIdentity,
			"legacy key %s was loaded as %s %s for another legal entity, customer or currency; the identity of a loaded item cannot change",
			row.LegacyKey, cur.SourceType, cur.DocumentNumber)
	}
	curOpen, curAmount, err := currentAmounts(ctx, tx, cur.ID)
	if err != nil {
		return "", "", err
	}
	if curAmount.IsNegative() != amount.IsNegative() {
		return "", "", reject(domain.CodeOpeningIdentity,
			"legacy key %s was loaded as a %s item (%s); the side of a loaded item cannot change",
			row.LegacyKey, sideOf(curAmount), cur.DocumentNumber)
	}
	// A row that matches the item writes nothing, whatever Passage has done with it since.
	if curAmount.Equal(amount) && curOpen.Equal(open) && cur.DocumentNumber == row.DocumentNumber && cur.DocumentDate == row.DocumentDate &&
		cur.DueDate == row.DueDate && eqStrPtr(cur.CashDiscountDate, row.CashDiscountDate) && sameDiscount(ctx, tx, cur.ID, row.CashDiscountAmount) {
		return cur.ID, OutcomeUnchanged, nil
	}
	loadedOpen, err := decimal.NewFromString(importedOpen)
	if err != nil {
		return "", "", err
	}
	worked, err := workedInPassage(ctx, tx, cur.ID)
	if err != nil {
		return "", "", err
	}
	if (cur.Status != "OPEN" && cur.Status != "PARTIALLY_SETTLED") || !curOpen.Equal(loadedOpen) || worked {
		return "", "", reject(domain.CodeOpeningSettled,
			"%s was %s in Passage (open %s, loaded as %s); it is no longer the legacy item, so the snapshot cannot overwrite it",
			cur.DocumentNumber, settledWord(cur.Status, worked), curOpen.String(), loadedOpen.String())
	}
	collecting, err := inCollection(ctx, tx, cur.ID)
	if err != nil {
		return "", "", err
	}
	if collecting != "" {
		return "", "", reject(domain.CodeOpeningInCollection,
			"%s has %s in Passage; the snapshot cannot overwrite an item a collector is working", cur.DocumentNumber, collecting)
	}
	_, err = tx.Exec(ctx, `update open_item set document_number = $3, document_date = $4, due_date = $5, amount = $6::numeric,
		open_amount = $7::numeric, cash_discount_date = $8, cash_discount_amount = $9::numeric, status = $10,
		imported_open_amount = $7::numeric, imported_at = now(), version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`, tenantID, cur.ID, row.DocumentNumber, row.DocumentDate, row.DueDate, amount.String(),
		open.String(), row.CashDiscountDate, row.CashDiscountAmount.String(), status)
	return cur.ID, OutcomeUpdated, err
}

func settledWord(status string, worked bool) string {
	switch {
	case status == "SETTLED":
		return "settled"
	case status == "REVERSED":
		return "reversed"
	case worked:
		return "paid, cleared or deducted"
	default:
		return "partly settled or changed"
	}
}

func sideOf(amount decimal.Decimal) string {
	if amount.IsNegative() {
		return "CREDIT"
	}
	return "DEBIT"
}

// workedInPassage reports whether a payment was ever applied to the item, a down
// payment cleared against it or a deduction taken from it, even if that was later
// reversed and the open amount is back where the import left it.
func workedInPassage(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var worked bool
	err := tx.QueryRow(ctx, `select exists(select 1 from settlement where open_item_id = $1)
		or exists(select 1 from down_payment_clearing where open_item_id = $1)
		or exists(select 1 from credit_settlement where credit_item_id = $1 or debit_item_id = $1)
		or exists(select 1 from deduction where open_item_id = $1)`, id).Scan(&worked)
	return worked, err
}

// inCollection names an active dispute (OPEN, UNDER_REVIEW) or a promise to pay
// that still holds on the item, or returns "".
func inCollection(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var dispute, promise bool
	err := tx.QueryRow(ctx, `select exists(select 1 from dispute where open_item_id = $1 and status in ('OPEN', 'UNDER_REVIEW')),
		exists(select 1 from promise_to_pay where open_item_id = $1 and promise_date + $2::int >= current_date)`,
		id, domain.PromiseGraceDays).Scan(&dispute, &promise)
	switch {
	case err != nil:
		return "", err
	case dispute:
		return "an active dispute", nil
	case promise:
		return "a promise to pay that still holds", nil
	}
	return "", nil
}

func currentAmounts(ctx context.Context, tx pgx.Tx, id string) (open, amount decimal.Decimal, err error) {
	var o, a string
	if err = tx.QueryRow(ctx, `select open_amount::text, amount::text from open_item where id = $1`, id).Scan(&o, &a); err != nil {
		return
	}
	if open, err = decimal.NewFromString(o); err != nil {
		return
	}
	amount, err = decimal.NewFromString(a)
	return
}

func sameDiscount(ctx context.Context, tx pgx.Tx, id string, want decimal.Decimal) bool {
	var same bool
	if err := tx.QueryRow(ctx, `select cash_discount_amount = $2::numeric from open_item where id = $1`, id, want.String()).Scan(&same); err != nil {
		return false
	}
	return same
}

// ListOpeningImports totals the imported items still in Passage per import
// source, legal entity and currency (reconciliation against the legacy ledger).
func (s *Postgres) ListOpeningImports(ctx context.Context, limit int) ([]OpeningImportSummary, error) {
	out := []OpeningImportSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select import_source, legal_entity_id, currency, count(*), count(*) filter (where amount > 0),
			count(*) filter (where amount < 0), coalesce(sum(open_amount) filter (where amount > 0), 0)::text,
			coalesce(sum(open_amount) filter (where amount < 0), 0)::text, sum(open_amount)::text, max(imported_at)
			from open_item where import_source is not null
			group by import_source, legal_entity_id, currency
			order by max(imported_at) desc, import_source, currency limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r OpeningImportSummary
			var debit, credit, net string
			if err := rows.Scan(&r.ImportSource, &r.LegalEntityID, &r.Currency, &r.ItemCount, &r.DebitCount, &r.CreditCount,
				&debit, &credit, &net, &r.LastImportedAt); err != nil {
				return err
			}
			for _, f := range []struct {
				dst *decimal.Decimal
				src string
			}{{&r.DebitOpen, debit}, {&r.CreditOpen, credit}, {&r.NetOpen, net}} {
				if *f.dst, err = decimal.NewFromString(f.src); err != nil {
					return err
				}
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
