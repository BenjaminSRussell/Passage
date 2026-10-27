package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	qevents "github.com/BenjaminSRussell/passage/services/quality/internal/events"
)

// maxCompositionNodes bounds one roll-up. The BOM depth limit lives in the domain.
const maxCompositionNodes = 500

// CompositionTree is an item's current composition and every item reachable
// through its declared parts, one current version each. Parts without a
// composition are leaves with nothing declared and are named in Undeclared.
type CompositionTree struct {
	RootVersion int
	Nodes       map[string]domain.Constituent
	Undeclared  []string
}

const listColumns = "id, code, version, name, kind, block_mode, regions, limits"

func scanList(row pgx.Row) (domain.ComplianceList, error) {
	var l domain.ComplianceList
	var regions, limits []byte
	if err := row.Scan(&l.ID, &l.Code, &l.Version, &l.Name, &l.Kind, &l.BlockMode, &regions, &limits); err != nil {
		return l, err
	}
	if err := json.Unmarshal(regions, &l.Regions); err != nil {
		return l, err
	}
	return l, json.Unmarshal(limits, &l.Limits)
}

func collectLists(rows pgx.Rows, err error) ([]domain.ComplianceList, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ComplianceList{}
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CreateComplianceList inserts the next version of the list code (FR-33.13) and
// writes compliancelist.created to the outbox. A replayed key returns the stored version.
func (s *Postgres) CreateComplianceList(ctx context.Context, l domain.ComplianceList, key string) (domain.ComplianceList, bool, error) {
	p, _ := tenant.FromContext(ctx)
	regions, err := json.Marshal(l.Regions)
	if err != nil {
		return l, false, err
	}
	limits, err := json.Marshal(l.Limits)
	if err != nil {
		return l, false, err
	}
	var out domain.ComplianceList
	var inserted bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			replay, err := scanList(tx.QueryRow(ctx, `select `+listColumns+` from quality_compliance_list where `+ownTenant+` and idempotency_key = $1`, key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		created, err := scanList(tx.QueryRow(ctx, `insert into quality_compliance_list (tenant_id, id, code, version, name, kind, block_mode, regions, limits, idempotency_key)
			values ($1, $2, $3, (select coalesce(max(version), 0) + 1 from quality_compliance_list where tenant_id = $1 and code = $3), $4, $5, $6, $7, $8, $9)
			returning `+listColumns,
			p.TenantID, ids.New(), l.Code, l.Name, l.Kind, l.BlockMode, string(regions), string(limits), keyArg(key)))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeListRace, "another version of this compliance list was created at the same time; retry")
		}
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "compliancelist", created.ID, qevents.ComplianceListCreated, p.TenantID, qevents.ComplianceListCreatedData{
			ListID: created.ID, Code: created.Code, Version: created.Version, Kind: created.Kind, BlockMode: created.BlockMode,
			Regions: created.Regions, CASCount: len(created.Limits),
		}); err != nil {
			return err
		}
		out, inserted = created, true
		return nil
	})
	return out, inserted, err
}

// ListComplianceLists returns list versions by code, newest version first.
func (s *Postgres) ListComplianceLists(ctx context.Context, limit int) ([]domain.ComplianceList, error) {
	var out []domain.ComplianceList
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = collectLists(tx.Query(ctx, `select `+listColumns+` from quality_compliance_list where `+ownTenant+`
			order by code, version desc limit $1`, limit))
		return err
	})
	return out, err
}

// CurrentComplianceLists returns the highest version of every list code: the
// versions an evaluation applies.
func (s *Postgres) CurrentComplianceLists(ctx context.Context) ([]domain.ComplianceList, error) {
	var out []domain.ComplianceList
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = collectLists(tx.Query(ctx, `select distinct on (code) `+listColumns+` from quality_compliance_list where `+ownTenant+`
			order by code, version desc`))
		return err
	})
	return out, err
}

const compositionColumns = "version, item_id, direct, parts"

func scanComposition(row pgx.Row) (domain.Composition, error) {
	var c domain.Composition
	var direct, parts []byte
	if err := row.Scan(&c.Version, &c.ItemID, &direct, &parts); err != nil {
		return c, err
	}
	if err := json.Unmarshal(direct, &c.Direct); err != nil {
		return c, err
	}
	return c, json.Unmarshal(parts, &c.Parts)
}

// SaveComposition appends a new version of an item's composition. A replayed key
// returns the stored version.
func (s *Postgres) SaveComposition(ctx context.Context, c domain.Constituent, key string) (domain.Composition, bool, error) {
	p, _ := tenant.FromContext(ctx)
	if c.Direct == nil {
		c.Direct = map[string]decimal.Decimal{}
	}
	if c.Parts == nil {
		c.Parts = []domain.MassPart{}
	}
	direct, err := json.Marshal(c.Direct)
	if err != nil {
		return domain.Composition{}, false, err
	}
	parts, err := json.Marshal(c.Parts)
	if err != nil {
		return domain.Composition{}, false, err
	}
	var out domain.Composition
	var inserted bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			replay, err := scanComposition(tx.QueryRow(ctx, `select `+compositionColumns+` from quality_item_composition where `+ownTenant+` and idempotency_key = $1`, key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		created, err := scanComposition(tx.QueryRow(ctx, `insert into quality_item_composition (tenant_id, id, item_id, version, direct, parts, idempotency_key)
			values ($1, $2, $3, (select coalesce(max(version), 0) + 1 from quality_item_composition where tenant_id = $1 and item_id = $3), $4, $5, $6)
			returning `+compositionColumns,
			p.TenantID, ids.New(), c.ItemID, string(direct), string(parts), keyArg(key)))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeCompositionRace, "another composition version was saved at the same time; retry")
		}
		if err != nil {
			return err
		}
		out, inserted = created, true
		return nil
	})
	return out, inserted, err
}

func currentComposition(ctx context.Context, tx pgx.Tx, itemID string) (domain.Composition, error) {
	return scanComposition(tx.QueryRow(ctx, `select `+compositionColumns+` from quality_item_composition
		where `+ownTenant+` and item_id = $1 order by version desc limit 1`, itemID))
}

// GetComposition returns the current (highest) composition version of an item.
func (s *Postgres) GetComposition(ctx context.Context, itemID string) (domain.Composition, error) {
	var out domain.Composition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := currentComposition(ctx, tx, itemID)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeCompositionMissing, "no composition is recorded for this item")
		}
		out = c
		return err
	})
	return out, err
}

// LoadCompositionTree reads root and every item reachable through its parts in one
// transaction. The root must have a composition (QM-0164); a tree of more than
// 500 items is refused (QM-0162). Cycles are left to the domain roll-up.
func (s *Postgres) LoadCompositionTree(ctx context.Context, root string) (CompositionTree, error) {
	tree := CompositionTree{Nodes: map[string]domain.Constituent{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		queue := []string{root}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if _, seen := tree.Nodes[id]; seen {
				continue
			}
			if len(tree.Nodes) >= maxCompositionNodes {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCompositionTooLarge, "the bill of materials has more than 500 items")
			}
			c, err := currentComposition(ctx, tx, id)
			switch {
			case errors.Is(err, pgx.ErrNoRows) && id == root:
				return notFound(domain.CodeCompositionMissing, "no composition is recorded for this item")
			case errors.Is(err, pgx.ErrNoRows):
				tree.Undeclared = append(tree.Undeclared, id)
				c = domain.Composition{Constituent: domain.Constituent{ItemID: id}}
			case err != nil:
				return err
			}
			if id == root {
				tree.RootVersion = c.Version
			}
			tree.Nodes[id] = c.Constituent
			for _, part := range c.Parts {
				queue = append(queue, part.ItemID)
			}
		}
		return nil
	})
	sort.Strings(tree.Undeclared)
	return tree, err
}

// RecordComplianceResults stores the evaluation of an item (BR-33.11). For each
// (list version, region) a new result version is appended, and
// complianceresult.recorded written to the outbox, only when the outcome differs
// from the latest stored version; an unchanged outcome keeps its version. The
// results come back with their stored version numbers.
func (s *Postgres) RecordComplianceResults(ctx context.Context, itemID string, compositionVersion int, results []domain.ComplianceResult) ([]domain.ComplianceResult, error) {
	p, _ := tenant.FromContext(ctx)
	out := make([]domain.ComplianceResult, len(results))
	copy(out, results)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for i := range out {
			r := &out[i]
			prev, found, err := latestResult(ctx, tx, itemID, *r)
			if err != nil {
				return err
			}
			if found && prev.SameOutcome(*r) {
				r.Version = prev.Version
				continue
			}
			hits, err := json.Marshal(r.Hits)
			if err != nil {
				return err
			}
			r.Version = prev.Version + 1
			tag, err := tx.Exec(ctx, `insert into quality_compliance_result
				(tenant_id, item_id, list_code, list_version, region, version, composition_version, restricted, code, severity, hits)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				on conflict (tenant_id, item_id, list_code, list_version, region, version) do nothing`,
				p.TenantID, itemID, r.ListCode, r.ListVersion, r.Region, r.Version, compositionVersion, r.Restricted, r.Code, r.Severity, string(hits))
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				// A concurrent evaluation stored this version first; keep its row.
				continue
			}
			evtHits := make([]qevents.ComplianceHit, len(r.Hits))
			for j, h := range r.Hits {
				evtHits[j] = qevents.ComplianceHit{CAS: h.CAS, PercentWW: h.PercentWW.String(), ThresholdPct: h.ThresholdPct.String()}
			}
			if err := enqueue(ctx, tx, "complianceresult", itemID, qevents.ComplianceResultRecorded, p.TenantID, qevents.ComplianceResultRecordedData{
				ItemID: itemID, ListCode: r.ListCode, ListVersion: r.ListVersion, Region: r.Region, Version: r.Version,
				CompositionVersion: compositionVersion, Restricted: r.Restricted, Code: r.Code, Severity: r.Severity, Hits: evtHits,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func latestResult(ctx context.Context, tx pgx.Tx, itemID string, r domain.ComplianceResult) (domain.ComplianceResult, bool, error) {
	prev := domain.ComplianceResult{ListCode: r.ListCode, ListVersion: r.ListVersion, Region: r.Region}
	var hits []byte
	err := tx.QueryRow(ctx, `select version, restricted, code, severity, hits from quality_compliance_result
		where `+ownTenant+` and item_id = $1 and list_code = $2 and list_version = $3 and region = $4
		order by version desc limit 1`, itemID, r.ListCode, r.ListVersion, r.Region).Scan(&prev.Version, &prev.Restricted, &prev.Code, &prev.Severity, &hits)
	if errors.Is(err, pgx.ErrNoRows) {
		return prev, false, nil
	}
	if err != nil {
		return prev, false, err
	}
	return prev, true, json.Unmarshal(hits, &prev.Hits)
}
