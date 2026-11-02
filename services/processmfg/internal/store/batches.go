package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
)

// Problem codes of the batch quality record.
const (
	CodeBatchNotFound        = "PMF-0108"
	CodeBatchDuplicate       = "PMF-0109"
	CodeAttributeDuplicate   = "PMF-0110"
	CodeDispositionUnchanged = "PMF-0111"

	dateLayout = "2006-01-02"
	// maxHistory bounds the attribute history on a batch detail.
	maxHistory = 500
)

// AttributeDefinition is a batch attribute an item defines (PRD 4.24.4 BatchAttribute).
type AttributeDefinition struct {
	ID        string           `json:"id"`
	ItemID    string           `json:"itemId"`
	Name      string           `json:"name"`
	Kind      string           `json:"kind"`
	Role      string           `json:"role"`
	Target    *decimal.Decimal `json:"target,omitempty"`
	Min       *decimal.Decimal `json:"min,omitempty"`
	Max       *decimal.Decimal `json:"max,omitempty"`
	Options   []string         `json:"options"`
	CreatedBy string           `json:"createdBy"`
	CreatedAt time.Time        `json:"createdAt"`
}

// BatchAttribute is one recorded value. Date is yyyy-mm-dd. Pass is false for a
// numeric value outside its limits.
type BatchAttribute struct {
	Name       string           `json:"name"`
	Kind       string           `json:"kind"`
	Num        *decimal.Decimal `json:"num,omitempty"`
	Text       *string          `json:"text,omitempty"`
	Date       *string          `json:"date,omitempty"`
	Pass       bool             `json:"pass"`
	Detail     string           `json:"detail,omitempty"`
	RecordedBy string           `json:"recordedBy"`
	RecordedAt time.Time        `json:"recordedAt"`
}

// DispositionChange is one entry of a batch's disposition history.
// SegregationConflict is true when the user who changed the disposition had
// also recorded attribute values on the batch (PRD 4.24.17: flagged, not
// refused).
type DispositionChange struct {
	From                string    `json:"from"`
	To                  string    `json:"to"`
	Reason              string    `json:"reason"`
	SegregationConflict bool      `json:"segregationConflict"`
	ChangedBy           string    `json:"changedBy"`
	ChangedAt           time.Time `json:"changedAt"`
}

// Batch is the quality record of a batch. List results carry no attributes or
// histories; the detail carries the current value of each attribute, the full
// attribute history and the disposition history, newest first.
type Batch struct {
	ID               string              `json:"id"`
	LegalEntityID    string              `json:"legalEntityId"`
	ItemID           string              `json:"itemId"`
	BatchNumber      string              `json:"batchNumber"`
	BatchOrderID     *string             `json:"batchOrderId,omitempty"`
	Disposition      string              `json:"disposition"`
	ExpiryDate       *string             `json:"expiryDate,omitempty"`
	Version          int                 `json:"version"`
	CreatedBy        string              `json:"createdBy"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
	Attributes       []BatchAttribute    `json:"attributes,omitempty"`
	AttributeHistory []BatchAttribute    `json:"attributeHistory,omitempty"`
	Dispositions     []DispositionChange `json:"dispositions,omitempty"`
}

// NewBatch is the register command.
type NewBatch struct {
	LegalEntityID string
	ItemID        string
	BatchNumber   string
	BatchOrderID  *string
	Disposition   string
}

// BatchFilter narrows the batch list.
type BatchFilter struct {
	ItemID      string
	Disposition string
	Limit       int
}

// AttributeCheck is the outcome for one recorded value.
type AttributeCheck struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// AttributesResult is the answer of the record command: the batch with its new
// version and the check of each value recorded.
type AttributesResult struct {
	Batch  Batch            `json:"batch"`
	Checks []AttributeCheck `json:"checks"`
}

// DefineAttribute stores an attribute definition of an item (FR-24.5). The
// role defaults to generic; an item has at most one potency attribute.
func (s *Postgres) DefineAttribute(ctx context.Context, in AttributeDefinition, idem Idem) (AttributeDefinition, bool, error) {
	if in.Role == "" {
		in.Role = domain.RoleGeneric
	}
	d := toDomainDef(in)
	if err := domain.ValidateDefinitions([]domain.AttributeDef{d}); err != nil {
		return AttributeDefinition{}, false, err
	}
	if err := domain.CheckRole(in.Name, in.Kind, in.Role); err != nil {
		return AttributeDefinition{}, false, err
	}
	var out AttributeDefinition
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		p := principal(ctx)
		in.ID, in.CreatedBy = ids.New(), p.UserID
		if in.Options == nil {
			in.Options = []string{}
		}
		opts, _ := json.Marshal(in.Options)
		err := tx.QueryRow(ctx, `insert into batch_attribute_def (tenant_id, id, item_id, name, kind, role, target, min_value, max_value, options, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning created_at`,
			p.TenantID, in.ID, in.ItemID, in.Name, in.Kind, in.Role, in.Target, in.Min, in.Max, opts, in.CreatedBy).Scan(&in.CreatedAt)
		if err != nil {
			detail := fmt.Sprintf("item %s already defines the attribute %s", in.ItemID, in.Name)
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.ConstraintName == "batch_attribute_def_potency_idx" {
				detail = fmt.Sprintf("item %s already has a potency attribute", in.ItemID)
			}
			return mapDuplicate(err, CodeAttributeDuplicate, detail)
		}
		data := pmfevents.BatchAttributeDefinedData{DefinitionID: in.ID, ItemID: in.ItemID, Name: in.Name, Kind: in.Kind, Role: in.Role, Options: in.Options}
		if in.Target != nil {
			data.Target = in.Target.String()
		}
		if in.Min != nil {
			data.Min = in.Min.String()
		}
		if in.Max != nil {
			data.Max = in.Max.String()
		}
		if err := enqueue(ctx, tx, "batchattribute", in.ID, pmfevents.BatchAttributeDefined, data); err != nil {
			return err
		}
		out, created = in, true
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, created, err
}

func toDomainDef(in AttributeDefinition) domain.AttributeDef {
	return domain.AttributeDef{Name: in.Name, Kind: in.Kind, Target: in.Target, Min: in.Min, Max: in.Max, Options: in.Options}
}

const defCols = `id, item_id, name, kind, role, target, min_value, max_value, options, created_by, created_at`

func scanDef(row pgx.Row) (AttributeDefinition, error) {
	var d AttributeDefinition
	var opts []byte
	if err := row.Scan(&d.ID, &d.ItemID, &d.Name, &d.Kind, &d.Role, &d.Target, &d.Min, &d.Max, &opts, &d.CreatedBy, &d.CreatedAt); err != nil {
		return d, err
	}
	d.Options = []string{}
	return d, json.Unmarshal(opts, &d.Options)
}

func loadDefs(ctx context.Context, tx pgx.Tx, itemID string, limit int) ([]AttributeDefinition, error) {
	rows, err := tx.Query(ctx, `select `+defCols+` from batch_attribute_def where ($1 = '' or item_id::text = $1)
		order by item_id, name limit $2`, itemID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AttributeDefinition{}
	for rows.Next() {
		d, err := scanDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListAttributeDefinitions returns definitions by item and name; itemID may be empty.
func (s *Postgres) ListAttributeDefinitions(ctx context.Context, itemID string, limit int) ([]AttributeDefinition, error) {
	var out []AttributeDefinition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadDefs(ctx, tx, itemID, limit)
		return err
	})
	return out, err
}

const batchCols = `id, legal_entity_id, item_id, batch_number, batch_order_id, disposition,
	to_char(expiry_date, 'YYYY-MM-DD'), version, created_by, created_at, updated_at`

func scanBatch(row pgx.Row) (Batch, error) {
	var b Batch
	err := row.Scan(&b.ID, &b.LegalEntityID, &b.ItemID, &b.BatchNumber, &b.BatchOrderID, &b.Disposition, &b.ExpiryDate,
		&b.Version, &b.CreatedBy, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

// RegisterBatch creates the quality record of a batch (one per item and batch number).
func (s *Postgres) RegisterBatch(ctx context.Context, in NewBatch, idem Idem) (Batch, bool, error) {
	var out Batch
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if in.BatchOrderID != nil {
			if err := checkBatchOrder(ctx, tx, *in.BatchOrderID, in.LegalEntityID); err != nil {
				return err
			}
		}
		if in.Disposition == domain.DispositionAvailable {
			// A new batch has no record yet, so an item whose profile asks for an
			// approved record starts held until the record is approved.
			if err := releaseGate(ctx, tx, in.LegalEntityID, in.ItemID, nil, in.BatchNumber); err != nil {
				return err
			}
		}
		p := principal(ctx)
		id := ids.New()
		err := tx.QueryRow(ctx, `insert into batch_record (tenant_id, id, legal_entity_id, item_id, batch_number, batch_order_id, disposition, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+batchCols,
			p.TenantID, id, in.LegalEntityID, in.ItemID, in.BatchNumber, in.BatchOrderID, in.Disposition, p.UserID).
			Scan(&out.ID, &out.LegalEntityID, &out.ItemID, &out.BatchNumber, &out.BatchOrderID, &out.Disposition, &out.ExpiryDate,
				&out.Version, &out.CreatedBy, &out.CreatedAt, &out.UpdatedAt)
		if err != nil {
			return mapDuplicate(err, CodeBatchDuplicate, fmt.Sprintf("batch %s of item %s already exists", in.BatchNumber, in.ItemID))
		}
		data := pmfevents.BatchRegisteredData{BatchID: out.ID, LegalEntityID: out.LegalEntityID, ItemID: out.ItemID,
			BatchNumber: out.BatchNumber, Disposition: out.Disposition}
		if out.BatchOrderID != nil {
			data.BatchOrderID = *out.BatchOrderID
		}
		if err := enqueue(ctx, tx, "batch", out.ID, pmfevents.BatchRegistered, data); err != nil {
			return err
		}
		created = true
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, created, err
}

// checkBatchOrder makes sure the batch order a batch is linked to exists in
// the caller's scope and belongs to the batch's legal entity.
func checkBatchOrder(ctx context.Context, tx pgx.Tx, orderID, legalEntityID string) error {
	var entity string
	err := tx.QueryRow(ctx, `select legal_entity_id::text from batch_order where id = $1`, orderID).Scan(&entity)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(CodeOrderNotFound, "batch order", orderID)
	}
	if err != nil {
		return err
	}
	if entity != legalEntityID {
		return httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid,
			fmt.Sprintf("batch order %s belongs to legal entity %s, not %s", orderID, entity, legalEntityID))
	}
	return nil
}

// ListBatches returns batches newest first, without attributes or histories.
func (s *Postgres) ListBatches(ctx context.Context, f BatchFilter) ([]Batch, error) {
	out := []Batch{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+batchCols+` from batch_record
			where ($1 = '' or item_id::text = $1) and ($2 = '' or disposition = $2)
			order by created_at desc, id desc limit $3`, f.ItemID, f.Disposition, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBatch(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// GetBatch returns one batch with its current attributes and both histories.
func (s *Postgres) GetBatch(ctx context.Context, id string) (Batch, error) {
	var out Batch
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadBatch(ctx, tx, id, "")
		return err
	})
	return out, err
}

// loadBatch reads a batch with its detail; lock is "" or " for update".
func loadBatch(ctx context.Context, tx pgx.Tx, id, lock string) (Batch, error) {
	b, err := scanBatch(tx.QueryRow(ctx, `select `+batchCols+` from batch_record where id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, notFound(CodeBatchNotFound, "batch", id)
	}
	if err != nil {
		return b, err
	}
	const attrCols = `name, kind, num_value, text_value, to_char(date_value, 'YYYY-MM-DD'), pass, detail, recorded_by, recorded_at`
	if b.Attributes, err = queryAttributes(ctx, tx, `select distinct on (name) `+attrCols+` from batch_attribute_history
		where batch_id = $1 order by name, seq desc`, id); err != nil {
		return b, err
	}
	if b.AttributeHistory, err = queryAttributes(ctx, tx, `select `+attrCols+` from batch_attribute_history
		where batch_id = $1 order by seq desc limit `+fmt.Sprint(maxHistory), id); err != nil {
		return b, err
	}
	rows, err := tx.Query(ctx, `select from_disposition, to_disposition, reason, segregation_conflict, changed_by, changed_at
		from batch_disposition_history where batch_id = $1 order by seq desc`, id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	b.Dispositions = []DispositionChange{}
	for rows.Next() {
		var c DispositionChange
		if err := rows.Scan(&c.From, &c.To, &c.Reason, &c.SegregationConflict, &c.ChangedBy, &c.ChangedAt); err != nil {
			return b, err
		}
		b.Dispositions = append(b.Dispositions, c)
	}
	return b, rows.Err()
}

func queryAttributes(ctx context.Context, tx pgx.Tx, sql, id string) ([]BatchAttribute, error) {
	rows, err := tx.Query(ctx, sql, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BatchAttribute{}
	for rows.Next() {
		var a BatchAttribute
		if err := rows.Scan(&a.Name, &a.Kind, &a.Num, &a.Text, &a.Date, &a.Pass, &a.Detail, &a.RecordedBy, &a.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func staleBatch(b Batch, version int) error {
	return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
		fmt.Sprintf("batch %s is at version %d, not %d; reload and try again", b.BatchNumber, b.Version, version))
}

// RecordAttributes validates values against the item's definitions (FR-24.5)
// and appends them to the batch's history. A numeric value outside its limits
// is recorded with pass false; an unknown attribute or a value of the wrong
// type is refused. A date recorded under the "expiry" attribute sets the
// batch's expiry date (FR-24.6). The batch version moves by one.
func (s *Postgres) RecordAttributes(ctx context.Context, id string, values map[string]domain.Value, version int, idem Idem) (AttributesResult, bool, error) {
	var out AttributesResult
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		b, err := loadBatch(ctx, tx, id, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if b.Version != version {
			return staleBatch(b, version)
		}
		defs, err := loadDefs(ctx, tx, b.ItemID, MaxList)
		if err != nil {
			return err
		}
		dd := make([]domain.AttributeDef, len(defs))
		for i, d := range defs {
			dd[i] = toDomainDef(d)
		}
		checks, err := domain.CheckAttributes(dd, values)
		if err != nil {
			return err
		}
		kinds := map[string]string{}
		potency := ""
		for _, d := range defs {
			kinds[d.Name] = d.Kind
			if d.Role == domain.RolePotency {
				potency = d.Name
			}
		}
		p := principal(ctx)
		evt := pmfevents.BatchAttributesRecordedData{BatchID: b.ID, LegalEntityID: b.LegalEntityID, ItemID: b.ItemID,
			BatchNumber: b.BatchNumber, Version: b.Version + 1, Attributes: []pmfevents.RecordedAttribute{}}
		var expiry *time.Time
		res := AttributesResult{Checks: make([]AttributeCheck, len(checks))}
		for i, c := range checks {
			v := values[c.Name]
			if _, err := tx.Exec(ctx, `insert into batch_attribute_history (tenant_id, batch_id, legal_entity_id, name, kind, num_value, text_value, date_value, pass, detail, recorded_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				p.TenantID, b.ID, b.LegalEntityID, c.Name, kinds[c.Name], v.Num, v.Text, v.Date, c.Pass, c.Detail, p.UserID); err != nil {
				return err
			}
			res.Checks[i] = AttributeCheck{Name: c.Name, Pass: c.Pass, Detail: c.Detail}
			ra := pmfevents.RecordedAttribute{Name: c.Name, Kind: kinds[c.Name], Pass: c.Pass, Detail: c.Detail}
			switch {
			case v.Num != nil:
				ra.Value = v.Num.String()
				if c.Name == potency {
					evt.Potency = ra.Value
				}
			case v.Text != nil:
				ra.Value = *v.Text
			case v.Date != nil:
				ra.Value = v.Date.Format(dateLayout)
				if c.Name == domain.ExpiryAttribute {
					expiry = v.Date
				}
			}
			if !c.Pass {
				evt.Failures++
			}
			evt.Attributes = append(evt.Attributes, ra)
		}
		if _, err := tx.Exec(ctx, `update batch_record set version = version + 1, updated_at = now(), expiry_date = coalesce($2, expiry_date) where id = $1`, b.ID, expiry); err != nil {
			return err
		}
		fresh, err := loadBatch(ctx, tx, id, "")
		if err != nil {
			return err
		}
		if fresh.ExpiryDate != nil {
			evt.ExpiryDate = *fresh.ExpiryDate
		}
		if err := enqueue(ctx, tx, "batch", b.ID, pmfevents.BatchAttributesRecorded, evt); err != nil {
			return err
		}
		res.Batch = fresh
		sort.Slice(res.Checks, func(i, j int) bool { return res.Checks[i].Name < res.Checks[j].Name })
		out, changed = res, true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// ChangeDisposition moves a batch to another disposition with a reason and
// records it in the history (AC-24.10). A change by a user who recorded
// attribute values on the same batch is flagged as a segregation conflict.
func (s *Postgres) ChangeDisposition(ctx context.Context, id, to, reason string, version int, idem Idem) (Batch, bool, error) {
	var out Batch
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		b, err := loadBatch(ctx, tx, id, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if b.Version != version {
			return staleBatch(b, version)
		}
		if b.Disposition == to {
			return httpx.NewProblem(http.StatusConflict, CodeDispositionUnchanged,
				fmt.Sprintf("batch %s is already %s", b.BatchNumber, to))
		}
		if to == domain.DispositionAvailable {
			if err := releaseGate(ctx, tx, b.LegalEntityID, b.ItemID, []string{b.ID}, b.BatchNumber); err != nil {
				return err
			}
		}
		p := principal(ctx)
		var conflict bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from batch_attribute_history where batch_id = $1 and recorded_by = $2)`,
			b.ID, p.UserID).Scan(&conflict); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into batch_disposition_history (tenant_id, batch_id, legal_entity_id, from_disposition, to_disposition, reason, segregation_conflict, changed_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, b.ID, b.LegalEntityID, b.Disposition, to, reason, conflict, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update batch_record set disposition = $2, version = version + 1, updated_at = now() where id = $1`, b.ID, to); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batch", b.ID, pmfevents.BatchDispositionChanged, pmfevents.BatchDispositionChangedData{
			BatchID: b.ID, LegalEntityID: b.LegalEntityID, ItemID: b.ItemID, BatchNumber: b.BatchNumber,
			From: b.Disposition, To: to, Reason: reason, SegregationConflict: conflict, Version: b.Version + 1}); err != nil {
			return err
		}
		if out, err = loadBatch(ctx, tx, id, ""); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}
