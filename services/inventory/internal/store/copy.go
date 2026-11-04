package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// CopyJournal creates a DRAFT journal from an existing journal of any status:
// header and lines are copied in one transaction, the source is only read. The
// copy goes through the same checks as a new journal (line rules and limit, an
// active journal name that still allows the type). A repeated Idempotency-Key
// returns the copy made the first time with inserted=false; the same key with
// another posting date is idempotency_key_reuse (another source is another
// key). A zero postingDate keeps the source's posting date. An
// OWNERSHIP_CHANGE source needs the consignment duty, as creating one does
// (mayOwnershipChange, decided by the caller); without it the copy is
// AUTH-0003.
func (s *Postgres) CopyJournal(ctx context.Context, sourceID string, postingDate time.Time, idempotencyKey string, mayOwnershipChange bool) (domain.Journal, bool, error) {
	tp, _ := tenant.FromContext(ctx)
	// The stored key names the source, so one caller key cannot copy two journals.
	stored := "copy:" + sourceID + ":" + idempotencyKey
	var out domain.Journal
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "journal", tp.TenantID, stored); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(ctx, `select id::text from inventory_journal where tenant_id = $1 and idempotency_key = $2`, tp.TenantID, stored).Scan(&existing)
		switch {
		case err == nil:
			out, err = loadJournal(ctx, tx, existing, false)
			if err == nil && !postingDate.IsZero() && !out.PostingDate.Equal(postingDate) {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		src, err := loadJournal(ctx, tx, sourceID, false)
		if err != nil {
			return err
		}
		if src.Type == domain.JournalOwnershipChange && !mayOwnershipChange {
			return problem(http.StatusForbidden, CodeOutOfScope, "copying an ownership change journal needs the duty inventory.consignment.write")
		}
		if postingDate.IsZero() {
			postingDate = src.PostingDate
		}
		cp, err := domain.CopyJournal(src, ids.New(), postingDate)
		if err != nil {
			return domainProblem(err)
		}
		var nameID *string
		if cp.JournalNameID != "" {
			n, err := loadJournalName(ctx, tx, cp.JournalNameID, false)
			if err != nil {
				return err
			}
			if err := n.CheckForJournal(cp.LegalEntityID, cp.Type); err != nil {
				return domainProblem(err)
			}
			nameID = &cp.JournalNameID
		}
		if err := domain.ValidateJournal(cp, s.rules); err != nil {
			return domainProblem(err)
		}
		var offset *string
		if cp.OffsetAccountID != "" {
			offset = &cp.OffsetAccountID
		}
		if _, err := tx.Exec(ctx, `insert into inventory_journal (tenant_id, legal_entity_id, id, journal_type, status, posting_date, offset_account_id, currency, idempotency_key, journal_name_id)
			values ($1, $2, $3, $4, 'DRAFT', $5, $6, $7, $8, $9)`,
			tp.TenantID, cp.LegalEntityID, cp.ID, string(cp.Type), cp.PostingDate.Format(dateLayout), offset, cp.Currency, stored, nameID); err != nil {
			if isUniqueOn(err, "idempotency_key") {
				// The key is held by a copy RLS hides (a legal entity outside the caller's scope).
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			return scopeProblem(err)
		}
		sets := make([]domain.Dimensions, 0, 2*len(cp.Lines))
		for _, l := range cp.Lines {
			sets = append(sets, l.Dims)
			if cp.Type == domain.JournalTransfer {
				sets = append(sets, l.ToDims)
			}
		}
		if err := upsertDimensionSets(ctx, tx, tp.TenantID, sets...); err != nil {
			return err
		}
		for _, l := range cp.Lines {
			hash, raw, err := upsertDimensionSet(ctx, tx, tp.TenantID, l.Dims)
			if err != nil {
				return err
			}
			var toHash, toRaw []byte
			if cp.Type == domain.JournalTransfer {
				if toHash, toRaw, err = upsertDimensionSet(ctx, tx, tp.TenantID, l.ToDims); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `insert into inventory_journal_line (tenant_id, journal_id, id, line_no, product_id, dimension_set_hash, dims, qty, entered_qty,
				entered_unit, cost_price, to_dimension_set_hash, to_dims)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				tp.TenantID, cp.ID, ids.New(), l.LineNo, l.ProductID, hash, raw, l.Qty.String(), l.EnteredQty.String(), l.EnteredUnit, l.CostPrice.String(), toHash, toRaw); err != nil {
				return err
			}
		}
		inserted = true
		out, err = loadJournal(ctx, tx, cp.ID, false)
		return err
	})
	if err != nil {
		return domain.Journal{}, false, err
	}
	return out, inserted, nil
}
