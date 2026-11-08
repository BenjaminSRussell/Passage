package store

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// The buyer workspace queues that are not about delivery dates (FR-17.14): agreement lines near
// their commitment, and purchase order changes waiting for re-approval. Both are read-only.

// DefaultNearLimitPct is the release level the near-limit queue starts at, the 90 % at which a
// release writes agreement.threshold.v1; the bounds are those of 3.17.13 agreement_threshold_pct.
const (
	DefaultNearLimitPct = 90
	MinNearLimitPct     = 50
	MaxNearLimitPct     = 100
)

// NearLimitLine is a line of an EFFECTIVE purchase agreement whose released amount (or quantity,
// for a QUANTITY line) is at or above the threshold of what was committed. PctReleased is
// domain.Pct of the line, the figure agreement.threshold.v1 carries: released * 100 / committed
// rounded half away from zero to 8 places, then half to even to 2. The query computes it in exact
// integer arithmetic so the queue and the event agree on which side of the threshold a line is.
// Overrun marks a line whose released figure is above the committed one, however small the excess
// (a SOFT or NONE line can pass it; a HARD line cannot). Committed, Released and
// Remaining are quantities in Unit for a QUANTITY line and amounts in Currency otherwise; Remaining
// is negative after an overrun.
type NearLimitLine struct {
	AgreementID    string          `json:"agreementId"`
	Number         string          `json:"number"`
	LegalEntityID  string          `json:"legalEntityId"`
	VendorID       string          `json:"vendorId"`
	Currency       string          `json:"currency"`
	ValidTo        *domain.Day     `json:"validTo,omitempty"`
	LineID         string          `json:"lineId"`
	LineNo         int             `json:"lineNo"`
	CommitmentType string          `json:"commitmentType"`
	ProductID      string          `json:"productId,omitempty"`
	Category       string          `json:"category,omitempty"`
	Unit           string          `json:"unit,omitempty"`
	MaxEnforced    string          `json:"maxEnforced"`
	Committed      decimal.Decimal `json:"committed"`
	Released       decimal.Decimal `json:"released"`
	Remaining      decimal.Decimal `json:"remaining"`
	PctReleased    decimal.Decimal `json:"pctReleased"`
	Overrun        bool            `json:"overrun"`
}

// ListNearLimitAgreementLines pages the lines of EFFECTIVE agreements released to at least
// f.ThresholdPct (default 90) percent of their commitment, most released first; the cursor is
// "<percent>|<line id>". An agreement that is ON_HOLD, DRAFT or CLOSED takes no releases and is
// left out.
func (s *Postgres) ListNearLimitAgreementLines(ctx context.Context, f InquiryFilter, after string, limit int) (Page[NearLimitLine], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	threshold := DefaultNearLimitPct
	if f.ThresholdPct != nil {
		threshold = *f.ThresholdPct
	}
	if threshold < MinNearLimitPct || threshold > MaxNearLimitPct {
		return Page[NearLimitLine]{}, &domain.Error{Code: domain.CodeValidation, Detail: "thresholdPct must be between " + strconv.Itoa(MinNearLimitPct) + " and " + strconv.Itoa(MaxNearLimitPct)}
	}
	var curPct *decimal.Decimal
	var curID *string
	if after != "" {
		parts := strings.Split(after, "|")
		pct, perr := decimal.NewFromString(parts[0])
		if len(parts) != 2 || perr != nil || !ids.Valid(parts[1]) {
			return Page[NearLimitLine]{}, &domain.Error{Code: domain.CodeValidation, Detail: "cursor is not one this list returned"}
		}
		curPct, curID = &pct, &parts[1]
	}
	var items []NearLimitLine
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select a.id, a.number, a.legal_entity_id, a.vendor_id, a.currency, a.valid_to, l.id, l.line_no,
			l.commitment_type, coalesce(l.product_id::text, ''), l.category, l.unit, l.max_enforced, m.committed, m.released, b.pct
			from purchase_agreement a
			join purchase_agreement_line l on l.tenant_id = a.tenant_id and l.agreement_id = a.id
			cross join lateral (select case when l.commitment_type = 'QUANTITY' then l.committed_qty else l.committed_amount end as committed,
				case when l.commitment_type = 'QUANTITY' then l.released_qty else l.released_amount end as released) m
			cross join lateral (select div(m.released * 10000000000, m.committed) as q, mod(m.released * 10000000000, m.committed) as r) d
			cross join lateral (select d.q + case when 2 * d.r >= m.committed then 1 else 0 end as p8) e
			cross join lateral (select (div(e.p8, 1000000) + case when mod(e.p8, 1000000) > 500000
				or (mod(e.p8, 1000000) = 500000 and mod(div(e.p8, 1000000), 2) = 1) then 1 else 0 end) * 0.01 as pct) b
			where a.tenant_id = $1 and a.status = 'EFFECTIVE'
			and ($3::uuid is null or a.legal_entity_id = $3::uuid)
			and ($4::uuid is null or a.vendor_id = $4::uuid)
			and b.pct >= $2::numeric
			and ($5::numeric is null or (b.pct, l.id) < ($5::numeric, $6::uuid))
			order by b.pct desc, l.id desc limit $7`,
			p.TenantID, threshold, nullable(f.LegalEntityID), nullable(f.VendorID), curPct, curID, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (NearLimitLine, error) {
			var l NearLimitLine
			var to *time.Time
			err := row.Scan(&l.AgreementID, &l.Number, &l.LegalEntityID, &l.VendorID, &l.Currency, &to, &l.LineID, &l.LineNo,
				&l.CommitmentType, &l.ProductID, &l.Category, &l.Unit, &l.MaxEnforced, &l.Committed, &l.Released, &l.PctReleased)
			if to != nil {
				d := day(*to)
				l.ValidTo = &d
			}
			// Overrun compares the stored figures: a line a fraction past its commitment rounds to
			// 100.00 % but is still over.
			l.Overrun = l.Released.GreaterThan(l.Committed)
			if l.CommitmentType != domain.CommitQuantity {
				minor := money.MinorUnits(l.Currency)
				l.Committed, l.Released = l.Committed.RoundBank(minor), l.Released.RoundBank(minor)
			}
			l.Remaining = l.Committed.Sub(l.Released)
			return l, err
		})
		return err
	})
	if err != nil {
		return Page[NearLimitLine]{}, err
	}
	if items == nil {
		items = []NearLimitLine{}
	}
	return pageOf(items, limit, func(l NearLimitLine) string { return l.PctReleased.StringFixed(2) + "|" + l.LineID }), nil
}

// ChangeOrder is a confirmed purchase order with a change waiting for re-approval (FR-17.8,
// FR-17.14): version PendingVersion is stored and not confirmed, and the PO is IN_REVIEW. The
// totals are the confirmed version's and the pending version's; ChangeCount is the number of
// operations in the pending patch. ChangedAt is when the pending version was last written (an edit
// of the change while it waits starts it again).
type ChangeOrder struct {
	PurchaseOrderID  string          `json:"purchaseOrderId"`
	Number           string          `json:"number"`
	LegalEntityID    string          `json:"legalEntityId"`
	VendorID         string          `json:"vendorId"`
	Currency         string          `json:"currency"`
	ConfirmedVersion int             `json:"confirmedVersion"`
	PendingVersion   int             `json:"pendingVersion"`
	ChangeCount      int             `json:"changeCount"`
	ConfirmedTotal   decimal.Decimal `json:"confirmedTotal"`
	ProposedTotal    decimal.Decimal `json:"proposedTotal"`
	ChangedAt        time.Time       `json:"changedAt"`
	ChangedBy        string          `json:"changedBy,omitempty"`
}

// ListChangeOrders pages IN_REVIEW purchase orders that have an unconfirmed version after their
// confirmed one, longest waiting first; the cursor is "<changed at>|<purchase order id>". A first
// submission (no version confirmed yet) is not a change order, and neither is a rejected change.
func (s *Postgres) ListChangeOrders(ctx context.Context, f InquiryFilter, after string, limit int) (Page[ChangeOrder], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var curAt *time.Time
	var curID *string
	if after != "" {
		parts := strings.SplitN(after, "|", 2)
		at, terr := time.Parse(time.RFC3339Nano, parts[0])
		if len(parts) != 2 || terr != nil || !ids.Valid(parts[1]) {
			return Page[ChangeOrder]{}, &domain.Error{Code: domain.CodeValidation, Detail: "cursor is not one this list returned"}
		}
		curAt, curID = &at, &parts[1]
	}
	var items []ChangeOrder
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id, o.number, o.legal_entity_id, o.vendor_id, o.currency, o.version_no, v.version_no,
			jsonb_array_length(v.patch), coalesce((c.snapshot->>'total')::numeric, 0), coalesce((v.snapshot->>'total')::numeric, o.total),
			v.created_at, coalesce(o.changed_by::text, '')
			from purchase_order o
			join purchase_order_version v on v.tenant_id = o.tenant_id and v.purchase_order_id = o.id and v.version_no = o.version_no + 1
			left join purchase_order_version c on c.tenant_id = o.tenant_id and c.purchase_order_id = o.id and c.version_no = o.version_no
			where o.tenant_id = $1 and o.approval_status = 'IN_REVIEW' and o.version_no >= 1 and v.confirmed_at is null
			and ($2::uuid is null or o.legal_entity_id = $2::uuid)
			and ($3::uuid is null or o.vendor_id = $3::uuid)
			and ($4::timestamptz is null or (v.created_at, o.id) > ($4::timestamptz, $5::uuid))
			order by v.created_at, o.id limit $6`,
			p.TenantID, nullable(f.LegalEntityID), nullable(f.VendorID), curAt, curID, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ChangeOrder, error) {
			var c ChangeOrder
			err := row.Scan(&c.PurchaseOrderID, &c.Number, &c.LegalEntityID, &c.VendorID, &c.Currency, &c.ConfirmedVersion, &c.PendingVersion,
				&c.ChangeCount, &c.ConfirmedTotal, &c.ProposedTotal, &c.ChangedAt, &c.ChangedBy)
			c.ChangedAt = c.ChangedAt.UTC()
			minor := money.MinorUnits(c.Currency)
			c.ConfirmedTotal, c.ProposedTotal = c.ConfirmedTotal.RoundBank(minor), c.ProposedTotal.RoundBank(minor)
			return c, err
		})
		return err
	})
	if err != nil {
		return Page[ChangeOrder]{}, err
	}
	if items == nil {
		items = []ChangeOrder{}
	}
	return pageOf(items, limit, func(c ChangeOrder) string { return c.ChangedAt.Format(time.RFC3339Nano) + "|" + c.PurchaseOrderID }), nil
}
