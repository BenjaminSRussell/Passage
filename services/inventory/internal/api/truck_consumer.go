package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// The truck consumer books field service's completed work orders (FR-45.9, BR-45.7): each PART
// line that names a product and has a consumed quantity is issued from the technician's truck
// (the site and warehouse the event carries) at DEDUCTED, valued at the truck's running
// average, under the origin SERVICE_LINE of the work order line, so a replay books nothing
// twice. A truck that does not hold the quantity is refused with INV-NEG-001 unless
// service.allow-negative-truck is on (INV_ALLOW_NEGATIVE_TRUCK); a refused line parks the
// event on the dead-letter topic after the other lines are booked. Once a truck issue is
// booked, the truck is checked against its levels and a draft replenishment transfer order
// is created when it is below its minimum.
const (
	// WorkOrderCompletedType is fieldservice's workorder.completed.v1, on WorkOrderTopic.
	WorkOrderCompletedType = "dev.passage.service.workorder.completed.v1"
	WorkOrderTopic         = "passage.service.workorder.completed"
	ServiceSource          = "fieldservice"

	sourceServiceLine = "SERVICE_LINE"
	// CodeTruckNoTruck parks a completion that consumed stock for a technician with no truck.
	CodeTruckNoTruck = "INV-TRK-003"
)

// TruckReplenisher creates the draft transfer orders of trucks below their minimum.
type TruckReplenisher interface {
	ReplenishTrucks(ctx context.Context, legalEntityID, siteID, warehouseID string, shipDate time.Time) ([]domain.TransferOrder, error)
}

type workOrderCompletedData struct {
	WorkOrderID   string `json:"workOrderId"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legalEntityId"`
	CompletedAt   string `json:"completedAt"`
	Currency      string `json:"currency"`
	Truck         *struct {
		SiteID      string  `json:"siteId"`
		WarehouseID string  `json:"warehouseId"`
		LocationID  *string `json:"locationId"`
	} `json:"truck"`
	Lines []struct {
		LineID      string `json:"lineId"`
		Kind        string `json:"kind"`
		ConsumedQty string `json:"consumedQty"`
		ProductID   string `json:"productId"`
		Unit        string `json:"unit"`
	} `json:"lines"`
}

// TruckConsumerHandlers handles workorder.completed. conv converts the line unit into the
// product's inventory unit; allowNegative is the service.allow-negative-truck policy; repl,
// when set, creates the replenishment orders. Register the handlers with the postings consumer.
func TruckConsumerHandlers(s PostingsStore, conv UnitConverter, allowNegative bool, repl TruckReplenisher) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		WorkOrderCompletedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return bookTruckIssues(ctx, s, conv, allowNegative, repl, evt)
		}),
	}
}

// truckSourceID is the origin of the issue of one work order line.
func truckSourceID(tenantID, workOrderID, lineID string) string {
	return postingSourceID(tenantID, "service-truck-issue", strings.ToLower(workOrderID), strings.ToLower(lineID))
}

func bookTruckIssues(ctx context.Context, s PostingsStore, conv UnitConverter, allowNegative bool, repl TruckReplenisher, evt events.Event) error {
	var d workOrderCompletedData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("workorder.completed data: %w", err))
	}
	ctx, err := postingsScope(ctx, evt, ServiceSource, d.LegalEntityID)
	if err != nil {
		return err
	}
	if !ids.Valid(d.WorkOrderID) {
		return eventing.Permanent(errors.New("workorder.completed needs workOrderId"))
	}
	type issue struct {
		lineID, product, unit string
		qty                   decimal.Decimal
	}
	var todo []issue
	for _, l := range d.Lines {
		if l.Kind != "PART" || l.ProductID == "" {
			continue
		}
		qty, err := decimal.NewFromString(l.ConsumedQty)
		if err != nil || !qty.IsPositive() {
			continue
		}
		if !ids.Valid(l.LineID) || !ids.Valid(l.ProductID) || !unitCode.MatchString(postingUnit(l.Unit)) {
			return eventing.Permanent(fmt.Errorf("line %s needs a line id, a product id and a unit", l.LineID))
		}
		todo = append(todo, issue{l.LineID, strings.ToLower(l.ProductID), postingUnit(l.Unit), qty})
	}
	if len(todo) == 0 {
		return nil // nothing was consumed from stock
	}
	if d.Truck == nil || !ids.Valid(d.Truck.SiteID) || !ids.Valid(d.Truck.WarehouseID) {
		return eventing.Permanent(fmt.Errorf("%s: the technician of %s has no truck to issue %d part lines from", CodeTruckNoTruck, d.Number, len(todo)))
	}
	date, err := postingDate(strings.SplitN(d.CompletedAt, "T", 2)[0], evt)
	if err != nil {
		return eventing.Permanent(fmt.Errorf("completedAt: %w", err))
	}
	le := strings.ToLower(d.LegalEntityID)
	dims := domain.Dimensions{domain.DimSite: strings.ToLower(d.Truck.SiteID), domain.DimWarehouse: strings.ToLower(d.Truck.WarehouseID)}
	neg := domain.NegativePolicy{PhysicalAllowed: allowNegative, FinancialAllowed: allowNegative}
	var failed lineFailures
	booked := 0
	for _, it := range todo {
		what := "line " + it.lineID
		qty, _, err := inventoryQty(ctx, conv, le, it.product, it.unit, it.qty)
		if err != nil {
			failed.add(what, err)
			continue
		}
		src := truckSourceID(evt.TenantID, d.WorkOrderID, it.lineID)
		already, err := s.LineBookings(ctx, le, sourceServiceLine, []string{src})
		if err != nil {
			failed.add(what, fmt.Errorf("read the booked issue: %w", err))
			continue
		}
		if len(already[src]) > 0 {
			booked++
			continue
		}
		currency, cost, err := s.IssueBasis(ctx, le, it.product, dims)
		if err != nil {
			failed.add(what, err)
			continue
		}
		if currency == "" {
			currency = d.Currency
		}
		change, applied, err := s.CreateTransaction(ctx, store.CreateTransactionInput{
			LegalEntityID: le, SourceType: sourceServiceLine, SourceID: src, Reference: postingReference(d.Number), ProductID: it.product, Dims: dims,
			Qty: qty.Neg(), EnteredQty: it.qty.Neg(), EnteredUnit: it.unit, Status: domain.StatusDeducted, Currency: currency, CostPrice: cost,
			Date: date, IdempotencyKey: postingsKeyPrefix + "truck:" + src, Negative: neg,
		})
		if err != nil {
			failed.add(what, err)
			continue
		}
		booked++
		slog.InfoContext(ctx, "truck issue booked", "workOrderId", d.WorkOrderID, "lineId", it.lineID, "transactionId", change.TransactionID,
			"qty", qty.String(), "applied", applied, "eventId", evt.ID)
	}
	if booked > 0 && repl != nil {
		// The truck is short only if this issue made it so; a failure here must not undo or park the booked issue.
		if _, err := repl.ReplenishTrucks(ctx, le, dims[domain.DimSite], dims[domain.DimWarehouse], date); err != nil {
			slog.WarnContext(ctx, "truck replenishment failed", "workOrderId", d.WorkOrderID, "err", err)
		}
	}
	return failed.err(evt)
}
