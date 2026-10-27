package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

// PostParams are the posting-profile and policy inputs of a journal post.
type PostParams struct {
	Negative          domain.NegativePolicy
	InventoryAccount  string
	AdjustmentAccount string // offset for ADJUSTMENT journals
	OpeningAccount    string // offset (migration clearing) for OPENING_BALANCE journals
}

// row is one projection row touched by a journal: the indexes of its plan
// lines (in line order) and the projection state before and after the posting.
type row struct {
	product string
	dims    domain.Dimensions
	hash    []byte
	lines   []int
	before  domain.OnHand
	oh      domain.OnHand
	seq     int64
}

// rowKey orders rows by product, then dimension hash, the lock order of BR-16.4.
func rowKey(product string, hash []byte) string { return product + "|" + string(hash) }

// PostJournal posts a DRAFT or APPROVED journal in one transaction: it locks the
// projection rows in ascending order, values each line (receipts at their cost
// price, issues at the row's running average), applies the deltas, checks the
// negative policy and serial uniqueness under the locks, writes transactions
// and origins, updates the projection, stores the balanced GL entry and
// enqueues the events. The version must match (If-Match). Posting a POSTED
// journal again returns it unchanged (the journal id is the idempotency key).
func (s *Postgres) PostJournal(ctx context.Context, id string, version int, p PostParams) (domain.Journal, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := loadJournal(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if j.Status == domain.JournalPosted {
			out = j
			return nil
		}
		if j.Status == domain.JournalPosting {
			return problem(http.StatusConflict, domain.CodeJournalNotReady, "journal is already posting")
		}
		if j.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "journal changed since it was read; reread and retry")
		}
		if j.Status != domain.JournalDraft && j.Status != domain.JournalApproved {
			return problem(http.StatusConflict, CodeJournalState, "journal cannot be posted from status "+string(j.Status))
		}
		if err := domain.ValidateJournal(j, s.rules); err != nil {
			return domainProblem(err)
		}
		offset := j.OffsetAccountID
		switch j.Type {
		case domain.JournalAdjustment:
			offset = p.AdjustmentAccount
		case domain.JournalOpeningBalance:
			offset = p.OpeningAccount
		}
		plan := domain.PlanPosting(j)
		rows, order, err := groupRows(plan)
		if err != nil {
			return err
		}
		serials := receiptSerials(plan)
		if err := lockSerials(ctx, tx, tp.TenantID, j.LegalEntityID, serials); err != nil {
			return err
		}
		for _, k := range order {
			if err := lockAndApply(ctx, tx, tp.TenantID, j, rows[k], plan, p.Negative); err != nil {
				return err
			}
		}
		if err := checkSerials(ctx, tx, tp.TenantID, j.LegalEntityID, serials); err != nil {
			return err
		}
		entry, err := domain.BuildEntry(plan, p.InventoryAccount, offset, j.Currency)
		if err != nil {
			return err
		}
		if !entry.Balanced() {
			return errors.New("inventory: journal entry does not balance")
		}
		if err := writeTransactions(ctx, tx, tp.TenantID, j, plan, rows, order); err != nil {
			return err
		}
		glJSON, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update inventory_journal set status = 'POSTED', gl_entry = $3, posted_at = now(), version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, tp.TenantID, id, glJSON); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "journal", id, invevents.JournalPosted, tp.TenantID, journalPosted(j, entry)); err != nil {
			return err
		}
		out, err = loadJournal(ctx, tx, id, false)
		return err
	})
	return out, err
}

// groupRows collects the plan into projection rows, sorted by lock key.
func groupRows(plan []domain.PostedLine) (map[string]*row, []string, error) {
	rows := map[string]*row{}
	var order []string
	for i, pl := range plan {
		dims, err := pl.Line.Dims.Normalize()
		if err != nil {
			return nil, nil, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
		}
		hash := dims.Hash()
		k := rowKey(pl.Line.ProductID, hash)
		r, ok := rows[k]
		if !ok {
			r = &row{product: pl.Line.ProductID, dims: dims, hash: hash}
			rows[k] = r
			order = append(order, k)
		}
		r.lines = append(r.lines, i)
	}
	slices.Sort(order)
	return rows, order, nil
}

// serialKey is one (product, serial) a journal receives.
type serialKey struct{ product, serial string }

// receiptSerials lists the distinct serials received by the plan, sorted.
func receiptSerials(plan []domain.PostedLine) []serialKey {
	var out []serialKey
	for _, pl := range plan {
		if serial := strings.TrimSpace(pl.Line.Dims[domain.DimSerial]); serial != "" && pl.Line.Qty.Sign() > 0 {
			k := serialKey{pl.Line.ProductID, serial}
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	slices.SortFunc(out, func(a, b serialKey) int {
		return strings.Compare(a.product+"|"+a.serial, b.product+"|"+b.serial)
	})
	return out
}

// lockSerials takes a transaction advisory lock per received serial, in sorted
// order, so two journals receiving the same serial at different warehouses
// serialize and the second sees the first's stock.
func lockSerials(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, serials []serialKey) error {
	for _, k := range serials {
		key := strings.Join([]string{"inventory.serial", tenantID, legalEntityID, k.product, k.serial}, "|")
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return err
		}
	}
	return nil
}

// checkSerials enforces BR-16.9 after the projection is updated: a received
// serial may be in physical stock at most once across the legal entity.
func checkSerials(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, serials []serialKey) error {
	for _, k := range serials {
		var total string
		if err := tx.QueryRow(ctx, `select coalesce(sum(o.posted_qty + o.received + o.registered - o.deducted - o.picked), 0)::text
			from on_hand o join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
			where o.tenant_id = $1 and o.legal_entity_id = $2 and o.product_id = $3 and d.dims->>'serial' = $4`,
			tenantID, legalEntityID, k.product, k.serial).Scan(&total); err != nil {
			return err
		}
		if decimal.RequireFromString(total).GreaterThan(decimal.NewFromInt(1)) {
			return problem(http.StatusUnprocessableEntity, domain.CodeDuplicateSerial,
				fmt.Sprintf("serial %s of product %s is already in physical stock", k.serial, k.product))
		}
	}
	return nil
}

// lockAndApply locks one projection row, values and applies the row's lines in
// line order, and checks the negative policy while the lock is held (FR-16.4,
// BR-16.4). Issue values come from the locked row, so concurrent issues see a
// consistent running average.
func lockAndApply(ctx context.Context, tx pgx.Tx, tenantID string, j domain.Journal, r *row, plan []domain.PostedLine, neg domain.NegativePolicy) error {
	if _, _, err := upsertDimensionSet(ctx, tx, tenantID, r.dims); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into on_hand (tenant_id, legal_entity_id, product_id, dimension_set_hash, currency) values ($1, $2, $3, $4, $5)
		on conflict do nothing`, tenantID, j.LegalEntityID, r.product, r.hash, j.Currency); err != nil {
		return scopeProblem(err)
	}
	var b [11]string
	var currency *string
	dest := make([]any, 0, 13)
	for i := range b {
		dest = append(dest, &b[i])
	}
	dest = append(dest, &currency, &r.seq)
	if err := tx.QueryRow(ctx, `select posted_qty::text, posted_value::text, received::text, registered::text, arrived::text, ordered::text,
		deducted::text, picked::text, reserved_physical::text, reserved_ordered::text, on_order::text, currency::text, last_txn_seq
		from on_hand where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and dimension_set_hash = $4 for update`,
		tenantID, j.LegalEntityID, r.product, r.hash).Scan(dest...); err != nil {
		return err
	}
	if currency != nil && *currency != j.Currency {
		return problem(http.StatusUnprocessableEntity, CodeCurrencyMismatch,
			fmt.Sprintf("on-hand for product %s at %s is valued in %s; the journal is in %s", r.product, describeDims(r.dims), *currency, j.Currency))
	}
	r.before = onHandFrom(b)
	r.oh = r.before
	for _, i := range r.lines {
		l := plan[i].Line
		if l.Qty.Sign() > 0 {
			plan[i].Value = domain.ReceiptValue(l.Qty, l.CostPrice, j.Currency)
		} else {
			plan[i].Value = r.oh.IssueValue(l.Qty, l.CostPrice, j.Currency)
		}
		if err := r.oh.Post(plan[i].Status, l.Qty, plan[i].Value, 1); err != nil {
			return err
		}
	}
	if err := r.oh.CheckNegative(r.before, neg); err != nil {
		var ne *domain.NegativeError
		if errors.As(err, &ne) {
			what := "available physical"
			if ne.Code == domain.CodeNegativeFinancial {
				what = "financial (ordered)"
			}
			return problem(http.StatusUnprocessableEntity, ne.Code,
				fmt.Sprintf("issue would make %s stock negative for product %s at %s (shortfall %s)", what, r.product, describeDims(r.dims), ne.Shortfall))
		}
		return err
	}
	_, err := tx.Exec(ctx, `update on_hand set posted_qty = $5, posted_value = $6, received = $7, registered = $8, arrived = $9, ordered = $10,
		deducted = $11, picked = $12, reserved_physical = $13, reserved_ordered = $14, on_order = $15, currency = $16, last_txn_seq = $17, updated_at = now()
		where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and dimension_set_hash = $4`,
		tenantID, j.LegalEntityID, r.product, r.hash, r.oh.PostedQty.String(), r.oh.PostedValue.String(), r.oh.Received.String(), r.oh.Registered.String(),
		r.oh.Arrived.String(), r.oh.Ordered.String(), r.oh.Deducted.String(), r.oh.Picked.String(), r.oh.ReservedPhysical.String(),
		r.oh.ReservedOrdered.String(), r.oh.OnOrder.String(), j.Currency, r.seq+int64(len(r.lines)))
	return err
}

// describeDims renders a dimension set for messages: "site S1, warehouse W1".
func describeDims(d domain.Dimensions) string {
	var parts []string
	for _, name := range domain.DimensionNames() {
		if v := d[name]; v != "" {
			parts = append(parts, name+" "+v)
		}
	}
	if len(parts) == 0 {
		return "no dimensions"
	}
	return strings.Join(parts, ", ")
}

// writeTransactions inserts one origin and one transaction per journal line and
// enqueues the transaction and on-hand events in the same transaction.
func writeTransactions(ctx context.Context, tx pgx.Tx, tenantID string, j domain.Journal, plan []domain.PostedLine, rows map[string]*row, order []string) error {
	for _, k := range order {
		r := rows[k]
		for _, i := range r.lines {
			if err := writeTransaction(ctx, tx, tenantID, j, r, plan[i]); err != nil {
				return err
			}
		}
	}
	for _, k := range order {
		r := rows[k]
		delta := decimal.Zero
		for _, i := range r.lines {
			delta = delta.Add(plan[i].Line.Qty)
		}
		if err := enqueue(ctx, tx, "onhand", r.product, invevents.OnhandChanged, tenantID, invevents.OnhandChangedData{
			LegalEntityID: j.LegalEntityID, ProductID: r.product, DimensionSet: map[string]string(r.dims),
			AvailPhysical: r.oh.AvailPhysical().String(), AvailOrdered: r.oh.AvailOrdered().String(), PhysicalInvent: r.oh.PhysicalInvent().String(),
			DeltaBucket: invevents.BucketPosted, DeltaQty: delta.String(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// writeTransaction inserts the origin and the transaction for one journal line
// and enqueues the transaction event.
func writeTransaction(ctx context.Context, tx pgx.Tx, tenantID string, j domain.Journal, r *row, pl domain.PostedLine) error {
	originID, txnID := ids.New(), ids.New()
	direction := string(domain.ReceiptDirection)
	if pl.Status == domain.StatusSold {
		direction = string(domain.IssueDirection)
	}
	if _, err := tx.Exec(ctx, `insert into inventory_transaction_origin (tenant_id, legal_entity_id, id, source_type, source_id, product_id, direction)
		values ($1, $2, $3, 'INVENT_JOURNAL_LINE', $4, $5, $6)`, tenantID, j.LegalEntityID, originID, pl.Line.ID, r.product, direction); err != nil {
		return err
	}
	status := string(pl.Status)
	var issue, receipt *string
	if direction == string(domain.IssueDirection) {
		issue = &status
	} else {
		receipt = &status
	}
	value := pl.Value.String()
	posting := j.PostingDate.Format(dateLayout)
	if _, err := tx.Exec(ctx, `insert into inventory_transaction (tenant_id, legal_entity_id, id, origin_id, product_id, dimension_set_hash, qty, entered_qty,
		entered_unit, issue_status, receipt_status, physical_date, financial_date, currency, cost_amount_physical, cost_amount_posted)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12, $13, $14, $14)`,
		tenantID, j.LegalEntityID, txnID, originID, r.product, r.hash, pl.Line.Qty.String(), pl.Line.EnteredQty.String(), pl.Line.EnteredUnit,
		issue, receipt, posting, j.Currency, value); err != nil {
		return err
	}
	return enqueue(ctx, tx, "transaction", txnID, invevents.TransactionPosted, tenantID, invevents.TransactionPostedData{
		TransactionID: txnID, LegalEntityID: j.LegalEntityID, JournalID: j.ID, OriginID: originID, ProductID: r.product,
		DimensionSet: map[string]string(r.dims), Qty: pl.Line.Qty.String(), IssueStatus: issue, ReceiptStatus: receipt,
		PhysicalDate: posting, FinancialDate: posting, Currency: j.Currency, CostAmountPhysical: value, CostAmountPosted: value,
	})
}

func onHandFrom(b [11]string) domain.OnHand {
	d := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }
	return domain.OnHand{PostedQty: d(b[0]), PostedValue: d(b[1]), Received: d(b[2]), Registered: d(b[3]), Arrived: d(b[4]),
		Ordered: d(b[5]), Deducted: d(b[6]), Picked: d(b[7]), ReservedPhysical: d(b[8]), ReservedOrdered: d(b[9]), OnOrder: d(b[10])}
}

func journalPosted(j domain.Journal, entry domain.JournalEntry) invevents.JournalPostedData {
	out := invevents.JournalPostedData{JournalID: j.ID, LegalEntityID: j.LegalEntityID, JournalType: string(j.Type),
		PostingDate: j.PostingDate.Format(dateLayout), Currency: entry.Currency, LineCount: len(j.Lines), GLLines: []invevents.GLLineData{}}
	for _, l := range entry.Lines {
		out.GLLines = append(out.GLLines, invevents.GLLineData{Account: l.Account, Debit: l.Debit.String(), Credit: l.Credit.String()})
	}
	return out
}
