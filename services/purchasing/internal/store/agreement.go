package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// AgreementDetail is a purchase agreement with its lines and the PO lines
// released against them (FR-17.5 release tracking).
type AgreementDetail struct {
	Agreement domain.Agreement
	Releases  []AgreementRelease
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AgreementRelease is one PO line drawing on an agreement line.
type AgreementRelease struct {
	AgreementLineID string          `json:"agreementLineId"`
	PurchaseOrderID string          `json:"purchaseOrderId"`
	PurchaseOrder   string          `json:"purchaseOrderNumber"`
	LineNo          int             `json:"lineNo"`
	Qty             decimal.Decimal `json:"qty"`
	Unit            string          `json:"unit"`
	LineAmount      decimal.Decimal `json:"lineAmount"`
	DeliveryDate    domain.Day      `json:"deliveryDate"`
	ApprovalStatus  string          `json:"approvalStatus"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// AgreementSummary is a list row. MaxPctReleased is the highest release level
// across the lines, each on its own measure, for "near limit" queues (FR-17.14).
type AgreementSummary struct {
	ID             string          `json:"id"`
	Number         string          `json:"number"`
	LegalEntityID  string          `json:"legalEntityId"`
	VendorID       string          `json:"vendorId"`
	Currency       string          `json:"currency"`
	ValidFrom      domain.Day      `json:"validFrom"`
	ValidTo        *domain.Day     `json:"validTo,omitempty"`
	Status         string          `json:"status"`
	LineCount      int             `json:"lineCount"`
	MaxPctReleased decimal.Decimal `json:"maxPctReleased"`
	Version        int             `json:"version"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// AgreementFilter narrows ListAgreements; empty fields do not filter.
type AgreementFilter struct {
	Status   string
	VendorID string
}

// CreateAgreement stores a purchase agreement (DRAFT or EFFECTIVE, as given)
// with a fresh PA- number and its lines; released figures start at zero. A
// replayed Idempotency-Key returns the agreement created by the first request.
func (s *Postgres) CreateAgreement(ctx context.Context, in domain.Agreement, idem Idem) (AgreementDetail, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out AgreementDetail
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadAgreement(ctx, tx, existing)
			return err
		}
		var seq int64
		if err := tx.QueryRow(ctx, `select nextval('purchase_agreement_number_seq')`).Scan(&seq); err != nil {
			return err
		}
		in.Number = fmt.Sprintf("PA-%08d", seq)
		if _, err := tx.Exec(ctx, `insert into purchase_agreement (tenant_id, id, number, legal_entity_id, vendor_id, currency,
			valid_from, valid_to, status) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.TenantID, in.ID, in.Number, in.LegalEntityID, in.VendorID, in.Currency, in.ValidFrom.Time, dayPtr(in.ValidTo), in.Status); err != nil {
			return err
		}
		for i, l := range in.Lines {
			if _, err := tx.Exec(ctx, `insert into purchase_agreement_line (tenant_id, agreement_id, line_no, id, commitment_type,
				product_id, category, unit, committed_qty, committed_amount, unit_price, discount_pct, max_enforced, price_override)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
				p.TenantID, in.ID, i+1, ids.New(), l.CommitmentType, nullable(l.ProductID), l.Category, l.Unit,
				l.CommittedQty, l.CommittedAmount, l.UnitPrice, l.DiscountPct, l.MaxEnforced, l.PriceOverride); err != nil {
				return err
			}
		}
		out, err = loadAgreement(ctx, tx, in.ID)
		return err
	})
	return out, err
}

// GetAgreement returns one agreement with its lines and releases.
func (s *Postgres) GetAgreement(ctx context.Context, id string) (AgreementDetail, error) {
	var out AgreementDetail
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadAgreement(ctx, tx, id)
		return err
	})
	return out, err
}

// GetAgreementLine returns the agreement header with only the named line, for
// pricing a PO line that references it (FR-17.5 price override).
func (s *Postgres) GetAgreementLine(ctx context.Context, lineID string) (domain.Agreement, error) {
	var out domain.Agreement
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadAgreementLine(ctx, tx, lineID, false)
		return err
	})
	return out, err
}

// ChangeAgreementStatus applies activate, hold or close (domain.NextStatus) at
// the caller's version. A replayed Idempotency-Key returns the agreement as it
// is now instead of a conflict.
func (s *Postgres) ChangeAgreementStatus(ctx context.Context, id string, version int, action string, idem Idem) (AgreementDetail, error) {
	p, _ := tenant.FromContext(ctx)
	var out AgreementDetail
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadAgreement(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil || replay {
			out = cur
			return err
		}
		if cur.Agreement.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("agreement is at version %d, not %d; reload and retry", cur.Agreement.Version, version))
		}
		next, err := domain.NextStatus(cur.Agreement.Status, action)
		if err != nil {
			var de *domain.Error
			if errors.As(err, &de) {
				return httpx.NewProblem(http.StatusConflict, de.Code, de.Detail)
			}
			return err
		}
		tag, err := tx.Exec(ctx, `update purchase_agreement set status = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $4`, p.TenantID, id, next, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "agreement changed since it was read; reload and retry")
		}
		out, err = loadAgreement(ctx, tx, id)
		return err
	})
	return out, err
}

// ListAgreements returns agreement headers newest first, after the cursor id.
func (s *Postgres) ListAgreements(ctx context.Context, f AgreementFilter, after string, limit int) (Page[AgreementSummary], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []AgreementSummary
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select a.id, a.number, a.legal_entity_id, a.vendor_id, a.currency, a.valid_from, a.valid_to, a.status,
			count(l.line_no), coalesce(max(case when l.commitment_type = 'QUANTITY'
				then l.released_qty * 100 / nullif(l.committed_qty, 0)
				else l.released_amount * 100 / nullif(l.committed_amount, 0) end), 0),
			a.version, a.created_at
			from purchase_agreement a
			left join purchase_agreement_line l on l.tenant_id = a.tenant_id and l.agreement_id = a.id
			where a.tenant_id = $1 and ($2 = '' or a.status = $2) and ($3 = '' or a.vendor_id::text = $3)
			and ($4::uuid is null or a.id < $4::uuid)
			group by a.tenant_id, a.id
			order by a.id desc limit $5`, p.TenantID, f.Status, f.VendorID, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AgreementSummary, error) {
			var a AgreementSummary
			var from time.Time
			var to *time.Time
			err := row.Scan(&a.ID, &a.Number, &a.LegalEntityID, &a.VendorID, &a.Currency, &from, &to, &a.Status, &a.LineCount,
				&a.MaxPctReleased, &a.Version, &a.CreatedAt)
			a.ValidFrom, a.ValidTo = day(from), dayOrNil(to)
			a.MaxPctReleased = a.MaxPctReleased.RoundBank(2)
			return a, err
		})
		return err
	})
	if err != nil {
		return Page[AgreementSummary]{}, err
	}
	if items == nil {
		items = []AgreementSummary{}
	}
	return pageOf(items, limit, func(a AgreementSummary) string { return a.ID }), nil
}

const purchaseAgreementColumns = `a.id, a.number, a.legal_entity_id, a.vendor_id, a.currency, a.valid_from, a.valid_to, a.status, a.version`

func scanAgreementHeader(row pgx.Row, a *domain.Agreement, extra ...any) error {
	var from time.Time
	var to *time.Time
	dest := append([]any{&a.ID, &a.Number, &a.LegalEntityID, &a.VendorID, &a.Currency, &from, &to, &a.Status, &a.Version}, extra...)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	a.ValidFrom, a.ValidTo = day(from), dayOrNil(to)
	return nil
}

const purchaseAgreementLineColumns = `l.id, l.line_no, l.commitment_type, coalesce(l.product_id::text, ''), l.category, l.unit,
	l.committed_qty, l.committed_amount, l.unit_price, l.discount_pct, l.max_enforced, l.price_override, l.released_qty, l.released_amount`

func agreementLineDest(l *domain.AgreementLine) []any {
	return []any{&l.ID, &l.LineNo, &l.CommitmentType, &l.ProductID, &l.Category, &l.Unit, &l.CommittedQty, &l.CommittedAmount,
		&l.UnitPrice, &l.DiscountPct, &l.MaxEnforced, &l.PriceOverride, &l.ReleasedQty, &l.ReleasedAmount}
}

func loadAgreement(ctx context.Context, tx pgx.Tx, id string) (AgreementDetail, error) {
	p, _ := tenant.FromContext(ctx)
	var out AgreementDetail
	err := scanAgreementHeader(tx.QueryRow(ctx, `select `+purchaseAgreementColumns+`, a.created_at, a.updated_at
		from purchase_agreement a where a.tenant_id = $1 and a.id = $2`, p.TenantID, id), &out.Agreement, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return AgreementDetail{}, notFound(err, CodeAgreementNotFound, "purchase agreement not found")
	}
	rows, err := tx.Query(ctx, `select `+purchaseAgreementLineColumns+` from purchase_agreement_line l
		where l.tenant_id = $1 and l.agreement_id = $2 order by l.line_no`, p.TenantID, id)
	if err != nil {
		return AgreementDetail{}, err
	}
	out.Agreement.Lines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.AgreementLine, error) {
		var l domain.AgreementLine
		return l, row.Scan(agreementLineDest(&l)...)
	})
	if err != nil {
		return AgreementDetail{}, err
	}
	rows, err = tx.Query(ctx, `select ol.agreement_line_id, o.id, o.number, ol.line_no, ol.qty, ol.unit, ol.line_amount, ol.delivery_date,
		o.approval_status, o.created_at
		from purchase_order_line ol
		join purchase_order o on o.tenant_id = ol.tenant_id and o.id = ol.purchase_order_id
		join purchase_agreement_line l on l.tenant_id = ol.tenant_id and l.id = ol.agreement_line_id
		where ol.tenant_id = $1 and l.agreement_id = $2
		order by o.created_at, o.number, ol.line_no`, p.TenantID, id)
	if err != nil {
		return AgreementDetail{}, err
	}
	out.Releases, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AgreementRelease, error) {
		var r AgreementRelease
		var due time.Time
		err := row.Scan(&r.AgreementLineID, &r.PurchaseOrderID, &r.PurchaseOrder, &r.LineNo, &r.Qty, &r.Unit, &r.LineAmount, &due,
			&r.ApprovalStatus, &r.CreatedAt)
		r.DeliveryDate = day(due)
		return r, err
	})
	if out.Releases == nil {
		out.Releases = []AgreementRelease{}
	}
	return out, err
}

// loadAgreementLine reads the agreement header and one of its lines. With lock,
// the line is locked for update and the header for share, so concurrent
// releases on the line serialize (a HARD limit holds) and a status change
// cannot slip in between the check and the draw.
func loadAgreementLine(ctx context.Context, tx pgx.Tx, lineID string, lock bool) (domain.Agreement, error) {
	p, _ := tenant.FromContext(ctx)
	q := `select ` + purchaseAgreementColumns + `, ` + purchaseAgreementLineColumns + `
		from purchase_agreement_line l join purchase_agreement a on a.tenant_id = l.tenant_id and a.id = l.agreement_id
		where l.tenant_id = $1 and l.id = $2`
	if lock {
		q += ` for update of l for share of a`
	}
	var a domain.Agreement
	var l domain.AgreementLine
	if err := scanAgreementHeader(tx.QueryRow(ctx, q, p.TenantID, lineID), &a, agreementLineDest(&l)...); err != nil {
		return domain.Agreement{}, notFound(err, CodeAgreementNotFound, "agreement line "+lineID+" not found")
	}
	a.Lines = []domain.AgreementLine{l}
	return a, nil
}

// lockAgreementLinesOf locks, in one statement in id order, the agreement lines the PO lines draw on. It is the
// last step of the lock order every writer of purchase documents follows: requisitions, requisition lines, then
// agreement lines. Two POs that draw the same two agreement lines in opposite order would otherwise lock them in
// their own line order and deadlock; the PO cancel and edit paths that give a release back use it too.
func lockAgreementLinesOf(ctx context.Context, tx pgx.Tx, lines []PurchaseOrderLine) error {
	var lineIDs []string
	for _, l := range lines {
		if l.AgreementLineID != "" {
			lineIDs = append(lineIDs, l.AgreementLineID)
		}
	}
	if len(lineIDs) == 0 {
		return nil
	}
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `select 1 from purchase_agreement_line where tenant_id = $1 and id = any($2::uuid[]) order by id for update`,
		p.TenantID, lineIDs)
	return err
}

// drawAgreements releases each PO line that references an agreement line
// against it (FR-17.5) inside the PO's transaction: the release must pass
// domain.CheckRelease, a HARD overrun refuses the whole PO with PRC-AGR-003,
// and a release that reaches the threshold or overruns a SOFT limit writes
// agreement.threshold.v1 and returns a PRC-AGR-004 warning for the line.
func drawAgreements(ctx context.Context, tx pgx.Tx, po PurchaseOrder) ([]Warning, error) {
	p, _ := tenant.FromContext(ctx)
	var warnings []Warning
	if err := lockAgreementLinesOf(ctx, tx, po.Lines); err != nil {
		return nil, err
	}
	for _, pl := range po.Lines {
		if pl.AgreementLineID == "" {
			continue
		}
		agr, err := loadAgreementLine(ctx, tx, pl.AgreementLineID, true)
		if err != nil {
			return nil, err
		}
		line := agr.Lines[0]
		if err := agr.CheckRelease(line, domain.Release{LegalEntityID: po.LegalEntityID, VendorID: po.VendorID, Currency: po.Currency,
			ProductID: pl.ProductID, Unit: pl.Unit, DeliveryDate: pl.DeliveryDate}); err != nil {
			return nil, err
		}
		res, err := line.Draw(domain.Draw{Qty: pl.Qty, Amount: pl.LineAmount}, domain.DefaultThresholdPct)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `update purchase_agreement_line set released_qty = $3, released_amount = $4
			where tenant_id = $1 and id = $2`, p.TenantID, line.ID, line.ReleasedQty, line.ReleasedAmount); err != nil {
			return nil, err
		}
		if !res.Notify() {
			continue
		}
		data := purchasingevents.AgreementThresholdData{AgreementID: agr.ID, Number: agr.Number, VendorID: agr.VendorID, Line: line.LineNo,
			LineID: line.ID, CommitmentType: line.CommitmentType, Pct: res.PctAfter.String(), Overrun: res.Overrun}
		if agr.ValidTo != nil {
			data.Expiry = agr.ValidTo.String()
		}
		if err := enqueue(ctx, tx, "purchase_agreement", agr.ID, purchasingevents.AgreementThreshold, data); err != nil {
			return nil, err
		}
		detail := fmt.Sprintf("Agreement line %d is at %s %% of commitment", line.LineNo, res.PctAfter)
		if res.Overrun {
			detail += " (SOFT limit exceeded)"
		}
		warnings = append(warnings, Warning{Code: domain.CodeAgreementThreshold, LineNo: pl.LineNo, Detail: detail})
	}
	return warnings, nil
}

func dayPtr(d *domain.Day) *time.Time {
	if d == nil {
		return nil
	}
	t := d.Time
	return &t
}

func dayOrNil(t *time.Time) *domain.Day {
	if t == nil {
		return nil
	}
	d := day(*t)
	return &d
}
