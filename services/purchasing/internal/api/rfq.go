package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// RFQ duties (PRD 3.17.17): RFQMaintain creates, sends, records replies,
// closes and cancels; RFQAward awards. Reading an RFQ and its comparison
// needs either.
const (
	dutyRFQMaintain = "RFQMaintain"
	dutyRFQAward    = "RFQAward"
)

// maxRFQLines caps the lines of one RFQ, as for requisitions.
const maxRFQLines = 500

// maxNotes caps the free-text notes of one reply line.
const maxNotes = 2000

// anyDuty allows a request when the principal holds one of the duties.
type anyDuty []string

// Allow implements authz.Policy.
func (d anyDuty) Allow(_ context.Context, p tenant.Principal, _, _ string) (bool, error) {
	for _, duty := range d {
		if p.HasDuty(duty) {
			return true, nil
		}
	}
	return false, nil
}

// rfqActionDuty is the duty an RFQ custom method needs.
func rfqActionDuty(action string) string {
	if action == domain.RFQActionAward {
		return dutyRFQAward
	}
	return dutyRFQMaintain
}

func isRFQAction(action string) bool {
	for _, a := range domain.RFQActions {
		if a == action {
			return true
		}
	}
	return false
}

// rfqBody is the body of POST /rfqs. A sealed RFQ hides its replies until the
// deadline (BR-17.16); criteria are its scoring group (FR-17.19).
type rfqBody struct {
	LegalEntityID string             `json:"legalEntityId"`
	ReplyDeadline time.Time          `json:"replyDeadline"`
	RankingRule   string             `json:"rankingRule,omitempty"`
	Sealed        bool               `json:"sealed,omitempty"`
	Criteria      []rfqCriterionBody `json:"criteria,omitempty"`
	Lines         []rfqLineBody      `json:"lines"`
}

type rfqCriterionBody struct {
	Name   string `json:"name"`
	Weight string `json:"weight"`
}

// rfqScoresBody is the body of PUT /rfqs/{id}/scores/{vendorId}.
type rfqScoresBody struct {
	Scores []rfqScoreBody `json:"scores"`
}

type rfqScoreBody struct {
	CriterionID string `json:"criterionId"`
	Score       int    `json:"score"`
}

type rfqLineBody struct {
	ProductID    string      `json:"productId,omitempty"`
	Description  string      `json:"description,omitempty"`
	Qty          string      `json:"qty"`
	Unit         string      `json:"unit"`
	RequiredDate *domain.Day `json:"requiredDate,omitempty"`
}

// rfqActionBody carries the arguments of :send (vendorIds) and :award (awards).
type rfqActionBody struct {
	VendorIDs []string       `json:"vendorIds,omitempty"`
	Awards    []domain.Award `json:"awards,omitempty"`
}

// rfqReplyBody is the body of POST /rfqs/{id}/replies: one vendor's answer to
// some lines, in one currency.
type rfqReplyBody struct {
	VendorID string         `json:"vendorId"`
	Currency string         `json:"currency"`
	Replies  []rfqReplyLine `json:"replies"`
}

type rfqReplyLine struct {
	LineID       string      `json:"lineId"`
	UnitPrice    string      `json:"unitPrice"`
	PriceUnit    string      `json:"priceUnit,omitempty"`
	LeadTimeDays int         `json:"leadTimeDays"`
	ValidUntil   *domain.Day `json:"validUntil,omitempty"`
	Notes        string      `json:"notes,omitempty"`
}

// rfqReplyResult is the RFQ after replies were recorded, with a PRC-RFQ-003
// warning for each line replied to after the deadline (BR-17.11).
type rfqReplyResult struct {
	domain.RFQ
	Warnings []store.Warning `json:"warnings,omitempty"`
}

// rfqComparedLine is one line of the compare view: ranked candidates and the
// replies kept out of the ranking with their reason (BR-17.11).
type rfqComparedLine struct {
	LineID      string                 `json:"lineId"`
	LineNo      int                    `json:"lineNo"`
	ProductID   string                 `json:"productId,omitempty"`
	Description string                 `json:"description,omitempty"`
	Qty         decimal.Decimal        `json:"qty"`
	Unit        string                 `json:"unit"`
	Ranked      []domain.RankedReply   `json:"ranked"`
	Excluded    []domain.ExcludedReply `json:"excluded"`
}

type rfqComparison struct {
	RFQID       string            `json:"rfqId"`
	RankingRule string            `json:"rankingRule"`
	Lines       []rfqComparedLine `json:"lines"`
}

// rfqAwardResult is the response of :award: the RFQ and the POs it created.
type rfqAwardResult struct {
	RFQ            domain.RFQ            `json:"rfq"`
	PurchaseOrders []store.PurchaseOrder `json:"purchaseOrders"`
}

// createRFQ handles POST /rfqs: a DRAFT RFQ with its lines (FR-17.4).
func (a *API) createRFQ(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body rfqBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	in, err := buildRFQ(body, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, in.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateRFQ(r.Context(), in, store.Idem{Key: key, Hash: hashBody("rfq.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqsCreated.Add(1)
	a.writeRFQ(w, r, http.StatusCreated, out)
}

// present is the RFQ as the caller may see it now (BR-17.16): a sealed RFQ's
// replies and scores are hidden before the deadline, and the first response
// after the deadline that shows them logs the opening (AC-17.14).
func (a *API) present(ctx context.Context, rfq domain.RFQ) (domain.RFQ, error) {
	now := a.now().UTC()
	if rfq.Sealed && rfq.OpenedAt == nil && !domain.SealedAt(rfq, now) {
		p, _ := tenant.FromContext(ctx)
		opened, err := a.store.OpenSealedRFQ(ctx, rfq.ID, p.UserID, now)
		if err != nil {
			return domain.RFQ{}, err
		}
		a.sealedRFQsOpened.Add(1)
		slog.InfoContext(ctx, "sealed RFQ opened", "rfq", rfq.Number, "user", p.UserID, "at", now.Format(time.RFC3339))
		rfq.OpenedAt, rfq.OpenedBy = opened.OpenedAt, opened.OpenedBy
	}
	return domain.Redact(rfq, now), nil
}

// writeRFQ answers with the presented RFQ and its version as the ETag.
func (a *API) writeRFQ(w http.ResponseWriter, r *http.Request, status int, rfq domain.RFQ) {
	out, err := a.present(r.Context(), rfq)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, status, out)
}

// buildRFQ validates a create request. The deadline must lie after now.
func buildRFQ(body rfqBody, now time.Time) (domain.RFQ, error) {
	if err := uuidField("legalEntityId", body.LegalEntityID, true); err != nil {
		return domain.RFQ{}, err
	}
	if body.ReplyDeadline.IsZero() {
		return domain.RFQ{}, invalid("replyDeadline", "is required")
	}
	if !body.ReplyDeadline.After(now) {
		return domain.RFQ{}, invalid("replyDeadline", "must be in the future")
	}
	rule := body.RankingRule
	if rule == "" {
		rule = domain.RankLowestPrice
	}
	if domain.ValidRankingRule(rule) != nil {
		return domain.RFQ{}, invalid("rankingRule", "must be LOWEST_PRICE, LEAD_TIME or WEIGHTED_SCORE")
	}
	criteria := make([]domain.ScoringCriterion, 0, len(body.Criteria))
	for i, c := range body.Criteria {
		weight, err := decimalField("criteria["+strconv.Itoa(i)+"].weight", c.Weight, "")
		if err != nil {
			return domain.RFQ{}, err
		}
		criteria = append(criteria, domain.ScoringCriterion{Name: strings.TrimSpace(c.Name), Weight: weight})
	}
	if err := domain.ValidateCriteria(rule, criteria); err != nil {
		return domain.RFQ{}, invalid("criteria", err.(*domain.Error).Detail)
	}
	if len(body.Lines) == 0 {
		return domain.RFQ{}, invalid("lines", "needs at least one line")
	}
	if len(body.Lines) > maxRFQLines {
		return domain.RFQ{}, invalid("lines", "may have at most "+strconv.Itoa(maxRFQLines)+" lines")
	}
	in := domain.RFQ{LegalEntityID: body.LegalEntityID, ReplyDeadline: body.ReplyDeadline.UTC(), RankingRule: rule,
		Sealed: body.Sealed, Criteria: criteria, Lines: make([]domain.RFQLine, 0, len(body.Lines))}
	for i, l := range body.Lines {
		field := func(n string) string { return "lines[" + strconv.Itoa(i) + "]." + n }
		if err := uuidField(field("productId"), l.ProductID, false); err != nil {
			return domain.RFQ{}, err
		}
		desc := strings.TrimSpace(l.Description)
		if l.ProductID == "" && desc == "" {
			return domain.RFQ{}, invalid(field("productId"), "or description is required")
		}
		qty, err := decimalField(field("qty"), l.Qty, "")
		if err != nil {
			return domain.RFQ{}, err
		}
		if qty.Sign() <= 0 {
			return domain.RFQ{}, invalid(field("qty"), "must be positive")
		}
		unit := strings.TrimSpace(l.Unit)
		if unit == "" {
			return domain.RFQ{}, invalid(field("unit"), "is required")
		}
		in.Lines = append(in.Lines, domain.RFQLine{ProductID: l.ProductID, Description: desc, Qty: qty, Unit: unit,
			RequiredDate: l.RequiredDate})
	}
	return in, nil
}

// listRFQs handles GET /rfqs?status=SENT,REPLIES_OPEN: headers newest first.
func (a *API) listRFQs(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var f store.RFQFilter
	if raw := r.URL.Query().Get("status"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			switch s {
			case domain.RFQDraft, domain.RFQSent, domain.RFQRepliesOpen, domain.RFQClosed, domain.RFQAwarded, domain.RFQCancelled:
				f.Statuses = append(f.Statuses, s)
			default:
				fail(w, r, invalid("status", "must be a comma-separated list of DRAFT, SENT, REPLIES_OPEN, CLOSED, AWARDED, CANCELLED"))
				return
			}
		}
	}
	page, err := a.store.ListRFQs(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// getRFQ handles GET /rfqs/{id} (ETag = version).
func (a *API) getRFQ(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetRFQ(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.writeRFQ(w, r, http.StatusOK, out)
}

// rfqAction handles POST /rfqs/{id}:send|close|cancel|award. Each needs
// Idempotency-Key and If-Match; award needs RFQAward, the others RFQMaintain.
func (a *API) rfqAction(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || !isRFQAction(action) || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown RFQ action"))
		return
	}
	if duty := rfqActionDuty(action); !p.HasDuty(duty) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeScope, "missing duty "+duty))
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body rfqActionBody
	if action == domain.RFQActionSend || action == domain.RFQActionAward {
		if err := httpx.Decode(r, &body); err != nil {
			fail(w, r, err)
			return
		}
	}
	idem := store.Idem{Key: key, Hash: hashBody("rfq."+action, map[string]any{"id": id, "ifMatch": want, "body": body})}
	var out domain.RFQ
	switch action {
	case domain.RFQActionSend:
		vendors, verr := uniqueIDs("vendorIds", body.VendorIDs)
		if verr != nil {
			fail(w, r, verr)
			return
		}
		out, err = a.store.SendRFQ(r.Context(), id, want, vendors, idem)
		if err == nil {
			a.rfqsSent.Add(1)
		}
	case domain.RFQActionAward:
		a.awardRFQ(w, r, id, want, body.Awards, idem)
		return
	default:
		out, err = a.store.ActOnRFQ(r.Context(), id, want, action, idem)
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	a.writeRFQ(w, r, http.StatusOK, out)
}

// awardRFQ checks the awards against the RFQ, builds one priced DRAFT PO per
// winning vendor and currency, and hands them to the store, which checks the
// awards again under the row lock and writes everything in one transaction
// (FR-17.4). When the RFQ is no longer at If-Match no orders are built: the
// store either replays an earlier award with the same key or refuses with 409.
func (a *API) awardRFQ(w http.ResponseWriter, r *http.Request, id string, want int, awards []domain.Award, idem store.Idem) {
	for i, aw := range awards {
		field := func(n string) string { return "awards[" + strconv.Itoa(i) + "]." + n }
		if err := uuidField(field("lineId"), aw.LineID, true); err != nil {
			fail(w, r, err)
			return
		}
		if err := uuidField(field("replyId"), aw.ReplyID, true); err != nil {
			fail(w, r, err)
			return
		}
	}
	cur, err := a.store.GetRFQ(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	today := a.today()
	var orders []store.AwardedOrder
	if cur.Version == want {
		// The deadline of an RFQ never changes, so a sealed RFQ that is open
		// here is still open when the store writes the award.
		if err := domain.CheckSealedView(cur, a.now()); err != nil {
			fail(w, r, err)
			return
		}
		if err := domain.CheckAward(cur, awards, today); err != nil {
			fail(w, r, rfqStateConflict(err))
			return
		}
		if orders, err = buildAwardOrders(cur, awards, today); err != nil {
			fail(w, r, err)
			return
		}
	}
	out, pos, err := a.store.AwardRFQ(r.Context(), id, want, awards, orders, today, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqsAwarded.Add(1)
	a.ordersCreated.Add(int64(len(orders)))
	if out, err = a.present(r.Context(), out); err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, rfqAwardResult{RFQ: out, PurchaseOrders: pos})
}

// rfqStateConflict answers a wrong RFQ status with 409, as the store does.
func rfqStateConflict(err error) error {
	if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeRFQState {
		return httpx.NewProblem(http.StatusConflict, de.Code, de.Detail)
	}
	return err
}

// buildAwardOrders groups the awarded lines by winning vendor and currency, in
// award order. Each PO line keeps the RFQ quantity and unit, the reply price
// and price unit (price source RFQ_REPLY:<reply id>), and is due today plus the
// reply lead time, or on the line's required date when that is later. Totals
// come from domain.Price like any PO (BR-17.5); change management is on.
func buildAwardOrders(rfq domain.RFQ, awards []domain.Award, today domain.Day) ([]store.AwardedOrder, error) {
	replies := make(map[string]domain.RFQReply, len(rfq.Replies))
	for _, rp := range rfq.Replies {
		replies[rp.ID] = rp
	}
	lines := make(map[string]domain.RFQLine, len(rfq.Lines))
	for _, l := range rfq.Lines {
		lines[l.ID] = l
	}
	type groupKey struct{ vendor, currency string }
	index := map[groupKey]int{}
	var out []store.AwardedOrder
	var inputs [][]domain.LineInput
	for _, aw := range awards {
		rp, ln := replies[aw.ReplyID], lines[aw.LineID]
		k := groupKey{rp.VendorID, rp.Currency}
		i, ok := index[k]
		if !ok {
			out = append(out, store.AwardedOrder{Order: store.PurchaseOrder{LegalEntityID: rfq.LegalEntityID,
				VendorID: rp.VendorID, Currency: rp.Currency, ChangeManagement: true}})
			inputs = append(inputs, nil)
			i = len(out) - 1
			index[k] = i
		}
		unit := rp.PriceUnit
		if unit.Sign() <= 0 {
			unit = decimal.NewFromInt(1)
		}
		due := domain.Day{Time: today.AddDate(0, 0, rp.LeadTimeDays)}
		if ln.RequiredDate != nil && ln.RequiredDate.After(due.Time) {
			due = *ln.RequiredDate
		}
		po := &out[i]
		po.Order.Lines = append(po.Order.Lines, store.PurchaseOrderLine{LineNo: len(po.Order.Lines) + 1,
			ProductID: ln.ProductID, Description: ln.Description, Qty: ln.Qty, Unit: ln.Unit,
			UnitPrice: rp.UnitPrice, PriceUnit: unit, DiscountPct: decimal.Zero, DeliveryDate: due,
			PriceSource: "RFQ_REPLY:" + rp.ID, RequisitionLineID: ln.RequisitionLineID})
		po.ReplyIDs = append(po.ReplyIDs, rp.ID)
		inputs[i] = append(inputs[i], domain.LineInput{Qty: ln.Qty, UnitPrice: rp.UnitPrice, PriceUnit: unit,
			DiscountPct: decimal.Zero, Tax: decimal.Zero})
	}
	for i := range out {
		if err := priceDocument(&out[i].Order, inputs[i], nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// recordRFQReplies handles POST /rfqs/{id}/replies: a vendor's prices for lines
// of an RFQ it was invited to (FR-17.4). Replies after the deadline are stored
// late and answered with a PRC-RFQ-003 warning per line (BR-17.11).
func (a *API) recordRFQReplies(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body rfqReplyBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	items, err := buildReplies(body, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.RecordRFQReplies(r.Context(), id, body.VendorID, items,
		store.Idem{Key: key, Hash: hashBody("rfq.replies", map[string]any{"id": id, "body": body})})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqRepliesRecorded.Add(int64(len(items)))
	warnings := lateWarnings(out, body.VendorID, items)
	if out, err = a.present(r.Context(), out); err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, rfqReplyResult{RFQ: out, Warnings: warnings})
}

// lateWarnings returns PRC-RFQ-003 for each line of the request whose stored
// reply from the vendor is late.
func lateWarnings(rfq domain.RFQ, vendorID string, items []domain.RFQReply) []store.Warning {
	asked := make(map[string]bool, len(items))
	for _, it := range items {
		asked[it.LineID] = true
	}
	lineNo := make(map[string]int, len(rfq.Lines))
	for _, l := range rfq.Lines {
		lineNo[l.ID] = l.LineNo
	}
	var out []store.Warning
	for _, rp := range rfq.Replies {
		if rp.VendorID == vendorID && rp.Late && asked[rp.LineID] {
			out = append(out, store.Warning{Code: domain.CodeRFQLate, LineNo: lineNo[rp.LineID],
				Detail: "reply from vendor " + vendorID + " received after the deadline " + rfq.ReplyDeadline.UTC().Format(time.RFC3339) + "; excluded from ranking"})
		}
	}
	return out
}

// buildReplies validates a reply request and stamps each line with now.
func buildReplies(body rfqReplyBody, now time.Time) ([]domain.RFQReply, error) {
	if err := uuidField("vendorId", body.VendorID, true); err != nil {
		return nil, err
	}
	cur, err := currencyField(body.Currency)
	if err != nil {
		return nil, err
	}
	if len(body.Replies) == 0 {
		return nil, invalid("replies", "needs at least one line reply")
	}
	if len(body.Replies) > maxRFQLines {
		return nil, invalid("replies", "may have at most "+strconv.Itoa(maxRFQLines)+" lines")
	}
	seen := make(map[string]bool, len(body.Replies))
	out := make([]domain.RFQReply, 0, len(body.Replies))
	for i, l := range body.Replies {
		field := func(n string) string { return "replies[" + strconv.Itoa(i) + "]." + n }
		if err := uuidField(field("lineId"), l.LineID, true); err != nil {
			return nil, err
		}
		if seen[l.LineID] {
			return nil, invalid(field("lineId"), "appears more than once")
		}
		seen[l.LineID] = true
		price, err := decimalField(field("unitPrice"), l.UnitPrice, "")
		if err != nil {
			return nil, err
		}
		if price.Sign() < 0 {
			return nil, invalid(field("unitPrice"), "must not be negative")
		}
		unit, err := decimalField(field("priceUnit"), l.PriceUnit, "1")
		if err != nil {
			return nil, err
		}
		if unit.Sign() <= 0 {
			return nil, invalid(field("priceUnit"), "must be positive")
		}
		if l.LeadTimeDays < 0 || l.LeadTimeDays > 3650 {
			return nil, invalid(field("leadTimeDays"), "must be from 0 to 3650")
		}
		if len(l.Notes) > maxNotes {
			return nil, invalid(field("notes"), "must be at most "+strconv.Itoa(maxNotes)+" characters")
		}
		out = append(out, domain.RFQReply{LineID: l.LineID, VendorID: body.VendorID, UnitPrice: price, PriceUnit: unit,
			Currency: cur, LeadTimeDays: l.LeadTimeDays, ValidUntil: l.ValidUntil, Notes: strings.TrimSpace(l.Notes),
			ReceivedAt: now.UTC()})
	}
	return out, nil
}

// compareRFQ handles GET /rfqs/{id}/compare?rule=: the ranking per line
// (FR-17.4), late replies listed as excluded (BR-17.11), with each scored
// vendor's weighted score (FR-17.19). A sealed RFQ is 403 PRC-RFQ-004 before
// its deadline; the first comparison after it logs the opening (AC-17.14).
func (a *API) compareRFQ(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	rule := r.URL.Query().Get("rule")
	if rule != "" && domain.ValidRankingRule(rule) != nil {
		fail(w, r, invalid("rule", "must be LOWEST_PRICE, LEAD_TIME or WEIGHTED_SCORE"))
		return
	}
	rfq, err := a.store.GetRFQ(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := domain.CheckSealedView(rfq, a.now()); err != nil {
		fail(w, r, err)
		return
	}
	if rule == "" {
		rule = rfq.RankingRule
	}
	if rule == domain.RankWeightedScore && len(rfq.Criteria) == 0 {
		fail(w, r, invalid("rule", "WEIGHTED_SCORE needs scoring criteria, and this RFQ has none"))
		return
	}
	if rfq, err = a.present(r.Context(), rfq); err != nil {
		fail(w, r, err)
		return
	}
	scores := domain.WeightedScores(rfq.Criteria, rfq.Scores)
	byLine := make(map[string][]domain.RFQReply, len(rfq.Lines))
	for _, rp := range rfq.Replies {
		byLine[rp.LineID] = append(byLine[rp.LineID], rp)
	}
	cmp := rfqComparison{RFQID: rfq.ID, RankingRule: rule, Lines: make([]rfqComparedLine, 0, len(rfq.Lines))}
	for _, l := range rfq.Lines {
		ranked, excluded, err := domain.RankLineScored(l.Qty, byLine[l.ID], rule, scores)
		if err != nil {
			fail(w, r, err)
			return
		}
		cmp.Lines = append(cmp.Lines, rfqComparedLine{LineID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID,
			Description: l.Description, Qty: l.Qty, Unit: l.Unit, Ranked: ranked, Excluded: excluded})
	}
	httpx.JSON(w, http.StatusOK, cmp)
}

// scoreRFQVendor handles PUT /rfqs/{id}/scores/{vendorId} (duty RFQAward):
// the buyer's 1–10 scores of an invited vendor on the RFQ's criteria, which
// the WEIGHTED_SCORE ranking uses (FR-17.19). If-Match is the RFQ version; a
// sealed RFQ is scored only after its deadline (403 PRC-RFQ-004).
func (a *API) scoreRFQVendor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	vendorID := chi.URLParam(r, "vendorId")
	if err := uuidField("vendorId", vendorID, true); err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body rfqScoresBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if len(body.Scores) == 0 || len(body.Scores) > domain.MaxScoringCriteria {
		fail(w, r, invalid("scores", "needs from 1 to "+strconv.Itoa(domain.MaxScoringCriteria)+" scores"))
		return
	}
	scores := make([]domain.VendorScore, 0, len(body.Scores))
	for i, sc := range body.Scores {
		field := func(n string) string { return "scores[" + strconv.Itoa(i) + "]." + n }
		if err := uuidField(field("criterionId"), sc.CriterionID, true); err != nil {
			fail(w, r, err)
			return
		}
		if sc.Score < domain.MinScore || sc.Score > domain.MaxScore {
			fail(w, r, invalid(field("score"), "must be a whole number from 1 to 10"))
			return
		}
		scores = append(scores, domain.VendorScore{VendorID: vendorID, CriterionID: sc.CriterionID, Score: sc.Score})
	}
	out, err := a.store.SetRFQScores(r.Context(), id, want, vendorID, scores, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqVendorsScored.Add(1)
	a.writeRFQ(w, r, http.StatusOK, out)
}

// uniqueIDs validates a list of UUIDs and drops repeats, keeping the first.
func uniqueIDs(field string, in []string) ([]string, error) {
	seen := make(map[string]bool, len(in))
	var out []string
	for i, v := range in {
		if !ids.Valid(v) {
			return nil, invalid(field+"["+strconv.Itoa(i)+"]", "must be a UUID")
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, invalid(field, "needs at least one vendor")
	}
	return out, nil
}
