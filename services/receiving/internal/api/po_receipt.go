package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func openQuantity(l domain.PurchaseOrderLine) decimal.Decimal {
	return decimal.Max(l.Qty.Sub(l.ReceivedQty), decimal.Zero)
}

func hasPOLineNo(lines []store.Line) bool {
	for _, l := range lines {
		if l.POLineNo > 0 {
			return true
		}
	}
	return false
}

func poProblem(status int, code, format string, args ...any) error {
	return httpx.NewProblem(status, code, fmt.Sprintf(format, args...))
}

func (a *API) bindPurchaseOrder(ctx context.Context, in *store.Receipt) error {
	if !hasPOLineNo(in.Lines) {
		return nil
	}
	fields := map[string]string{}
	seen := map[int]int{}
	for i, l := range in.Lines {
		name := fmt.Sprintf("lines[%d].poLineNo", i)
		if l.POLineNo < 1 {
			fields[name] = "is required when another line names a purchase order line"
		} else if first, dup := seen[l.POLineNo]; dup {
			fields[name] = fmt.Sprintf("duplicates lines[%d].poLineNo", first)
		} else {
			seen[l.POLineNo] = i
		}
	}
	if len(fields) > 0 {
		return validationProblem(fields)
	}
	po, err := a.loadPurchaseOrder(ctx, in.LegalEntityID, in.PurchaseOrderID)
	if err != nil {
		return err
	}
	switch {
	case !strings.EqualFold(po.LegalEntityID, in.LegalEntityID):
		return poProblem(http.StatusUnprocessableEntity, "RCV-PO-003", "purchase order belongs to another legal entity")
	case !strings.EqualFold(po.VendorID, in.VendorID):
		return poProblem(http.StatusUnprocessableEntity, "RCV-PO-004", "purchase order is placed with another vendor")
	case po.Currency != in.Currency:
		return poProblem(http.StatusUnprocessableEntity, "RCV-PO-005", "receipt currency %s differs from the purchase order currency %s", in.Currency, po.Currency)
	}
	byNo := make(map[int]domain.PurchaseOrderLine, len(po.Lines))
	for _, l := range po.Lines {
		byNo[l.LineNo] = l
	}
	for i := range in.Lines {
		l := &in.Lines[i]
		key := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		line, ok := byNo[l.POLineNo]
		switch {
		case !ok:
			fields[key("poLineNo")] = "no such line on the purchase order"
			continue
		case !line.Usable():
			return poProblem(http.StatusUnprocessableEntity, "RCV-PO-002", "purchase order line %d is cancelled", l.POLineNo)
		case line.ProductID != "" && !strings.EqualFold(line.ProductID, l.ProductID):
			fields[key("productId")] = "does not match the purchase order line"
		case line.Unit != "" && !strings.EqualFold(line.Unit, l.Unit):
			fields[key("unit")] = "does not match the purchase order line unit " + line.Unit
		}
		open := openQuantity(line)
		if !open.IsPositive() {
			return poProblem(http.StatusUnprocessableEntity, domain.CodeOverDelivery, "purchase order line %d has no open quantity", l.POLineNo)
		}
		l.OrderedRemaining, l.UnitPrice = open, line.NetUnitPrice()
	}
	if len(fields) > 0 {
		return validationProblem(fields)
	}
	return nil
}

func (a *API) recheckOpenQuantity(ctx context.Context, rec store.Receipt, inputs map[int]registerInput) error {
	if rec.State != domain.Arrived || !hasPOLineNo(rec.Lines) {
		return nil
	}
	po, err := a.loadPurchaseOrder(ctx, rec.LegalEntityID, rec.PurchaseOrderID)
	if err != nil {
		return err
	}
	byNo := make(map[int]domain.PurchaseOrderLine, len(po.Lines))
	for _, l := range po.Lines {
		byNo[l.LineNo] = l
	}
	for _, l := range rec.Lines {
		in, hasInput := inputs[l.LineNo]
		line, onOrder := byNo[l.POLineNo]
		if l.POLineNo < 1 || !hasInput || !onOrder {
			continue
		}
		allowance := openQuantity(line).Mul(decimal.NewFromInt(100).Add(l.OverDeliveryPct)).Div(decimal.NewFromInt(100))
		if in.Qty.GreaterThan(allowance) {
			return poProblem(http.StatusUnprocessableEntity, domain.CodeOverDelivery, "line %d quantity %s exceeds the open quantity allowance %s of purchase order line %d", l.LineNo, in.Qty, allowance, l.POLineNo)
		}
	}
	return nil
}
