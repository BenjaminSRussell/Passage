package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// Stable error codes of journals and settlement.
const (
	CodeJournalNotFound     = "PRODUCTION_JOURNAL_NOT_FOUND"
	CodeJournalNotAllowed   = "PRODUCTION_JOURNAL_NOT_ALLOWED"
	CodeJournalPosted       = "PRODUCTION_JOURNAL_ALREADY_POSTED"
	CodeJournalEmpty        = "PRODUCTION_JOURNAL_EMPTY"
	CodeInvalidJournalLine  = "PRODUCTION_INVALID_JOURNAL_LINE"
	CodeOperationFinished   = "PRODUCTION_OPERATION_FINISHED"
	CodePostingBeforeOrder  = "PRODUCTION_POSTING_DATE_BEFORE_ORDER"
	CodeUnpostedJournals    = "PRODUCTION_UNPOSTED_JOURNALS"
	CodeEndBeforeLastPosted = "PRODUCTION_END_BEFORE_LAST_JOURNAL"
)

// Journal line kinds.
const (
	KindIssue   = "issue"
	KindHours   = "hours"
	KindReceipt = "receipt"
)

// Journal is a production journal (PRD 4.21.4 ProductionJournal) with its
// lines and, once posted, its WIP postings.
type Journal struct {
	ID             string             `json:"id"`
	OrderID        string             `json:"orderId"`
	Type           domain.JournalType `json:"type"`
	JournalNumber  string             `json:"journalNumber"`
	PostingDate    string             `json:"postingDate"`
	Posted         bool               `json:"posted"`
	PostedAt       *time.Time         `json:"postedAt,omitempty"`
	PostedBy       *string            `json:"postedBy,omitempty"`
	QtyGood        decimal.Decimal    `json:"qtyGood"`
	QtyError       decimal.Decimal    `json:"qtyError"`
	ReversalOf     *string            `json:"reversalOf,omitempty"`
	ReversedBy     *string            `json:"reversedBy,omitempty"`
	JournalEntryID *string            `json:"journalEntryId,omitempty"`
	CreatedBy      string             `json:"createdBy"`
	CreatedAt      time.Time          `json:"createdAt"`
	Lines          []JournalLine      `json:"lines"`
	Postings       []domain.Posting   `json:"postings"`
	// TriggerFirings are the trigger points a posted route card made due (FR-21.17).
	TriggerFirings []TriggerFiring `json:"triggerFirings,omitempty"`
}

// JournalLine is one line: an issue of a BOM line (Qty is the quantity), hours
// on an operation (Qty is the hours, with good and error quantities), or the
// finished receipt of a report as finished (Qty is the good quantity).
type JournalLine struct {
	Kind           string          `json:"kind"`
	LineNo         *string         `json:"lineNo,omitempty"`
	OperNumber     *int            `json:"operNumber,omitempty"`
	ItemID         *string         `json:"itemId,omitempty"`
	Qty            decimal.Decimal `json:"qty"`
	QtyGood        decimal.Decimal `json:"qtyGood"`
	QtyError       decimal.Decimal `json:"qtyError"`
	Finished       bool            `json:"finished"`
	UnitCost       decimal.Decimal `json:"unitCost"`
	Amount         decimal.Decimal `json:"amount"`
	OverheadAmount decimal.Decimal `json:"overheadAmount"`
}

// IssueInput is an explicit picking line. A nil UnitCost issues at the line's
// estimated unit cost; inventory (wave 16) will supply the actual cost.
type IssueInput struct {
	LineNo   string
	Qty      decimal.Decimal
	UnitCost *decimal.Decimal
}

// HoursInput is an explicit route-card line for an operation (FR-21.6).
type HoursInput struct {
	OperNumber int
	Hours      decimal.Decimal
	QtyGood    decimal.Decimal
	QtyError   decimal.Decimal
	Finished   bool
}

// NewJournal is a journal to create as a draft. Without explicit lines the
// journal is proposed (FR-21.5): picking lists over manual and
// available-at-location lines, route cards over in-house open operations,
// report as finished over finish-flushed lines. AutoStart lets a route card be
// created on a Released order (FR-21.6).
type NewJournal struct {
	OrderID        string
	Type           domain.JournalType
	PostingDate    string
	Proposal       domain.Proposal
	QtyGood        decimal.Decimal
	QtyError       decimal.Decimal
	Issues         []IssueInput
	Hours          []HoursInput
	AutoStart      bool
	Actor          string
	IdempotencyKey string
	RequestHash    string
}

// PostRequest posts a draft journal. The duty and the two tolerances come
// from the caller and the service parameters (PRD 4.21.13).
type PostRequest struct {
	JournalID           string
	OverConsumptionDuty bool
	OverConsumptionPct  decimal.Decimal
	RAFTolerancePct     decimal.Decimal
	AutoStart           bool
	Actor               string
	TraceID             string
	IdempotencyKey      string
}

// CostSummary is the cost read of an order (FR-21.11): planned rows from the
// estimate, actual rows from posted journals, the WIP balance, and the
// variances once the order is Ended.
type CostSummary struct {
	Currency   string
	Planned    []domain.CostRow
	Actual     []domain.CostRow
	WIPBalance decimal.Decimal
	Variances  []domain.Variance
}

func today() string { return time.Now().UTC().Format(time.DateOnly) }

func journalNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeJournalNotFound, "production journal not found")
}

// CreateJournal creates a draft journal on an order. A replay of the same
// Idempotency-Key and body returns the stored journal with created=false.
func (s *Postgres) CreateJournal(ctx context.Context, in NewJournal) (Journal, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Journal
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var replayID, replayOrder, replayHash string
		err := tx.QueryRow(ctx, `select id::text, order_id::text, request_hash from production_journal where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&replayID, &replayOrder, &replayHash)
		if err == nil {
			if replayOrder != in.OrderID || replayHash != in.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
			}
			out, err = loadJournal(ctx, tx, p.TenantID, replayID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		h, err := lockOrder(ctx, tx, p.TenantID, in.OrderID, 0)
		if err != nil {
			return err
		}
		if !domain.JournalAllowed(in.Type, h.status, in.AutoStart) {
			return journalNotAllowed(in.Type, h.status)
		}
		if in.PostingDate == "" {
			in.PostingDate = today()
		}
		out, err = createJournal(ctx, tx, p.TenantID, &h, in)
		created = err == nil
		return err
	})
	return out, created, err
}

func journalNotAllowed(t domain.JournalType, s domain.Status) error {
	return httpx.NewProblem(http.StatusConflict, CodeJournalNotAllowed, fmt.Sprintf("a %s journal cannot be posted on a %s order", t, s))
}

// createJournal plans the lines and inserts the draft.
func createJournal(ctx context.Context, tx pgx.Tx, tenantID string, h *orderHead, in NewJournal) (Journal, error) {
	lines, err := loadLines(ctx, tx, tenantID, h.id)
	if err != nil {
		return Journal{}, err
	}
	ops, err := loadOperations(ctx, tx, tenantID, h.id)
	if err != nil {
		return Journal{}, err
	}
	planned, err := planJournal(in, *h, lines, ops)
	if err != nil {
		return Journal{}, err
	}
	number, err := nextJournalNumber(ctx, tx, tenantID, h.legalEntityID)
	if err != nil {
		return Journal{}, err
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into production_journal (tenant_id, id, legal_entity_id, order_id, journal_type, journal_number,
			posting_date, qty_good, qty_error, idempotency_key, request_hash, created_by)
		values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10, $11, $12)`,
		tenantID, id, h.legalEntityID, h.id, string(in.Type), number, in.PostingDate, in.QtyGood.String(), in.QtyError.String(),
		in.IdempotencyKey, in.RequestHash, in.Actor); err != nil {
		return Journal{}, err
	}
	if err := insertJournalLines(ctx, tx, tenantID, id, planned); err != nil {
		return Journal{}, err
	}
	return loadJournal(ctx, tx, tenantID, id)
}

func lineProblem(format string, args ...any) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeInvalidJournalLine, fmt.Sprintf(format, args...))
}

// planJournal builds the lines of a new journal from explicit input or from
// the proposal. The share of a proportional proposal is the order's reported
// quantity (good and error, including this RAF) over its quantity.
func planJournal(in NewJournal, h orderHead, lines []domain.OrderLine, ops []domain.Operation) ([]JournalLine, error) {
	reported := h.qtyRAF.Add(h.qtyError)
	if in.Type == domain.JournalReportFinished {
		reported = reported.Add(in.QtyGood).Add(in.QtyError)
	}
	share := reported.Div(h.quantity)
	var out []JournalLine
	if in.Type == domain.JournalReportFinished {
		product := h.productID
		out = append(out, JournalLine{Kind: KindReceipt, ItemID: &product, Qty: in.QtyGood, QtyGood: in.QtyGood, QtyError: in.QtyError})
	}
	switch in.Type {
	case domain.JournalPickingList, domain.JournalReportFinished:
		if len(in.Hours) > 0 {
			return nil, lineProblem("a %s journal takes no hours", in.Type)
		}
		if len(in.Issues) > 0 {
			seen := map[string]bool{}
			for _, is := range in.Issues {
				l, ok := findLine(lines, is.LineNo)
				if !ok || seen[is.LineNo] {
					return nil, lineProblem("line %s is not a BOM line of the order, or is listed twice", is.LineNo)
				}
				seen[is.LineNo] = true
				cost := l.UnitCost
				if is.UnitCost != nil {
					cost = *is.UnitCost
				}
				out = append(out, issueLine(l, is.Qty, cost))
			}
			break
		}
		for _, l := range lines {
			proposed := l.Flushing == domain.FlushManual || l.Flushing == domain.FlushAvailable
			if in.Type == domain.JournalReportFinished {
				proposed = l.Flushing == domain.FlushFinish
			}
			if !proposed {
				continue
			}
			if q := domain.ProposeQty(in.Proposal, l.QtyRequired, l.QtyConsumed, share); q.IsPositive() {
				out = append(out, issueLine(l, q, l.UnitCost))
			}
		}
	case domain.JournalRouteCard:
		if len(in.Issues) > 0 {
			return nil, lineProblem("a route card takes no issue lines")
		}
		if len(in.Hours) > 0 {
			seen := map[int]bool{}
			for _, hi := range in.Hours {
				op, ok := findOperation(ops, hi.OperNumber)
				if !ok || seen[hi.OperNumber] {
					return nil, lineProblem("operation %d is not on the order's route, or is listed twice", hi.OperNumber)
				}
				seen[hi.OperNumber] = true
				if err := checkFeedback(op); err != nil {
					return nil, err
				}
				if !hi.Hours.IsZero() {
					if err := domain.CheckHours(hi.Hours); err != nil {
						return nil, domainProblem(err)
					}
				}
				out = append(out, hoursLine(op, hi.Hours, hi.QtyGood, hi.QtyError, hi.Finished))
			}
			break
		}
		for _, op := range ops {
			if op.Subcontract || op.Status == domain.OperFinished {
				continue
			}
			planned := op.SetupH.Add(op.RunHPer.Mul(op.ProcessQty))
			if hours := domain.ProposeHours(in.Proposal, planned, op.HoursReported, share); !hours.LessThan(domain.MinHours) {
				out = append(out, hoursLine(op, hours, decimal.Zero, decimal.Zero, false))
			}
		}
	}
	if len(out) == 0 {
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeJournalEmpty, "the proposal found nothing left to post; enter the lines")
	}
	return out, nil
}

// checkFeedback refuses route-card feedback on a subcontracted operation
// (its cost comes from the vendor invoice, wave 17) or a finished one (FR-21.6).
func checkFeedback(op domain.Operation) error {
	if op.Subcontract {
		return lineProblem("operation %d is subcontracted; its cost comes from the purchase order", op.OperNumber)
	}
	if op.Status == domain.OperFinished {
		return httpx.NewProblem(http.StatusConflict, CodeOperationFinished, fmt.Sprintf("operation %d is finished", op.OperNumber))
	}
	return nil
}

func issueLine(l domain.OrderLine, qty, unitCost decimal.Decimal) JournalLine {
	no, item := l.LineNo, l.ItemID
	return JournalLine{Kind: KindIssue, LineNo: &no, ItemID: &item, Qty: qty, UnitCost: unitCost}
}

func hoursLine(op domain.Operation, hours, good, bad decimal.Decimal, finished bool) JournalLine {
	n := op.OperNumber
	return JournalLine{Kind: KindHours, OperNumber: &n, Qty: hours, QtyGood: good, QtyError: bad, Finished: finished}
}

func findLine(lines []domain.OrderLine, no string) (domain.OrderLine, bool) {
	for _, l := range lines {
		if l.LineNo == no {
			return l, true
		}
	}
	return domain.OrderLine{}, false
}

func findOperation(ops []domain.Operation, n int) (domain.Operation, bool) {
	for _, op := range ops {
		if op.OperNumber == n {
			return op, true
		}
	}
	return domain.Operation{}, false
}

func nextJournalNumber(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (string, error) {
	var seq int64
	if err := tx.QueryRow(ctx, `insert into production_journal_sequence (tenant_id, legal_entity_id, next_value) values ($1, $2, 2)
		on conflict (tenant_id, legal_entity_id) do update set next_value = production_journal_sequence.next_value + 1
		returning next_value - 1`, tenantID, legalEntityID).Scan(&seq); err != nil {
		return "", err
	}
	return fmt.Sprintf("PJ-%06d", seq), nil
}

func insertJournalLines(ctx context.Context, tx pgx.Tx, tenantID, journalID string, lines []JournalLine) error {
	for i, l := range lines {
		if _, err := tx.Exec(ctx, `insert into production_journal_line (tenant_id, journal_id, line_seq, kind, bom_line_number, oper_number, item_id,
				qty, qty_good, qty_error, finish_operation, unit_cost, amount, overhead_amount)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			tenantID, journalID, i+1, l.Kind, l.LineNo, l.OperNumber, l.ItemID, l.Qty.String(), l.QtyGood.String(), l.QtyError.String(),
			l.Finished, l.UnitCost.String(), l.Amount.String(), l.OverheadAmount.String()); err != nil {
			return err
		}
	}
	return nil
}

// PostJournal posts a draft journal (FR-21.5 to FR-21.7) in one transaction:
// values its lines, writes the balanced WIP postings, books consumption,
// hours and finished quantities, moves the order (a route card auto-starts a
// Released order, a report as finished moves a Started order to
// ReportedAsFinished) and enqueues journal.posted. The order row is locked
// first (BR-21.4). A replay of the same Idempotency-Key returns the journal.
func (s *Postgres) PostJournal(ctx context.Context, r PostRequest) (Journal, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Journal
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var postedID string
		err := tx.QueryRow(ctx, `select id::text from production_journal where tenant_id = $1 and post_key = $2`, p.TenantID, r.IdempotencyKey).Scan(&postedID)
		if err == nil {
			if postedID != r.JournalID {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used to post another journal")
			}
			replayed = true
			out, err = loadJournal(ctx, tx, p.TenantID, r.JournalID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var orderID string
		if err := tx.QueryRow(ctx, `select order_id::text from production_journal where tenant_id = $1 and id = $2`, p.TenantID, r.JournalID).Scan(&orderID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return journalNotFound()
			}
			return err
		}
		h, err := lockOrder(ctx, tx, p.TenantID, orderID, 0)
		if err != nil {
			return err
		}
		j, err := loadJournal(ctx, tx, p.TenantID, r.JournalID, "for update")
		if err != nil {
			return err
		}
		if j.Posted {
			return httpx.NewProblem(http.StatusConflict, CodeJournalPosted, "the journal is already posted")
		}
		if !domain.JournalAllowed(j.Type, h.status, r.AutoStart) {
			return journalNotAllowed(j.Type, h.status)
		}
		from := h.status
		if h.status == domain.Released {
			// FR-21.6: feedback on a Released order starts it, with the effects of :start.
			if err := startOrder(ctx, tx, p.TenantID, &h, r.Actor, "sys:auto-start:"+r.IdempotencyKey); err != nil {
				return err
			}
			if err := logStatus(ctx, tx, p.TenantID, h, domain.CmdStart, from, domain.Started, r.Actor, r.TraceID, "sys:auto-start:"+r.IdempotencyKey); err != nil {
				return err
			}
			h.status = domain.Started
		}
		opts := postOptions{overDuty: r.OverConsumptionDuty, overPct: r.OverConsumptionPct, rafTolerancePct: r.RAFTolerancePct, actor: r.Actor, key: r.IdempotencyKey}
		if err := postJournal(ctx, tx, p.TenantID, &h, &j, opts); err != nil {
			return err
		}
		if j.Type == domain.JournalReportFinished {
			before := h.status
			h.status = domain.ReportedAsFinished
			if err := logStatus(ctx, tx, p.TenantID, h, domain.CmdReportFinished, before, h.status, r.Actor, r.TraceID, "sys:raf-post:"+r.IdempotencyKey); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update production_order set status = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, h.id, string(h.status)); err != nil {
			return err
		}
		out, err = loadJournal(ctx, tx, p.TenantID, r.JournalID)
		return err
	})
	return out, replayed, err
}

// logStatus writes the status log row and the statuschanged event of a status
// change that a journal post made (auto-start, report as finished).
func logStatus(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, cmd domain.Command, from, to domain.Status, actor, traceID, key string) error {
	if _, err := tx.Exec(ctx, `insert into production_status_log (tenant_id, id, legal_entity_id, order_id, command, from_status, to_status, actor, trace_id, idempotency_key, request_hash)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, tenantID, ids.New(), h.legalEntityID, h.id, string(cmd),
		string(from), string(to), actor, traceID, key, string(cmd)); err != nil {
		return err
	}
	return enqueue(ctx, tx, h.id, prodevents.OrderStatusChanged, tenantID, actor, prodevents.OrderStatusChangedData{
		OrderID: h.id, From: string(from), To: string(to), Quantity: h.quantity.String(), QtyRAF: h.qtyRAF.String(),
		QtyError: h.qtyError.String(), Actor: actor, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

type postOptions struct {
	overDuty        bool
	overPct         decimal.Decimal
	rafTolerancePct decimal.Decimal
	actor           string
	key             string
}

// postJournal posts j on the locked order h: it checks the posting date
// (BR-21.3), values each line, applies the over-consumption (FR-21.5) and
// RAF tolerance (FR-21.7) rules, writes the WIP postings and the line values,
// books consumption, operation feedback and the reported quantities (h is
// updated), marks the journal posted and enqueues journal.posted.
func postJournal(ctx context.Context, tx pgx.Tx, tenantID string, h *orderHead, j *Journal, o postOptions) error {
	if j.PostingDate < h.createdOn {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodePostingBeforeOrder,
			fmt.Sprintf("posting date %s is before the order was created (%s)", j.PostingDate, h.createdOn))
	}
	profile, err := resolveProfile(ctx, tx, tenantID, *h)
	if err != nil {
		return err
	}
	lines, err := loadLines(ctx, tx, tenantID, h.id)
	if err != nil {
		return err
	}
	ops, err := loadOperations(ctx, tx, tenantID, h.id)
	if err != nil {
		return err
	}
	var plannedTotal decimal.Decimal
	if j.Type == domain.JournalReportFinished {
		costs, err := loadCosts(ctx, tx, tenantID, h.id)
		if err != nil {
			return err
		}
		if h.rework() {
			// FR-21.20: the reworked units come back at their existing cost; the
			// rework itself stays in WIP and settles as a rework variance.
			plannedTotal = domain.ReworkReceiptBase(costs, h.productID)
		} else {
			for _, c := range costs {
				plannedTotal = plannedTotal.Add(c.Amount)
			}
		}
	}
	consumed := map[string]decimal.Decimal{}
	var postings []domain.Posting
	for i := range j.Lines {
		l := &j.Lines[i]
		switch l.Kind {
		case KindIssue:
			bl, ok := findLine(lines, *l.LineNo)
			if !ok {
				return lineProblem("line %s is not a BOM line of the order", *l.LineNo)
			}
			after := bl.QtyConsumed.Add(consumed[bl.LineNo]).Add(l.Qty)
			if err := domain.CheckConsumption(bl.LineNo, bl.QtyRequired, after, o.overPct, o.overDuty); err != nil {
				return domainProblem(err)
			}
			consumed[bl.LineNo] = consumed[bl.LineNo].Add(l.Qty)
			l.Amount = domain.RoundMoney(l.Qty.Mul(l.UnitCost), h.currency)
			postings = append(postings, domain.IssuePostings(profile, l.Amount)...)
		case KindHours:
			op, ok := findOperation(ops, *l.OperNumber)
			if !ok {
				return lineProblem("operation %d is not on the order's route", *l.OperNumber)
			}
			if err := checkFeedback(op); err != nil {
				return err
			}
			l.UnitCost = op.LaborRate
			l.Amount = domain.RoundMoney(l.Qty.Mul(op.LaborRate), h.currency)
			l.OverheadAmount = domain.RoundMoney(l.Qty.Mul(op.OverheadRate), h.currency)
			postings = append(postings, domain.HoursPostings(profile, l.Amount, l.OverheadAmount)...)
		case KindReceipt:
			if err := domain.CheckReportFinished(h.quantity, h.qtyRAF, h.qtyError, l.QtyGood, l.QtyError, o.rafTolerancePct); err != nil {
				return domainProblem(err)
			}
			after := h.qtyRAF.Add(l.QtyGood)
			l.Amount = domain.ReceiptValue(plannedTotal, h.quantity, h.qtyRAF, after, h.currency)
			l.UnitCost = l.Amount.Div(l.QtyGood).Round(6)
			postings = append(postings, domain.ReceiptPostings(profile, l.Amount)...)
			h.qtyRAF, h.qtyError = after, h.qtyError.Add(l.QtyError)
		}
	}
	if !domain.Balanced(postings) {
		return fmt.Errorf("journal %s postings do not balance", j.ID)
	}
	if err := insertPostings(ctx, tx, tenantID, *h, &j.ID, j.PostingDate, postings); err != nil {
		return err
	}
	if err := applyLines(ctx, tx, tenantID, h.id, j.ID, j.Lines); err != nil {
		return err
	}
	if j.Type == domain.JournalReportFinished {
		if _, err := tx.Exec(ctx, `update production_order set qty_raf = $3, qty_error = $4 where tenant_id = $1 and id = $2`,
			tenantID, h.id, h.qtyRAF.String(), h.qtyError.String()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `update production_journal set posted = true, posted_at = now(), posted_by = $3, post_key = $4
		where tenant_id = $1 and id = $2`, tenantID, j.ID, o.actor, o.key); err != nil {
		return err
	}
	j.Posted, j.Postings = true, postings
	if err := enqueueJournalPosted(ctx, tx, tenantID, *h, *j, o.actor); err != nil {
		return err
	}
	// FR-21.17: the route card's feedback evaluates the trigger points of its operations.
	return fireTriggers(ctx, tx, tenantID, *h, j, o.actor)
}

// applyLines stores the valued lines of a posted journal and books them on
// the BOM lines and operations: feedback starts an operation and a finishing
// line closes it (FR-21.6).
func applyLines(ctx context.Context, tx pgx.Tx, tenantID, orderID, journalID string, lines []JournalLine) error {
	for i, l := range lines {
		if _, err := tx.Exec(ctx, `update production_journal_line set unit_cost = $4, amount = $5, overhead_amount = $6
			where tenant_id = $1 and journal_id = $2 and line_seq = $3`,
			tenantID, journalID, i+1, l.UnitCost.String(), l.Amount.String(), l.OverheadAmount.String()); err != nil {
			return err
		}
		switch l.Kind {
		case KindIssue:
			if _, err := tx.Exec(ctx, `update production_bom_line set qty_consumed = qty_consumed + $4
				where tenant_id = $1 and order_id = $2 and line_number = $3`, tenantID, orderID, *l.LineNo, l.Qty.String()); err != nil {
				return err
			}
		case KindHours:
			if _, err := tx.Exec(ctx, `update production_operation set hours_reported = hours_reported + $4,
					qty_good = qty_good + $5, qty_error = qty_error + $6,
					status = case when $7 then 'finished' else 'started' end
				where tenant_id = $1 and order_id = $2 and oper_number = $3`,
				tenantID, orderID, *l.OperNumber, l.Qty.String(), l.QtyGood.String(), l.QtyError.String(), l.Finished); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertPostings(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, journalID *string, postingDate string, ps []domain.Posting) error {
	for _, w := range ps {
		if _, err := tx.Exec(ctx, `insert into production_wip_posting (tenant_id, id, legal_entity_id, order_id, journal_id, posting_type,
				ledger_account, wip, amount_dr, amount_cr, currency, posting_date)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::date)`,
			tenantID, ids.New(), h.legalEntityID, h.id, journalID, w.Type, w.Account, w.WIP, w.Dr.String(), w.Cr.String(), h.currency, postingDate); err != nil {
			return err
		}
	}
	return nil
}

func eventPostings(ps []domain.Posting) []prodevents.Posting {
	out := make([]prodevents.Posting, 0, len(ps))
	for _, p := range ps {
		out = append(out, prodevents.Posting{PostingType: p.Type, Account: p.Account, Debit: p.Dr.String(), Credit: p.Cr.String()})
	}
	return out
}

func enqueueJournalPosted(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, j Journal, actor string) error {
	data := prodevents.JournalPostedData{JournalID: j.ID, JournalNumber: j.JournalNumber, OrderID: h.id, Type: string(j.Type),
		PostingDate: j.PostingDate, Currency: h.currency, ReversalOf: j.ReversalOf, Lines: []prodevents.JournalLine{}, Postings: eventPostings(j.Postings)}
	for _, l := range j.Lines {
		line := prodevents.JournalLine{Kind: l.Kind, LineNo: l.LineNo, OperNumber: l.OperNumber, ItemID: l.ItemID, Qty: l.Qty.String(),
			UnitCost: l.UnitCost.String(), Amount: l.Amount.Add(l.OverheadAmount).String()}
		if l.Kind == KindHours {
			hours := l.Qty.String()
			line.Hours = &hours
		}
		data.Lines = append(data.Lines, line)
	}
	return enqueue(ctx, tx, h.id, prodevents.JournalPosted, tenantID, actor, data)
}

// startOrder applies the posting effect of Released → Started (BR-21.1): the
// start-flushed lines are issued for their remaining quantity by an
// auto-posted picking list. key makes the journal idempotent with the command.
func startOrder(ctx context.Context, tx pgx.Tx, tenantID string, h *orderHead, actor, key string) error {
	lines, err := loadLines(ctx, tx, tenantID, h.id)
	if err != nil {
		return err
	}
	var issues []IssueInput
	for _, l := range lines {
		if l.Flushing != domain.FlushStart {
			continue
		}
		if q := domain.ProposeQty(domain.ProposeRemaining, l.QtyRequired, l.QtyConsumed, decimal.Zero); q.IsPositive() {
			issues = append(issues, IssueInput{LineNo: l.LineNo, Qty: q})
		}
	}
	if len(issues) == 0 {
		return nil
	}
	return autoPost(ctx, tx, tenantID, h, NewJournal{Type: domain.JournalPickingList, Issues: issues}, actor, key, decimal.Zero)
}

// autoPost creates and posts a system journal (start backflush, report as
// finished through the :report-finished command) under key.
func autoPost(ctx context.Context, tx pgx.Tx, tenantID string, h *orderHead, in NewJournal, actor, key string, rafTolerancePct decimal.Decimal) error {
	in.OrderID, in.PostingDate, in.Actor, in.IdempotencyKey, in.RequestHash = h.id, today(), actor, key, key
	j, err := createJournal(ctx, tx, tenantID, h, in)
	if err != nil {
		return err
	}
	return postJournal(ctx, tx, tenantID, h, &j, postOptions{overDuty: true, rafTolerancePct: rafTolerancePct, actor: actor, key: key})
}

// reverseJournals reverses the posted journals of the given types that are
// not reversed yet, newest first (FR-21.9): each gets a posted reversal
// journal with negated lines and swapped WIP postings, and the booked
// consumption, hours and reported quantities are taken back (h is updated).
func reverseJournals(ctx context.Context, tx pgx.Tx, tenantID string, h *orderHead, types []domain.JournalType, actor, key string) error {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = string(t)
	}
	rows, err := tx.Query(ctx, `select j.id::text from production_journal j
		where j.tenant_id = $1 and j.order_id = $2 and j.posted and j.reversal_of is null and j.journal_type = any($3)
		  and not exists (select 1 from production_journal r where r.tenant_id = j.tenant_id and r.reversal_of = j.id)
		order by j.posted_at desc, j.id desc`, tenantID, h.id, names)
	if err != nil {
		return err
	}
	var toReverse []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		toReverse = append(toReverse, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, originalID := range toReverse {
		orig, err := loadJournal(ctx, tx, tenantID, originalID)
		if err != nil {
			return err
		}
		number, err := nextJournalNumber(ctx, tx, tenantID, h.legalEntityID)
		if err != nil {
			return err
		}
		date := today()
		if orig.PostingDate > date {
			date = orig.PostingDate
		}
		rev := Journal{ID: ids.New(), OrderID: h.id, Type: orig.Type, JournalNumber: number, PostingDate: date, Posted: true,
			QtyGood: orig.QtyGood.Neg(), QtyError: orig.QtyError.Neg(), ReversalOf: &orig.ID}
		revKey := key + ":" + orig.ID
		if _, err := tx.Exec(ctx, `insert into production_journal (tenant_id, id, legal_entity_id, order_id, journal_type, journal_number,
				posting_date, posted, posted_at, posted_by, qty_good, qty_error, reversal_of, idempotency_key, request_hash, post_key, created_by)
			values ($1, $2, $3, $4, $5, $6, $7::date, true, now(), $8, $9, $10, $11, $12, $12, $12, $8)`,
			tenantID, rev.ID, h.legalEntityID, h.id, string(rev.Type), number, date, actor, rev.QtyGood.String(), rev.QtyError.String(),
			orig.ID, revKey); err != nil {
			return err
		}
		for _, l := range orig.Lines {
			l.Qty, l.QtyGood, l.QtyError = l.Qty.Neg(), l.QtyGood.Neg(), l.QtyError.Neg()
			l.Amount, l.OverheadAmount, l.Finished = l.Amount.Neg(), l.OverheadAmount.Neg(), false
			rev.Lines = append(rev.Lines, l)
			if l.Kind == KindReceipt {
				h.qtyRAF, h.qtyError = h.qtyRAF.Add(l.QtyGood), h.qtyError.Add(l.QtyError)
			}
		}
		if err := insertJournalLines(ctx, tx, tenantID, rev.ID, rev.Lines); err != nil {
			return err
		}
		for _, w := range orig.Postings {
			w.Dr, w.Cr = w.Cr, w.Dr
			rev.Postings = append(rev.Postings, w)
		}
		if err := insertPostings(ctx, tx, tenantID, *h, &rev.ID, date, rev.Postings); err != nil {
			return err
		}
		if err := applyReversal(ctx, tx, tenantID, h.id, rev.Lines); err != nil {
			return err
		}
		if err := enqueueJournalPosted(ctx, tx, tenantID, *h, rev, actor); err != nil {
			return err
		}
	}
	if len(toReverse) > 0 {
		if _, err := tx.Exec(ctx, `update production_order set qty_raf = $3, qty_error = $4 where tenant_id = $1 and id = $2`,
			tenantID, h.id, h.qtyRAF.String(), h.qtyError.String()); err != nil {
			return err
		}
	}
	return nil
}

// applyReversal takes back the negated lines of a reversal journal; an
// operation with nothing left reported is open again.
func applyReversal(ctx context.Context, tx pgx.Tx, tenantID, orderID string, lines []JournalLine) error {
	for _, l := range lines {
		switch l.Kind {
		case KindIssue:
			if _, err := tx.Exec(ctx, `update production_bom_line set qty_consumed = qty_consumed + $4
				where tenant_id = $1 and order_id = $2 and line_number = $3`, tenantID, orderID, *l.LineNo, l.Qty.String()); err != nil {
				return err
			}
		case KindHours:
			if _, err := tx.Exec(ctx, `update production_operation set hours_reported = hours_reported + $4,
					qty_good = qty_good + $5, qty_error = qty_error + $6,
					status = case when hours_reported + $4 = 0 and qty_good + $5 = 0 and qty_error + $6 = 0 then 'open' else 'started' end
				where tenant_id = $1 and order_id = $2 and oper_number = $3`,
				tenantID, orderID, *l.OperNumber, l.Qty.String(), l.QtyGood.String(), l.QtyError.String()); err != nil {
				return err
			}
		}
	}
	return nil
}

// settleOrder runs End (FR-21.8, BR-21.6): it refuses open journals and an
// End date before the last posting, computes actual cost from the posted
// journals, decomposes it against the estimate, clears the WIP balance into
// the variance accounts, stores the variances and enqueues order.settled.
func settleOrder(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, actor string) error {
	var drafts int
	var lastPosted *string
	if err := tx.QueryRow(ctx, `select count(*) filter (where not posted), max(posting_date) filter (where posted)::text
		from production_journal where tenant_id = $1 and order_id = $2`, tenantID, h.id).Scan(&drafts, &lastPosted); err != nil {
		return err
	}
	if drafts > 0 {
		return httpx.NewProblem(http.StatusConflict, CodeUnpostedJournals, fmt.Sprintf("cannot end: %d journals are unposted; post or delete them", drafts))
	}
	date := today()
	if lastPosted != nil && *lastPosted > date {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeEndBeforeLastPosted,
			fmt.Sprintf("the end date %s is before the last journal posting date %s", date, *lastPosted))
	}
	planned, err := loadCosts(ctx, tx, tenantID, h.id)
	if err != nil {
		return err
	}
	actual, err := loadActual(ctx, tx, tenantID, h.id)
	if err != nil {
		return err
	}
	balance, err := wipBalance(ctx, tx, tenantID, h.id)
	if err != nil {
		return err
	}
	var vars []domain.Variance
	if h.rework() {
		// FR-21.20, AC-21.15: rework is not capitalized; the WIP balance is rework cost.
		byGroup, err := wipBalanceByGroup(ctx, tx, tenantID, h.id)
		if err != nil {
			return err
		}
		vars = domain.ReworkVariances(byGroup)
	} else if vars, err = domain.SettleWIP(h.quantity, h.qtyRAF, planned, actual, balance, h.currency); err != nil {
		return domainProblem(err)
	}
	var postings []domain.Posting
	if !balance.IsZero() || len(vars) > 0 {
		profile, err := resolveProfile(ctx, tx, tenantID, h)
		if err != nil {
			return err
		}
		if postings, err = domain.SettlementPostings(profile, balance, vars); err != nil {
			return domainProblem(err)
		}
		if err := insertPostings(ctx, tx, tenantID, h, nil, date, postings); err != nil {
			return err
		}
	}
	data := prodevents.OrderSettledData{OrderID: h.id, Currency: h.currency, PostingDate: date, QtyRAF: h.qtyRAF.String(),
		WIPCleared: balance.String(), PlannedCost: byGroup(planned), ActualCost: byGroup(actual),
		Variances: []prodevents.VarianceAmount{}, Postings: eventPostings(postings)}
	for _, v := range vars {
		if _, err := tx.Exec(ctx, `insert into production_variance (tenant_id, id, legal_entity_id, order_id, variance_type, cost_group, amount, currency)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`, tenantID, ids.New(), h.legalEntityID, h.id, string(v.Type), string(v.Group), v.Amount.String(), h.currency); err != nil {
			return err
		}
		data.Variances = append(data.Variances, prodevents.VarianceAmount{Type: string(v.Type), CostGroup: string(v.Group), Amount: v.Amount.String()})
	}
	return enqueue(ctx, tx, h.id, prodevents.OrderSettled, tenantID, actor, data)
}

func byGroup(rows []domain.CostRow) prodevents.CostByGroup {
	sums := map[domain.CostGroup]decimal.Decimal{}
	for _, r := range rows {
		sums[r.Group] = sums[r.Group].Add(r.Amount)
	}
	return prodevents.CostByGroup{Material: sums[domain.GroupMaterial].StringFixed(2), Labor: sums[domain.GroupLabor].StringFixed(2),
		Overhead: sums[domain.GroupOverhead].StringFixed(2), Subcontract: sums[domain.GroupSubcontract].StringFixed(2)}
}

// loadActual sums the posted issue and hours lines (reversals included) into
// actual cost rows keyed like the estimate.
func loadActual(ctx context.Context, q querier, tenantID, orderID string) ([]domain.CostRow, error) {
	rows, err := q.Query(ctx, `select l.kind = 'issue', coalesce(l.item_id::text, ''), coalesce(o.resource_id::text, ''), coalesce(l.oper_number, 0),
			l.qty::text, l.amount::text, l.overhead_amount::text
		from production_journal_line l
		join production_journal j on j.tenant_id = l.tenant_id and j.id = l.journal_id
		left join production_operation o on o.tenant_id = l.tenant_id and o.order_id = j.order_id and o.oper_number = l.oper_number
		where l.tenant_id = $1 and j.order_id = $2 and j.posted and l.kind in ('issue', 'hours')
		order by j.posted_at, j.id, l.line_seq`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lines []domain.ActualLine
	for rows.Next() {
		var l domain.ActualLine
		var item, resource, qty, amount, overhead string
		var oper int
		if err := rows.Scan(&l.Material, &item, &resource, &oper, &qty, &amount, &overhead); err != nil {
			return nil, err
		}
		l.Key = item
		if !l.Material {
			l.Key = domain.OperationKey(resource, oper)
		}
		l.Qty, l.Amount, l.Overhead = dec(qty), dec(amount), dec(overhead)
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return domain.ActualRows(lines), nil
}

// wipBalanceByGroup is the order's WIP balance by cost group (domain.WIPGroup).
func wipBalanceByGroup(ctx context.Context, q querier, tenantID, orderID string) (map[domain.CostGroup]decimal.Decimal, error) {
	rows, err := q.Query(ctx, `select posting_type, sum(amount_dr - amount_cr)::text from production_wip_posting
		where tenant_id = $1 and order_id = $2 and wip group by posting_type`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.CostGroup]decimal.Decimal{}
	for rows.Next() {
		var typ, amount string
		if err := rows.Scan(&typ, &amount); err != nil {
			return nil, err
		}
		if g, ok := domain.WIPGroup(typ); ok {
			out[g] = out[g].Add(dec(amount))
		}
	}
	return out, rows.Err()
}

func wipBalance(ctx context.Context, q querier, tenantID, orderID string) (decimal.Decimal, error) {
	var v string
	err := q.QueryRow(ctx, `select coalesce(sum(amount_dr - amount_cr), 0)::text from production_wip_posting
		where tenant_id = $1 and order_id = $2 and wip`, tenantID, orderID).Scan(&v)
	return dec(v), err
}

// Costs returns the cost read of an order (FR-21.11).
func (s *Postgres) Costs(ctx context.Context, orderID string) (CostSummary, error) {
	p, _ := tenant.FromContext(ctx)
	var out CostSummary
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select currency::text from production_order where tenant_id = $1 and id = $2`, p.TenantID, orderID).Scan(&out.Currency); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound()
			}
			return err
		}
		var err error
		if out.Planned, err = loadCosts(ctx, tx, p.TenantID, orderID); err != nil {
			return err
		}
		if out.Actual, err = loadActual(ctx, tx, p.TenantID, orderID); err != nil {
			return err
		}
		if out.WIPBalance, err = wipBalance(ctx, tx, p.TenantID, orderID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select variance_type, cost_group, amount::text from production_variance
			where tenant_id = $1 and order_id = $2 order by cost_group, variance_type`, p.TenantID, orderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Variances = []domain.Variance{}
		for rows.Next() {
			var v domain.Variance
			var typ, group, amount string
			if err := rows.Scan(&typ, &group, &amount); err != nil {
				return err
			}
			v.Type, v.Group, v.Amount = domain.VarianceType(typ), domain.CostGroup(group), dec(amount)
			out.Variances = append(out.Variances, v)
		}
		return rows.Err()
	})
	return out, err
}

// GetJournal returns one journal with its lines and postings.
func (s *Postgres) GetJournal(ctx context.Context, id string) (Journal, error) {
	p, _ := tenant.FromContext(ctx)
	var out Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadJournal(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ListJournals returns the journals of an order, oldest first (FR-21.11).
func (s *Postgres) ListJournals(ctx context.Context, orderID string) ([]Journal, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Journal{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from production_order where tenant_id = $1 and id = $2)`, p.TenantID, orderID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound()
		}
		rows, err := tx.Query(ctx, `select id::text from production_journal where tenant_id = $1 and order_id = $2 order by created_at, id`, p.TenantID, orderID)
		if err != nil {
			return err
		}
		var journalIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			journalIDs = append(journalIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range journalIDs {
			j, err := loadJournal(ctx, tx, p.TenantID, id)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		return nil
	})
	return out, err
}

// DeleteJournal deletes a draft journal (PROD-0012: "post or delete the journals").
func (s *Postgres) DeleteJournal(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := loadJournal(ctx, tx, p.TenantID, id, "for update")
		if err != nil {
			return err
		}
		if j.Posted {
			return httpx.NewProblem(http.StatusConflict, CodeJournalPosted, "a posted journal cannot be deleted; reset the order status to reverse it")
		}
		if _, err := tx.Exec(ctx, `delete from production_journal_line where tenant_id = $1 and journal_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `delete from production_journal where tenant_id = $1 and id = $2`, p.TenantID, id)
		return err
	})
}

// loadJournal reads a journal, its lines and postings; lock is "" or "for update".
func loadJournal(ctx context.Context, q querier, tenantID, id string, lock ...string) (Journal, error) {
	var j Journal
	var typ, qg, qe string
	suffix := ""
	if len(lock) > 0 {
		suffix = " " + lock[0]
	}
	err := q.QueryRow(ctx, `select id::text, order_id::text, journal_type, journal_number, posting_date::text, posted, posted_at, posted_by,
			qty_good::text, qty_error::text, reversal_of::text, journal_entry_id::text, created_by, created_at,
			(select r.id::text from production_journal r where r.tenant_id = j.tenant_id and r.reversal_of = j.id)
		from production_journal j where tenant_id = $1 and id = $2`+suffix, tenantID, id).Scan(&j.ID, &j.OrderID, &typ, &j.JournalNumber,
		&j.PostingDate, &j.Posted, &j.PostedAt, &j.PostedBy, &qg, &qe, &j.ReversalOf, &j.JournalEntryID, &j.CreatedBy, &j.CreatedAt, &j.ReversedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Journal{}, journalNotFound()
	}
	if err != nil {
		return Journal{}, err
	}
	j.Type, j.QtyGood, j.QtyError = domain.JournalType(typ), dec(qg), dec(qe)
	rows, err := q.Query(ctx, `select kind, bom_line_number, oper_number, item_id::text, qty::text, qty_good::text, qty_error::text,
			finish_operation, unit_cost::text, amount::text, overhead_amount::text
		from production_journal_line where tenant_id = $1 and journal_id = $2 order by line_seq`, tenantID, id)
	if err != nil {
		return Journal{}, err
	}
	defer rows.Close()
	j.Lines = []JournalLine{}
	for rows.Next() {
		var l JournalLine
		var qty, good, bad, unit, amount, overhead string
		if err := rows.Scan(&l.Kind, &l.LineNo, &l.OperNumber, &l.ItemID, &qty, &good, &bad, &l.Finished, &unit, &amount, &overhead); err != nil {
			return Journal{}, err
		}
		l.Qty, l.QtyGood, l.QtyError, l.UnitCost, l.Amount, l.OverheadAmount = dec(qty), dec(good), dec(bad), dec(unit), dec(amount), dec(overhead)
		j.Lines = append(j.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return Journal{}, err
	}
	rows.Close()
	j.Postings = []domain.Posting{}
	prows, err := q.Query(ctx, `select posting_type, ledger_account, wip, amount_dr::text, amount_cr::text from production_wip_posting
		where tenant_id = $1 and journal_id = $2 order by created_at, id`, tenantID, id)
	if err != nil {
		return Journal{}, err
	}
	defer prows.Close()
	for prows.Next() {
		var w domain.Posting
		var dr, cr string
		if err := prows.Scan(&w.Type, &w.Account, &w.WIP, &dr, &cr); err != nil {
			return Journal{}, err
		}
		w.Dr, w.Cr = dec(dr), dec(cr)
		j.Postings = append(j.Postings, w)
	}
	if err := prows.Err(); err != nil {
		return Journal{}, err
	}
	prows.Close()
	if j.Type == domain.JournalRouteCard && j.Posted {
		if j.TriggerFirings, err = loadFirings(ctx, q, tenantID, `f.journal_id = $2`, id); err != nil {
			return Journal{}, err
		}
	}
	return j, nil
}
