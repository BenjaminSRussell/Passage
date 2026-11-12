// Package store persists formulas and batch orders in Postgres. Every query
// runs inside dbx.WithTenantTx, so row-level security applies, and every write
// commits its outbox events in that same transaction.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
)

// Stable problem codes raised by the store (README "Error codes").
const (
	CodeFormulaNotFound     = "PMF-0101"
	CodeOrderNotFound       = "PMF-0102"
	CodeFormulaDuplicate    = "PMF-0103"
	CodeVersionConflict     = "PMF-0104"
	CodeOrderNotRescalable  = "PMF-0105"
	CodeOrderState          = "PMF-0106"
	CodeReportInvalid       = "PMF-0107"
	CodeIdempotencyReuse    = "idempotency_key_reuse"
	CodeIdempotencyInFlight = "idempotency_key_in_flight"
	// CodeRecipeSent refuses a rescale of an order whose control recipe the shopfloor service already has: the
	// recipe would keep the old quantities, and shopfloor has no way to replace or cancel it.
	CodeRecipeSent = "PMF-0506"

	uniqueViolation = "23505"
	// MaxList bounds every list query.
	MaxList = 200
)

// Idem carries an Idempotency-Key and a fingerprint of the request. A replay
// with a different fingerprint is refused (422).
type Idem struct{ Key, Fingerprint string }

// FormulaLine is one stored formula line (FR-24.1).
type FormulaLine struct {
	LineNumber        int             `json:"lineNumber"`
	ItemID            string          `json:"itemId"`
	Type              domain.LineType `json:"type"`
	Qty               decimal.Decimal `json:"qty"`
	Percent           decimal.Decimal `json:"percent"`
	Scalable          bool            `json:"scalable"`
	PercentControlled bool            `json:"percentControlled"`
}

// Formula is a formula with its lines. List results carry LineCount only.
type Formula struct {
	ID           string          `json:"id"`
	Code         string          `json:"code"`
	Name         string          `json:"name"`
	ItemID       string          `json:"itemId"`
	BomVersionID *string         `json:"bomVersionId"`
	Size         decimal.Decimal `json:"size"`
	Unit         string          `json:"unit"`
	Version      int             `json:"version"`
	LineCount    int             `json:"lineCount"`
	Lines        []FormulaLine   `json:"lines,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
}

// OrderLine is a formula line as the order snapshotted it, with its quantity
// at the order's batch size.
type OrderLine struct {
	LineNumber        int             `json:"lineNumber"`
	ItemID            string          `json:"itemId"`
	Type              domain.LineType `json:"type"`
	FormulaQty        decimal.Decimal `json:"formulaQty"`
	Percent           decimal.Decimal `json:"percent"`
	Scalable          bool            `json:"scalable"`
	PercentControlled bool            `json:"percentControlled"`
	Qty               decimal.Decimal `json:"qty"`
	// RafQty is the quantity reported as finished of a co- or by-product line.
	RafQty *decimal.Decimal `json:"rafQty,omitempty"`
}

// BatchOrder is a batch order (FR-24.2). List results omit Lines.
type BatchOrder struct {
	ID             string          `json:"id"`
	LegalEntityID  string          `json:"legalEntityId"`
	OrderNumber    string          `json:"orderNumber"`
	FormulaID      string          `json:"formulaId"`
	FormulaCode    string          `json:"formulaCode"`
	FormulaVersion int             `json:"formulaVersion"`
	ItemID         string          `json:"itemId"`
	Unit           string          `json:"unit"`
	FormulaSize    decimal.Decimal `json:"formulaSize"`
	BatchSize      decimal.Decimal `json:"batchSize"`
	ScaleFactor    decimal.Decimal `json:"scaleFactor"`
	Status         string          `json:"status"`
	Version        int             `json:"version"`
	CreatedBy      string          `json:"createdBy"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	ReleasedAt     *time.Time      `json:"releasedAt,omitempty"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	ReportedAt     *time.Time      `json:"reportedAt,omitempty"`
	EndedAt        *time.Time      `json:"endedAt,omitempty"`
	// ActualQty is the quantity of the order's own item reported as finished.
	ActualQty  *decimal.Decimal `json:"actualQty,omitempty"`
	Lines      []OrderLine      `json:"lines,omitempty"`
	Allocation *Allocation      `json:"allocation,omitempty"`
}

// NewBatchOrder is the create command.
type NewBatchOrder struct {
	LegalEntityID string
	FormulaID     string
	BatchSize     decimal.Decimal
}

// OrderFilter narrows the batch-order list.
type OrderFilter struct {
	Status    string
	FormulaID string
	Limit     int
}

// Store is what the API layer needs.
type Store interface {
	CreateFormula(ctx context.Context, in Formula, idem Idem) (Formula, bool, error)
	ListFormulas(ctx context.Context, limit int) ([]Formula, error)
	GetFormula(ctx context.Context, id string) (Formula, error)
	CreateBatchOrder(ctx context.Context, in NewBatchOrder, idem Idem) (BatchOrder, bool, error)
	ListBatchOrders(ctx context.Context, f OrderFilter) ([]BatchOrder, error)
	GetBatchOrder(ctx context.Context, id string) (BatchOrder, error)
	RescaleBatchOrder(ctx context.Context, id string, batchSize decimal.Decimal, version int, idem Idem) (BatchOrder, bool, error)
	AdvanceBatchOrder(ctx context.Context, id, command string, version int, idem Idem) (BatchOrder, bool, error)
	ReportBatchOrder(ctx context.Context, id string, in ReportInput, version int, idem Idem) (BatchOrder, bool, error)
	EndBatchOrder(ctx context.Context, id string, in EndInput, version int, idem Idem) (BatchOrder, bool, error)
	DefineAttribute(ctx context.Context, in AttributeDefinition, idem Idem) (AttributeDefinition, bool, error)
	ListAttributeDefinitions(ctx context.Context, itemID string, limit int) ([]AttributeDefinition, error)
	RegisterBatch(ctx context.Context, in NewBatch, idem Idem) (Batch, bool, error)
	ListBatches(ctx context.Context, f BatchFilter) ([]Batch, error)
	GetBatch(ctx context.Context, id string) (Batch, error)
	RecordAttributes(ctx context.Context, id string, values map[string]domain.Value, version int, idem Idem) (AttributesResult, bool, error)
	ChangeDisposition(ctx context.Context, id, to, reason string, version int, idem Idem) (Batch, bool, error)
	MergeBatches(ctx context.Context, in MergeInput, idem Idem) (MergeResult, bool, error)
	GetGenealogy(ctx context.Context, id string) (Genealogy, error)
	CreateCustomerRequirement(ctx context.Context, in CustomerRequirement, idem Idem) (CustomerRequirement, bool, error)
	ListCustomerRequirements(ctx context.Context, f RequirementFilter) ([]CustomerRequirement, error)
	GetCustomerRequirement(ctx context.Context, id string) (CustomerRequirement, error)
	RetireCustomerRequirement(ctx context.Context, id string, version int, idem Idem) (CustomerRequirement, bool, error)
	CreateInheritanceRule(ctx context.Context, in InheritanceRule, idem Idem) (InheritanceRule, bool, error)
	ListInheritanceRules(ctx context.Context, f RuleFilter) ([]InheritanceRule, error)
	GetInheritanceRule(ctx context.Context, id string) (InheritanceRule, error)
	RetireInheritanceRule(ctx context.Context, id string, version int, idem Idem) (InheritanceRule, bool, error)
	DeriveBatch(ctx context.Context, in DeriveInput, idem Idem) (DeriveResult, bool, error)
	ListDerivations(ctx context.Context, f DerivationFilter) ([]Derivation, error)
	CreateReworkOrder(ctx context.Context, in NewRework, idem Idem) (ReworkOrder, bool, error)
	ListReworkOrders(ctx context.Context, f ReworkFilter) ([]ReworkOrder, error)
	GetReworkOrder(ctx context.Context, id string) (ReworkOrder, error)
	ReceiveReworkOrder(ctx context.Context, id, newBatchNumber string, version int, canRelease bool, idem Idem) (ReworkOrder, bool, error)
	ApplicableRequirements(ctx context.Context, legalEntityID, itemID string, who ReqScope, shipDate string) ([]CustomerRequirement, error)
	DefineRecordProfile(ctx context.Context, in RecordProfile, idem Idem) (RecordProfile, bool, error)
	ListRecordProfiles(ctx context.Context, f ProfileFilter) ([]RecordProfile, error)
	GetRecordProfile(ctx context.Context, id string) (RecordProfile, error)
	RetireRecordProfile(ctx context.Context, id string, version int, idem Idem) (RecordProfile, bool, error)
	CompileRecord(ctx context.Context, batchID string, idem Idem) (Record, bool, error)
	ListRecords(ctx context.Context, f RecordFilter) ([]Record, error)
	GetRecord(ctx context.Context, id string) (Record, error)
	RaiseException(ctx context.Context, recordID string, in NewException, version int, idem Idem) (Record, bool, error)
	DispositionException(ctx context.Context, recordID, exceptionID string, in ExceptionDisposition, version int, idem Idem) (Record, bool, error)
	ApproveRecord(ctx context.Context, id string, version int, sign Signing, idem Idem) (Record, bool, error)
	SignerLockout(ctx context.Context, signer string) (domain.Lockout, error)
	RecordReauthResult(ctx context.Context, signer string, failed bool, now time.Time) (domain.Lockout, error)
	SealRecord(ctx context.Context, id string, version int, idem Idem) (Record, bool, error)
	PickCandidates(ctx context.Context, itemID string, onHand []OnHand) ([]PickCandidate, string, error)
	RegisteredBatches(ctx context.Context, batchIDs []string) ([]string, error)
	PutRecipeTarget(ctx context.Context, t RecipeTarget) (RecipeTarget, error)
	GetRecipeTarget(ctx context.Context, legalEntityID, itemID string) (RecipeTarget, bool, error)
	ListRecipeTargets(ctx context.Context, limit int) ([]RecipeTarget, error)
	DeleteRecipeTarget(ctx context.Context, legalEntityID, itemID string) (bool, error)
	SetControlRecipe(ctx context.Context, orderID string, recipeID, errText *string) error
	GetControlRecipeLink(ctx context.Context, orderID string) (ControlRecipeLink, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store on a pgx pool.
type Postgres struct{ pool *pgxpool.Pool }

// New returns a Postgres store.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks database reachability for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// rescalable lists the statuses before Started (PRD 4.24.5 ":rescale").
func rescalable(status string) bool { return status == "planned" || status == "released" }

func principal(ctx context.Context) tenant.Principal {
	p, _ := tenant.FromContext(ctx)
	return p
}

func reuseProblem() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
}

func notFound(code, what, id string) error {
	return httpx.NewProblem(http.StatusNotFound, code, fmt.Sprintf("%s %s not found", what, id))
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, data any) error {
	evt, err := events.New(eventType, pmfevents.Source, aggregateID, principal(ctx).TenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = principal(ctx).UserID
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func toDomain(size decimal.Decimal, lines []FormulaLine) domain.Formula {
	f := domain.Formula{Size: size, Lines: make([]domain.FormulaLine, len(lines))}
	for i, l := range lines {
		f.Lines[i] = domain.FormulaLine{LineNumber: l.LineNumber, ItemID: l.ItemID, Type: l.Type, Qty: l.Qty,
			Percent: l.Percent, Scalable: l.Scalable, PercentControlled: l.PercentControlled}
	}
	return f
}

// CreateFormula validates (FR-24.1) and stores a formula with its lines.
func (s *Postgres) CreateFormula(ctx context.Context, in Formula, idem Idem) (Formula, bool, error) {
	if err := domain.ValidateFormula(toDomain(in.Size, in.Lines)); err != nil {
		return Formula{}, false, err
	}
	var out Formula
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idem.Key != "" {
			var id, fp string
			err := tx.QueryRow(ctx, `select id, coalesce(idempotency_fingerprint, '') from formula where idempotency_key = $1`, idem.Key).Scan(&id, &fp)
			if err == nil {
				if fp != idem.Fingerprint {
					return reuseProblem()
				}
				out, err = getFormula(ctx, tx, id)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		p := principal(ctx)
		in.ID = ids.New()
		in.Version = 1
		in.LineCount = len(in.Lines)
		err := tx.QueryRow(ctx, `insert into formula (tenant_id, id, code, name, item_id, bom_version_id, formula_size, unit, idempotency_key, idempotency_fingerprint)
			values ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9, ''), nullif($10, '')) returning created_at`,
			p.TenantID, in.ID, in.Code, in.Name, in.ItemID, in.BomVersionID, in.Size, in.Unit, idem.Key, idem.Fingerprint).Scan(&in.CreatedAt)
		if err != nil {
			return mapDuplicate(err, CodeFormulaDuplicate, "a formula with code "+in.Code+" already exists")
		}
		for _, l := range in.Lines {
			if _, err := tx.Exec(ctx, `insert into formula_line (tenant_id, formula_id, line_number, item_id, line_type, qty, percent, scalable, percent_controlled)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				p.TenantID, in.ID, l.LineNumber, l.ItemID, string(l.Type), l.Qty, l.Percent, l.Scalable, l.PercentControlled); err != nil {
				return err
			}
		}
		out, created = in, true
		return enqueue(ctx, tx, "formula", in.ID, pmfevents.FormulaCreated, pmfevents.FormulaCreatedData{
			FormulaID: in.ID, Code: in.Code, ItemID: in.ItemID, Size: in.Size.String(), Unit: in.Unit, Lines: len(in.Lines)})
	})
	return out, created, err
}

// mapDuplicate turns a unique violation into a 409. A clash on the
// idempotency key means a concurrent call with the same key committed first;
// the client retries and gets that call's answer.
func mapDuplicate(err error, code, detail string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == uniqueViolation {
		if strings.Contains(pg.ConstraintName, "idempotency_key") {
			return httpx.NewProblem(http.StatusConflict, CodeIdempotencyInFlight, "a request with this Idempotency-Key was processed concurrently; retry to get its result")
		}
		return httpx.NewProblem(http.StatusConflict, code, detail)
	}
	return err
}

const formulaCols = `id, code, name, item_id, bom_version_id, formula_size, unit, version, created_at,
	(select count(*) from formula_line l where l.formula_id = formula.id)`

func scanFormula(row pgx.Row) (Formula, error) {
	var f Formula
	err := row.Scan(&f.ID, &f.Code, &f.Name, &f.ItemID, &f.BomVersionID, &f.Size, &f.Unit, &f.Version, &f.CreatedAt, &f.LineCount)
	return f, err
}

// ListFormulas returns formulas ordered by code, without their lines.
func (s *Postgres) ListFormulas(ctx context.Context, limit int) ([]Formula, error) {
	out := []Formula{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+formulaCols+` from formula order by code limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFormula(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

// GetFormula returns one formula with its lines.
func (s *Postgres) GetFormula(ctx context.Context, id string) (Formula, error) {
	var out Formula
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = getFormula(ctx, tx, id)
		return err
	})
	return out, err
}

func getFormula(ctx context.Context, tx pgx.Tx, id string) (Formula, error) {
	f, err := scanFormula(tx.QueryRow(ctx, `select `+formulaCols+` from formula where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return f, notFound(CodeFormulaNotFound, "formula", id)
	}
	if err != nil {
		return f, err
	}
	rows, err := tx.Query(ctx, `select line_number, item_id, line_type, qty, percent, scalable, percent_controlled
		from formula_line where formula_id = $1 order by line_number`, id)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	f.Lines = []FormulaLine{}
	for rows.Next() {
		var l FormulaLine
		if err := rows.Scan(&l.LineNumber, &l.ItemID, &l.Type, &l.Qty, &l.Percent, &l.Scalable, &l.PercentControlled); err != nil {
			return f, err
		}
		f.Lines = append(f.Lines, l)
	}
	return f, rows.Err()
}

func outputs(lines []OrderLine) []pmfevents.OutputLine {
	out := []pmfevents.OutputLine{}
	for _, l := range lines {
		if domain.IsOutput(l.Type) {
			out = append(out, pmfevents.OutputLine{LineNumber: l.LineNumber, ItemID: l.ItemID, Type: string(l.Type), Qty: l.Qty.String()})
		}
	}
	return out
}

// orderLines scales a formula snapshot to batchSize (BR-24.1).
func orderLines(size, batchSize decimal.Decimal, snapshot []FormulaLine) (decimal.Decimal, []OrderLine, error) {
	res, err := domain.Scale(toDomain(size, snapshot), batchSize)
	if err != nil {
		return decimal.Zero, nil, err
	}
	lines := make([]OrderLine, len(snapshot))
	for i, l := range snapshot {
		lines[i] = OrderLine{LineNumber: l.LineNumber, ItemID: l.ItemID, Type: l.Type, FormulaQty: l.Qty, Percent: l.Percent,
			Scalable: l.Scalable, PercentControlled: l.PercentControlled, Qty: res.Lines[i].Qty}
	}
	return res.ScaleFactor, lines, nil
}

// CreateBatchOrder snapshots the formula, scales it to the batch size, numbers
// the order per legal entity and writes the created event, in one transaction.
func (s *Postgres) CreateBatchOrder(ctx context.Context, in NewBatchOrder, idem Idem) (BatchOrder, bool, error) {
	var out BatchOrder
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idem.Key != "" {
			var id, fp string
			err := tx.QueryRow(ctx, `select id, coalesce(idempotency_fingerprint, '') from batch_order where idempotency_key = $1`, idem.Key).Scan(&id, &fp)
			if err == nil {
				if fp != idem.Fingerprint {
					return reuseProblem()
				}
				out, err = getBatchOrder(ctx, tx, id)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		o, err := insertBatchOrder(ctx, tx, in, idem)
		if err != nil {
			return err
		}
		out, created = o, true
		return nil
	})
	return out, created, err
}

// insertBatchOrder snapshots the formula, scales it to the batch size, numbers
// the order per legal entity and writes the created event inside tx. idem is
// stored on the order for replays; a caller that keeps its own record passes none.
func insertBatchOrder(ctx context.Context, tx pgx.Tx, in NewBatchOrder, idem Idem) (BatchOrder, error) {
	f, err := getFormula(ctx, tx, in.FormulaID)
	if err != nil {
		return BatchOrder{}, err
	}
	factor, lines, err := orderLines(f.Size, in.BatchSize, f.Lines)
	if err != nil {
		return BatchOrder{}, err
	}
	p := principal(ctx)
	var seq int64
	if err := tx.QueryRow(ctx, `insert into batch_order_sequence (tenant_id, legal_entity_id, next_value) values ($1, $2, 2)
		on conflict (tenant_id, legal_entity_id) do update set next_value = batch_order_sequence.next_value + 1
		returning next_value - 1`, p.TenantID, in.LegalEntityID).Scan(&seq); err != nil {
		return BatchOrder{}, err
	}
	o := BatchOrder{ID: ids.New(), LegalEntityID: in.LegalEntityID, OrderNumber: fmt.Sprintf("BO-%06d", seq), FormulaID: f.ID,
		FormulaCode: f.Code, FormulaVersion: f.Version, ItemID: f.ItemID, Unit: f.Unit, FormulaSize: f.Size, BatchSize: in.BatchSize,
		ScaleFactor: factor, Status: "planned", Version: 1, CreatedBy: p.UserID, Lines: lines}
	if err := tx.QueryRow(ctx, `insert into batch_order (tenant_id, id, legal_entity_id, order_number, formula_id, formula_code, formula_version,
			item_id, unit, formula_size, batch_size, scale_factor, created_by, idempotency_key, idempotency_fingerprint)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, nullif($14, ''), nullif($15, '')) returning created_at, updated_at`,
		p.TenantID, o.ID, o.LegalEntityID, o.OrderNumber, o.FormulaID, o.FormulaCode, o.FormulaVersion, o.ItemID, o.Unit,
		o.FormulaSize, o.BatchSize, o.ScaleFactor, o.CreatedBy, idem.Key, idem.Fingerprint).Scan(&o.CreatedAt, &o.UpdatedAt); err != nil {
		return BatchOrder{}, mapDuplicate(err, CodeIdempotencyInFlight, "a concurrent request took batch order number "+o.OrderNumber+"; retry")
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `insert into batch_order_line (tenant_id, order_id, legal_entity_id, line_number, item_id, line_type,
				formula_qty, percent, scalable, percent_controlled, qty)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			p.TenantID, o.ID, o.LegalEntityID, l.LineNumber, l.ItemID, string(l.Type), l.FormulaQty, l.Percent, l.Scalable, l.PercentControlled, l.Qty); err != nil {
			return BatchOrder{}, err
		}
	}
	if err := enqueue(ctx, tx, "batchorder", o.ID, pmfevents.BatchOrderCreated, pmfevents.BatchOrderCreatedData{
		OrderID: o.ID, OrderNumber: o.OrderNumber, LegalEntityID: o.LegalEntityID, FormulaID: o.FormulaID, FormulaVersion: o.FormulaVersion,
		ItemID: o.ItemID, BatchSize: o.BatchSize.String(), ScaleFactor: o.ScaleFactor.String(), CoProducts: outputs(lines)}); err != nil {
		return BatchOrder{}, err
	}
	return o, nil
}

const orderCols = `id, legal_entity_id, order_number, formula_id, formula_code, formula_version, item_id, unit,
	formula_size, batch_size, scale_factor, status, version, created_by, created_at, updated_at,
	released_at, started_at, reported_at, ended_at, actual_qty`

func scanOrder(row pgx.Row) (BatchOrder, error) {
	var o BatchOrder
	err := row.Scan(&o.ID, &o.LegalEntityID, &o.OrderNumber, &o.FormulaID, &o.FormulaCode, &o.FormulaVersion, &o.ItemID, &o.Unit,
		&o.FormulaSize, &o.BatchSize, &o.ScaleFactor, &o.Status, &o.Version, &o.CreatedBy, &o.CreatedAt, &o.UpdatedAt,
		&o.ReleasedAt, &o.StartedAt, &o.ReportedAt, &o.EndedAt, &o.ActualQty)
	return o, err
}

// ListBatchOrders returns orders newest first, without their lines.
func (s *Postgres) ListBatchOrders(ctx context.Context, f OrderFilter) ([]BatchOrder, error) {
	out := []BatchOrder{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+orderCols+` from batch_order
			where ($1 = '' or status = $1) and ($2 = '' or formula_id::text = $2)
			order by created_at desc, id desc limit $3`, f.Status, f.FormulaID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOrder(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

// GetBatchOrder returns one order with its lines.
func (s *Postgres) GetBatchOrder(ctx context.Context, id string) (BatchOrder, error) {
	var out BatchOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = getBatchOrder(ctx, tx, id)
		return err
	})
	return out, err
}

func getBatchOrder(ctx context.Context, tx pgx.Tx, id string) (BatchOrder, error) {
	return loadOrder(ctx, tx, id, "")
}

// loadOrder reads an order and its lines; lock is "" or " for update".
func loadOrder(ctx context.Context, tx pgx.Tx, id, lock string) (BatchOrder, error) {
	o, err := scanOrder(tx.QueryRow(ctx, `select `+orderCols+` from batch_order where id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return o, notFound(CodeOrderNotFound, "batch order", id)
	}
	if err != nil {
		return o, err
	}
	rows, err := tx.Query(ctx, `select line_number, item_id, line_type, formula_qty, percent, scalable, percent_controlled, qty, raf_qty
		from batch_order_line where order_id = $1 order by line_number`, id)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	o.Lines = []OrderLine{}
	for rows.Next() {
		var l OrderLine
		if err := rows.Scan(&l.LineNumber, &l.ItemID, &l.Type, &l.FormulaQty, &l.Percent, &l.Scalable, &l.PercentControlled, &l.Qty, &l.RafQty); err != nil {
			return o, err
		}
		o.Lines = append(o.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return o, err
	}
	rows.Close()
	if o.Status == domain.StatusEnded {
		o.Allocation, err = loadAllocation(ctx, tx, id)
	}
	return o, err
}

// replay returns the stored response of a command key, if any.
func replay(ctx context.Context, tx pgx.Tx, idem Idem, out any) (bool, error) {
	if idem.Key == "" {
		return false, nil
	}
	var hash string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where key = $1`, idem.Key).Scan(&hash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != idem.Fingerprint {
		return false, reuseProblem()
	}
	return true, json.Unmarshal(body, out)
}

func remember(ctx context.Context, tx pgx.Tx, idem Idem, status int, body any) error {
	if idem.Key == "" {
		return nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, $4, $5)`,
		principal(ctx).TenantID, idem.Key, idem.Fingerprint, status, raw)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == uniqueViolation {
		// The key was stored meanwhile by a command on another resource.
		return reuseProblem()
	}
	return err
}

// RescaleBatchOrder changes the batch size of an order before it starts
// (PRD 4.24.5 ":rescale"). version is the version the caller read; a newer
// stored version is a conflict (optimistic concurrency).
func (s *Postgres) RescaleBatchOrder(ctx context.Context, id string, batchSize decimal.Decimal, version int, idem Idem) (BatchOrder, bool, error) {
	var out BatchOrder
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock first, then look for a replay: a concurrent retry with the same
		// key waits here and then sees the first call's stored answer.
		o, err := loadOrder(ctx, tx, id, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if o.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("batch order %s is at version %d, not %d; reload and try again", o.OrderNumber, o.Version, version))
		}
		if !rescalable(o.Status) {
			return httpx.NewProblem(http.StatusConflict, CodeOrderNotRescalable,
				fmt.Sprintf("batch order %s is %s; only planned or released orders can be rescaled", o.OrderNumber, o.Status))
		}
		var recipeID *string
		if err := tx.QueryRow(ctx, `select control_recipe_id::text from batch_order where id = $1`, id).Scan(&recipeID); err != nil {
			return err
		}
		if recipeID != nil {
			return httpx.NewProblem(http.StatusConflict, CodeRecipeSent,
				fmt.Sprintf("batch order %s has control recipe %s on the shop floor at the current quantities; a rescale would leave it at the old ones", o.OrderNumber, *recipeID))
		}
		snapshot := make([]FormulaLine, len(o.Lines))
		for i, l := range o.Lines {
			snapshot[i] = FormulaLine{LineNumber: l.LineNumber, ItemID: l.ItemID, Type: l.Type, Qty: l.FormulaQty, Percent: l.Percent,
				Scalable: l.Scalable, PercentControlled: l.PercentControlled}
		}
		factor, lines, err := orderLines(o.FormulaSize, batchSize, snapshot)
		if err != nil {
			return err
		}
		previous := o.BatchSize
		if err := tx.QueryRow(ctx, `update batch_order set batch_size = $2, scale_factor = $3, version = version + 1, updated_at = now()
			where id = $1 returning version, updated_at`, id, batchSize, factor).Scan(&o.Version, &o.UpdatedAt); err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := tx.Exec(ctx, `update batch_order_line set qty = $3 where order_id = $1 and line_number = $2`, id, l.LineNumber, l.Qty); err != nil {
				return err
			}
		}
		o.BatchSize, o.ScaleFactor, o.Lines = batchSize, factor, lines
		if err := enqueue(ctx, tx, "batchorder", o.ID, pmfevents.BatchOrderRescaled, pmfevents.BatchOrderRescaledData{
			OrderID: o.ID, PreviousBatchSize: previous.String(), BatchSize: batchSize.String(), ScaleFactor: factor.String(),
			Version: o.Version, CoProducts: outputs(lines)}); err != nil {
			return err
		}
		out, changed = o, true
		return remember(ctx, tx, idem, http.StatusOK, o)
	})
	return out, changed, err
}
