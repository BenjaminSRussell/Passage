package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// dateRule is what decides the confirmed ship date of a line (FR-27.3,
// BR-27.2): the legal entity's delivery date control default and sales lead
// time, and today. A line's own control wins; a supplementary line takes its
// parent line's control.
type dateRule struct {
	params domain.Parameters
	today  time.Time
}

// loadDateRule reads the legal entity's parameters and fixes today. Today is
// the UTC calendar day, as for every other date rule of this service
// (agreement validity, credit release validity, listings): sales holds no
// time zone for a legal entity or a shipping site, so close to midnight UTC
// the "past date" and lead-time checks of an entity far from UTC can be one
// day off (see NOT-IMPLEMENTED, decision to confirm).
func loadDateRule(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, now time.Time) (*dateRule, error) {
	p, err := loadParameters(ctx, tx, tenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	n := now.UTC()
	return &dateRule{params: p.Parameters, today: time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)}, nil
}

// control returns the delivery date control that applies to l.
func (r *dateRule) control(l Line, lines []Line) string {
	if l.SupplementaryOf != nil {
		for _, p := range lines {
			if p.ID == *l.SupplementaryOf {
				return r.control(p, lines)
			}
		}
	}
	if l.DeliveryDateControl != nil {
		return *l.DeliveryDateControl
	}
	return r.params.DeliveryDateControlDefault
}

// ship returns the confirmed ship date l gets for the requested date.
func (r *dateRule) ship(l Line, lines []Line, requested string) (domain.ShipDecision, error) {
	day, err := time.Parse(dayLayout, requested)
	if err != nil {
		return domain.ShipDecision{}, err
	}
	return domain.ShipDate(r.control(l, lines), day, r.today, r.params.SalesLeadTimeDays), nil
}

// dateNoticesOf returns the SLS-0003 and SLS-0057 messages of a decision.
func dateNoticesOf(lineNo int, requested string, dec domain.ShipDecision, leadDays int) []Notice {
	var out []Notice
	if dec.PastMoved {
		out = append(out, Notice{Code: CodePastDate, LineNo: lineNo,
			Message: fmt.Sprintf("Requested date %s is in the past; line %d is moved to today", requested, lineNo)})
	}
	if dec.LeadMoved {
		out = append(out, Notice{Code: CodeLeadTimeApplied, LineNo: lineNo,
			Message: fmt.Sprintf("Line %d is confirmed for %s: the sales lead time of %d days is later than the requested date",
				lineNo, dec.Date.Format(dayLayout), leadDays)})
	}
	return out
}

// confirmDatesTx sets the confirmed ship date of every line that is not
// cancelled from the date rule and returns the notices of lines whose
// requested date moved.
func confirmDatesTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, rule *dateRule) ([]Notice, error) {
	var notices []Notice
	for _, l := range o.Lines {
		if l.LineStatus == domain.LineCancelled {
			continue
		}
		dec, err := rule.ship(l, o.Lines, l.RequestedShipDate)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `update sales_order_line set confirmed_ship_date = $4::date where tenant_id = $1 and order_id = $2 and id = $3`,
			tenantID, o.ID, l.ID, dec.Date.Format(dayLayout)); err != nil {
			return nil, err
		}
		if l.SupplementaryOf == nil {
			notices = append(notices, dateNoticesOf(l.LineNo, l.RequestedShipDate, dec, rule.params.SalesLeadTimeDays)...)
		}
	}
	return notices, nil
}

// copyDeliveryTermsTx copies the customer's delivery terms in force onto the
// order being confirmed (BR-27.2). Its lines' confirmed receipt dates are
// dated by this copy from then on, so a later change of the customer's terms
// does not move a confirmed promise; a release that confirms again takes the
// terms in force at that moment.
func copyDeliveryTermsTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order) error {
	terms, err := termsForCustomerTx(ctx, tx, tenantID, o.LegalEntityID, o.CustomerID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `update sales_order set confirmed_transport_days = $3, confirmed_receiving_days = $4
		where tenant_id = $1 and id = $2`, tenantID, o.ID, terms.TransportDays, int16s(terms.ReceivingDays))
	return err
}

// addReceiptDates sets each line's confirmed receipt date from its confirmed
// ship date: plus the transport days of the terms copied at confirm, rolled to
// their next receiving day (BR-27.2). An order without a copy (confirmed
// before customer terms existed) receives on the ship date.
func addReceiptDates(o *Order) {
	var terms domain.DeliveryTerms
	if o.ConfirmedDeliveryTerms != nil {
		terms = *o.ConfirmedDeliveryTerms
	}
	for i := range o.Lines {
		l := &o.Lines[i]
		if l.ConfirmedShipDate == nil {
			continue
		}
		ship, err := time.Parse(dayLayout, *l.ConfirmedShipDate)
		if err != nil {
			continue // the column is a date; this cannot happen
		}
		receipt := terms.ReceiptDate(ship).Format(dayLayout)
		l.ConfirmedReceiptDate = &receipt
	}
}
