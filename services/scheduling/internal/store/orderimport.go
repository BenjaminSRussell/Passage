package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// CodeOrderImportNotFound is returned when no production order was received.
const CodeOrderImportNotFound = "SCHED-0131"

// MaxOrderImports bounds the order import list.
const MaxOrderImports = 200

// Order import statuses.
const (
	ImportImported = "imported"
	ImportPartial  = "partial"
	ImportRejected = "rejected"
	// ImportWithdrawn marks an order production took out of schedule (reset,
	// cancel or merge): its operations were withdrawn.
	ImportWithdrawn = "withdrawn"
)

// OrderImport records what scheduling did with the latest order.scheduled.v1
// of one production order (FR-23.10). Skipped lists the operations that could
// not be registered and why; ReplanRequired is set when an operation that
// already held jobs changed, so its jobs were dropped and the next run must
// place it again.
type OrderImport struct {
	OrderID         string                    `json:"orderId"`
	OrderNumber     string                    `json:"orderNumber"`
	OrderVersion    int                       `json:"orderVersion"`
	Reason          string                    `json:"reason"`
	EventID         string                    `json:"eventId"`
	Status          string                    `json:"status"`
	OperationsTotal int                       `json:"operationsTotal"`
	Created         int                       `json:"created"`
	Updated         int                       `json:"updated"`
	Unchanged       int                       `json:"unchanged"`
	Removed         int                       `json:"removed"`
	KeptFirmed      int                       `json:"keptFirmed"`
	Skipped         []domain.SkippedOperation `json:"skipped"`
	ReplanRequired  bool                      `json:"replanRequired"`
	OrderStart      *time.Time                `json:"orderStart"`
	OrderEnd        *time.Time                `json:"orderEnd"`
	ReceivedAt      time.Time                 `json:"receivedAt"`
	// Reimportable is true when the received order is kept, so it can be
	// imported again (after a resource was mapped) without a new event.
	Reimportable bool `json:"reimportable"`
	// WithdrawnAt and WithdrawnTo are set while the order is withdrawn: when
	// production took it out of schedule and the status it moved to.
	WithdrawnAt *time.Time `json:"withdrawnAt"`
	WithdrawnTo *string    `json:"withdrawnTo"`
	// EventAt is the time of the latest production event applied to the row;
	// an older event does not undo it. It is not part of the API.
	EventAt *time.Time `json:"-"`
	// Stale is true on the answer to an event older than the stored one: it
	// changed nothing. It is not stored.
	Stale bool `json:"-"`
}

// OrderImportRequest is one order.scheduled.v1 event ready to import.
type OrderImportRequest struct {
	Order   domain.ProductionOrder
	End     time.Time
	Version int
	Reason  string
	EventID string
	// EventTime is the CloudEvent time of the event; zero when unknown.
	EventTime time.Time
}

const importCols = `order_id::text, order_number, order_version, reason, event_id, status, operations_total, created_count, updated_count,
	unchanged_count, removed_count, kept_firmed_count, skipped, replan_required, order_start, order_end, received_at, source is not null, withdrawn_at, withdrawn_to, order_event_at`

func scanImport(row pgx.Row) (OrderImport, error) {
	var o OrderImport
	var skipped []byte
	if err := row.Scan(&o.OrderID, &o.OrderNumber, &o.OrderVersion, &o.Reason, &o.EventID, &o.Status, &o.OperationsTotal, &o.Created, &o.Updated,
		&o.Unchanged, &o.Removed, &o.KeptFirmed, &skipped, &o.ReplanRequired, &o.OrderStart, &o.OrderEnd, &o.ReceivedAt, &o.Reimportable, &o.WithdrawnAt, &o.WithdrawnTo, &o.EventAt); err != nil {
		return OrderImport{}, err
	}
	o.Skipped = []domain.SkippedOperation{}
	return o, json.Unmarshal(skipped, &o.Skipped)
}

// OrderImportStore imports production orders and reads what was received.
type OrderImportStore interface {
	ImportProductionOrder(ctx context.Context, req OrderImportRequest) (OrderImport, error)
	ListOrderImports(ctx context.Context, f OrderImportFilter) ([]OrderImport, error)
	GetOrderImport(ctx context.Context, orderID string) (OrderImport, error)
	ReimportProductionOrder(ctx context.Context, orderID string) (OrderImport, error)
	WithdrawProductionOrder(ctx context.Context, req OrderWithdrawal) (OrderImport, bool, error)
}

// CodeOrderNotReimportable is returned when the order was received before
// scheduling kept the received event.
const CodeOrderNotReimportable = "SCHED-0133"

// CodeOrderWithdrawn is returned when a withdrawn order is imported again: it
// is no longer scheduled in production and returns with its next schedule event.
const CodeOrderWithdrawn = "SCHED-0134"

// importSource is the received order as kept on its import row.
type importSource struct {
	Order domain.ProductionOrder
	End   time.Time
}

type importedRow struct {
	id     string
	number int
}

// ImportProductionOrder registers the operations of a production order that
// production dated on infinite capacity (FR-23.10), in one transaction with
// its outbox event. Each route operation becomes (or updates) the scheduling
// operation with the same order and number; an operation that already held
// non-firmed jobs and changed loses them and is open again, an operation that
// left the route is removed, and firmed work is never touched. Operations
// whose resource is not a scheduling resource are skipped and listed. An
// event older than the stored order version changes nothing.
func (s *Postgres) ImportProductionOrder(ctx context.Context, req OrderImportRequest) (OrderImport, error) {
	var out OrderImport
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		return importOrder(ctx, tx, req, &out)
	})
	if err != nil {
		return OrderImport{}, err
	}
	return out, nil
}

// lockOrderImport serialises the imports of one order: a delivery and a
// re-import, or two deliveries racing across a rebalance, must not both pass
// the version check. It holds also before the order's first row exists
// (FOR UPDATE locks nothing then) and until the transaction ends; taking it
// twice in one transaction is harmless.
func lockOrderImport(ctx context.Context, tx pgx.Tx, orderID string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('scheduling-order-import:' || $1::text || ':' || $2::text, 0))`,
		tenantOf(ctx), orderID)
	return err
}

// importOrder is ImportProductionOrder inside the caller's transaction.
func importOrder(ctx context.Context, tx pgx.Tx, req OrderImportRequest, out *OrderImport) error {
	if err := lockOrderImport(ctx, tx, req.Order.OrderID); err != nil {
		return err
	}
	prior, err := scanImport(tx.QueryRow(ctx, `select `+importCols+` from scheduling_order_import where order_id = $1::uuid for update`, req.Order.OrderID))
	hasPrior := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// An older order version is stale, and so is a schedule event older than
	// the statuschanged event that withdrew the order (it carries no version).
	if hasPrior && (req.Version < prior.OrderVersion || (prior.Status == ImportWithdrawn && olderThan(req.EventTime, prior.EventAt))) {
		*out = prior
		out.Stale = true
		return nil
	}
	planned, skipped := domain.PlanImport(req.Order)

	var wanted []string
	for _, op := range planned {
		wanted = append(wanted, op.ResourceIDs...)
	}
	// A job names a production (engineering) resource. A scheduling resource
	// mapped to that id (engineering_resource_id) stands for it; failing
	// that, a scheduling resource with that very id does.
	zones := map[string]string{}
	resolved := map[string]string{}
	if len(wanted) > 0 {
		rows, err := tx.Query(ctx, `select id::text, engineering_resource_id::text, time_zone from scheduling_resource
				where id = any($1::uuid[]) or engineering_resource_id = any($1::uuid[])`, wanted)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, zone string
			var eng *string
			if err := rows.Scan(&id, &eng, &zone); err != nil {
				rows.Close()
				return err
			}
			zones[id] = zone
			if eng != nil {
				resolved[*eng] = id
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, w := range wanted {
			if _, mapped := resolved[w]; !mapped {
				if _, direct := zones[w]; direct {
					resolved[w] = w
				}
			}
		}
	}
	// Keep only the candidates scheduling knows (once each, in the order
	// of the route); the due instant is the end of the delivery day where
	// the order finishes, in the time zone of the preferred resource of its
	// last registrable operation, the same for every operation so the
	// order has one due date.
	candidatesOf := map[int][]string{}
	finishZone := ""
	for _, op := range planned {
		for _, id := range op.ResourceIDs {
			if rid, ok := resolved[id]; ok && !slices.Contains(candidatesOf[op.OperNumber], rid) {
				candidatesOf[op.OperNumber] = append(candidatesOf[op.OperNumber], rid)
			}
		}
		if c := candidatesOf[op.OperNumber]; len(c) > 0 {
			finishZone = zones[c[0]]
		}
	}
	var due *time.Time
	if req.Order.DeliveryDate != nil {
		loc, err := time.LoadLocation(finishZone)
		if err != nil || finishZone == "" {
			loc = time.UTC
		}
		d := domain.DeliveryDue(*req.Order.DeliveryDate, loc)
		due = &d
	}

	existing := map[int]importedRow{}
	rows, err := tx.Query(ctx, `select id::text, oper_number from scheduling_operation where order_id = $1::uuid order by oper_number for update`, req.Order.OrderID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r importedRow
		if err := rows.Scan(&r.id, &r.number); err != nil {
			rows.Close()
			return err
		}
		existing[r.number] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	firmed := map[string]bool{}
	if len(existing) > 0 {
		rows, err := tx.Query(ctx, `select distinct job_id::text from scheduled_job where order_id = $1::uuid and firmed`, req.Order.OrderID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			firmed[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	res := OrderImport{OrderID: req.Order.OrderID, OrderNumber: req.Order.OrderNumber, OrderVersion: req.Version, Reason: req.Reason, EventID: req.EventID,
		OperationsTotal: len(req.Order.OperationNumbers()), Skipped: skipped, OrderStart: nonZero(req.Order.Start), OrderEnd: nonZero(req.End)}
	data := schedevents.OrderImportRecordedData{OrderID: req.Order.OrderID, OrderNumber: req.Order.OrderNumber, OrderVersion: req.Version, Reason: req.Reason,
		Operations: []schedevents.OrderImportOperation{}, Skipped: []schedevents.OrderImportSkipped{}, RemovedOperNumbers: []int{}, KeptFirmed: []int{}}
	inEvent := map[int]bool{}
	for _, n := range req.Order.OperationNumbers() {
		inEvent[n] = true
	}

	for _, op := range planned {
		op.DueAt = due
		candidates := candidatesOf[op.OperNumber]
		if len(candidates) == 0 {
			skipped = append(skipped, domain.SkippedOperation{OperNumber: op.OperNumber, Code: domain.SkipUnknownResource,
				Detail: "no resource of the operation is a scheduling resource or is mapped to one"})
			continue
		}
		cur, found := existing[op.OperNumber]
		change := "created"
		opID := ""
		switch {
		case !found:
			opID = ids.New()
			if _, err := tx.Exec(ctx, `insert into scheduling_operation (tenant_id, id, order_id, oper_number, minutes, due_at, ready_at, priority,
						candidate_resource_ids, secondary_resource_ids, setup_minutes)
					values ($1, $2, $3, $4, $5, $6, $7, 100, $8::uuid[], '{}', $9)`,
				tenantOf(ctx), opID, req.Order.OrderID, op.OperNumber, op.Minutes, op.DueAt, op.ReadyAt, candidates, op.SetupMinutes); err != nil {
				return err
			}
			res.Created++
		case firmed[cur.id]:
			skipped = append(skipped, domain.SkippedOperation{OperNumber: op.OperNumber, Code: domain.SkipFirmed, Detail: "a firmed job holds this operation; it was left as it is"})
			res.KeptFirmed++
			data.KeptFirmed = append(data.KeptFirmed, op.OperNumber)
			continue
		default:
			opID = cur.id
			var same bool
			if err := tx.QueryRow(ctx, `select minutes = $2 and setup_minutes = $3 and candidate_resource_ids = $4::uuid[]
						and due_at is not distinct from $5 and ready_at is not distinct from $6
					from scheduling_operation where id = $1::uuid`, opID, op.Minutes, op.SetupMinutes, candidates, op.DueAt, op.ReadyAt).Scan(&same); err != nil {
				return err
			}
			if same {
				change = "unchanged"
				res.Unchanged++
				break
			}
			change = "updated"
			res.Updated++
			// The version moves so a planner's change based on the previous
			// values is refused as stale (If-Match) instead of overwriting these.
			if _, err := tx.Exec(ctx, `update scheduling_operation set minutes = $2, setup_minutes = $3, candidate_resource_ids = $4::uuid[], due_at = $5, ready_at = $6,
					version = version + 1, updated_at = now() where id = $1::uuid`, opID, op.Minutes, op.SetupMinutes, candidates, op.DueAt, op.ReadyAt); err != nil {
				return err
			}
			dropped, err := dropJobs(ctx, tx, opID)
			if err != nil {
				return err
			}
			if dropped {
				res.ReplanRequired = true
			}
		}
		data.Operations = append(data.Operations, schedevents.OrderImportOperation{OperationID: opID, OperNumber: op.OperNumber, Minutes: op.Minutes, ResourceIDs: candidates, Change: change})
	}

	// Operations that left the route go, unless a firmed job holds them.
	leaving := make([]int, 0)
	for n := range existing {
		if !inEvent[n] {
			leaving = append(leaving, n)
		}
	}
	slices.Sort(leaving)
	for _, n := range leaving {
		cur := existing[n]
		if firmed[cur.id] {
			res.KeptFirmed++
			data.KeptFirmed = append(data.KeptFirmed, n)
			skipped = append(skipped, domain.SkippedOperation{OperNumber: n, Code: domain.SkipFirmed, Detail: "the operation left the route but a firmed job holds it; it was kept"})
			continue
		}
		if _, err := dropJobs(ctx, tx, cur.id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from scheduling_operation where id = $1::uuid`, cur.id); err != nil {
			return err
		}
		res.Removed++
		data.RemovedOperNumbers = append(data.RemovedOperNumbers, n)
	}

	slices.SortFunc(skipped, func(a, b domain.SkippedOperation) int { return a.OperNumber - b.OperNumber })
	res.Skipped = skipped
	failed := 0
	for _, sk := range skipped {
		if sk.Code != domain.SkipFirmed {
			failed++
		}
	}
	switch {
	case failed == 0:
		res.Status = ImportImported
	case res.Created+res.Updated+res.Unchanged > 0:
		res.Status = ImportPartial
	default:
		res.Status = ImportRejected
	}
	data.Status, data.ReplanRequired = res.Status, res.ReplanRequired
	for _, sk := range skipped {
		if sk.Code != domain.SkipFirmed {
			data.Skipped = append(data.Skipped, schedevents.OrderImportSkipped{OperNumber: sk.OperNumber, Code: sk.Code, Detail: sk.Detail})
		}
	}
	skippedJSON, err := json.Marshal(skipped)
	if err != nil {
		return err
	}
	sourceJSON, err := json.Marshal(importSource{Order: req.Order, End: req.End})
	if err != nil {
		return err
	}
	stored, err := scanImport(tx.QueryRow(ctx, `insert into scheduling_order_import (tenant_id, order_id, order_number, order_version, reason, event_id, status,
				operations_total, created_count, updated_count, unchanged_count, removed_count, kept_firmed_count, skipped, replan_required, order_start, order_end, source, order_event_at)
			values ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
			on conflict (tenant_id, order_id) do update set order_number = excluded.order_number, order_version = excluded.order_version, reason = excluded.reason,
				event_id = excluded.event_id, status = excluded.status, operations_total = excluded.operations_total, created_count = excluded.created_count,
				updated_count = excluded.updated_count, unchanged_count = excluded.unchanged_count, removed_count = excluded.removed_count,
				kept_firmed_count = excluded.kept_firmed_count, skipped = excluded.skipped, replan_required = excluded.replan_required,
				order_start = excluded.order_start, order_end = excluded.order_end, source = excluded.source, received_at = now(),
				withdrawn_at = null, withdrawn_to = null, order_event_at = coalesce(excluded.order_event_at, scheduling_order_import.order_event_at)
			returning `+importCols,
		tenantOf(ctx), res.OrderID, res.OrderNumber, res.OrderVersion, res.Reason, res.EventID, res.Status, res.OperationsTotal, res.Created, res.Updated,
		res.Unchanged, res.Removed, res.KeptFirmed, skippedJSON, res.ReplanRequired, res.OrderStart, res.OrderEnd, sourceJSON, eventTime(req.EventTime)))
	if err != nil {
		return err
	}
	*out = stored
	return enqueue(ctx, tx, "orderimport", res.OrderID, schedevents.OrderImportRecorded, data)
}

// eventTime is an event's time as stored (microseconds, UTC); nil when unknown.
func eventTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC().Truncate(time.Microsecond)
	return &u
}

// olderThan reports whether an event at t is older than the stored event time;
// an unknown time on either side is not older.
func olderThan(t time.Time, stored *time.Time) bool {
	at := eventTime(t)
	return at != nil && stored != nil && at.Before(*stored)
}

func nonZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// dropJobs deletes the non-firmed jobs of an operation and puts it back to
// open; it reports whether any job was removed.
func dropJobs(ctx context.Context, tx pgx.Tx, opID string) (bool, error) {
	if err := trackOps(ctx, tx, []string{opID}); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `delete from scheduled_job where job_id = $1::uuid and not firmed`, opID)
	if err != nil {
		return false, err
	}
	if err := dropOrphanSecondaries(ctx, tx, []string{opID}); err != nil {
		return false, err
	}
	if err := setStatus(ctx, tx, []string{opID}, "open"); err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// OrderImportFilter narrows ListOrderImports.
type OrderImportFilter struct {
	Status string
	Limit  int
}

// ListOrderImports returns the received production orders, most recent first.
func (s *Postgres) ListOrderImports(ctx context.Context, f OrderImportFilter) ([]OrderImport, error) {
	out := []OrderImport{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+importCols+` from scheduling_order_import where ($1 = '' or status = $1)
			order by received_at desc, order_id limit $2`, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanImport(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

// GetOrderImport returns one received production order.
func (s *Postgres) GetOrderImport(ctx context.Context, orderID string) (OrderImport, error) {
	var out OrderImport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := scanImport(tx.QueryRow(ctx, `select `+importCols+` from scheduling_order_import where order_id = $1::uuid`, orderID))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeOrderImportNotFound, "no production order with that id has been received")
		}
		out = o
		return err
	})
	return out, err
}

// ReimportProductionOrder imports the latest received event of an order again
// against the resources and mappings that exist now, for a planner who has
// mapped a resource since the order arrived. It is a normal import of the kept
// order at its own version, in one transaction under the order's import lock,
// so a delivery of a newer version either commits first (and its kept order is
// the one imported again) or waits for the re-import and then applies on top.
// Operations already registered stay unchanged unless their resolved
// candidates changed (a remapped resource): then they are updated and lose
// their non-firmed jobs, as for a changed event. The import row keeps the
// version and reason of the kept event; its event id becomes
// "reimport:<uuid>" so the record shows the last import was a re-import.
func (s *Postgres) ReimportProductionOrder(ctx context.Context, orderID string) (OrderImport, error) {
	var out OrderImport
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		if err := lockOrderImport(ctx, tx, orderID); err != nil {
			return err
		}
		var version int
		var reason, status string
		var raw []byte
		err := tx.QueryRow(ctx, `select order_version, reason, status, source from scheduling_order_import where order_id = $1::uuid`, orderID).Scan(&version, &reason, &status, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeOrderImportNotFound, "no production order with that id has been received")
		}
		if err != nil {
			return err
		}
		if status == ImportWithdrawn {
			return httpx.NewProblem(http.StatusConflict, CodeOrderWithdrawn,
				"production withdrew the order from schedule; it is imported when production schedules it again")
		}
		if len(raw) == 0 {
			return httpx.NewProblem(http.StatusConflict, CodeOrderNotReimportable,
				"the received order was not kept; it is imported again when production sends its next event")
		}
		var src importSource
		if err := json.Unmarshal(raw, &src); err != nil {
			return fmt.Errorf("kept order %s: %w", orderID, err)
		}
		return importOrder(ctx, tx, OrderImportRequest{Order: src.Order, End: src.End, Version: version, Reason: reason, EventID: "reimport:" + ids.New()}, &out)
	})
	if err != nil {
		return OrderImport{}, err
	}
	return out, nil
}
