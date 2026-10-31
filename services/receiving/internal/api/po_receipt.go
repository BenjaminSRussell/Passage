package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func openQuantity(poQty, received decimal.Decimal) decimal.Decimal {
	return decimal.Max(poQty.Sub(received), decimal.Zero)
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

func (a *API) requirePOLine(in store.Receipt, fields map[string]string) {
	if !a.ports.RequirePOLine {
		return
	}
	for i, l := range in.Lines {
		if l.POLineNo < 1 {
			fields[fmt.Sprintf("lines[%d].poLineNo", i)] = "is required"
		}
	}
}

func lineMismatch(line domain.PurchaseOrderLine, l store.Line) (field, message string) {
	switch {
	case line.ProductID != "" && !strings.EqualFold(line.ProductID, l.ProductID):
		return "productId", "does not match the purchase order line"
	case line.Unit != "" && !strings.EqualFold(line.Unit, l.Unit):
		return "unit", "does not match the purchase order line unit " + line.Unit
	}
	return "", ""
}

func (a *API) bindPurchaseOrder(ctx context.Context, in *store.Receipt) (map[int]decimal.Decimal, error) {
	if !hasPOLineNo(in.Lines) {
		return nil, nil
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
		return nil, validationProblem(fields)
	}
	po, err := a.loadPurchaseOrder(ctx, in.LegalEntityID, in.PurchaseOrderID)
	if err != nil {
		return nil, err
	}
	switch {
	case !strings.EqualFold(po.LegalEntityID, in.LegalEntityID):
		return nil, poProblem(http.StatusUnprocessableEntity, "RCV-PO-003", "purchase order belongs to another legal entity")
	case !strings.EqualFold(po.VendorID, in.VendorID):
		return nil, poProblem(http.StatusUnprocessableEntity, "RCV-PO-004", "purchase order is placed with another vendor")
	case po.Currency != in.Currency:
		return nil, poProblem(http.StatusUnprocessableEntity, "RCV-PO-005", "receipt currency %s differs from the purchase order currency %s", in.Currency, po.Currency)
	}
	received, err := a.store.ReceivedOnPurchaseOrder(ctx, in.PurchaseOrderID)
	if err != nil {
		return nil, err
	}
	principal, _ := tenant.FromContext(ctx)
	byNo := make(map[int]domain.PurchaseOrderLine, len(po.Lines))
	for _, l := range po.Lines {
		byNo[l.LineNo] = l
	}
	poQty := map[int]decimal.Decimal{}
	for i := range in.Lines {
		l := &in.Lines[i]
		line, ok := byNo[l.POLineNo]
		switch {
		case !ok:
			fields[fmt.Sprintf("lines[%d].poLineNo", i)] = "no such line on the purchase order"
			continue
		case !line.Usable():
			return nil, poProblem(http.StatusUnprocessableEntity, "RCV-PO-002", "purchase order line %d is cancelled", l.POLineNo)
		}
		if field, message := lineMismatch(line, *l); field != "" {
			fields[fmt.Sprintf("lines[%d].%s", i, field)] = message
		}
		open := openQuantity(line.Qty, received[l.POLineNo])
		if !open.IsPositive() {
			return nil, poProblem(http.StatusUnprocessableEntity, domain.CodeOverDelivery, "purchase order line %d has no open quantity", l.POLineNo)
		}
		l.OrderedRemaining, l.UnitPrice = open, line.NetUnitPrice()
		l.POLineID = stableID(principal.TenantID, strings.ToLower(in.PurchaseOrderID), "po-line", fmt.Sprint(l.POLineNo))
		poQty[l.LineNo] = line.Qty
	}
	if len(fields) > 0 {
		return nil, validationProblem(fields)
	}
	return poQty, nil
}

func (a *API) registrationQuantities(ctx context.Context, rec store.Receipt) (map[int]decimal.Decimal, error) {
	if rec.State != domain.Arrived || !hasPOLineNo(rec.Lines) {
		return nil, nil
	}
	po, err := a.loadPurchaseOrder(ctx, rec.LegalEntityID, rec.PurchaseOrderID)
	if err != nil {
		return nil, err
	}
	byNo := make(map[int]domain.PurchaseOrderLine, len(po.Lines))
	for _, l := range po.Lines {
		byNo[l.LineNo] = l
	}
	poQty := map[int]decimal.Decimal{}
	for _, l := range rec.Lines {
		if l.POLineNo < 1 {
			continue
		}
		line, onOrder := byNo[l.POLineNo]
		switch {
		case !onOrder:
			return nil, poProblem(http.StatusUnprocessableEntity, "RCV-PO-006", "purchase order line %d is no longer on the purchase order", l.POLineNo)
		case !line.Usable():
			return nil, poProblem(http.StatusUnprocessableEntity, "RCV-PO-002", "purchase order line %d is cancelled", l.POLineNo)
		}
		if field, message := lineMismatch(line, l); field != "" {
			return nil, poProblem(http.StatusUnprocessableEntity, "RCV-PO-007", "line %d %s %s", l.LineNo, field, message)
		}
		poQty[l.LineNo] = line.Qty
	}
	return poQty, nil
}

func checkOpenQuantity(l store.Line, qty, poQty decimal.Decimal) error {
	allowance := openQuantity(poQty, l.OtherReceived).Mul(decimal.NewFromInt(100).Add(l.OverDeliveryPct)).Div(decimal.NewFromInt(100))
	if qty.GreaterThan(allowance) {
		return &domain.Error{Code: domain.CodeOverDelivery, Detail: fmt.Sprintf("line %d quantity %s exceeds the allowance %s left on purchase order line %d (%s ordered, %s received on other receipts)", l.LineNo, qty, allowance, l.POLineNo, poQty, l.OtherReceived)}
	}
	return nil
}

func (a *API) correctionQuantities(ctx context.Context, id string, deltas map[int]decimal.Decimal) (map[int]decimal.Decimal, error) {
	rec, err := a.store.GetReceipt(ctx, id)
	if err != nil {
		return nil, err
	}
	var upward []store.Line
	for _, l := range rec.Lines {
		if l.POLineNo > 0 && deltas[l.LineNo].IsPositive() {
			upward = append(upward, l)
		}
	}
	if len(upward) == 0 {
		return nil, nil
	}
	po, err := a.loadPurchaseOrder(ctx, rec.LegalEntityID, rec.PurchaseOrderID)
	if err != nil {
		return nil, err
	}
	poQty := map[int]decimal.Decimal{}
	for _, l := range upward {
		at := -1
		for i, pl := range po.Lines {
			if pl.LineNo == l.POLineNo {
				at = i
			}
		}
		if at < 0 || !po.Lines[at].Usable() {
			return nil, poProblem(http.StatusUnprocessableEntity, "RCV-PO-006", "purchase order line %d is gone or cancelled", l.POLineNo)
		}
		poQty[l.LineNo] = po.Lines[at].Qty
	}
	return poQty, nil
}
