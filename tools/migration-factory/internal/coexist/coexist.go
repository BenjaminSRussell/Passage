// Package coexist drives a legacy coexistence adapter entity by entity (FR-49.7, D6): it pulls an
// entity kind's legacy changes, routes them under the kind's cutover flag and the legacy
// cross-reference (BR-49.10), and suspends or resumes the adapter's flow when the flag flips.
package coexist

import (
	"context"
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// Flags reads the cutover flags; *store.Postgres implements it.
type Flags interface {
	GetCoexistence(ctx context.Context, system domain.SourceSystem, company, entity string) (domain.CoexistFlag, error)
}

// Xrefs reads the legacy cross-reference; *store.Postgres implements it.
type Xrefs interface {
	KnownXrefs(ctx context.Context, system domain.SourceSystem, company, table string, legacyIDs []string) (map[string]string, error)
}

// Pull reads the adapter's changes of one entity kind since the given time and routes them. The
// caller applies the CREATE and MATCH decisions through the owning context's API and reports the
// REFUSED ones to the cutover manager.
func Pull(ctx context.Context, a domain.CoexistenceAdapter, flags Flags, xrefs Xrefs, company, entity string, since time.Time) ([]domain.RoutedRecord, error) {
	flag, err := flags.GetCoexistence(ctx, a.System(), company, entity)
	if err != nil {
		return nil, err
	}
	in, err := a.Pull(ctx, company, entity, since)
	if err != nil {
		return nil, fmt.Errorf("coexistence pull %s %s %s: %w", a.System(), company, entity, err)
	}
	byTable := map[string][]string{}
	for _, r := range in {
		byTable[r.SourceTable] = append(byTable[r.SourceTable], r.LegacyID)
	}
	known := map[string]string{}
	for table, ids := range byTable {
		got, err := xrefs.KnownXrefs(ctx, a.System(), company, table, ids)
		if err != nil {
			return nil, err
		}
		for k, v := range got {
			known[k] = v
		}
	}
	return domain.RouteInbound(flag, in, known), nil
}

// Apply makes the adapter follow a flag that just changed: a kind cut over to Passage stops flowing
// from the legacy system, and a kind flipped back to LEGACY (a rollback) flows again.
func Apply(ctx context.Context, a domain.CoexistenceAdapter, flag domain.CoexistFlag) error {
	if flag.SourceSystem != a.System() {
		return fmt.Errorf("%w: flag is for %s, the adapter serves %s", domain.ErrInvalid, flag.SourceSystem, a.System())
	}
	if flag.Mode == domain.CoexistPassage {
		return a.Suspend(ctx, flag.Company, flag.Entity)
	}
	return a.Resume(ctx, flag.Company, flag.Entity)
}
