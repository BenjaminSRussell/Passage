package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Release run statuses. EMPTY found no line to release; COMPLETED released every line it found; PARTIAL released
// some and left others in the queue; FAILED found lines and released none.
const (
	ReleaseEmpty     = "EMPTY"
	ReleaseCompleted = "COMPLETED"
	ReleasePartial   = "PARTIAL"
	ReleaseFailed    = "FAILED"
)

// CodeReleaseRunNotFound is the 404 for an unknown release run.
const CodeReleaseRunNotFound = "PRC-REL-NOT-FOUND"

// ReleaseSkip is a requisition line a run left in the queue, with the problem that stopped it.
type ReleaseSkip struct {
	RequisitionLineID string `json:"requisitionLineId"`
	RequisitionNumber string `json:"requisitionNumber"`
	LineNo            int    `json:"lineNo"`
	VendorID          string `json:"vendorId,omitempty"`
	Code              string `json:"code"`
	Reason            string `json:"reason"`
}

// ReleasedOrder is a draft PO a run created.
type ReleasedOrder struct {
	PurchaseOrderID string          `json:"purchaseOrderId"`
	Number          string          `json:"number"`
	VendorID        string          `json:"vendorId"`
	Currency        string          `json:"currency"`
	Total           decimal.Decimal `json:"total"`
	LineCount       int             `json:"lineCount"`
}

// ReleaseRun is the buyer's record of one requisition release run.
type ReleaseRun struct {
	ID              string          `json:"id"`
	LegalEntityID   string          `json:"legalEntityId"`
	Status          string          `json:"status"`
	WindowDays      int             `json:"windowDays"`
	MaxLines        int             `json:"maxLines"`
	LinesConsidered int             `json:"linesConsidered"`
	LinesReleased   int             `json:"linesReleased"`
	LinesSkipped    int             `json:"linesSkipped"`
	PurchaseOrders  []ReleasedOrder `json:"purchaseOrders"`
	Skipped         []ReleaseSkip   `json:"skipped"`
	Warnings        []Warning       `json:"warnings"`
	RequestedBy     string          `json:"requestedBy,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// ReleaseGroup is the consolidation of one vendor's lines: the lines as read, the priced draft POs built from them and
// the warnings building them raised (FR-17.20 SOFT category findings). The vendor's lines succeed or are skipped
// together, and a skipped group's warnings are not reported.
type ReleaseGroup struct {
	VendorID string
	Lines    []SourcingLine
	Orders   []PurchaseOrder
	Warnings []Warning
}

// ReleaseInput is what the API worked out before the write: how many lines the run considered, the vendor groups it
// could build POs for, and the lines it already knows it cannot order (no vendor, a group that failed to price).
type ReleaseInput struct {
	LegalEntityID string
	WindowDays    int
	MaxLines      int
	Considered    int
	Groups        []ReleaseGroup
	Skipped       []ReleaseSkip
	RequestedBy   string
}

// ReleaseRequisitions writes the POs of a release run and the run record in one transaction (PRD 3.17.16). It locks every
// line the groups name once, in the order every requisition line writer uses, then writes each vendor group under a
// savepoint: a group that a concurrent write made impossible (a line no longer OPEN, split, held by an RFQ, an agreement
// over its limit) is rolled back and its lines are listed as skipped, so one vendor's problem never stops the others.
// The run row and requisitionrelease.completed.v1 are written in the same transaction. A replayed Idempotency-Key
// returns the stored run.
func (s *Postgres) ReleaseRequisitions(ctx context.Context, in ReleaseInput, idem Idem) (ReleaseRun, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReleaseRun
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		runID := ids.New()
		existing, replay, err := claimKey(ctx, tx, idem, runID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadReleaseRun(ctx, tx, existing)
			return err
		}
		var all []PurchaseOrder
		for _, g := range in.Groups {
			all = append(all, g.Orders...)
		}
		var locked []SourcingLine
		if len(all) > 0 {
			if locked, err = lockRequisitionLines(ctx, tx, all); err != nil {
				return err
			}
		}
		run := ReleaseRun{ID: runID, LegalEntityID: in.LegalEntityID, WindowDays: in.WindowDays, MaxLines: in.MaxLines,
			LinesConsidered: in.Considered, PurchaseOrders: []ReleasedOrder{}, Skipped: append([]ReleaseSkip{}, in.Skipped...),
			Warnings: []Warning{}, RequestedBy: in.RequestedBy}
		for _, g := range in.Groups {
			pos, err := writeReleaseGroup(ctx, tx, g, locked)
			if err != nil {
				var prob *httpx.Problem
				if !errors.As(err, &prob) {
					return err
				}
				for _, l := range g.Lines {
					run.Skipped = append(run.Skipped, ReleaseSkip{RequisitionLineID: l.ID, RequisitionNumber: l.RequisitionNumber,
						LineNo: l.LineNo, VendorID: g.VendorID, Code: prob.Code, Reason: prob.Detail})
				}
				continue
			}
			run.Warnings = append(run.Warnings, g.Warnings...)
			for _, po := range pos {
				run.PurchaseOrders = append(run.PurchaseOrders, ReleasedOrder{PurchaseOrderID: po.ID, Number: po.Number,
					VendorID: po.VendorID, Currency: po.Currency, Total: po.Total, LineCount: len(po.Lines)})
				run.Warnings = append(run.Warnings, po.Warnings...) // PRC-AGR-004: an agreement line drawn to its threshold
			}
			run.LinesReleased += len(g.Lines)
		}
		run.LinesSkipped = len(run.Skipped)
		switch {
		case run.LinesConsidered == 0:
			run.Status = ReleaseEmpty
		case run.LinesReleased == 0:
			run.Status = ReleaseFailed
		case run.LinesSkipped > 0:
			run.Status = ReleasePartial
		default:
			run.Status = ReleaseCompleted
		}
		if err := tx.QueryRow(ctx, `insert into purchase_release_run (tenant_id, id, legal_entity_id, window_days, max_lines, status,
			lines_considered, lines_released, lines_skipped, orders, skipped, warnings, requested_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) returning created_at`,
			p.TenantID, run.ID, run.LegalEntityID, run.WindowDays, run.MaxLines, run.Status, run.LinesConsidered, run.LinesReleased,
			run.LinesSkipped, run.PurchaseOrders, run.Skipped, run.Warnings, nullable(run.RequestedBy)).Scan(&run.CreatedAt); err != nil {
			return err
		}
		if run.Status != ReleaseEmpty {
			if err := enqueue(ctx, tx, "requisitionrelease", run.ID, purchasingevents.RequisitionReleaseCompleted, releaseEventData(run)); err != nil {
				return err
			}
		}
		out = run
		return nil
	})
	return out, err
}

// writeReleaseGroup writes one vendor group's POs under a savepoint. Any error rolls the savepoint back, so nothing the
// group wrote survives: its POs, PO lines, requisition line links, agreement draws and the outbox rows they enqueued
// (agreement.threshold.v1). The row locks taken before the savepoint are kept. A problem (a 4xx the group's lines
// caused) is returned for the caller to record and the transaction goes on; any other error aborts the run.
func writeReleaseGroup(ctx context.Context, tx pgx.Tx, g ReleaseGroup, locked []SourcingLine) (_ []PurchaseOrder, err error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = sp.Rollback(ctx)
		}
	}()
	if err = sameQuantities(g.Orders, locked); err != nil {
		return nil, err
	}
	var out []PurchaseOrder
	for _, o := range g.Orders {
		o.ID = ids.New()
		var po PurchaseOrder
		if po, err = insertPurchaseOrder(ctx, sp, o); err != nil {
			return nil, err
		}
		out = append(out, po)
	}
	if err = sp.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func releaseEventData(run ReleaseRun) purchasingevents.RequisitionReleaseData {
	d := purchasingevents.RequisitionReleaseData{RunID: run.ID, LegalEntityID: run.LegalEntityID, Status: run.Status, WindowDays: run.WindowDays,
		LinesConsidered: run.LinesConsidered, LinesReleased: run.LinesReleased, SkippedCount: run.LinesSkipped,
		PurchaseOrders: []purchasingevents.ReleasedOrder{}, Skipped: []purchasingevents.ReleaseSkippedLine{}, RequestedBy: run.RequestedBy}
	for _, o := range run.PurchaseOrders {
		d.PurchaseOrders = append(d.PurchaseOrders, purchasingevents.ReleasedOrder{PurchaseOrderID: o.PurchaseOrderID, Number: o.Number,
			VendorID: o.VendorID, Currency: o.Currency, Total: o.Total, LineCount: o.LineCount})
	}
	for _, sk := range run.Skipped {
		d.Skipped = append(d.Skipped, purchasingevents.ReleaseSkippedLine{RequisitionLineID: sk.RequisitionLineID, Code: sk.Code})
	}
	return d
}

const releaseRunColumns = `id, legal_entity_id, status, window_days, max_lines, lines_considered, lines_released, lines_skipped,
	orders, skipped, warnings, coalesce(requested_by::text, ''), created_at`

func scanReleaseRun(row pgx.Row) (ReleaseRun, error) {
	var r ReleaseRun
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.Status, &r.WindowDays, &r.MaxLines, &r.LinesConsidered, &r.LinesReleased, &r.LinesSkipped,
		&r.PurchaseOrders, &r.Skipped, &r.Warnings, &r.RequestedBy, &r.CreatedAt)
	if r.PurchaseOrders == nil {
		r.PurchaseOrders = []ReleasedOrder{}
	}
	if r.Skipped == nil {
		r.Skipped = []ReleaseSkip{}
	}
	if r.Warnings == nil {
		r.Warnings = []Warning{}
	}
	return r, err
}

func loadReleaseRun(ctx context.Context, tx pgx.Tx, id string) (ReleaseRun, error) {
	p, _ := tenant.FromContext(ctx)
	r, err := scanReleaseRun(tx.QueryRow(ctx, `select `+releaseRunColumns+` from purchase_release_run where tenant_id = $1 and id = $2`, p.TenantID, id))
	return r, notFound(err, CodeReleaseRunNotFound, "release run "+id+" not found")
}

// GetReleaseRun returns one release run.
func (s *Postgres) GetReleaseRun(ctx context.Context, id string) (ReleaseRun, error) {
	var out ReleaseRun
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadReleaseRun(ctx, tx, id)
		return err
	})
	return out, err
}

// ReleaseRunFilter narrows ListReleaseRuns; empty fields do not filter.
type ReleaseRunFilter struct {
	LegalEntityID string
	Status        string
}

// ListReleaseRuns lists release runs newest first.
func (s *Postgres) ListReleaseRuns(ctx context.Context, f ReleaseRunFilter, after string, limit int) (Page[ReleaseRun], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []ReleaseRun
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+releaseRunColumns+` from purchase_release_run where tenant_id = $1
			and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or status = $3) and ($4::uuid is null or id < $4::uuid)
			order by id desc limit $5`, p.TenantID, f.LegalEntityID, f.Status, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ReleaseRun, error) { return scanReleaseRun(row) })
		return err
	})
	if err != nil {
		return Page[ReleaseRun]{}, err
	}
	if items == nil {
		items = []ReleaseRun{}
	}
	return pageOf(items, limit, func(r ReleaseRun) string { return r.ID }), nil
}
