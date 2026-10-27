// Package store persists quality data in Postgres. Every mutation runs in a
// tenant-scoped transaction and writes its event to the outbox in that same
// transaction.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	platevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	qevents "github.com/BenjaminSRussell/passage/services/quality/internal/events"
)

const (
	eventSource     = "quality"
	uniqueViolation = "23505"
	fkViolation     = "23503"
	// CodeVersionConflict is the stable code for a stale If-Match or version.
	CodeVersionConflict = "conflict_version"
	CodeGroupRace       = "QM-0152"
)

// Store is the persistence port the API depends on.
type Store interface {
	CreateTestGroup(ctx context.Context, in domain.TestGroup, key string) (domain.TestGroup, bool, error)
	GetTestGroup(ctx context.Context, id string) (domain.TestGroup, error)
	ListTestGroups(ctx context.Context, limit int) ([]domain.TestGroup, error)
	CreateInstrument(ctx context.Context, in domain.Instrument, key string) (domain.Instrument, bool, error)
	GetInstruments(ctx context.Context, ids []string) (map[string]domain.Instrument, error)
	CalibrateInstrument(ctx context.Context, id string, calibrate func(domain.Instrument) (domain.Instrument, error)) (domain.Instrument, error)
	ListInstruments(ctx context.Context, limit int) ([]domain.Instrument, error)
	CreateAssociation(ctx context.Context, in domain.Association, key string) (domain.Association, bool, error)
	ListAssociations(ctx context.Context) ([]domain.Association, error)
	CreateOrder(ctx context.Context, in domain.Order, key string) (domain.Order, bool, error)
	GetOrder(ctx context.Context, id string) (domain.Order, error)
	ListOrders(ctx context.Context, f OrderFilter) ([]domain.Order, error)
	ListResults(ctx context.Context, orderID string) ([]domain.Result, error)
	HasResultBatch(ctx context.Context, orderID, key string) (bool, error)
	RecordResults(ctx context.Context, orderID string, expectedVersion int, status string, results []domain.Result, key string) (domain.Order, bool, error)
	FindDecisionByKey(ctx context.Context, key string) (domain.Decision, bool, error)
	GetDecision(ctx context.Context, orderID string) (domain.Decision, bool, error)
	PostUsageDecision(ctx context.Context, orderID string, expectedVersion int, d domain.Decision, key string) (domain.Order, domain.Decision, error)
	CreateComplianceList(ctx context.Context, l domain.ComplianceList, key string) (domain.ComplianceList, bool, error)
	ListComplianceLists(ctx context.Context, limit int) ([]domain.ComplianceList, error)
	CurrentComplianceLists(ctx context.Context) ([]domain.ComplianceList, error)
	SaveComposition(ctx context.Context, c domain.Constituent, key string) (domain.Composition, bool, error)
	GetComposition(ctx context.Context, itemID string) (domain.Composition, error)
	LoadCompositionTree(ctx context.Context, root string) (CompositionTree, error)
	RecordComplianceResults(ctx context.Context, itemID string, compositionVersion int, results []domain.ComplianceResult) ([]domain.ComplianceResult, error)
	CreateStudy(ctx context.Context, st domain.Study, key string) (domain.Study, bool, error)
	GetStudy(ctx context.Context, id string) (domain.Study, error)
	ListStudies(ctx context.Context, f StudyFilter) ([]domain.Study, error)
	ListSPCObservations(ctx context.Context, itemID, testID string, maxOrders int) ([]domain.Observation, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a Postgres store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := platevents.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// keyArg maps an empty Idempotency-Key to SQL NULL.
func keyArg(key string) any {
	if key == "" {
		return nil
	}
	return key
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func decStr(d *decimal.Decimal) any {
	if d == nil {
		return nil
	}
	return d.String()
}

func isUnique(err error) bool { return pgCode(err) == uniqueViolation }

func uniqueOn(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == constraint
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// OrderFilter narrows the quality order worklist (section 6.33.14).
type OrderFilter struct {
	Status string
	SiteID string
	Limit  int
}

// ownTenant repeats the RLS predicate in every read and guarded update, so a
// connection that bypasses RLS (a superuser or the table owner without FORCE)
// still sees only the caller's tenant. WithTenantTx sets the setting.
const ownTenant = "tenant_id = current_setting('passage.tenant_id', true)::uuid"

func notFound(code, detail string) error { return httpx.NewProblem(http.StatusNotFound, code, detail) }

// --- test groups -----------------------------------------------------------

const groupColumns = "id, code, version, tests"

func scanGroup(row pgx.Row) (domain.TestGroup, error) {
	var g domain.TestGroup
	var tests []byte
	if err := row.Scan(&g.ID, &g.Code, &g.Version, &tests); err != nil {
		return g, err
	}
	return g, json.Unmarshal(tests, &g.Tests)
}

// CreateTestGroup inserts the next version of the group code. A replayed key returns the stored version.
func (s *Postgres) CreateTestGroup(ctx context.Context, in domain.TestGroup, key string) (domain.TestGroup, bool, error) {
	p, _ := tenant.FromContext(ctx)
	tests, err := json.Marshal(in.Tests)
	if err != nil {
		return domain.TestGroup{}, false, err
	}
	var out domain.TestGroup
	var inserted bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			replay, err := scanGroup(tx.QueryRow(ctx, `select `+groupColumns+` from quality_test_group where tenant_id = $1 and idempotency_key = $2`, p.TenantID, key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		g, err := scanGroup(tx.QueryRow(ctx, `insert into quality_test_group (tenant_id, id, code, version, tests, idempotency_key)
			values ($1, $2, $3, (select coalesce(max(version), 0) + 1 from quality_test_group where tenant_id = $1 and code = $3), $4, $5)
			returning `+groupColumns, p.TenantID, ids.New(), in.Code, string(tests), keyArg(key)))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, CodeGroupRace, "another version of this test group was created at the same time; retry")
		}
		if err != nil {
			return err
		}
		out, inserted = g, true
		return nil
	})
	return out, inserted, err
}

// GetTestGroup reads one test group version by id.
func (s *Postgres) GetTestGroup(ctx context.Context, id string) (domain.TestGroup, error) {
	var g domain.TestGroup
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		g, err = scanGroup(tx.QueryRow(ctx, `select `+groupColumns+` from quality_test_group where `+ownTenant+` and id = $1`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return g, notFound(domain.CodeTestGroupMissing, "test group not found")
	}
	return g, err
}

// ListTestGroups returns test group versions, newest version of each code first.
func (s *Postgres) ListTestGroups(ctx context.Context, limit int) ([]domain.TestGroup, error) {
	out := []domain.TestGroup{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+groupColumns+` from quality_test_group where `+ownTenant+` order by code, version desc limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanGroup(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

// --- instruments -----------------------------------------------------------

const instrumentColumns = "id, code, name, calibration_interval_days, last_calibrated_at, due_at"

func scanInstrument(row pgx.Row) (domain.Instrument, error) {
	var i domain.Instrument
	err := row.Scan(&i.ID, &i.Code, &i.Name, &i.CalibrationIntervalDays, &i.LastCalibratedAt, &i.DueAt)
	return i, err
}

// CreateInstrument registers an instrument with its calibration schedule.
func (s *Postgres) CreateInstrument(ctx context.Context, in domain.Instrument, key string) (domain.Instrument, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Instrument
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			replay, err := scanInstrument(tx.QueryRow(ctx, `select `+instrumentColumns+` from quality_instrument where tenant_id = $1 and idempotency_key = $2`, p.TenantID, key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		i, err := scanInstrument(tx.QueryRow(ctx, `insert into quality_instrument (tenant_id, id, code, name, calibration_interval_days, last_calibrated_at, due_at, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+instrumentColumns,
			p.TenantID, ids.New(), in.Code, in.Name, in.CalibrationIntervalDays, in.LastCalibratedAt, in.DueAt, keyArg(key)))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeInstrumentDupe, "an instrument with this code already exists")
		}
		if err != nil {
			return err
		}
		out, inserted = i, true
		return nil
	})
	return out, inserted, err
}

// GetInstruments returns the instruments among ids; unknown ids are absent from the map.
func (s *Postgres) GetInstruments(ctx context.Context, instrumentIDs []string) (map[string]domain.Instrument, error) {
	out := map[string]domain.Instrument{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, id := range instrumentIDs {
			i, err := scanInstrument(tx.QueryRow(ctx, `select `+instrumentColumns+` from quality_instrument where `+ownTenant+` and id = $1`, id))
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			out[id] = i
		}
		return nil
	})
	return out, err
}

// CalibrateInstrument applies calibrate to the locked instrument and stores the
// new last-calibrated and due dates.
func (s *Postgres) CalibrateInstrument(ctx context.Context, id string, calibrate func(domain.Instrument) (domain.Instrument, error)) (domain.Instrument, error) {
	var out domain.Instrument
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanInstrument(tx.QueryRow(ctx, `select `+instrumentColumns+` from quality_instrument where `+ownTenant+` and id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeInstrumentMissing, "instrument not found")
		}
		if err != nil {
			return err
		}
		next, err := calibrate(cur)
		if err != nil {
			return err
		}
		out, err = scanInstrument(tx.QueryRow(ctx, `update quality_instrument set last_calibrated_at = $2, due_at = $3
			where `+ownTenant+` and id = $1 returning `+instrumentColumns, id, next.LastCalibratedAt, next.DueAt))
		return err
	})
	return out, err
}

// ListInstruments returns instruments, the soonest calibration due first.
func (s *Postgres) ListInstruments(ctx context.Context, limit int) ([]domain.Instrument, error) {
	out := []domain.Instrument{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+instrumentColumns+` from quality_instrument where `+ownTenant+` order by due_at, code limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			i, err := scanInstrument(rows)
			if err != nil {
				return err
			}
			out = append(out, i)
		}
		return rows.Err()
	})
	return out, err
}

// --- associations ----------------------------------------------------------

const assocColumns = "id, trigger, item_scope, coalesce(item_ref::text, ''), party_scope, coalesce(party_ref::text, ''), " +
	"site_scope, coalesce(site_ref::text, ''), sequence, test_group_id, sampling_method, fixed_qty::text, percent::text, aql::text, severity, inspection_level"

func scanAssociation(row pgx.Row) (domain.Association, error) {
	var a domain.Association
	var method, fixed, pct, aql, sev, level string
	err := row.Scan(&a.ID, &a.Trigger, &a.ItemScope, &a.ItemRef, &a.PartyScope, &a.PartyRef, &a.SiteScope, &a.SiteRef,
		&a.Sequence, &a.TestGroupID, &method, &fixed, &pct, &aql, &sev, &level)
	if err != nil {
		return a, err
	}
	a.Plan = domain.SamplingPlan{Method: domain.Method(method), FixedQty: decimal.RequireFromString(fixed),
		Percent: decimal.RequireFromString(pct), AQL: decimal.RequireFromString(aql), Severity: domain.Severity(sev), InspectionLevel: level}
	return a, nil
}

// CreateAssociation stores a trigger rule. Its test group must exist (QM-0140).
func (s *Postgres) CreateAssociation(ctx context.Context, in domain.Association, key string) (domain.Association, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Association
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			replay, err := scanAssociation(tx.QueryRow(ctx, `select `+assocColumns+` from quality_association where tenant_id = $1 and idempotency_key = $2`, p.TenantID, key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		a, err := scanAssociation(tx.QueryRow(ctx, `insert into quality_association (tenant_id, id, trigger, item_scope, item_ref, party_scope, party_ref,
			site_scope, site_ref, sequence, test_group_id, sampling_method, fixed_qty, percent, aql, severity, inspection_level, idempotency_key)
			values ($1, $2, $3, $4, nullif($5, '')::uuid, $6, nullif($7, '')::uuid, $8, nullif($9, '')::uuid, $10, $11, $12,
			$13::numeric, $14::numeric, $15::numeric, $16, $17, $18) returning `+assocColumns,
			p.TenantID, ids.New(), in.Trigger, in.ItemScope, in.ItemRef, in.PartyScope, in.PartyRef, in.SiteScope, in.SiteRef,
			in.Sequence, in.TestGroupID, string(in.Plan.Method), in.Plan.FixedQty.String(), in.Plan.Percent.String(), in.Plan.AQL.String(),
			string(in.Plan.Severity), in.Plan.InspectionLevel, keyArg(key)))
		if pgCode(err) == fkViolation {
			return notFound(domain.CodeTestGroupMissing, "test group not found")
		}
		if err != nil {
			return err
		}
		out, inserted = a, true
		return nil
	})
	return out, inserted, err
}

// ListAssociations returns every association of the tenant in precedence order.
func (s *Postgres) ListAssociations(ctx context.Context) ([]domain.Association, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Association{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+assocColumns+` from quality_association where tenant_id = $1 order by sequence, id`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAssociation(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// --- quality orders --------------------------------------------------------

const orderColumns = "id, trigger, source_ref, item_id, site_id, warehouse_id, dims, lot_qty::text, sample_qty::text, uom, " +
	"test_group_id, test_group_version, sampling_method, inspection_severity, code_letter, accept_no, reject_no, status, version, created_at, closed_at"

func scanOrder(row pgx.Row) (domain.Order, error) {
	var o domain.Order
	var sourceRef, dims []byte
	var lot, sample, method, sev string
	err := row.Scan(&o.ID, &o.Trigger, &sourceRef, &o.ItemID, &o.SiteID, &o.WarehouseID, &dims, &lot, &sample, &o.UOM,
		&o.TestGroupID, &o.TestGroupVersion, &method, &sev, &o.CodeLetter, &o.AcceptNo, &o.RejectNo, &o.Status, &o.Version, &o.CreatedAt, &o.ClosedAt)
	if err != nil {
		return o, err
	}
	if err := json.Unmarshal(sourceRef, &o.SourceRef); err != nil {
		return o, err
	}
	if err := json.Unmarshal(dims, &o.Dims); err != nil {
		return o, err
	}
	o.LotQty, o.SampleQty = decimal.RequireFromString(lot), decimal.RequireFromString(sample)
	o.SamplingMethod, o.Severity = domain.Method(method), domain.Severity(sev)
	return o, nil
}

// CreateOrder inserts a quality order in CREATED and enqueues order.created.
func (s *Postgres) CreateOrder(ctx context.Context, in domain.Order, key string) (domain.Order, bool, error) {
	p, _ := tenant.FromContext(ctx)
	sourceRef, err := json.Marshal(in.SourceRef)
	if err != nil {
		return domain.Order{}, false, err
	}
	dims, err := json.Marshal(in.Dims)
	if err != nil {
		return domain.Order{}, false, err
	}
	var out domain.Order
	var inserted bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			replay, err := scanOrder(tx.QueryRow(ctx, `select `+orderColumns+` from quality_order where tenant_id = $1 and idempotency_key = $2`, p.TenantID, key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		o, err := scanOrder(tx.QueryRow(ctx, `insert into quality_order (tenant_id, id, trigger, source_ref, item_id, site_id, warehouse_id, dims,
			lot_qty, sample_qty, uom, test_group_id, test_group_version, sampling_method, inspection_severity, code_letter, accept_no, reject_no,
			status, version, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11, $12, $13, $14, $15, $16, $17, $18, $19, 1, $20)
			returning `+orderColumns,
			p.TenantID, in.ID, in.Trigger, string(sourceRef), in.ItemID, in.SiteID, in.WarehouseID, string(dims),
			in.LotQty.String(), in.SampleQty.String(), in.UOM, in.TestGroupID, in.TestGroupVersion, string(in.SamplingMethod),
			string(in.Severity), in.CodeLetter, in.AcceptNo, in.RejectNo, domain.StatusCreated, keyArg(key)))
		if isUnique(err) && uniqueOn(err, "quality_order_source_line") {
			return httpx.NewProblem(http.StatusConflict, domain.CodeSourceLineDupe, "a quality order already exists for this source line and trigger")
		}
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "order", o.ID, qevents.OrderCreated, p.TenantID, qevents.OrderCreatedData{
			QualityOrderID: o.ID, Trigger: o.Trigger, SourceRef: o.SourceRef, ItemID: o.ItemID, Dims: o.Dims,
			LotQty: o.LotQty.String(), SampleQty: o.SampleQty.String(),
		}); err != nil {
			return err
		}
		out, inserted = o, true
		return nil
	})
	return out, inserted, err
}

func orderTx(ctx context.Context, tx pgx.Tx, id string) (domain.Order, error) {
	o, err := scanOrder(tx.QueryRow(ctx, `select `+orderColumns+` from quality_order where `+ownTenant+` and id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return o, notFound(domain.CodeOrderMissing, "quality order not found")
	}
	return o, err
}

// GetOrder reads one quality order.
func (s *Postgres) GetOrder(ctx context.Context, id string) (domain.Order, error) {
	var o domain.Order
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		o, err = orderTx(ctx, tx, id)
		return err
	})
	return o, err
}

// ListOrders returns the quality order worklist, newest first.
func (s *Postgres) ListOrders(ctx context.Context, f OrderFilter) ([]domain.Order, error) {
	out := []domain.Order{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+orderColumns+` from quality_order
			where `+ownTenant+` and ($1 = '' or status = $1) and ($2 = '' or site_id = nullif($2, '')::uuid)
			order by created_at desc, id limit $3`, f.Status, f.SiteID, f.Limit)
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

// ListResults returns every recorded result of an order.
func (s *Postgres) ListResults(ctx context.Context, orderID string) ([]domain.Result, error) {
	out := []domain.Result{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select test_id, sample_no, retest_no, value_numeric::text, outcome_id, passed, instrument_id, recorded_by, recorded_at
			from quality_result where `+ownTenant+` and quality_order_id = $1 order by sample_no, test_id, retest_no`, orderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.Result
			var value, outcome, instrument *string
			if err := rows.Scan(&r.TestID, &r.SampleNo, &r.RetestNo, &value, &outcome, &r.Passed, &instrument, &r.RecordedBy, &r.RecordedAt); err != nil {
				return err
			}
			if value != nil {
				v := decimal.RequireFromString(*value)
				r.Value = &v
			}
			if outcome != nil {
				r.OutcomeID = *outcome
			}
			if instrument != nil {
				r.InstrumentID = *instrument
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// HasResultBatch reports whether a result batch with this Idempotency-Key was
// already recorded on the order.
func (s *Postgres) HasResultBatch(ctx context.Context, orderID, key string) (bool, error) {
	found := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select exists (select 1 from quality_result where `+ownTenant+` and quality_order_id = $1 and idempotency_key = $2)`, orderID, key).Scan(&found)
	})
	return found, err
}

// RecordResults stores a batch of results and moves the order to status, guarded
// by expectedVersion. A replayed key returns the order without writing.
func (s *Postgres) RecordResults(ctx context.Context, orderID string, expectedVersion int, status string, results []domain.Result, key string) (domain.Order, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Order
	applied := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from quality_result where `+ownTenant+` and quality_order_id = $1 and idempotency_key = $2`, orderID, key).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				var err error
				out, err = orderTx(ctx, tx, orderID)
				return err
			}
		}
		o, err := scanOrder(tx.QueryRow(ctx, `update quality_order set status = $3, version = version + 1
			where `+ownTenant+` and id = $1 and version = $2 returning `+orderColumns, orderID, expectedVersion, status))
		if errors.Is(err, pgx.ErrNoRows) {
			return staleOrMissing(ctx, tx, orderID, expectedVersion)
		}
		if err != nil {
			return err
		}
		items := make([]qevents.ResultItem, 0, len(results))
		for _, r := range results {
			if _, err := tx.Exec(ctx, `insert into quality_result (tenant_id, id, quality_order_id, test_id, sample_no, retest_no, value_numeric,
				outcome_id, passed, instrument_id, recorded_by, recorded_at, idempotency_key)
				values ($1, $2, $3, $4, $5, $6, $7::numeric, nullif($8, '')::uuid, $9, nullif($10, '')::uuid, $11, $12, $13)`,
				p.TenantID, ids.New(), orderID, r.TestID, r.SampleNo, r.RetestNo, decStr(r.Value), r.OutcomeID, r.Passed,
				r.InstrumentID, r.RecordedBy, r.RecordedAt, keyArg(key)); err != nil {
				if isUnique(err) {
					return httpx.NewProblem(http.StatusConflict, domain.CodeResultDuplicate, "a result for this test, sample and retest already exists; record a retest")
				}
				return err
			}
			items = append(items, qevents.ResultItem{TestID: r.TestID, SampleNo: r.SampleNo, RetestNo: r.RetestNo, Passed: r.Passed})
		}
		if err := enqueue(ctx, tx, "order", orderID, qevents.ResultRecorded, p.TenantID, qevents.ResultRecordedData{QualityOrderID: orderID, Results: items}); err != nil {
			return err
		}
		out, applied = o, true
		return nil
	})
	return out, applied, err
}

// staleOrMissing explains a failed guarded update: 404 for an unknown order,
// 412 conflict_version when the version moved.
func staleOrMissing(ctx context.Context, tx pgx.Tx, orderID string, expectedVersion int) error {
	var version int
	err := tx.QueryRow(ctx, `select version from quality_order where `+ownTenant+` and id = $1`, orderID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(domain.CodeOrderMissing, "quality order not found")
	}
	if err != nil {
		return err
	}
	return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("current version is %d, not %d", version, expectedVersion))
}

// --- usage decisions -------------------------------------------------------

const decisionColumns = "id, quality_order_id, lines, accept_qty::text, reject_qty::text, deviation_qty::text, decided_by, decided_at"

func scanDecision(row pgx.Row) (domain.Decision, error) {
	var d domain.Decision
	var lines []byte
	var accept, reject, deviation string
	err := row.Scan(&d.ID, &d.OrderID, &lines, &accept, &reject, &deviation, &d.DecidedBy, &d.DecidedAt)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(lines, &d.Lines); err != nil {
		return d, err
	}
	d.Split = domain.Split{Accept: decimal.RequireFromString(accept), Reject: decimal.RequireFromString(reject), Deviation: decimal.RequireFromString(deviation)}
	return d, nil
}

// FindDecisionByKey returns the decision stored under an Idempotency-Key.
func (s *Postgres) FindDecisionByKey(ctx context.Context, key string) (domain.Decision, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var d domain.Decision
	found := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		d, err = scanDecision(tx.QueryRow(ctx, `select `+decisionColumns+` from quality_usage_decision where tenant_id = $1 and idempotency_key = $2`, p.TenantID, key))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	return d, found, err
}

// GetDecision returns the usage decision of an order, if one was posted.
func (s *Postgres) GetDecision(ctx context.Context, orderID string) (domain.Decision, bool, error) {
	var d domain.Decision
	found := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		d, err = scanDecision(tx.QueryRow(ctx, `select `+decisionColumns+` from quality_usage_decision where `+ownTenant+` and quality_order_id = $1`, orderID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	return d, found, err
}

// PostUsageDecision closes a pending order with its decision and enqueues
// usagedecision.posted in the same transaction. The guard on version and status
// is part of the update, so concurrent decisions cannot both win.
func (s *Postgres) PostUsageDecision(ctx context.Context, orderID string, expectedVersion int, d domain.Decision, key string) (domain.Order, domain.Decision, error) {
	p, _ := tenant.FromContext(ctx)
	var order domain.Order
	var stored domain.Decision
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			existing, err := scanDecision(tx.QueryRow(ctx, `select `+decisionColumns+` from quality_usage_decision where tenant_id = $1 and idempotency_key = $2`, p.TenantID, key))
			if err == nil {
				order, err = orderTx(ctx, tx, existing.OrderID)
				stored = existing
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		o, err := scanOrder(tx.QueryRow(ctx, `update quality_order set status = 'CLOSED', closed_at = now(), version = version + 1
			where `+ownTenant+` and id = $1 and version = $2 and status = 'PENDING_DECISION' returning `+orderColumns, orderID, expectedVersion))
		if errors.Is(err, pgx.ErrNoRows) {
			return staleOrStatus(ctx, tx, orderID, expectedVersion)
		}
		if err != nil {
			return err
		}
		lines, err := json.Marshal(d.Lines)
		if err != nil {
			return err
		}
		d.ID, d.OrderID, d.DecidedAt = ids.New(), orderID, time.Now().UTC()
		if _, err := tx.Exec(ctx, `insert into quality_usage_decision (tenant_id, id, quality_order_id, lines, accept_qty, reject_qty,
			deviation_qty, decided_by, decided_at, idempotency_key)
			values ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8, $9, $10)`,
			p.TenantID, d.ID, orderID, string(lines), d.Split.Accept.String(), d.Split.Reject.String(), d.Split.Deviation.String(),
			d.DecidedBy, d.DecidedAt, keyArg(key)); err != nil {
			if isUnique(err) {
				return httpx.NewProblem(http.StatusConflict, domain.CodeClosed, "this order already has a usage decision")
			}
			return err
		}
		kinds := make([]string, 0, len(d.Lines))
		for _, l := range d.Lines {
			kinds = append(kinds, l.Decision)
		}
		if err := enqueue(ctx, tx, "order", orderID, qevents.UsageDecisionPosted, p.TenantID, qevents.UsageDecisionPostedData{
			QualityOrderID: orderID, Decisions: kinds, AcceptQty: d.Split.Accept.String(), RejectQty: d.Split.Reject.String(),
			DeviationQty: d.Split.Deviation.String(), User: d.DecidedBy,
		}); err != nil {
			return err
		}
		order, stored = o, d
		return nil
	})
	return order, stored, err
}

// staleOrStatus explains a failed decision guard: 404, 412, or 409 for a
// closed order (QM-0109) or an order that is not pending (QM-0115).
func staleOrStatus(ctx context.Context, tx pgx.Tx, orderID string, expectedVersion int) error {
	var version int
	var status string
	err := tx.QueryRow(ctx, `select version, status from quality_order where `+ownTenant+` and id = $1`, orderID).Scan(&version, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(domain.CodeOrderMissing, "quality order not found")
	}
	if err != nil {
		return err
	}
	if version != expectedVersion {
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("current version is %d, not %d", version, expectedVersion))
	}
	if status == domain.StatusClosed {
		return httpx.NewProblem(http.StatusConflict, domain.CodeClosed, "order is closed; amend through a nonconformance")
	}
	return httpx.NewProblem(http.StatusConflict, domain.CodeStatus, "a usage decision needs an order in PENDING_DECISION, not "+status)
}
