// Package verify checks the reporting read model of a ledger against the
// ledger's own trial balance (FR-15.1, job reporting-balance-verify). The API
// runs it on request; the nightly job runner calls the same Run.
package verify

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/ledgerbalances"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// CodeLedgerUnavailable is a slice-local code (not in the PRD catalog yet): the
// ledger could not be read, so nothing was compared and nothing is stored.
const CodeLedgerUnavailable = "REPORT-0036"

// Store is what a verification needs from the reporting store.
type Store interface {
	VerifyReplay(ctx context.Context, key, requestHash string) (store.Verification, bool, error)
	VerifySnapshot(ctx context.Context, ledgerID, requestedEntity string) (store.VerifySnapshot, error)
	Mark(ctx context.Context, ledgerID string) (hwm int64, pending int, err error)
	RecordVerification(ctx context.Context, spec store.VerificationSpec) (store.Verification, error)
	ListVerifications(ctx context.Context, ledgerID string, limit int) ([]store.VerificationSummary, error)
	GetVerification(ctx context.Context, id string) (store.Verification, error)
}

// Ledger reads the ledger's net per account.
type Ledger interface {
	Nets(ctx context.Context, ledgerID, legalEntityID string) (map[string]decimal.Decimal, error)
}

// Service runs and serves verifications.
type Service struct {
	Store  Store
	Ledger Ledger
}

// Request names the ledger to check.
type Request struct {
	LedgerID       string
	LegalEntityID  string
	RequestedBy    string
	IdempotencyKey string
	RequestHash    string
}

// Run compares the read model with the ledger and stores the result. A retried
// Idempotency-Key returns the stored check without reading the ledger again.
// The read model's mark is read before and after the ledger call; a mark that
// moved, or entries waiting ahead of it, make the check inconclusive rather
// than a mismatch.
//
// Failures reading the ledger: a ledger 404 (it does not know the ledger in the
// owner's legal entity) is 422; a token finreport cannot sign (the service grant
// is not seeded, or the ledger has no single owning entity to sign for) is 503,
// like a missing LEDGER_URL, because retrying does not help until it is
// configured; any other failure (transport, 401, 403, 429, 5xx, a malformed or
// filtered answer) is 502. The cause is logged, never returned to the caller.
func (s *Service) Run(ctx context.Context, req Request) (store.Verification, error) {
	if s.Ledger == nil {
		return store.Verification{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeLedgerUnavailable,
			"balance verification is not configured: LEDGER_URL and the service signing key are not set")
	}
	if v, found, err := s.Store.VerifyReplay(ctx, req.IdempotencyKey, req.RequestHash); err != nil || found {
		return v, err
	}
	snap, err := s.Store.VerifySnapshot(ctx, req.LedgerID, req.LegalEntityID)
	if err != nil {
		return store.Verification{}, err
	}
	ledger, err := s.Ledger.Nets(ctx, req.LedgerID, snap.LegalEntityID)
	if err != nil {
		slog.WarnContext(ctx, "balance verification: ledger trial balance not read", "ledger_id", req.LedgerID, "error", err)
		var le *ledgerbalances.Error
		switch {
		case errors.As(err, &le) && le.Status == http.StatusNotFound:
			return store.Verification{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerUnavailable,
				"the ledger does not know ledger "+req.LedgerID+" in the legal entity that owns its reporting data")
		case errors.Is(err, ledgerbalances.ErrSign):
			return store.Verification{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeLedgerUnavailable,
				"balance verification cannot read the ledger as the service for this ledger's legal entity")
		}
		return store.Verification{}, httpx.NewProblem(http.StatusBadGateway, CodeLedgerUnavailable,
			"the ledger trial balance could not be read; nothing was stored, check again later")
	}
	after, pending, err := s.Store.Mark(ctx, req.LedgerID)
	if err != nil {
		return store.Verification{}, err
	}
	return s.Store.RecordVerification(ctx, store.VerificationSpec{
		LedgerID: req.LedgerID, LegalEntityID: snap.LegalEntityID, Result: domain.Verify(snap.Nets, ledger, snap.HWM, after, max(snap.EntriesPending, pending)),
		RequestedBy: req.RequestedBy, IdempotencyKey: req.IdempotencyKey, RequestHash: req.RequestHash,
	})
}

// List returns the stored checks of a ledger (all ledgers when empty), newest
// first, without their differing accounts.
func (s *Service) List(ctx context.Context, ledgerID string, limit int) ([]store.VerificationSummary, error) {
	return s.Store.ListVerifications(ctx, ledgerID, limit)
}

// Get returns one stored check.
func (s *Service) Get(ctx context.Context, id string) (store.Verification, error) {
	return s.Store.GetVerification(ctx, id)
}
