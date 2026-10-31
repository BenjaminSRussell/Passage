package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// errDryRun rolls back a validation run that succeeded.
var errDryRun = errors.New("inventory: journal validation dry run")

// ValidateJournal is the :validate dry run (FR-16.6): it runs the posting of a
// DRAFT, SUBMITTED or APPROVED journal in a transaction that is always rolled
// back, so it applies exactly the checks a post would (lines, offset account,
// negative policy and vendor-owned quantity under the row locks, serial
// uniqueness, currency) and writes nothing. It returns the journal unchanged
// when the post would succeed and the post's problem otherwise. The checks
// hold for the stock at the time of the call; a later post re-checks them.
func (s *Postgres) ValidateJournal(ctx context.Context, id string, p PostParams) (domain.Journal, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := loadJournal(ctx, tx, id, true)
		if err != nil {
			return err
		}
		out = j
		switch j.Status {
		case domain.JournalDraft, domain.JournalSubmitted, domain.JournalApproved:
		default:
			return problem(http.StatusConflict, CodeJournalState, "a "+string(j.Status)+" journal cannot be validated")
		}
		if err := s.postLoaded(ctx, tx, tp.TenantID, j, p, ""); err != nil {
			return err
		}
		return errDryRun
	})
	if errors.Is(err, errDryRun) {
		return out, nil
	}
	return domain.Journal{}, err
}

// TransitionJournal moves a journal through the approval workflow of BR-16.11:
// DRAFT -> SUBMITTED -> APPROVED or REJECTED, REJECTED -> DRAFT (reopen), and
// DRAFT or REJECTED -> CANCELLED. POSTING and POSTED are reached only by
// :post. The version must match (If-Match), so a repeated or stale request
// cannot move the journal twice. Submitting re-runs the line rules, so a
// journal that cannot post does not enter approval, and a reversal draft
// cannot be cancelled (it is its original's one reversal).
func (s *Postgres) TransitionJournal(ctx context.Context, id string, version int, to domain.JournalStatus) (domain.Journal, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := loadJournal(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if j.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "journal changed since it was read; reread and retry")
		}
		if to == domain.JournalPosting || to == domain.JournalPosted || !domain.CanMoveJournal(j.Status, to) {
			return problem(http.StatusConflict, CodeJournalState, fmt.Sprintf("a %s journal cannot move to %s", j.Status, to))
		}
		if j.Type == domain.JournalCount {
			return problem(http.StatusConflict, CodeJournalState, "a COUNT journal is posted by its count sheet")
		}
		if to == domain.JournalCancelled && j.ReversesJournalID != "" {
			return problem(http.StatusConflict, CodeJournalState, "a reversal draft cannot be cancelled; post it or leave it as the original's reversal")
		}
		if to == domain.JournalSubmitted {
			if err := domain.ValidateJournal(j, s.rules); err != nil {
				return domainProblem(err)
			}
		}
		if _, err := tx.Exec(ctx, `update inventory_journal set status = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, tp.TenantID, id, string(to)); err != nil {
			return err
		}
		out, err = loadJournal(ctx, tx, id, false)
		return err
	})
	return out, err
}
