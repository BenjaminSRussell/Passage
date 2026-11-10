package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

const (
	CodeAckStale        = "PRC-ACK-001"
	CodeAckState        = "PRC-ACK-002"
	CodeAckOpenProposal = "PRC-ACK-003"
	CodeAckAnswered     = "PRC-ACK-004"
	CodeAckDecided      = "PRC-ACK-005"
	CodeAckNotFound     = "PRC-ACK-NOT-FOUND"
)

type POAcknowledgement struct {
	ID                 string              `json:"id"`
	LegalEntityID      string              `json:"legalEntityId"`
	PurchaseOrderID    string              `json:"purchaseOrderId"`
	PONumber           string              `json:"poNumber"`
	VendorID           string              `json:"vendorId"`
	VersionNo          int                 `json:"versionNo"`
	Outcome            string              `json:"outcome"`
	Reason             string              `json:"reason,omitempty"`
	Changes            []domain.LineChange `json:"changes"`
	AcknowledgedBy     string              `json:"acknowledgedBy,omitempty"`
	AcknowledgedAt     time.Time           `json:"acknowledgedAt"`
	Decision           string              `json:"decision,omitempty"`
	DecidedBy          string              `json:"decidedBy,omitempty"`
	DecidedAt          *time.Time          `json:"decidedAt,omitempty"`
	DecisionNote       string              `json:"decisionNote,omitempty"`
	ResultingVersionNo int                 `json:"resultingVersionNo,omitempty"`
}

type AckFilter struct {
	PurchaseOrderID string
	Outcome         string
	OpenProposals   bool
}

type PurchaseOrderAckSummary struct {
	ID             string          `json:"id"`
	Number         string          `json:"number"`
	LegalEntityID  string          `json:"legalEntityId"`
	VendorID       string          `json:"vendorId"`
	Currency       string          `json:"currency"`
	ApprovalStatus string          `json:"approvalStatus"`
	VersionNo      int             `json:"versionNo"`
	Total          decimal.Decimal `json:"total"`
	LineCount      int             `json:"lineCount"`
	AckState       string          `json:"acknowledgement"`
	CreatedAt      time.Time       `json:"createdAt"`
}

const ackColumns = `a.id, a.legal_entity_id, a.purchase_order_id, o.number, a.vendor_id, a.version_no, a.outcome, a.reason,
	a.changes::text, coalesce(a.acknowledged_by::text, ''), a.acknowledged_at, coalesce(a.decision, ''),
	coalesce(a.decided_by::text, ''), a.decided_at, a.decision_note, coalesce(a.resulting_version_no, 0)`

const ackFrom = ` from purchase_order_ack a join purchase_order o on o.tenant_id = a.tenant_id and o.id = a.purchase_order_id`

// ackStateSQL is the state of the current version of the order o: NONE until the vendor answers it (a
// declined proposal leaves it NONE), ACCEPTED, REJECTED, or PROPOSAL_PENDING while a proposal waits.
const ackStateSQL = `coalesce((select case when a.outcome = 'CHANGES_PROPOSED' then 'PROPOSAL_PENDING' else a.outcome end
	from purchase_order_ack a where a.tenant_id = o.tenant_id and a.purchase_order_id = o.id and a.version_no = o.version_no
	and (a.outcome in ('ACCEPTED', 'REJECTED') or a.decision is null) order by a.id desc limit 1), 'NONE')`

func scanAck(row pgx.Row) (POAcknowledgement, error) {
	var a POAcknowledgement
	var changes string
	err := row.Scan(&a.ID, &a.LegalEntityID, &a.PurchaseOrderID, &a.PONumber, &a.VendorID, &a.VersionNo, &a.Outcome, &a.Reason,
		&changes, &a.AcknowledgedBy, &a.AcknowledgedAt, &a.Decision, &a.DecidedBy, &a.DecidedAt, &a.DecisionNote, &a.ResultingVersionNo)
	if err != nil {
		return a, err
	}
	a.Changes = []domain.LineChange{}
	return a, json.Unmarshal([]byte(changes), &a.Changes)
}

func loadAck(ctx context.Context, tx pgx.Tx, id string, lock bool) (POAcknowledgement, error) {
	p, _ := tenant.FromContext(ctx)
	suffix := ""
	if lock {
		suffix = " for update of a"
	}
	a, err := scanAck(tx.QueryRow(ctx, `select `+ackColumns+ackFrom+` where a.tenant_id = $1 and a.id = $2`+suffix, p.TenantID, id))
	if err != nil {
		return POAcknowledgement{}, notFound(err, CodeAckNotFound, "acknowledgement not found")
	}
	return a, nil
}

func ackConflict(code, detail string) error {
	return httpx.NewProblem(http.StatusConflict, code, detail)
}

func ackEvent(a POAcknowledgement) purchasingevents.POAckData {
	data := purchasingevents.POAckData{AckID: a.ID, POID: a.PurchaseOrderID, Number: a.PONumber, VersionNo: a.VersionNo,
		VendorID: a.VendorID, LegalEntityID: a.LegalEntityID, Outcome: a.Outcome, Reason: a.Reason, Decision: a.Decision,
		DecidedBy: a.DecidedBy, Note: a.DecisionNote, ResultingVersionNo: a.ResultingVersionNo}
	for _, c := range a.Changes {
		ev := purchasingevents.POAckChange{LineNo: c.LineNo}
		if c.Qty != nil {
			ev.Qty = c.Qty.String()
		}
		if c.UnitPrice != nil {
			ev.UnitPrice = c.UnitPrice.String()
		}
		if c.DeliveryDate != nil {
			ev.DeliveryDate = c.DeliveryDate.String()
		}
		data.Changes = append(data.Changes, ev)
	}
	return data
}

// AcknowledgePurchaseOrder records the vendor's answer to the current version of a CONFIRMED order that
// belongs to vendorID; an order of another vendor is 404. Only the latest version can be answered, a version
// is accepted or rejected once, and one proposal per order is open at a time. The idempotency key is the
// vendor's own, so another vendor cannot replay it.
func (s *Postgres) AcknowledgePurchaseOrder(ctx context.Context, vendorID, poID string, in domain.AckInput, idem Idem) (POAcknowledgement, error) {
	var out POAcknowledgement
	actor, _ := tenant.FromContext(ctx)
	if idem.Key != "" {
		idem.Key = "po-ack:" + vendorID + ":" + idem.Key
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		id := ids.New()
		existing, replay, err := claimKey(ctx, tx, idem, id)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadAck(ctx, tx, existing, false)
			return err
		}
		var locked int
		err = tx.QueryRow(ctx, `select 1 from purchase_order where tenant_id = $1 and id = $2 and vendor_id = $3 and version_no > 0 for update`,
			actor.TenantID, poID, vendorID).Scan(&locked)
		if err != nil {
			return notFound(err, CodePurchaseOrderNotFound, "purchase order not found")
		}
		po, err := loadPurchaseOrder(ctx, tx, poID)
		if err != nil {
			return err
		}
		if err := checkAckable(ctx, tx, po, in); err != nil {
			return err
		}
		changes, err := json.Marshal(append([]domain.LineChange{}, in.Changes...))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into purchase_order_ack (tenant_id, id, legal_entity_id, purchase_order_id, vendor_id, version_no,
			outcome, reason, changes, acknowledged_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			actor.TenantID, id, po.LegalEntityID, poID, vendorID, po.VersionNo, in.Outcome, in.Reason, string(changes), userOrNil(actor.UserID)); err != nil {
			return err
		}
		if out, err = loadAck(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "purchase_order_ack", id, ackEventType(in.Outcome), ackEvent(out))
	})
	return out, err
}

func ackEventType(outcome string) string {
	switch outcome {
	case domain.AckAccepted:
		return purchasingevents.POAckAccepted
	case domain.AckRejected:
		return purchasingevents.POAckRejected
	}
	return purchasingevents.POAckProposed
}

// checkAckable refuses an answer the order cannot take: not CONFIRMED, not its latest version, already
// answered, or a proposal still waiting. A proposal may only name open lines and must change something.
func checkAckable(ctx context.Context, tx pgx.Tx, po PurchaseOrder, in domain.AckInput) error {
	if po.ApprovalStatus != domain.POStatusConfirmed {
		return ackConflict(CodeAckState, "This order is not open for an answer right now. It is either being changed by the buyer or has been closed.")
	}
	if in.VersionNo != po.VersionNo {
		return ackConflict(CodeAckStale, fmt.Sprintf("This order has been changed since you opened it. Reload it and answer the current version, %d.", po.VersionNo))
	}
	p, _ := tenant.FromContext(ctx)
	var openProposal bool
	var answered bool
	err := tx.QueryRow(ctx, `select
		exists (select 1 from purchase_order_ack where tenant_id = $1 and purchase_order_id = $2 and outcome = 'CHANGES_PROPOSED' and decision is null),
		exists (select 1 from purchase_order_ack where tenant_id = $1 and purchase_order_id = $2 and version_no = $3 and outcome in ('ACCEPTED', 'REJECTED'))`,
		p.TenantID, po.ID, po.VersionNo).Scan(&openProposal, &answered)
	if err != nil {
		return err
	}
	switch {
	case openProposal:
		return ackConflict(CodeAckOpenProposal, "Your earlier proposal for this order is still waiting for the buyer. Wait for the buyer to answer it before sending another response.")
	case answered:
		return ackConflict(CodeAckAnswered, "You have already answered this version of the order.")
	}
	if in.Outcome != domain.AckChangesProposed {
		return nil
	}
	open := make(map[int]PurchaseOrderLine, len(po.Lines))
	for _, l := range po.Lines {
		if l.Status != domain.POLineCancelled {
			open[l.LineNo] = l
		}
	}
	for _, c := range in.Changes {
		l, ok := open[c.LineNo]
		switch {
		case !ok:
			return &domain.Error{Code: domain.CodeValidation, Detail: fmt.Sprintf("line %d is not an open line of this order", c.LineNo)}
		case (c.Qty == nil || c.Qty.Equal(l.Qty)) && (c.UnitPrice == nil || c.UnitPrice.Equal(l.UnitPrice)) &&
			(c.DeliveryDate == nil || c.DeliveryDate.Equal(l.DeliveryDate.Time)):
			return &domain.Error{Code: domain.CodeValidation, Detail: fmt.Sprintf("line %d: the proposal is the same as the order", c.LineNo)}
		}
	}
	return nil
}

// proposedOrder is cur with a proposal applied and priced again. A price change makes the line MANUAL so
// a later subsequent price change run does not move what the buyer agreed with the vendor.
func proposedOrder(cur PurchaseOrder, changes []domain.LineChange) (PurchaseOrder, error) {
	byNo := make(map[int]domain.LineChange, len(changes))
	for _, c := range changes {
		byNo[c.LineNo] = c
	}
	next := cur
	next.Lines = make([]PurchaseOrderLine, 0, len(cur.Lines))
	applied := 0
	for _, l := range cur.Lines {
		if l.Status == domain.POLineCancelled {
			continue
		}
		if c, ok := byNo[l.LineNo]; ok {
			applied++
			oldNet := l.Qty.Mul(l.UnitPrice)
			if c.Qty != nil {
				l.Qty = *c.Qty
			}
			if c.UnitPrice != nil {
				l.UnitPrice, l.PriceSource = *c.UnitPrice, "MANUAL"
			}
			if !oldNet.IsZero() && !l.TaxAmount.IsZero() {
				l.TaxAmount = l.TaxAmount.Mul(l.Qty.Mul(l.UnitPrice)).DivRound(oldNet, 12).RoundBank(money.MinorUnits(cur.Currency))
			}
			if c.DeliveryDate != nil {
				l.DeliveryDate = *c.DeliveryDate
			}
		}
		next.Lines = append(next.Lines, l)
	}
	if applied != len(changes) {
		return PurchaseOrder{}, &domain.Error{Code: domain.CodeValidation, Detail: "a line in the proposal is no longer open on the order"}
	}
	return next, priceLines(&next, cur.Charges)
}

// DecideAcknowledgement approves or declines the open proposal id. Approving writes the proposal as the next
// version of the order through the PO edit path (so change management and re-approval apply as for any
// edit) and fails, leaving the proposal open, when the order has moved on or a purchasing rule stops the
// change; declining leaves the order as it is and the version unanswered.
func (s *Postgres) DecideAcknowledgement(ctx context.Context, id string, approve bool, note string, idem Idem) (POAcknowledgement, error) {
	var out POAcknowledgement
	actor, _ := tenant.FromContext(ctx)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, id)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadAck(ctx, tx, existing, false)
			return err
		}
		peek, err := loadAck(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if err := lockPurchaseOrder(ctx, tx, peek.PurchaseOrderID); err != nil {
			return err
		}
		ack, err := loadAck(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if ack.Outcome != domain.AckChangesProposed || ack.Decision != "" {
			return ackConflict(CodeAckDecided, "This proposal has already been answered.")
		}
		decision := domain.AckDeclined
		if approve {
			decision = domain.AckApproved
		}
		if _, err := tx.Exec(ctx, `update purchase_order_ack set decision = $3, decided_by = $4, decided_at = now(), decision_note = $5
			where tenant_id = $1 and id = $2`, actor.TenantID, id, decision, userOrNil(actor.UserID), note); err != nil {
			return err
		}
		if approve {
			resulting, err := applyProposal(ctx, tx, ack)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update purchase_order_ack set resulting_version_no = $3 where tenant_id = $1 and id = $2`,
				actor.TenantID, id, nullableInt(resulting)); err != nil {
				return err
			}
		}
		if out, err = loadAck(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "purchase_order_ack", id, purchasingevents.POAckDecided, ackEvent(out))
	})
	return out, err
}

func applyProposal(ctx context.Context, tx pgx.Tx, ack POAcknowledgement) (int, error) {
	po, err := loadPurchaseOrder(ctx, tx, ack.PurchaseOrderID)
	if err != nil {
		return 0, err
	}
	if po.ApprovalStatus != domain.POStatusConfirmed || po.VersionNo != ack.VersionNo {
		return 0, ackConflict(CodeAckStale, "The order has changed since the vendor made this proposal, so it can no longer be applied. Decline it and ask the vendor to propose again.")
	}
	next, err := proposedOrder(po, ack.Changes)
	if err != nil {
		return 0, err
	}
	_, res, err := editLocked(ctx, tx, po, po.RowVersion, next, false)
	if res.reapproval {
		return 0, err
	}
	return res.versionNo, err
}

// supersedeAcks marks the proposals still open on versions older than the confirmed version no as SUPERSEDED
// (BR-20.9), so the vendor can answer the new version, and writes poack.decided.v1 for each.
func supersedeAcks(ctx context.Context, tx pgx.Tx, poID string, no int) error {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, `update purchase_order_ack set decision = 'SUPERSEDED', decided_at = now()
		where tenant_id = $1 and purchase_order_id = $2 and version_no < $3 and outcome = 'CHANGES_PROPOSED' and decision is null returning id`,
		p.TenantID, poID, no)
	if err != nil {
		return err
	}
	superseded, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range superseded {
		a, err := loadAck(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "purchase_order_ack", id, purchasingevents.POAckDecided, ackEvent(a)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Postgres) GetAcknowledgement(ctx context.Context, id string) (POAcknowledgement, error) {
	var out POAcknowledgement
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadAck(ctx, tx, id, false)
		return err
	})
	return out, err
}

func (s *Postgres) ListAcknowledgements(ctx context.Context, f AckFilter, after string, limit int) (Page[POAcknowledgement], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []POAcknowledgement
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+ackColumns+ackFrom+`
			where a.tenant_id = $1 and ($2 = '' or a.purchase_order_id::text = $2) and ($3 = '' or a.outcome = $3)
			and (not $4 or (a.outcome = 'CHANGES_PROPOSED' and a.decision is null)) and ($5::uuid is null or a.id < $5::uuid)
			order by a.id desc limit $6`, p.TenantID, f.PurchaseOrderID, f.Outcome, f.OpenProposals, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (POAcknowledgement, error) { return scanAck(row) })
		return err
	})
	if err != nil {
		return Page[POAcknowledgement]{}, err
	}
	if items == nil {
		items = []POAcknowledgement{}
	}
	return pageOf(items, limit, func(a POAcknowledgement) string { return a.ID }), nil
}

func (s *Postgres) ListAwaitingAcknowledgement(ctx context.Context, after string, limit int) (Page[PurchaseOrderAckSummary], error) {
	return s.listAckOrders(ctx, "", true, after, limit)
}

// ListVendorPurchaseOrders lists the orders sent to vendorID (confirmed at least once), newest first.
func (s *Postgres) ListVendorPurchaseOrders(ctx context.Context, vendorID, after string, limit int) (Page[PurchaseOrderAckSummary], error) {
	return s.listAckOrders(ctx, vendorID, false, after, limit)
}

func (s *Postgres) listAckOrders(ctx context.Context, vendorID string, awaiting bool, after string, limit int) (Page[PurchaseOrderAckSummary], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []PurchaseOrderAckSummary
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select * from (select o.id, o.number, o.legal_entity_id, o.vendor_id, o.currency,
			case when $2 <> '' then 'CONFIRMED' else o.approval_status end as approval_status, o.version_no,
			case when $2 <> '' then (v.snapshot->>'total')::numeric else o.total end as total,
			case when $2 <> '' then (select count(*) from jsonb_array_elements(v.snapshot->'lines') e where e->>'status' <> 'CANCELLED')
				else (select count(*) from purchase_order_line l where l.tenant_id = o.tenant_id and l.purchase_order_id = o.id) end as line_count,
			`+ackStateSQL+` as ack_state, o.created_at
			from purchase_order o
			join purchase_order_version v on v.tenant_id = o.tenant_id and v.purchase_order_id = o.id and v.version_no = o.version_no
			where o.tenant_id = $1 and o.version_no > 0 and ($2 = '' or (o.vendor_id::text = $2 and o.approval_status <> 'CANCELLED'))
			and (not $3 or o.approval_status = 'CONFIRMED') and ($4::uuid is null or o.id < $4::uuid)) s
			where (not $3 or ack_state = 'NONE') order by id desc limit $5`, p.TenantID, vendorID, awaiting, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (PurchaseOrderAckSummary, error) {
			var o PurchaseOrderAckSummary
			err := row.Scan(&o.ID, &o.Number, &o.LegalEntityID, &o.VendorID, &o.Currency, &o.ApprovalStatus, &o.VersionNo, &o.Total,
				&o.LineCount, &o.AckState, &o.CreatedAt)
			return o, err
		})
		return err
	})
	if err != nil {
		return Page[PurchaseOrderAckSummary]{}, err
	}
	if items == nil {
		items = []PurchaseOrderAckSummary{}
	}
	return pageOf(items, limit, func(o PurchaseOrderAckSummary) string { return o.ID }), nil
}

// GetVendorPurchaseOrder returns an order of vendorID that was sent to the vendor; any other order,
// including one of another vendor, is 404.
func (s *Postgres) GetVendorPurchaseOrder(ctx context.Context, vendorID, id string) (PurchaseOrder, error) {
	var out PurchaseOrder
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		if out, err = loadPurchaseOrder(ctx, tx, id); err != nil {
			return err
		}
		if out.VendorID != vendorID || out.VersionNo == 0 || out.ApprovalStatus == domain.POStatusCancelled {
			return httpx.NewProblem(http.StatusNotFound, CodePurchaseOrderNotFound, "purchase order not found")
		}
		if out.ApprovalStatus == domain.POStatusConfirmed {
			return nil
		}
		v, ok, err := loadVersion(ctx, tx, id, out.VersionNo)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("purchase order %s: confirmed version %d is missing", out.Number, out.VersionNo)
		}
		out = confirmedView(out, v.Snapshot)
		return nil
	})
	return out, err
}

// confirmedView is head with the commercial content of its confirmed snapshot, so a vendor never sees a
// change the buyer has not confirmed. The received quantity is live and still comes from head.
func confirmedView(head PurchaseOrder, snap domain.Snapshot) PurchaseOrder {
	minor := money.MinorUnits(head.Currency)
	received := make(map[int]decimal.Decimal, len(head.Lines))
	for _, l := range head.Lines {
		received[l.LineNo] = l.ReceivedQty
	}
	out := head
	out.ApprovalStatus = domain.POStatusConfirmed
	out.Lines = make([]PurchaseOrderLine, 0, len(snap.Lines))
	out.Charges = make([]PurchaseOrderCharge, 0, len(snap.Charges))
	out.Subtotal, out.DiscountTotal, out.ChargesTotal, out.TaxTotal, out.Total = decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, snap.Total
	for _, l := range snap.Lines {
		out.Lines = append(out.Lines, PurchaseOrderLine{LineNo: l.LineNo, ProductID: l.ProductID, Description: l.Description, Qty: l.Qty,
			Unit: l.Unit, UnitPrice: l.UnitPrice, PriceUnit: l.PriceUnit, DiscountPct: l.DiscountPct, LineAmount: l.LineAmount,
			TaxAmount: l.TaxAmount, DeliveryDate: l.DeliveryDate, Status: l.Status, ReceivedQty: received[l.LineNo]})
		if l.Status == domain.POLineCancelled || l.PriceUnit.IsZero() {
			continue
		}
		gross := l.Qty.Mul(l.UnitPrice).DivRound(l.PriceUnit, 12).RoundBank(minor)
		out.Subtotal = out.Subtotal.Add(gross)
		out.DiscountTotal = out.DiscountTotal.Add(gross.Sub(l.LineAmount))
		out.TaxTotal = out.TaxTotal.Add(l.TaxAmount)
	}
	for _, c := range snap.Charges {
		out.Charges = append(out.Charges, PurchaseOrderCharge{Calc: c.Calc, Value: c.Value, Amount: c.Amount, LineNo: c.LineNo})
		out.ChargesTotal = out.ChargesTotal.Add(c.Amount)
	}
	return out
}
