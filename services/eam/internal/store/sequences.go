package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// CodeSequenceOpen is raised when a sequence occurrence is generated while the
// previous occurrence's work order is still open (FR-44.15; as BR-44.1 (d), one
// open order per line).
const CodeSequenceOpen = "EAM-SEQ-002"

// MaintenanceSequence is a hierarchy of job steps on an asset or an asset type
// over a base interval (FR-44.15).
type MaintenanceSequence struct {
	ID                   string                `json:"id"`
	Code                 string                `json:"code"`
	Name                 string                `json:"name"`
	AssetType            *string               `json:"assetType,omitempty"`
	AssetID              *string               `json:"assetId,omitempty"`
	BaseIntervalValue    int                   `json:"baseIntervalValue"`
	BaseIntervalUnit     *string               `json:"baseIntervalUnit,omitempty"`
	CounterID            *string               `json:"counterId,omitempty"`
	Steps                []domain.SequenceStep `json:"steps"`
	LastOccurrenceNumber int                   `json:"lastOccurrenceNumber"`
	Version              int                   `json:"version"`
	CreatedAt            time.Time             `json:"createdAt"`
}

// SequenceOccurrence is one generated occurrence of a sequence (BR-44.10) and
// the work order it opened for the selected step.
type SequenceOccurrence struct {
	ID          string    `json:"id"`
	SequenceID  string    `json:"sequenceId"`
	Number      int       `json:"occurrenceNumber"`
	Steps       []string  `json:"stepCodes"`
	Jobs        []string  `json:"jobTypes"`
	WorkOrderID *string   `json:"workOrderId,omitempty"`
	Actor       string    `json:"actor"`
	CreatedAt   time.Time `json:"createdAt"`
}

// OccurrenceInput is what a generated occurrence's work order needs.
type OccurrenceInput struct {
	Currency string
	DueAt    *time.Time
}

const sequenceColumns = `id::text, code, name, asset_type, asset_id::text, base_interval_value,
  base_interval_unit, counter_id::text, steps, last_occurrence_number, version, created_at`

func scanSequence(row pgx.Row) (MaintenanceSequence, error) {
	var s MaintenanceSequence
	var steps []byte
	err := row.Scan(&s.ID, &s.Code, &s.Name, &s.AssetType, &s.AssetID, &s.BaseIntervalValue,
		&s.BaseIntervalUnit, &s.CounterID, &steps, &s.LastOccurrenceNumber, &s.Version, &s.CreatedAt)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(steps, &s.Steps)
}

func getSequence(ctx context.Context, tx pgx.Tx, t, id string, lock bool) (MaintenanceSequence, error) {
	q := `select ` + sequenceColumns + ` from maintenance_sequence where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	s, err := scanSequence(tx.QueryRow(ctx, q, t, id))
	return s, notFound(err, "maintenance sequence")
}

const occurrenceColumns = `id::text, sequence_id::text, occurrence_number, step_codes, job_types,
  work_order_id::text, actor, created_at`

func scanOccurrence(row pgx.Row) (SequenceOccurrence, error) {
	var o SequenceOccurrence
	err := row.Scan(&o.ID, &o.SequenceID, &o.Number, &o.Steps, &o.Jobs, &o.WorkOrderID, &o.Actor, &o.CreatedAt)
	return o, err
}

// sequenceScope checks the scope and interval rules the database also enforces,
// so callers get a 422 with a stable code rather than a constraint error: an
// asset or an asset type (not both), and a time unit or a counter (not both); a
// counter belongs to one asset, so it needs the asset scope.
func sequenceScope(in MaintenanceSequence) error {
	switch {
	case (in.AssetID == nil) == (in.AssetType == nil):
		return problem(422, domain.CodeSequenceStep, "a sequence applies to one asset or to one asset type")
	case (in.BaseIntervalUnit == nil) == (in.CounterID == nil):
		return problem(422, domain.CodeSequenceStep, "give the base interval in a time unit or on a counter, not both")
	case in.CounterID != nil && in.AssetID == nil:
		return problem(422, domain.CodeSequenceStep, "a counter-based sequence needs an asset; asset type sequences use a time unit")
	}
	return nil
}

// CreateSequence defines a maintenance sequence (FR-44.15). The steps must form a
// valid hierarchy (EAM-SEQ-001), a counter must belong to the sequence's asset and
// the code is unique (EAM-DUP-001). A repeated key returns the stored sequence.
func (s *Postgres) CreateSequence(ctx context.Context, in MaintenanceSequence, key string) (MaintenanceSequence, error) {
	var out MaintenanceSequence
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "maintenance_sequence", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getSequence(ctx, tx, t, id, false)
			return gerr
		}
		if err := sequenceScope(in); err != nil {
			return err
		}
		if err := domain.ValidateSequenceSteps(in.Steps); err != nil {
			return gateProblem(err)
		}
		if in.AssetID != nil {
			if _, err := getAsset(ctx, tx, t, *in.AssetID, false); err != nil {
				return err
			}
		}
		if in.CounterID != nil {
			c, err := getCounter(ctx, tx, t, *in.CounterID)
			if err != nil {
				return err
			}
			if c.AssetID != *in.AssetID {
				return problem(422, domain.CodeSequenceStep, "the counter belongs to another asset")
			}
		}
		if dup, err := taken(ctx, tx, "maintenance_sequence", "tenant_id = $1 and code = $2", t, in.Code); err != nil || dup {
			if err != nil {
				return err
			}
			return problem(409, CodeDuplicate, "maintenance sequence "+in.Code+" already exists")
		}
		for i := range in.Steps {
			if in.Steps[i].Includes == nil {
				in.Steps[i].Includes = []string{}
			}
		}
		steps, err := json.Marshal(in.Steps)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into maintenance_sequence (tenant_id, id, code, name, asset_type, asset_id,
		    base_interval_value, base_interval_unit, counter_id, steps, idempotency_key, created_at)
		  values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12)`,
			t, id, in.Code, in.Name, in.AssetType, in.AssetID, in.BaseIntervalValue, in.BaseIntervalUnit,
			in.CounterID, string(steps), optStr(key), s.now()); err != nil {
			return err
		}
		out, err = getSequence(ctx, tx, t, id, false)
		return err
	})
	return out, err
}

// GetSequence returns one maintenance sequence with its steps.
func (s *Postgres) GetSequence(ctx context.Context, id string) (MaintenanceSequence, error) {
	var out MaintenanceSequence
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		var err error
		out, err = getSequence(ctx, tx, t, id, false)
		return err
	})
	return out, err
}

// ListSequences pages the maintenance sequences, newest first, optionally those
// of one asset.
func (s *Postgres) ListSequences(ctx context.Context, opt ListOptions) (Page[MaintenanceSequence], error) {
	var out Page[MaintenanceSequence]
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select `+sequenceColumns+` from maintenance_sequence
		  where tenant_id = $1 and ($2::uuid is null or asset_id = $2) and ($3::uuid is null or id < $3)
		  order by id desc limit $4`, t, optStr(opt.AssetID), optStr(opt.Cursor), opt.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []MaintenanceSequence{}
		for rows.Next() {
			seq, err := scanSequence(rows)
			if err != nil {
				return err
			}
			items = append(items, seq)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = pageOf(items, opt.Limit, func(m MaintenanceSequence) string { return m.ID })
		return nil
	})
	return out, err
}

// SimulateSequence returns the next n occurrences of a sequence without
// generating them (BR-44.10; the editor's "simulate" view).
func (s *Postgres) SimulateSequence(ctx context.Context, id string, n int) ([]domain.Occurrence, error) {
	var out []domain.Occurrence
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		seq, err := getSequence(ctx, tx, t, id, false)
		if err != nil {
			return err
		}
		out = domain.SimulateSteps(seq.Steps, seq.LastOccurrenceNumber, n)
		return nil
	})
	return out, err
}

// GenerateOccurrence advances an asset's sequence to its next occurrence k,
// selects the steps by BR-44.10 and opens a PREVENTIVE work order for the selected
// step's job type (the extra due steps are listed on the occurrence; work orders
// carry one job until job lines exist). The sequence row is locked so concurrent
// calls take distinct numbers. The asset must be ACTIVE (EAM-AST-001), and the
// previous occurrence's order must be finished or canceled (EAM-SEQ-002). An
// asset type sequence is a template and generates nothing here (EAM-SEQ-001).
// A repeated key returns the stored occurrence.
func (s *Postgres) GenerateOccurrence(ctx context.Context, id string, in OccurrenceInput, key string) (SequenceOccurrence, error) {
	var out SequenceOccurrence
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if occID, err := existingID(ctx, tx, "sequence_occurrence", t, key); err != nil || occID != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = scanOccurrence(tx.QueryRow(ctx, `select `+occurrenceColumns+` from sequence_occurrence
			  where tenant_id = $1 and id = $2`, t, occID))
			if gerr == nil && out.SequenceID != id {
				return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another sequence's occurrence")
			}
			return gerr
		}
		seq, err := getSequence(ctx, tx, t, id, true)
		if err != nil {
			return err
		}
		if seq.AssetID == nil {
			return problem(422, domain.CodeSequenceStep, "sequence "+seq.Code+" applies to an asset type; occurrences are generated on asset sequences")
		}
		asset, err := getAsset(ctx, tx, t, *seq.AssetID, false)
		if err != nil {
			return err
		}
		if !domain.ActiveState(asset.LifecycleState) {
			return problem(422, CodeAssetState, "asset is "+asset.LifecycleState+"; work orders need ACTIVE")
		}
		open, err := taken(ctx, tx, "sequence_occurrence o join work_order w on w.tenant_id = o.tenant_id and w.id = o.work_order_id",
			`o.tenant_id = $1 and o.sequence_id = $2 and w.status not in ('COMPLETED', 'CLOSED', 'CANCELED')`, t, id)
		if err != nil {
			return err
		}
		if open {
			return problem(422, CodeSequenceOpen, "the previous occurrence's work order is still open; complete or cancel it first")
		}
		occ := domain.SelectSteps(seq.Steps, seq.LastOccurrenceNumber+1)
		var woID *string
		if len(occ.Jobs) > 0 {
			wo := WorkOrder{AssetID: asset.ID, Type: domain.TypePreventive, Currency: in.Currency, JobType: occ.Jobs[0],
				DueAt: in.DueAt, Priority: domain.DefaultTypePriority(domain.TypePreventive, asset.Criticality)}
			created, err := insertWorkOrder(ctx, tx, t, wo, "", s.now())
			if err != nil {
				return err
			}
			if err := enqueue(ctx, tx, t, "workorder", created, eamevents.WorkOrderCreated, eamevents.WorkOrderCreatedData{
				WorkOrderID: created, AssetID: asset.ID, Type: wo.Type}); err != nil {
				return err
			}
			woID = &created
		}
		out, err = scanOccurrence(tx.QueryRow(ctx, `insert into sequence_occurrence (tenant_id, id, sequence_id,
		    occurrence_number, step_codes, job_types, work_order_id, actor, idempotency_key, created_at)
		  values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) returning `+occurrenceColumns,
			t, ids.New(), id, occ.Number, occ.Steps, occ.Jobs, woID, actorOf(ctx), optStr(key), s.now()))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update maintenance_sequence set last_occurrence_number = $3, version = version + 1,
		    updated_at = now() where tenant_id = $1 and id = $2`, t, id, occ.Number)
		return err
	})
	return out, err
}

// ListOccurrences returns the generated occurrences of a sequence, newest first.
func (s *Postgres) ListOccurrences(ctx context.Context, id string) ([]SequenceOccurrence, error) {
	out := []SequenceOccurrence{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if _, err := getSequence(ctx, tx, t, id, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+occurrenceColumns+` from sequence_occurrence
		  where tenant_id = $1 and sequence_id = $2 order by occurrence_number desc`, t, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOccurrence(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}
