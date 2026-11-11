package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// dutyOpeningTargetRelease is api.DutyOpeningTargetRelease; the tenant-wide transaction checks it.
const dutyOpeningTargetRelease = "migration.opening-target.release"

// OpeningMapping is the legal entity and ledger a cutover plan assigns to a legacy company's opening
// journal (#549).
type OpeningMapping struct {
	SourceSystem  domain.SourceSystem `json:"sourceSystem"`
	SourceCompany string              `json:"sourceCompany"`
	PlanID        string              `json:"planId"`
	LegalEntityID string              `json:"legalEntityId"`
	LedgerID      string              `json:"ledgerId"`
	SetBy         string              `json:"setBy"`
	SetAt         time.Time           `json:"setAt"`
}

// OpeningRelease is the audit record of an administrator releasing a company's opening target.
type OpeningRelease struct {
	ReleaseID     string              `json:"releaseId"`
	SourceSystem  domain.SourceSystem `json:"sourceSystem"`
	SourceCompany string              `json:"sourceCompany"`
	LegalEntityID string              `json:"legalEntityId"`
	LedgerID      string              `json:"ledgerId"`
	Reason        string              `json:"reason"`
	ReleasedBy    string              `json:"releasedBy"`
	ReleasedAt    time.Time           `json:"releasedAt"`
}

func readOpeningMapping(ctx context.Context, tx pgx.Tx, system, company string) (*openingTarget, error) {
	var t openingTarget
	err := tx.QueryRow(ctx, `select target_legal_entity_id::text, ledger_id from gl_opening_mapping
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2`, system, company).Scan(&t.legalEntityID, &t.ledgerID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &t, err
}

// lockOpeningCompany serialises the writes that decide a company's opening target (the plan's mapping and the
// first posting's pin), so neither can be recorded against the other's stale read.
func lockOpeningCompany(ctx context.Context, tx pgx.Tx, system, company string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('gl-opening|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2, 0))`, system, company)
	return err
}

func openingTargetMapped(company string) error {
	return fmt.Errorf("%w: the cutover plan assigns the opening balances of %s to another legal entity or ledger", ErrOpeningTargetTaken, company)
}

// SetOpeningMapping records the legal entity and ledger of a legacy company's opening journal under a
// plan. It is refused once a posting or a pin names another target, so a mapping can be corrected only
// while nothing has gone to the ledger.
func (s *Postgres) SetOpeningMapping(ctx context.Context, planID string, system domain.SourceSystem, company, legalEntityID, ledgerID, by string) (OpeningMapping, error) {
	var out OpeningMapping
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from cutover_plan where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1`, planID).Scan(&one); err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		if err := lockOpeningCompany(ctx, tx, string(system), company); err != nil {
			return err
		}
		pin, err := readOpeningTarget(ctx, tx, string(system), company)
		if err != nil {
			return err
		}
		if pin != nil && (pin.legalEntityID != legalEntityID || pin.ledgerID != ledgerID) {
			return openingTargetTaken(company)
		}
		var posted int
		if err := tx.QueryRow(ctx, `select count(*) from gl_opening_posting where tenant_id = current_setting('passage.tenant_id', true)::uuid
			and source_system = $1 and source_company = $2 and (legal_entity_id <> $3 or ledger_id <> $4)`, string(system), company, legalEntityID, ledgerID).Scan(&posted); err != nil {
			return err
		}
		if posted > 0 {
			return openingTargetTaken(company)
		}
		return tx.QueryRow(ctx, `insert into gl_opening_mapping (tenant_id, source_system, source_company, plan_id, target_legal_entity_id, ledger_id, set_by)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, source_system, source_company) do update
			set plan_id = excluded.plan_id, target_legal_entity_id = excluded.target_legal_entity_id, ledger_id = excluded.ledger_id, set_by = excluded.set_by, set_at = now()
			returning source_system, source_company, plan_id::text, target_legal_entity_id::text, ledger_id, set_by, set_at`,
			string(system), company, planID, legalEntityID, ledgerID, by).
			Scan(&out.SourceSystem, &out.SourceCompany, &out.PlanID, &out.LegalEntityID, &out.LedgerID, &out.SetBy, &out.SetAt)
	})
	return out, err
}

// ListOpeningMappings lists the mappings recorded under a plan.
func (s *Postgres) ListOpeningMappings(ctx context.Context, planID string) ([]OpeningMapping, error) {
	var out []OpeningMapping
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select source_system, source_company, plan_id::text, target_legal_entity_id::text, ledger_id, set_by, set_at
			from gl_opening_mapping where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1
			order by source_system, source_company`, planID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (OpeningMapping, error) {
			var m OpeningMapping
			err := r.Scan(&m.SourceSystem, &m.SourceCompany, &m.PlanID, &m.LegalEntityID, &m.LedgerID, &m.SetBy, &m.SetAt)
			return m, err
		})
		return err
	})
	if out == nil {
		out = []OpeningMapping{}
	}
	return out, err
}

// ReleaseOpeningTarget drops a company's pinned opening target and records who did it and why. A pin whose
// company has a posting stays (409 through ErrTransition): the postings are the record of where the journals
// went and are corrected in the ledger by reversal.
func (s *Postgres) ReleaseOpeningTarget(ctx context.Context, system domain.SourceSystem, company, reason, by string) (OpeningRelease, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return OpeningRelease{}, fmt.Errorf("%w: reason is required", domain.ErrInvalid)
	}
	var out OpeningRelease
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return out, fmt.Errorf("no principal in context")
	}
	// Postings are scoped by legal entity, so the check that none exists must see every entity's: the
	// release needs a caller with no legal-entity claim and the tenant.administer duty.
	err := dbx.WithTenantWideAdminTx(ctx, s.pool, p, dutyOpeningTargetRelease, func(tx pgx.Tx) error {
		// Under the company's lock, like a posting call's lease: a posting that holds a lease may be between the ledger booking
		// a journal and recording it, so the pin must stay until it ends (or its lease expires).
		if err := lockOpeningCompany(ctx, tx, string(system), company); err != nil {
			return err
		}
		var posting int
		if err := tx.QueryRow(ctx, `select count(*) from gl_opening_posting_lease where tenant_id = current_setting('passage.tenant_id', true)::uuid
			and source_system = $1 and source_company = $2 and expires_at > now()`, string(system), company).Scan(&posting); err != nil {
			return err
		}
		if posting > 0 {
			return fmt.Errorf("%w: an opening journal of %s is being posted right now; release the target after it has finished (a posting that was cut off frees it within %d minutes)",
				domain.ErrTransition, company, int(postingLeaseTTL.Minutes()))
		}
		pin, err := readOpeningTarget(ctx, tx, string(system), company)
		if err != nil {
			return err
		}
		if pin == nil {
			return fmt.Errorf("%w: the opening balances of %s are not pinned to a legal entity", domain.ErrNotFound, company)
		}
		var posted int
		if err := tx.QueryRow(ctx, `select count(*) from gl_opening_posting where tenant_id = current_setting('passage.tenant_id', true)::uuid
			and source_system = $1 and source_company = $2`, string(system), company).Scan(&posted); err != nil {
			return err
		}
		if posted > 0 {
			return fmt.Errorf("%w: %s already has opening journals posted; the target cannot be released, reverse them in the ledger", domain.ErrTransition, company)
		}
		if _, err := tx.Exec(ctx, `delete from gl_opening_target where tenant_id = current_setting('passage.tenant_id', true)::uuid
			and source_system = $1 and source_company = $2`, string(system), company); err != nil {
			return err
		}
		out = OpeningRelease{ReleaseID: ids.New(), SourceSystem: system, SourceCompany: company, LegalEntityID: pin.legalEntityID, LedgerID: pin.ledgerID, Reason: reason, ReleasedBy: by}
		return tx.QueryRow(ctx, `insert into gl_opening_target_release (release_id, tenant_id, source_system, source_company, released_legal_entity_id, released_ledger_id, reason, released_by)
			values ($1, current_setting('passage.tenant_id', true)::uuid, $2, $3, $4, $5, $6, $7) returning released_at`,
			out.ReleaseID, string(system), company, pin.legalEntityID, pin.ledgerID, reason, by).Scan(&out.ReleasedAt)
	})
	return out, err
}
