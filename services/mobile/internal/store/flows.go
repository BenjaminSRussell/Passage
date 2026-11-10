package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	evts "github.com/BenjaminSRussell/passage/services/mobile/internal/events"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/flows"
)

const (
	FlowDraft     = "DRAFT"
	FlowPublished = "PUBLISHED"
	FlowRetired   = "RETIRED"
)

// Flow is one stored version of a flow.
type Flow struct {
	FlowID      string       `json:"flowId"`
	Code        string       `json:"code"`
	Version     int          `json:"version"`
	Status      string       `json:"status"`
	EntryStepID string       `json:"entryStepId"`
	Steps       []flows.Step `json:"steps"`
	Warehouses  []string     `json:"warehouses"`
	PublishedAt *time.Time   `json:"publishedAt,omitempty"`
	PublishedBy string       `json:"publishedBy,omitempty"`
}

func (f Flow) Draft() flows.Draft {
	return flows.Draft{Code: f.Code, EntryStepID: f.EntryStepID, Steps: f.Steps, Warehouses: f.Warehouses}
}

// GraphError is a publish refused because the flow graph is invalid (MOB-0113).
type GraphError struct{ Issues []flows.Issue }

func (e *GraphError) Error() string { return "flow graph is invalid: " + e.Issues[0].Message }

const flowColumns = `flow_id::text, code, version, status, entry_step_id, steps, warehouses::text[], published_at, published_by`

func scanFlow(row scanner) (Flow, error) {
	var f Flow
	var steps []byte
	if err := row.Scan(&f.FlowID, &f.Code, &f.Version, &f.Status, &f.EntryStepID, &steps, &f.Warehouses, &f.PublishedAt, &f.PublishedBy); err != nil {
		return Flow{}, err
	}
	if err := json.Unmarshal(steps, &f.Steps); err != nil {
		return Flow{}, err
	}
	if f.Warehouses == nil {
		f.Warehouses = []string{}
	}
	return f, nil
}

func warehouseIDs(d flows.Draft) []string {
	if d.Warehouses == nil {
		return []string{}
	}
	return d.Warehouses
}

// SaveDraft creates the next DRAFT version of a flow code, or replaces the
// code's open DRAFT.
func (s *Store) SaveDraft(ctx context.Context, idem Idem, d flows.Draft) (Flow, bool, error) {
	var out Flow
	var replayed bool
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		out, replayed, err = once(ctx, tx, idem, 200, func() (Flow, error) { return saveDraft(ctx, tx, d) })
		return err
	})
	return out, replayed, err
}

func saveDraft(ctx context.Context, tx pgx.Tx, d flows.Draft) (Flow, error) {
	steps, err := json.Marshal(d.Steps)
	if err != nil {
		return Flow{}, err
	}
	row := tx.QueryRow(ctx, `update mobile_flow set entry_step_id = $3, steps = $4, warehouses = $5::uuid[], updated_at = now()
		where tenant_id = $1 and code = $2 and status = 'DRAFT' returning `+flowColumns, tenantID(ctx), d.Code, d.EntryStepID, steps, warehouseIDs(d))
	f, err := scanFlow(row)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Flow{}, err
	}
	return scanFlow(tx.QueryRow(ctx, `insert into mobile_flow (tenant_id, flow_id, code, version, status, entry_step_id, steps, warehouses, created_by)
		select $1, $2, $3, coalesce(max(version), 0) + 1, 'DRAFT', $4, $5, $6::uuid[], $7 from mobile_flow where tenant_id = $1 and code = $3
		returning `+flowColumns, tenantID(ctx), ids.New(), d.Code, d.EntryStepID, steps, warehouseIDs(d), userID(ctx)))
}

// Publish validates a DRAFT, retires the code's current PUBLISHED version,
// publishes the draft and emits dev.passage.mobile.flow.published.v1. ifMatch
// is the draft's version, or 0 to skip the check.
func (s *Store) Publish(ctx context.Context, idem Idem, flowID string, ifMatch int) (Flow, bool, error) {
	var out Flow
	var replayed bool
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		out, replayed, err = once(ctx, tx, idem, 200, func() (Flow, error) { return publish(ctx, tx, flowID, ifMatch) })
		return err
	})
	return out, replayed, err
}

func publish(ctx context.Context, tx pgx.Tx, flowID string, ifMatch int) (Flow, error) {
	f, err := scanFlow(tx.QueryRow(ctx, `select `+flowColumns+` from mobile_flow where tenant_id = $1 and flow_id = $2 for update`, tenantID(ctx), flowID))
	if err != nil {
		return Flow{}, err
	}
	if f.Status != FlowDraft || (ifMatch > 0 && ifMatch != f.Version) {
		return Flow{}, ErrVersionConflict
	}
	if issues := flows.Validate(f.Draft()); len(issues) > 0 {
		return Flow{}, &GraphError{Issues: issues}
	}
	return publishRow(ctx, tx, f)
}

func publishRow(ctx context.Context, tx pgx.Tx, f Flow) (Flow, error) {
	if _, err := tx.Exec(ctx, `update mobile_flow set status = 'RETIRED', updated_at = now() where tenant_id = $1 and code = $2 and status = 'PUBLISHED'`,
		tenantID(ctx), f.Code); err != nil {
		return Flow{}, err
	}
	out, err := scanFlow(tx.QueryRow(ctx, `update mobile_flow set status = 'PUBLISHED', published_at = now(), published_by = $3, updated_at = now()
		where tenant_id = $1 and flow_id = $2 returning `+flowColumns, tenantID(ctx), f.FlowID, userID(ctx)))
	if err != nil {
		return Flow{}, err
	}
	if err := enqueue(ctx, tx, "flow", out.FlowID, evts.FlowPublished,
		evts.FlowPublishedData{FlowID: out.FlowID, Code: out.Code, Version: out.Version, Warehouses: out.Warehouses}); err != nil {
		return Flow{}, err
	}
	return out, nil
}

// InstallCore publishes the core flows that have no PUBLISHED version yet and
// returns every core flow's published version.
func (s *Store) InstallCore(ctx context.Context, idem Idem, core []flows.Draft) ([]Flow, bool, error) {
	var out []Flow
	var replayed bool
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		out, replayed, err = once(ctx, tx, idem, 200, func() ([]Flow, error) {
			res := make([]Flow, 0, len(core))
			for _, d := range core {
				var existing Flow
				existing, err := scanFlow(tx.QueryRow(ctx, `select `+flowColumns+` from mobile_flow where tenant_id = $1 and code = $2 and status = 'PUBLISHED'`, tenantID(ctx), d.Code))
				if err == nil {
					res = append(res, existing)
					continue
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					return nil, err
				}
				if issues := flows.Validate(d); len(issues) > 0 {
					return nil, &GraphError{Issues: issues}
				}
				draft, err := saveDraft(ctx, tx, d)
				if err != nil {
					return nil, err
				}
				pub, err := publishRow(ctx, tx, draft)
				if err != nil {
					return nil, err
				}
				res = append(res, pub)
			}
			return res, nil
		})
		return err
	})
	return out, replayed, err
}

func (s *Store) GetFlow(ctx context.Context, flowID string) (Flow, error) {
	var f Flow
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		f, err = scanFlow(tx.QueryRow(ctx, `select `+flowColumns+` from mobile_flow where tenant_id = $1 and flow_id = $2`, tenantID(ctx), flowID))
		return err
	})
	return f, err
}

// FlowVersion is one version of a code, for a run pinned to it (BR-32.9).
func (s *Store) FlowVersion(ctx context.Context, code string, version int) (Flow, error) {
	var f Flow
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		f, err = scanFlow(tx.QueryRow(ctx, `select `+flowColumns+` from mobile_flow where tenant_id = $1 and code = $2 and version = $3 and status <> 'DRAFT'`,
			tenantID(ctx), code, version))
		return err
	})
	return f, err
}

// LatestPublished is the PUBLISHED version of a code.
func (s *Store) LatestPublished(ctx context.Context, code string) (Flow, error) {
	var f Flow
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		f, err = scanFlow(tx.QueryRow(ctx, `select `+flowColumns+` from mobile_flow where tenant_id = $1 and code = $2 and status = 'PUBLISHED'`, tenantID(ctx), code))
		return err
	})
	return f, err
}

// ListFlows lists versions newest first; an empty code lists every flow.
func (s *Store) ListFlows(ctx context.Context, code string, limit int) ([]Flow, error) {
	var out []Flow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+flowColumns+` from mobile_flow where tenant_id = $1 and ($2 = '' or code = $2)
			order by code, version desc limit $3`, tenantID(ctx), code, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFlow(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

// PublishedFor lists the PUBLISHED flows a warehouse may run, optionally only
// the given codes, in code order. With no warehouse, flows restricted to
// warehouses are left out.
func (s *Store) PublishedFor(ctx context.Context, warehouseID string, codes []string) ([]Flow, error) {
	var out []Flow
	if codes == nil {
		codes = []string{}
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+flowColumns+` from mobile_flow
			where tenant_id = $1 and status = 'PUBLISHED'
			  and (cardinality(warehouses) = 0 or $2::text = any(warehouses::text[]))
			  and (cardinality($3::text[]) = 0 or code = any($3::text[]))
			order by code`, tenantID(ctx), warehouseID, codes)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFlow(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}
