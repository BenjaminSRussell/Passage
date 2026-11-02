package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// ReverseJournal creates the DRAFT reversal of a POSTED journal (BR-16.12):
// same type, every line negated and linked to the line it reverses, the
// original untouched. The reversal is posted through the normal post (duty and
// If-Match), where each line takes the negated value of the transaction it
// corrects. A journal has at most one reversal: a repeated request returns it
// with inserted=false. The original is locked, so concurrent requests
// serialize.
func (s *Postgres) ReverseJournal(ctx context.Context, id string, postingDate time.Time) (domain.Journal, bool, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Journal
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		orig, err := loadJournal(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if orig.ReversedByJournalID != "" {
			out, err = loadJournal(ctx, tx, orig.ReversedByJournalID, false)
			return err
		}
		if orig.Status != domain.JournalPosted {
			return problem(http.StatusConflict, CodeJournalState, "only a POSTED journal can be reversed; this one is "+string(orig.Status))
		}
		rev, err := domain.ReverseJournal(orig, ids.New(), postingDate)
		if err != nil {
			return domainProblem(err)
		}
		if err := domain.ValidateJournal(rev, s.rules); err != nil {
			return domainProblem(err)
		}
		var offset *string
		if rev.OffsetAccountID != "" {
			offset = &rev.OffsetAccountID
		}
		if _, err := tx.Exec(ctx, `insert into inventory_journal
			(tenant_id, legal_entity_id, id, journal_type, status, posting_date, offset_account_id, currency, idempotency_key, reverses_journal_id)
			values ($1, $2, $3, $4, 'DRAFT', $5, $6, $7, $8, $9)`,
			tp.TenantID, rev.LegalEntityID, rev.ID, string(rev.Type), rev.PostingDate.Format(dateLayout),
			offset, rev.Currency, "reverse:"+id, id); err != nil {
			return scopeProblem(err)
		}
		sets := make([]domain.Dimensions, len(rev.Lines))
		for i, l := range rev.Lines {
			sets[i] = l.Dims
		}
		if err := upsertDimensionSets(ctx, tx, tp.TenantID, sets...); err != nil {
			return err
		}
		for _, l := range rev.Lines {
			hash, raw, err := upsertDimensionSet(ctx, tx, tp.TenantID, l.Dims)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into inventory_journal_line (tenant_id, journal_id, id, line_no, reverses_line_id, product_id,
				dimension_set_hash, dims, qty, entered_qty, entered_unit, cost_price)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
				tp.TenantID, rev.ID, ids.New(), l.LineNo, l.ReversesLineID, l.ProductID, hash, raw, l.Qty.String(),
				l.EnteredQty.String(), l.EnteredUnit, l.CostPrice.String()); err != nil {
				return err
			}
		}
		inserted = true
		out, err = loadJournal(ctx, tx, rev.ID, false)
		return err
	})
	if err != nil {
		return domain.Journal{}, false, err
	}
	return out, inserted, nil
}
