package store

import (
	"context"
	"encoding/json"
	"errors"
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
}

const importCols = `order_id::text, order_number, order_version, reason, event_id, status, operations_total, created_count, updated_count,
	unchanged_count, removed_count, kept_firmed_count, skipped, replan_required, order_start, order_end, received_at`

func scanImport(row pgx.Row) (OrderImport, error) {
	var o OrderImport
	var skipped []byte
	if err := row.Scan(&o.OrderID, &o.OrderNumber, &o.OrderVersion, &o.Reason, &o.EventID, &o.Status, &o.OperationsTotal, &o.Created, &o.Updated,
		&o.Unchanged, &o.Removed, &o.KeptFirmed, &skipped, &o.ReplanRequired, &o.OrderStart, &o.OrderEnd, &o.ReceivedAt); err != nil {
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
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// One import of an order at a time, also before its first row exists
		// (FOR UPDATE below locks nothing then): two deliveries of the order
		// racing across a rebalance must not both pass the version check.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('scheduling-order-import:' || $1::text || ':' || $2::text, 0))`,
			tenantOf(ctx), req.Order.OrderID); err != nil {
			return err
		}
		prior, err := scanImport(tx.QueryRow(ctx, `select `+importCols+` from scheduling_order_import where order_id = $1::uuid for update`, req.Order.OrderID))
		hasPrior := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if hasPrior && req.Version < prior.OrderVersion {
			out, out.Stale = prior, true
			return nil
		}
		planned, skipped := domain.PlanImport(req.Order)

		var wanted []string
		for _, op := range planned {
			wanted = append(wanted, op.ResourceIDs...)
		}
		zones := map[string]string{}
		if len(wanted) > 0 {
			rows, err := tx.Query(ctx, `select id::text, time_zone from scheduling_resource where id = any($1::uuid[])`, wanted)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id, zone string
				if err := rows.Scan(&id, &zone); err != nil {
					rows.Close()
					return err
				}
				zones[id] = zone
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		// Keep only the candidates scheduling knows; the due instant is the end
		// of the delivery day where the order finishes, in the time zone of the
		// preferred resource of its last registrable operation, the same for
		// every operation so the order has one due date.
		candidatesOf := map[int][]string{}
		finishZone := ""
		for _, op := range planned {
			for _, id := range op.ResourceIDs {
				if _, ok := zones[id]; ok {
					candidatesOf[op.OperNumber] = append(candidatesOf[op.OperNumber], id)
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
					Detail: "no resource of the operation is a scheduling resource"})
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
				if _, err := tx.Exec(ctx, `update scheduling_operation set minutes = $2, setup_minutes = $3, candidate_resource_ids = $4::uuid[], due_at = $5, ready_at = $6
					where id = $1::uuid`, opID, op.Minutes, op.SetupMinutes, candidates, op.DueAt, op.ReadyAt); err != nil {
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
		stored, err := scanImport(tx.QueryRow(ctx, `insert into scheduling_order_import (tenant_id, order_id, order_number, order_version, reason, event_id, status,
				operations_total, created_count, updated_count, unchanged_count, removed_count, kept_firmed_count, skipped, replan_required, order_start, order_end)
			values ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			on conflict (tenant_id, order_id) do update set order_number = excluded.order_number, order_version = excluded.order_version, reason = excluded.reason,
				event_id = excluded.event_id, status = excluded.status, operations_total = excluded.operations_total, created_count = excluded.created_count,
				updated_count = excluded.updated_count, unchanged_count = excluded.unchanged_count, removed_count = excluded.removed_count,
				kept_firmed_count = excluded.kept_firmed_count, skipped = excluded.skipped, replan_required = excluded.replan_required,
				order_start = excluded.order_start, order_end = excluded.order_end, received_at = now()
			returning `+importCols,
			tenantOf(ctx), res.OrderID, res.OrderNumber, res.OrderVersion, res.Reason, res.EventID, res.Status, res.OperationsTotal, res.Created, res.Updated,
			res.Unchanged, res.Removed, res.KeptFirmed, skippedJSON, res.ReplanRequired, res.OrderStart, res.OrderEnd))
		if err != nil {
			return err
		}
		out = stored
		return enqueue(ctx, tx, "orderimport", res.OrderID, schedevents.OrderImportRecorded, data)
	})
	if err != nil {
		return OrderImport{}, err
	}
	return out, nil
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
