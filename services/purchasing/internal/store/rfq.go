package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// CodeRFQNotFound is the 404 code for an unknown RFQ.
const CodeRFQNotFound = "PRC-RFQ-NOT-FOUND"

// AwardedOrder is a PO built from awards. ReplyIDs[i] is the RFQ reply that
// priced Order.Lines[i]; it is kept on purchase_order_line.rfq_reply_id.
type AwardedOrder struct {
	Order    PurchaseOrder
	ReplyIDs []string
}

// RFQSummary is a list row: the header with line, invitation and reply counts.
type RFQSummary struct {
	ID            string    `json:"id"`
	Number        string    `json:"number"`
	LegalEntityID string    `json:"legalEntityId"`
	Status        string    `json:"status"`
	ReplyDeadline time.Time `json:"replyDeadline"`
	RankingRule   string    `json:"rankingRule"`
	LineCount     int       `json:"lineCount"`
	VendorCount   int       `json:"vendorCount"`
	ReplyCount    int       `json:"replyCount"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
}

// RFQFilter narrows the RFQ list; Statuses empty means every status.
type RFQFilter struct {
	Statuses []string
}

// CreateRFQ stores a DRAFT RFQ with its lines (FR-17.4).
func (s *Postgres) CreateRFQ(ctx context.Context, in domain.RFQ, idem Idem) (domain.RFQ, error) {
	in.ID = ids.New()
	var out domain.RFQ
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadRFQ(ctx, tx, existing)
			return err
		}
		out, err = insertRFQ(ctx, tx, in)
		return err
	})
	return out, err
}

func insertRFQ(ctx context.Context, tx pgx.Tx, in domain.RFQ) (domain.RFQ, error) {
	p, _ := tenant.FromContext(ctx)
	var seq int64
	if err := tx.QueryRow(ctx, `select nextval('purchase_rfq_number_seq')`).Scan(&seq); err != nil {
		return domain.RFQ{}, err
	}
	number := fmt.Sprintf("RFQ-%08d", seq)
	if _, err := tx.Exec(ctx, `insert into purchase_rfq (tenant_id, id, legal_entity_id, number, status, reply_deadline, ranking_rule, version)
		values ($1, $2, $3, $4, 'DRAFT', $5, $6, 1)`, p.TenantID, in.ID, in.LegalEntityID, number, in.ReplyDeadline, in.RankingRule); err != nil {
		return domain.RFQ{}, err
	}
	for i, l := range in.Lines {
		if _, err := tx.Exec(ctx, `insert into purchase_rfq_line (tenant_id, rfq_id, line_no, id, product_id, description, qty, unit, required_date)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.TenantID, in.ID, i+1, ids.New(), nullable(l.ProductID), l.Description,
			l.Qty, l.Unit, optDay(l.RequiredDate)); err != nil {
			return domain.RFQ{}, err
		}
	}
	return loadRFQ(ctx, tx, in.ID)
}

// GetRFQ returns one RFQ with its lines, invitations and replies.
func (s *Postgres) GetRFQ(ctx context.Context, id string) (domain.RFQ, error) {
	var out domain.RFQ
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadRFQ(ctx, tx, id)
		return err
	})
	return out, err
}

// ListRFQs returns RFQ headers newest first, after the cursor id.
func (s *Postgres) ListRFQs(ctx context.Context, f RFQFilter, after string, limit int) (Page[RFQSummary], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	statuses := f.Statuses
	if statuses == nil {
		statuses = []string{}
	}
	var items []RFQSummary
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select r.id, r.number, r.legal_entity_id, r.status, r.reply_deadline, r.ranking_rule,
			(select count(*) from purchase_rfq_line l where l.tenant_id = r.tenant_id and l.rfq_id = r.id),
			(select count(*) from purchase_rfq_invitation i where i.tenant_id = r.tenant_id and i.rfq_id = r.id),
			(select count(*) from purchase_rfq_reply y where y.tenant_id = r.tenant_id and y.rfq_id = r.id),
			r.version, r.created_at
			from purchase_rfq r
			where r.tenant_id = $1 and (cardinality($2::text[]) = 0 or r.status = any($2::text[]))
			and ($3::uuid is null or r.id < $3::uuid)
			order by r.id desc limit $4`, p.TenantID, statuses, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RFQSummary, error) {
			var r RFQSummary
			err := row.Scan(&r.ID, &r.Number, &r.LegalEntityID, &r.Status, &r.ReplyDeadline, &r.RankingRule,
				&r.LineCount, &r.VendorCount, &r.ReplyCount, &r.Version, &r.CreatedAt)
			return r, err
		})
		return err
	})
	if err != nil {
		return Page[RFQSummary]{}, err
	}
	if items == nil {
		items = []RFQSummary{}
	}
	return pageOf(items, limit, func(r RFQSummary) string { return r.ID }), nil
}

// SendRFQ invites vendors and moves a DRAFT RFQ to SENT (FR-17.4), writing
// rfq.sent.v1 in the same transaction.
func (s *Postgres) SendRFQ(ctx context.Context, id string, ifMatch int, vendorIDs []string, idem Idem) (domain.RFQ, error) {
	return s.actOnRFQ(ctx, id, ifMatch, domain.RFQActionSend, vendorIDs, idem)
}

// ActOnRFQ applies close or cancel to an RFQ under optimistic concurrency.
func (s *Postgres) ActOnRFQ(ctx context.Context, id string, ifMatch int, action string, idem Idem) (domain.RFQ, error) {
	if action != domain.RFQActionClose && action != domain.RFQActionCancel {
		return domain.RFQ{}, fmt.Errorf("ActOnRFQ: action %q has its own method", action)
	}
	return s.actOnRFQ(ctx, id, ifMatch, action, nil, idem)
}

func (s *Postgres) actOnRFQ(ctx context.Context, id string, ifMatch int, action string, vendorIDs []string, idem Idem) (domain.RFQ, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RFQ
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, replayed, err := lockForAction(ctx, tx, id, ifMatch, idem)
		if err != nil || replayed {
			out = cur
			return err
		}
		next, err := domain.NextRFQStatus(cur.Status, action)
		if err != nil {
			return stateConflict(err)
		}
		if action == domain.RFQActionSend {
			for _, v := range vendorIDs {
				if _, err := tx.Exec(ctx, `insert into purchase_rfq_invitation (tenant_id, rfq_id, vendor_id, status)
					values ($1, $2, $3, 'INVITED') on conflict (tenant_id, rfq_id, vendor_id) do nothing`, p.TenantID, id, v); err != nil {
					return err
				}
			}
		}
		if err := bumpRFQ(ctx, tx, id, next, cur.Version); err != nil {
			return err
		}
		if out, err = loadRFQ(ctx, tx, id); err != nil {
			return err
		}
		if action == domain.RFQActionSend {
			return enqueue(ctx, tx, "rfq", id, purchasingevents.RFQSent, rfqSentData(out, vendorIDs))
		}
		return nil
	})
	return out, err
}

func rfqSentData(r domain.RFQ, vendorIDs []string) purchasingevents.RFQSentData {
	data := purchasingevents.RFQSentData{RFQID: r.ID, Number: r.Number, LegalEntityID: r.LegalEntityID, VendorIDs: vendorIDs,
		ReplyDeadline: r.ReplyDeadline.UTC().Format(time.RFC3339), Lines: make([]purchasingevents.RFQLineData, 0, len(r.Lines))}
	for _, l := range r.Lines {
		line := purchasingevents.RFQLineData{LineNo: l.LineNo, LineID: l.ID, ProductID: l.ProductID, Description: l.Description,
			Qty: l.Qty.String(), Unit: l.Unit}
		if l.RequiredDate != nil {
			line.RequiredDate = l.RequiredDate.String()
		}
		data.Lines = append(data.Lines, line)
	}
	return data
}

// RecordRFQReplies stores a vendor's replies to lines of an RFQ it was invited
// to (FR-17.4). A reply received after the deadline is stored with late=true
// (BR-17.11). A revision replaces the vendor's earlier reply to the line,
// except that a late revision cannot replace an on-time reply (PRC-RFQ-006).
// The first reply moves a SENT RFQ to REPLIES_OPEN.
func (s *Postgres) RecordRFQReplies(ctx context.Context, rfqID, vendorID string, replies []domain.RFQReply, idem Idem) (domain.RFQ, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RFQ
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, rfqID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadRFQ(ctx, tx, existing)
			return err
		}
		if err := lockRFQ(ctx, tx, rfqID); err != nil {
			return err
		}
		cur, err := loadRFQ(ctx, tx, rfqID)
		if err != nil {
			return err
		}
		if err := domain.CanRecordReply(cur.Status); err != nil {
			return stateConflict(err)
		}
		if !invited(cur, vendorID) {
			return &domain.Error{Code: domain.CodeRFQNotInvited, Detail: "vendor " + vendorID + " was not invited to RFQ " + cur.Number}
		}
		onLine := make(map[string]bool, len(cur.Lines))
		for _, l := range cur.Lines {
			onLine[l.ID] = true
		}
		prior := make(map[string]*domain.RFQReply)
		for i := range cur.Replies {
			if r := &cur.Replies[i]; r.VendorID == vendorID {
				prior[r.LineID] = r
			}
		}
		for _, r := range replies {
			if !onLine[r.LineID] {
				return &domain.Error{Code: domain.CodeValidation, Detail: "line " + r.LineID + " is not on RFQ " + cur.Number}
			}
			late := domain.IsLate(cur.ReplyDeadline, r.ReceivedAt)
			if err := domain.CheckRevision(prior[r.LineID], late); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into purchase_rfq_reply (tenant_id, id, rfq_id, line_id, vendor_id, unit_price, price_unit,
				currency, lead_time_days, valid_until, notes, received_at, late)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
				on conflict (tenant_id, rfq_id, line_id, vendor_id) do update set unit_price = excluded.unit_price,
				price_unit = excluded.price_unit, currency = excluded.currency, lead_time_days = excluded.lead_time_days,
				valid_until = excluded.valid_until, notes = excluded.notes, received_at = excluded.received_at, late = excluded.late`,
				p.TenantID, ids.New(), rfqID, r.LineID, vendorID, r.UnitPrice, r.PriceUnit, r.Currency, r.LeadTimeDays,
				optDay(r.ValidUntil), r.Notes, r.ReceivedAt, late); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update purchase_rfq_invitation set status = 'REPLIED' where tenant_id = $1 and rfq_id = $2 and vendor_id = $3`,
			p.TenantID, rfqID, vendorID); err != nil {
			return err
		}
		next := cur.Status
		if next == domain.RFQSent {
			next = domain.RFQRepliesOpen
		}
		if err := bumpRFQ(ctx, tx, rfqID, next, cur.Version); err != nil {
			return err
		}
		out, err = loadRFQ(ctx, tx, rfqID)
		return err
	})
	return out, err
}

// AwardRFQ creates the priced DRAFT POs built from the awards, links each PO
// line to its reply, marks the winning replies (with the override reason of a
// late one), moves the RFQ to AWARDED and writes rfq.awarded.v1, all in one
// transaction (FR-17.4). The awards are checked again against the locked RFQ
// with CheckAward. A replay with the same key returns the RFQ and the POs the
// first request created.
func (s *Postgres) AwardRFQ(ctx context.Context, id string, ifMatch int, awards []domain.Award, orders []AwardedOrder, today domain.Day, idem Idem) (domain.RFQ, []PurchaseOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RFQ
	pos := []PurchaseOrder{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, replayed, err := lockForAction(ctx, tx, id, ifMatch, idem)
		if err != nil {
			return err
		}
		if replayed {
			out = cur
			pos, err = loadAwardedOrders(ctx, tx, id)
			return err
		}
		if err := domain.CheckAward(cur, awards, today); err != nil {
			return stateConflict(err)
		}
		if err := checkOrdersMatchAwards(awards, orders); err != nil {
			return err
		}
		poByReply := make(map[string]string, len(awards))
		for _, ao := range orders {
			order := ao.Order
			order.ID = ids.New()
			po, err := insertPurchaseOrder(ctx, tx, order)
			if err != nil {
				return err
			}
			for i, replyID := range ao.ReplyIDs {
				poByReply[replyID] = po.ID
				if _, err := tx.Exec(ctx, `update purchase_order_line set rfq_reply_id = $4
					where tenant_id = $1 and purchase_order_id = $2 and line_no = $3`, p.TenantID, po.ID, order.Lines[i].LineNo, replyID); err != nil {
					return err
				}
			}
			pos = append(pos, po)
		}
		replies := make(map[string]domain.RFQReply, len(cur.Replies))
		for _, r := range cur.Replies {
			replies[r.ID] = r
		}
		lineNo := make(map[string]int, len(cur.Lines))
		for _, l := range cur.Lines {
			lineNo[l.ID] = l.LineNo
		}
		data := purchasingevents.RFQAwardedData{RFQID: id, Number: cur.Number, LegalEntityID: cur.LegalEntityID}
		for _, a := range awards {
			rp := replies[a.ReplyID]
			reason := ""
			if rp.Late {
				reason = a.OverrideReason
			}
			if _, err := tx.Exec(ctx, `update purchase_rfq_reply set awarded = true, override_reason = $3 where tenant_id = $1 and id = $2`,
				p.TenantID, a.ReplyID, nullable(reason)); err != nil {
				return err
			}
			data.Awards = append(data.Awards, purchasingevents.RFQAwardedLine{LineNo: lineNo[a.LineID], LineID: a.LineID, ReplyID: a.ReplyID,
				VendorID: rp.VendorID, UnitPrice: rp.UnitPrice.String(), PriceUnit: rp.PriceUnit.String(), Currency: rp.Currency,
				LeadTimeDays: rp.LeadTimeDays, PurchaseOrderID: poByReply[a.ReplyID], OverrideReason: reason})
		}
		if err := bumpRFQ(ctx, tx, id, domain.RFQAwarded, cur.Version); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "rfq", id, purchasingevents.RFQAwarded, data); err != nil {
			return err
		}
		out, err = loadRFQ(ctx, tx, id)
		return err
	})
	return out, pos, err
}

// checkOrdersMatchAwards guards against orders built from another read of the
// RFQ: every awarded reply must price exactly one PO line.
func checkOrdersMatchAwards(awards []domain.Award, orders []AwardedOrder) error {
	priced := map[string]int{}
	for _, o := range orders {
		if len(o.ReplyIDs) != len(o.Order.Lines) {
			return fmt.Errorf("award order has %d lines but %d reply links", len(o.Order.Lines), len(o.ReplyIDs))
		}
		for _, r := range o.ReplyIDs {
			priced[r]++
		}
	}
	if len(priced) != len(awards) {
		return fmt.Errorf("award orders price %d replies, want %d", len(priced), len(awards))
	}
	for _, a := range awards {
		if priced[a.ReplyID] != 1 {
			return fmt.Errorf("reply %s priced %d PO lines, want 1", a.ReplyID, priced[a.ReplyID])
		}
	}
	return nil
}

// lockForAction claims the idempotency key, locks the RFQ and checks If-Match.
// On a replay it returns the RFQ as it is now and replayed = true.
func lockForAction(ctx context.Context, tx pgx.Tx, id string, ifMatch int, idem Idem) (domain.RFQ, bool, error) {
	existing, replay, err := claimKey(ctx, tx, idem, id)
	if err != nil {
		return domain.RFQ{}, false, err
	}
	if replay {
		out, err := loadRFQ(ctx, tx, existing)
		return out, true, err
	}
	if err := lockRFQ(ctx, tx, id); err != nil {
		return domain.RFQ{}, false, err
	}
	cur, err := loadRFQ(ctx, tx, id)
	if err != nil {
		return domain.RFQ{}, false, err
	}
	if cur.Version != ifMatch {
		return domain.RFQ{}, false, httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
			fmt.Sprintf("RFQ is at version %d, not %d; reload and retry", cur.Version, ifMatch))
	}
	return cur, false, nil
}

// stateConflict turns a PRC-RFQ-STATE domain error into a 409; other errors
// pass through (domain rule errors become 422 in the API).
func stateConflict(err error) error {
	var de *domain.Error
	if errors.As(err, &de) && de.Code == domain.CodeRFQState {
		return httpx.NewProblem(http.StatusConflict, de.Code, de.Detail)
	}
	return err
}

// bumpRFQ sets the status and increments the version, guarded by the version read.
func bumpRFQ(ctx context.Context, tx pgx.Tx, id, status string, version int) error {
	p, _ := tenant.FromContext(ctx)
	tag, err := tx.Exec(ctx, `update purchase_rfq set status = $3, version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2 and version = $4`, p.TenantID, id, status, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "RFQ changed since it was read; reload and retry")
	}
	return nil
}

func lockRFQ(ctx context.Context, tx pgx.Tx, id string) error {
	p, _ := tenant.FromContext(ctx)
	var one int
	return notFound(tx.QueryRow(ctx, `select 1 from purchase_rfq where tenant_id = $1 and id = $2 for update`,
		p.TenantID, id).Scan(&one), CodeRFQNotFound, "RFQ not found")
}

func invited(r domain.RFQ, vendorID string) bool {
	for _, inv := range r.Invitations {
		if inv.VendorID == vendorID {
			return true
		}
	}
	return false
}

func loadRFQ(ctx context.Context, tx pgx.Tx, id string) (domain.RFQ, error) {
	p, _ := tenant.FromContext(ctx)
	var o domain.RFQ
	err := tx.QueryRow(ctx, `select id, number, legal_entity_id, status, reply_deadline, ranking_rule, version, created_at, updated_at
		from purchase_rfq where tenant_id = $1 and id = $2`, p.TenantID, id).
		Scan(&o.ID, &o.Number, &o.LegalEntityID, &o.Status, &o.ReplyDeadline, &o.RankingRule, &o.Version, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return domain.RFQ{}, notFound(err, CodeRFQNotFound, "RFQ not found")
	}

	lines, err := tx.Query(ctx, `select id, line_no, coalesce(product_id::text, ''), description, qty, unit, required_date
		from purchase_rfq_line where tenant_id = $1 and rfq_id = $2 order by line_no`, p.TenantID, id)
	if err != nil {
		return domain.RFQ{}, err
	}
	o.Lines, err = pgx.CollectRows(lines, func(row pgx.CollectableRow) (domain.RFQLine, error) {
		var l domain.RFQLine
		var due *time.Time
		err := row.Scan(&l.ID, &l.LineNo, &l.ProductID, &l.Description, &l.Qty, &l.Unit, &due)
		l.RequiredDate = dayOrNil(due)
		return l, err
	})
	if err != nil {
		return domain.RFQ{}, err
	}

	invs, err := tx.Query(ctx, `select vendor_id, status, sent_at from purchase_rfq_invitation
		where tenant_id = $1 and rfq_id = $2 order by sent_at, vendor_id`, p.TenantID, id)
	if err != nil {
		return domain.RFQ{}, err
	}
	o.Invitations, err = pgx.CollectRows(invs, func(row pgx.CollectableRow) (domain.RFQInvitation, error) {
		var inv domain.RFQInvitation
		err := row.Scan(&inv.VendorID, &inv.Status, &inv.SentAt)
		return inv, err
	})
	if err != nil {
		return domain.RFQ{}, err
	}

	replies, err := tx.Query(ctx, `select id, line_id, vendor_id, unit_price, price_unit, currency, lead_time_days, valid_until,
		notes, received_at, late, awarded, coalesce(override_reason, '')
		from purchase_rfq_reply where tenant_id = $1 and rfq_id = $2 order by received_at, id`, p.TenantID, id)
	if err != nil {
		return domain.RFQ{}, err
	}
	o.Replies, err = pgx.CollectRows(replies, func(row pgx.CollectableRow) (domain.RFQReply, error) {
		var r domain.RFQReply
		var validUntil *time.Time
		err := row.Scan(&r.ID, &r.LineID, &r.VendorID, &r.UnitPrice, &r.PriceUnit, &r.Currency, &r.LeadTimeDays,
			&validUntil, &r.Notes, &r.ReceivedAt, &r.Late, &r.Awarded, &r.OverrideReason)
		r.ValidUntil = dayOrNil(validUntil)
		return r, err
	})
	if err != nil {
		return domain.RFQ{}, err
	}
	if o.Lines == nil {
		o.Lines = []domain.RFQLine{}
	}
	if o.Invitations == nil {
		o.Invitations = []domain.RFQInvitation{}
	}
	if o.Replies == nil {
		o.Replies = []domain.RFQReply{}
	}
	return o, nil
}

// loadAwardedOrders returns the POs whose lines were priced from the RFQ's replies.
func loadAwardedOrders(ctx context.Context, tx pgx.Tx, rfqID string) ([]PurchaseOrder, error) {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, `select distinct l.purchase_order_id::text from purchase_order_line l
		join purchase_rfq_reply r on r.tenant_id = l.tenant_id and r.id = l.rfq_reply_id
		where l.tenant_id = $1 and r.rfq_id = $2 order by 1`, p.TenantID, rfqID)
	if err != nil {
		return nil, err
	}
	poIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make([]PurchaseOrder, 0, len(poIDs))
	for _, id := range poIDs {
		po, err := loadPurchaseOrder(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, po)
	}
	return out, nil
}
