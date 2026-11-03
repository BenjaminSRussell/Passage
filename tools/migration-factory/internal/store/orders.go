package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// OpenOrdersRun is a validated open_orders extract ready to load (FR-49.1). Run carries the
// identity, source, cutoff, input hash, counts and quarantine; its status is VALIDATED. Lines is
// the whole extract and Accepted the lines that passed the suite.
type OpenOrdersRun struct {
	Run       domain.PackRun
	Lines     []domain.OpenOrderLine
	Accepted  []domain.OpenOrderLine
	StartedBy string
}

// RunOpenOrders loads the accepted lines that Passage does not hold yet (by order type and legacy
// id), each with its legacy_xref row (source table open_orders.sales or open_orders.purchase) and its open_order_opening_line row, reconciles the run and stores it with its
// totals, quarantine and the pack.reconciled event, in one transaction. Runs of one (system,
// company) serialize on an advisory lock. Nothing posts to sales or purchasing here.
func (s *Postgres) RunOpenOrders(ctx context.Context, idem Idem, in OpenOrdersRun) (domain.PackRun, bool, error) {
	var out domain.PackRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (domain.PackRun, error) {
			return runOpenOrders(ctx, tx, in)
		})
		return err
	})
	return out, replayed, err
}

func runOpenOrders(ctx context.Context, tx pgx.Tx, in OpenOrdersRun) (domain.PackRun, error) {
	run := in.Run
	sys, company := string(run.SourceSystem), run.SourceCompany
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('xref|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
		sys, company, domain.TableOpenOrders); err != nil {
		return domain.PackRun{}, fmt.Errorf("lock legacy_xref: %w", err)
	}
	typesIn, idsIn := make([]string, len(in.Accepted)), make([]string, len(in.Accepted))
	for i, o := range in.Accepted {
		typesIn[i], idsIn[i] = o.OrderType, o.LegacyID
	}
	held := map[string]domain.OpenOrderLine{}
	rows, err := tx.Query(ctx, `select legacy_id, order_type, order_number, line_number, item, party, unit, currency,
			ordered_qty::text, delivered_qty::text, unit_price::text, coalesce(to_char(delivery_date, 'YYYY-MM-DD'), '')
		from open_order_opening_line
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2
		  and (order_type, legacy_id) in (select * from unnest($3::text[], $4::text[]))`,
		sys, company, typesIn, idsIn)
	if err != nil {
		return domain.PackRun{}, fmt.Errorf("read open_order_opening_line: %w", err)
	}
	for rows.Next() {
		var o domain.OpenOrderLine
		var ordered, delivered, price string
		if err := rows.Scan(&o.LegacyID, &o.OrderType, &o.OrderNumber, &o.LineNumber, &o.Item, &o.Party, &o.Unit, &o.Currency,
			&ordered, &delivered, &price, &o.DeliveryDate); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		for _, f := range []struct {
			dst *decimal.Decimal
			raw string
		}{{&o.OrderedQty, ordered}, {&o.DeliveredQty, delivered}, {&o.UnitPrice, price}} {
			if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
				rows.Close()
				return domain.PackRun{}, err
			}
		}
		held[o.HeldKey()] = o
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.PackRun{}, err
	}
	var toLoad []domain.OpenOrderLine
	for _, o := range in.Accepted {
		if _, ok := held[o.HeldKey()]; !ok {
			toLoad = append(toLoad, o)
		}
	}
	if err := checkOrderLines(ctx, tx, sys, company, toLoad); err != nil {
		return domain.PackRun{}, err
	}
	for _, o := range toLoad {
		held[o.HeldKey()] = o
	}
	if err := domain.ReconcileOpenOrders(&run, in.Lines, in.Accepted, len(toLoad), held); err != nil {
		return domain.PackRun{}, err
	}
	if err := storeNewRun(ctx, tx, &run, in.StartedBy); err != nil {
		return domain.PackRun{}, err
	}
	batch := &pgx.Batch{}
	for _, o := range toLoad {
		xrefID, lineID := ids.New(), ids.New()
		batch.Queue(`insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id,
			passage_entity, passage_id, run_id, amount, currency)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, 'open_order_opening_line', $6, $7, $8, $9)`,
			xrefID, sys, company, domain.OrderSourceTable(o.OrderType), o.LegacyID, lineID, run.RunID, o.OpenValue().String(), o.Currency)
		var delivery any
		if o.DeliveryDate != "" {
			delivery = o.DeliveryDate
		}
		batch.Queue(`insert into open_order_opening_line (tenant_id, line_id, run_id, legacy_xref_id, source_system, source_company, legacy_id,
			order_type, order_number, line_number, item, party, unit, currency, ordered_qty, delivered_qty, unit_price, delivery_date)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17::date)`,
			lineID, run.RunID, xrefID, sys, company, o.LegacyID, o.OrderType, o.OrderNumber, o.LineNumber, o.Item, o.Party, o.Unit, o.Currency,
			o.OrderedQty.String(), o.DeliveredQty.String(), o.UnitPrice.String(), delivery)
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return domain.PackRun{}, fmt.Errorf("insert open order opening lines: %w", err)
		}
	}
	if err := insertTotals(ctx, tx, run.RunID, run.Totals, 0); err != nil {
		return domain.PackRun{}, err
	}
	for i := range run.Quarantined {
		q := &run.Quarantined[i]
		q.Seq, q.Status = i+1, domain.QuarantineOpen
		if err := insertQuarantine(ctx, tx, run.RunID, q.Seq, q.Record.LegacyID, q.Rule, q.Reason, recordJSON(q.Record)); err != nil {
			return domain.PackRun{}, err
		}
	}
	if err := enqueueReconciled(ctx, tx, run); err != nil {
		return domain.PackRun{}, err
	}
	return run, nil
}

// checkOrderLines refuses a line whose order type, order and line number Passage already holds for
// the company under another legacy id: loading it would open the order line twice (FR-49.10). One
// conflict rejects the run before anything is written.
func checkOrderLines(ctx context.Context, tx pgx.Tx, sys, company string, toLoad []domain.OpenOrderLine) error {
	if len(toLoad) == 0 {
		return nil
	}
	types, orders, lines := make([]string, len(toLoad)), make([]string, len(toLoad)), make([]string, len(toLoad))
	for i, o := range toLoad {
		types[i], orders[i], lines[i] = o.OrderType, o.OrderNumber, o.LineNumber
	}
	var typ, order, line, legacyID, runID string
	err := tx.QueryRow(ctx, `select h.order_type, h.order_number, h.line_number, h.legacy_id, h.run_id::text from open_order_opening_line h
		join unnest($3::text[], $4::text[], $5::text[]) as t(ty, o, l) on (h.order_type, h.order_number, h.line_number) = (t.ty, t.o, t.l)
		where h.tenant_id = current_setting('passage.tenant_id', true)::uuid and h.source_system = $1 and h.source_company = $2
		order by h.order_type, h.order_number, h.line_number limit 1`, sys, company, types, orders, lines).Scan(&typ, &order, &line, &legacyID, &runID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("check open_order_opening_line: %w", err)
	}
	return fmt.Errorf("%w: %s order %s line %s already has an opening line, loaded as legacy id %s by run %s; re-extract with the same legacy ids",
		domain.ErrInvalid, typ, order, line, legacyID, runID)
}
