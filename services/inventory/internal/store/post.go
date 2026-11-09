package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

// Origin source types written by this service.
const (
	sourceJournalLine     = "INVENT_JOURNAL_LINE"
	sourceBlocking        = "BLOCKING"
	sourceOwnershipChange = "OWNERSHIP_CHANGE" // receipt leg of an ownership-change line
	sourceConsignmentLine = "CONSIGNMENT_LINE" // vendor-owned receipt of a replenishment line
	sourceTransferIn      = "TRANSFER_IN"      // destination receipt of a TRANSFER journal line
)

// PostParams are the posting-profile and policy inputs of a journal post.
type PostParams struct {
	Negative          domain.NegativePolicy
	InventoryAccount  string
	AdjustmentAccount string // offset for ADJUSTMENT and COUNT journals
	OpeningAccount    string // offset (migration clearing) for OPENING_BALANCE journals
	// ConsignmentAccount is the consignment accrual offset of OWNERSHIP_CHANGE
	// journals (posting profile account consignment_accrual; SAP OBYC KON).
	ConsignmentAccount string
}

// offsetAccount is the GL offset of a journal: MOVEMENT its own offset account,
// the other types the posting-profile account for their type.
func offsetAccount(j domain.Journal, p PostParams) string {
	switch j.Type {
	case domain.JournalAdjustment, domain.JournalCount:
		return p.AdjustmentAccount
	case domain.JournalOpeningBalance:
		return p.OpeningAccount
	case domain.JournalOwnershipChange:
		return p.ConsignmentAccount
	}
	return j.OffsetAccountID
}

// row is one projection row touched by a posting: the indexes of its plan
// lines (in line order) and the projection state before and after the posting.
type row struct {
	product string
	dims    domain.Dimensions
	hash    []byte
	lines   []int
	before  domain.OnHand
	oh      domain.OnHand
	seq     int64
	// currency the row is valued in; empty for a row never valued.
	currency string
}

// rowKey orders rows by product, then dimension hash, the lock order of BR-16.4.
func rowKey(product string, hash []byte) string { return product + "|" + string(hash) }

// document describes what a set of transactions belongs to: the legal entity,
// the accounting currency, the physical and financial date, the origin source
// type, and the journal or reference id carried on the events.
type document struct {
	legalEntityID string
	currency      string
	date          time.Time
	sourceType    string
	journalID     string // set for journal lines
	referenceID   string // set for other documents (blocking id)
	// statusChangeID is the audit row of a transaction registered through
	// transactions:apply (CREATE); empty for document postings.
	statusChangeID string
	// lineSourceID is the document line a tracked entry's origin belongs to
	// (#480); empty otherwise.
	lineSourceID string
}

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
		if err := checkNamePostable(ctx, tx, j); err != nil {
			return err
		}
		if err := s.postLoaded(ctx, tx, tp.TenantID, j, p, ""); err != nil {
			return err
		}
		out, err = loadJournal(ctx, tx, id, false)
		return err
	})
	return out, err
}

// postLoaded posts a journal already read (and locked) in tx: validation,
// row locks, valuation, negative and serial checks, transactions, the GL
// entry, the POSTED status and the events. Count sheets reuse it for their
// COUNT journal.
//
// countSheet is the sheet whose own COUNT journal this is (empty for any other
// journal); only that sheet's freeze does not apply to it.
func (s *Postgres) postLoaded(ctx context.Context, tx pgx.Tx, tenantID string, j domain.Journal, p PostParams, countSheet string) error {
	if err := domain.ValidateJournal(j, s.rules); err != nil {
		return domainProblem(err)
	}
	offset := offsetAccount(j, p)
	if offset == "" && !valuedBeforeLoop(j.Type) {
		return problem(http.StatusUnprocessableEntity, CodeAccountMissing, fmt.Sprintf("no offset account is configured for %s journals", j.Type))
	}
	plan := domain.PlanPosting(j)
	rows, order, err := groupRows(plan)
	if err != nil {
		return err
	}
	sets := make([]domain.Dimensions, 0, len(order))
	for _, k := range order {
		sets = append(sets, rows[k].dims)
	}
	// Shared dimension sets first, in hash order, before the serial and row
	// locks (a first insert holds its index entry until commit).
	if err := upsertDimensionSets(ctx, tx, tenantID, sets...); err != nil {
		return err
	}
	// Before any row lock, so the recount flag may wait for sheet locks; a
	// count posting already holds its sheet and rows and must not.
	if err := checkCountFreeze(ctx, tx, tenantID, j.LegalEntityID, countSheet, countSheet != "", orderedRows(rows, order)); err != nil {
		return err
	}
	serials := receiptSerials(plan)
	if err := lockSerials(ctx, tx, tenantID, j.LegalEntityID, serials); err != nil {
		return err
	}
	corrected, err := correctedTransactions(ctx, tx, tenantID, j)
	if err != nil {
		return err
	}
	switch j.Type {
	case domain.JournalTransfer:
		if err := valueTransfer(ctx, tx, tenantID, j, plan, rows, order); err != nil {
			return err
		}
	case domain.JournalBOM:
		if err := valueBOM(ctx, tx, tenantID, j, plan, rows, order); err != nil {
			return err
		}
	}
	for _, k := range order {
		r := rows[k]
		vendorOwned := domain.VendorOwned(r.dims)
		currency := j.Currency
		if vendorOwned {
			currency = "" // vendor-owned rows are never valued, in any currency
		}
		if err := lockRow(ctx, tx, tenantID, j.LegalEntityID, currency, r); err != nil {
			return err
		}
		for _, i := range r.lines {
			l := plan[i].Line
			if vendorOwned {
				// Vendor-owned stock never carries value (BR-16.14).
				plan[i].Value = decimal.Zero
			} else if valuedBeforeLoop(j.Type) {
				// Valued before the row locks of the loop (valueTransfer, valueBOM).
			} else if orig, ok := corrected[l.ReversesLineID]; ok {
				// A reversal line mirrors the transaction it corrects (BR-16.12).
				plan[i].Value, plan[i].Corrects = orig.value.Neg(), orig.id
			} else if l.Qty.Sign() > 0 {
				plan[i].Value = domain.ReceiptValue(l.Qty, l.CostPrice, j.Currency)
			} else {
				plan[i].Value = r.oh.IssueValue(l.Qty, l.CostPrice, j.Currency)
			}
			if err := r.oh.Post(plan[i].Status, l.Qty, plan[i].Value, 1); err != nil {
				return err
			}
		}
		if vendorOwned {
			if err := checkVendorOwned(r, j.Type == domain.JournalCount); err != nil {
				return err
			}
		} else {
			if err := checkNegative(r, p.Negative); err != nil {
				return err
			}
			r.currency = j.Currency
		}
		if err := saveRow(ctx, tx, tenantID, j.LegalEntityID, r, len(r.lines)); err != nil {
			return err
		}
	}
	// A count records what is there rather than moving it, so it is not
	// weighed against the claims (it may find claimed units already gone).
	if j.Type != domain.JournalCount {
		if err := guardClaimedStock(ctx, tx, tenantID, j.LegalEntityID, orderedRows(rows, order), &issueSettlement{key: "journal:" + j.ID}); err != nil {
			return err
		}
	}
	if err := checkSerials(ctx, tx, tenantID, j.LegalEntityID, serials); err != nil {
		return err
	}
	entry := domain.JournalEntry{Currency: j.Currency, Lines: []domain.GLLine{}}
	if !valuedBeforeLoop(j.Type) {
		// A transfer moves value between dimension sets, and a BOM journal
		// from the components to the parent, of the same inventory account:
		// both sides cancel and there is no GL entry.
		if entry, err = domain.BuildEntry(plan, p.InventoryAccount, offset, j.Currency); err != nil {
			return err
		}
	}
	if !entry.Balanced() {
		return errors.New("inventory: journal entry does not balance")
	}
	doc := document{legalEntityID: j.LegalEntityID, currency: j.Currency, date: j.PostingDate, sourceType: sourceJournalLine, journalID: j.ID}
	for _, k := range order {
		r := rows[k]
		for _, i := range r.lines {
			d := doc
			if plan[i].Paired {
				d.sourceType = sourceOwnershipChange
				if j.Type == domain.JournalTransfer {
					d.sourceType = sourceTransferIn
				}
			}
			if _, err := writeTransaction(ctx, tx, tenantID, d, plan[i].Line.ID, r, plan[i]); err != nil {
				return err
			}
		}
		bucket, delta := invevents.BucketPosted, r.oh.PostedQty.Sub(r.before.PostedQty)
		if domain.VendorOwned(r.dims) {
			// Vendor-owned rows move only physical buckets: DEDUCTED by an
			// ownership change or a count loss, RECEIVED by a count gain.
			bucket, delta = invevents.BucketDeducted, r.oh.Deducted.Sub(r.before.Deducted)
			if received := r.oh.Received.Sub(r.before.Received); !received.IsZero() {
				bucket, delta = invevents.BucketReceived, received
			}
		}
		if err := enqueueOnHand(ctx, tx, tenantID, j.LegalEntityID, r, bucket, delta); err != nil {
			return err
		}
	}
	glJSON, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update inventory_journal set status = 'POSTED', gl_entry = $3, posted_at = now(), version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`, tenantID, j.ID, glJSON); err != nil {
		return err
	}
	if err := enqueue(ctx, tx, "journal", j.ID, invevents.JournalPosted, tenantID, journalPosted(j, entry)); err != nil {
		return err
	}
	if j.Type == domain.JournalOwnershipChange {
		return enqueue(ctx, tx, "journal", j.ID, invevents.OwnershipChanged, tenantID, ownershipChanged(j, plan))
	}
	return nil
}

// valuedBeforeLoop reports the journal types whose lines are valued against
// each other before the posting loop and leave the inventory account unmoved
// (no offset account, no GL entry).
func valuedBeforeLoop(t domain.JournalType) bool {
	return t == domain.JournalTransfer || t == domain.JournalBOM
}

// valueBOM values a BOM journal before the posting loop: it locks every row in
// key order, values each component issue at its row's running average (lines
// of one row in line order) and gives the parent receipt the summed component
// value, so the inventory account does not move. CheckBOMLines guarantees one
// receipt and no component of the parent's product, so the parent row is never
// a component row.
func valueBOM(ctx context.Context, tx pgx.Tx, tenantID string, j domain.Journal, plan []domain.PostedLine, rows map[string]*row, order []string) error {
	total, parent := decimal.Zero, -1
	for _, k := range order {
		r := rows[k]
		if err := lockRow(ctx, tx, tenantID, j.LegalEntityID, j.Currency, r); err != nil {
			return err
		}
		oh := r.oh
		for _, i := range r.lines {
			l := plan[i].Line
			if l.Qty.Sign() > 0 {
				parent = i
				continue
			}
			v := oh.IssueValue(l.Qty, l.CostPrice, j.Currency)
			plan[i].Value, total = v, total.Add(v)
			if err := oh.Post(plan[i].Status, l.Qty, v, 1); err != nil {
				return err
			}
		}
	}
	if parent < 0 {
		return errors.New("inventory: BOM journal has no parent receipt")
	}
	plan[parent].Value = total.Neg()
	return nil
}

// valueTransfer values the legs of a TRANSFER journal before the posting loop:
// it locks every row in key order, values each issue at its source row's
// running average (lines of one source row in line order), and gives the
// paired receipt the same value with the opposite sign, so the inventory
// account does not move. CheckTransferLines guarantees that no row is both a
// source and a destination.
func valueTransfer(ctx context.Context, tx pgx.Tx, tenantID string, j domain.Journal, plan []domain.PostedLine, rows map[string]*row, order []string) error {
	for _, k := range order {
		r := rows[k]
		if err := lockRow(ctx, tx, tenantID, j.LegalEntityID, j.Currency, r); err != nil {
			return err
		}
		oh := r.oh
		for _, i := range r.lines {
			l := plan[i].Line
			if l.Qty.Sign() >= 0 {
				continue
			}
			v := oh.IssueValue(l.Qty, l.CostPrice, j.Currency)
			plan[i].Value, plan[i+1].Value = v, v.Neg()
			if err := oh.Post(plan[i].Status, l.Qty, v, 1); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkVendorOwned enforces BR-16.14 on a vendor-owned row after its deltas:
// it never carries posted quantity or value, and, whatever the negative policy
// says (the stock is not the company's), an ownership change cannot take more
// than its available physical quantity (INV-OWN-002).
//
// count marks a COUNT journal: a count sets the physical quantity to what was
// found, so a loss may take reserved or held quantity too, but no COUNT line
// (the reversal of an earlier count gain included) may leave the vendor with
// less than nothing.
func checkVendorOwned(r *row, count bool) error {
	if !r.oh.PostedQty.IsZero() || !r.oh.PostedValue.IsZero() {
		return problem(http.StatusUnprocessableEntity, domain.CodeOwnershipValue,
			fmt.Sprintf("vendor-owned stock of product %s at %s cannot be valued or financially posted", r.product, describeDims(r.dims)))
	}
	if count {
		if phys := r.oh.PhysicalInvent(); phys.Sign() < 0 && phys.LessThan(r.before.PhysicalInvent()) {
			have := decimal.Max(r.before.PhysicalInvent(), decimal.Zero)
			return problem(http.StatusUnprocessableEntity, domain.CodeOwnershipExceeds,
				fmt.Sprintf("count of %s exceeds vendor-owned quantity %s at %s", r.before.PhysicalInvent().Sub(r.oh.PhysicalInvent()), have, describeDims(r.dims)))
		}
		return nil
	}
	if avail := r.oh.AvailPhysical(); avail.Sign() < 0 && avail.LessThan(r.before.AvailPhysical()) {
		have := decimal.Max(r.before.AvailPhysical(), decimal.Zero)
		return problem(http.StatusUnprocessableEntity, domain.CodeOwnershipExceeds,
			fmt.Sprintf("ownership change of %s exceeds vendor-owned quantity %s at %s", have.Sub(avail), have, describeDims(r.dims)))
	}
	return nil
}

// ownershipChanged is the ownership.changed payload: per journal line the
// vendor-owned dimension set it moved, the quantity, the consignment price and
// the accounting amount (the value of its receipt leg).
func ownershipChanged(j domain.Journal, plan []domain.PostedLine) invevents.OwnershipChangedData {
	out := invevents.OwnershipChangedData{JournalID: j.ID, LegalEntityID: j.LegalEntityID, VendorID: domain.OwnershipVendor(j),
		PostingDate: j.PostingDate.Format(dateLayout), Currency: j.Currency, LedgerEntryID: nil, Lines: []invevents.OwnershipLineData{}}
	total := decimal.Zero
	for _, pl := range plan {
		if !pl.Paired {
			continue
		}
		total = total.Add(pl.Value)
		out.Lines = append(out.Lines, invevents.OwnershipLineData{JournalLineID: pl.Line.ID, ProductID: pl.Line.ProductID,
			DimensionSet: map[string]string(pl.Line.Dims), Qty: pl.Line.Qty.String(), Unit: pl.Line.EnteredUnit,
			ConsignmentPrice: pl.Line.CostPrice.String(), AmountAcct: pl.Value.String()})
	}
	out.TotalAmount = total.String()
	return out
}

// postedTransaction is the transaction a posted journal line produced.
type postedTransaction struct {
	id    string
	value decimal.Decimal
}

// correctedTransactions maps each reversed line id of a reversal journal to
// the transaction that line posted. A line whose original has no transaction
// (the original was not posted) is an error: a reversal only negates posted
// stock movements.
func correctedTransactions(ctx context.Context, tx pgx.Tx, tenantID string, j domain.Journal) (map[string]postedTransaction, error) {
	var lineIDs []string
	for _, l := range j.Lines {
		if l.ReversesLineID != "" {
			lineIDs = append(lineIDs, l.ReversesLineID)
		}
	}
	out := map[string]postedTransaction{}
	if len(lineIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select o.source_id::text, t.id::text, coalesce(t.cost_amount_posted, t.cost_amount_physical)::text
		from inventory_transaction_origin o join inventory_transaction t on t.tenant_id = o.tenant_id and t.origin_id = o.id
		where o.tenant_id = $1 and o.source_type = $2 and o.source_id = any($3::uuid[])`, tenantID, sourceJournalLine, lineIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var line, id, value string
		if err := rows.Scan(&line, &id, &value); err != nil {
			return nil, err
		}
		out[line] = postedTransaction{id: id, value: decimal.RequireFromString(value)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range lineIDs {
		if _, ok := out[id]; !ok {
			return nil, problem(http.StatusConflict, CodeJournalState, "a reversed line has no posted transaction; only posted journals can be reversed")
		}
	}
	return out, nil
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

// onHandColumns are the projection columns read under lock, in onHandFrom order.
const onHandColumns = `posted_qty::text, posted_value::text, received::text, registered::text, arrived::text, ordered::text,
	deducted::text, picked::text, reserved_physical::text, held_physical::text, reserved_ordered::text, on_order::text`

// lockRow makes sure the projection row exists, then reads it under FOR UPDATE
// into r.before and r.oh (BR-16.4). A row valued in a currency other than
// currency is rejected (INV-CUR-001); an empty currency accepts any and leaves
// the row's own in r.currency. Callers lock rows in rowKey order.
func lockRow(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, currency string, r *row) error {
	if _, _, err := upsertDimensionSet(ctx, tx, tenantID, r.dims); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into on_hand (tenant_id, legal_entity_id, product_id, dimension_set_hash) values ($1, $2, $3, $4)
		on conflict do nothing`, tenantID, legalEntityID, r.product, r.hash); err != nil {
		return scopeProblem(err)
	}
	var b [12]string
	var cur *string
	dest := make([]any, 0, 14)
	for i := range b {
		dest = append(dest, &b[i])
	}
	dest = append(dest, &cur, &r.seq)
	if err := tx.QueryRow(ctx, `select `+onHandColumns+`, currency::text, last_txn_seq
		from on_hand where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and dimension_set_hash = $4 for update`,
		tenantID, legalEntityID, r.product, r.hash).Scan(dest...); err != nil {
		return err
	}
	if cur != nil && currency != "" && *cur != currency {
		return problem(http.StatusUnprocessableEntity, CodeCurrencyMismatch,
			fmt.Sprintf("on-hand for product %s at %s is valued in %s; the posting is in %s", r.product, describeDims(r.dims), *cur, currency))
	}
	r.currency = currency
	if cur != nil {
		r.currency = *cur
	}
	r.before = onHandFrom(b)
	r.oh = r.before
	return nil
}

// checkNegative applies the negative policy to a row after its deltas
// (FR-16.4), turning a violation into QUALITY_HOLD, INV-NEG-001 or INV-NEG-002.
func checkNegative(r *row, neg domain.NegativePolicy) error {
	err := r.oh.CheckNegative(r.before, neg)
	var ne *domain.NegativeError
	if !errors.As(err, &ne) {
		return err
	}
	what := "available physical"
	switch ne.Code {
	case domain.CodeNegativeFinancial:
		what = "financial (ordered)"
	case domain.CodeQualityHold:
		what = "held"
	}
	return problem(http.StatusUnprocessableEntity, ne.Code,
		fmt.Sprintf("issue would make %s stock negative for product %s at %s (shortfall %s)", what, r.product, describeDims(r.dims), ne.Shortfall))
}

// saveRow writes r.oh back to the locked row; txns is the number of
// transactions it absorbed (last_txn_seq moves by that much).
func saveRow(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, r *row, txns int) error {
	var currency *string
	if r.currency != "" {
		currency = &r.currency
	}
	_, err := tx.Exec(ctx, `update on_hand set posted_qty = $5, posted_value = $6, received = $7, registered = $8, arrived = $9, ordered = $10,
		deducted = $11, picked = $12, reserved_physical = $13, held_physical = $14, reserved_ordered = $15, on_order = $16, currency = coalesce($17, currency),
		last_txn_seq = $18, updated_at = now()
		where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and dimension_set_hash = $4`,
		tenantID, legalEntityID, r.product, r.hash, r.oh.PostedQty.String(), r.oh.PostedValue.String(), r.oh.Received.String(), r.oh.Registered.String(),
		r.oh.Arrived.String(), r.oh.Ordered.String(), r.oh.Deducted.String(), r.oh.Picked.String(), r.oh.ReservedPhysical.String(), r.oh.HeldPhysical.String(),
		r.oh.ReservedOrdered.String(), r.oh.OnOrder.String(), currency, r.seq+int64(txns))
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

// enqueueOnHand emits onhand.changed for a row after a change of deltaQty in
// bucket.
func enqueueOnHand(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, r *row, bucket string, deltaQty decimal.Decimal) error {
	return enqueue(ctx, tx, "onhand", r.product, invevents.OnhandChanged, tenantID, invevents.OnhandChangedData{
		LegalEntityID: legalEntityID, ProductID: r.product, DimensionSet: map[string]string(r.dims),
		AvailPhysical: r.oh.AvailPhysical().String(), AvailOrdered: r.oh.AvailOrdered().String(), PhysicalInvent: r.oh.PhysicalInvent().String(),
		DeltaBucket: bucket, DeltaQty: deltaQty.String(),
	})
}

// writeTransaction inserts the origin (sourceID under doc.sourceType) and the
// transaction for one planned line, and enqueues the transaction event. It
// returns the transaction id.
func writeTransaction(ctx context.Context, tx pgx.Tx, tenantID string, doc document, sourceID string, r *row, pl domain.PostedLine) (string, error) {
	originID, txnID := ids.New(), ids.New()
	dir, err := domain.DirectionFor(pl.Status)
	if err != nil {
		return "", err
	}
	var reference *string
	if doc.referenceID != "" {
		reference = &doc.referenceID
	}
	var line *string
	if doc.lineSourceID != "" {
		line = &doc.lineSourceID
	}
	if _, err := tx.Exec(ctx, `insert into inventory_transaction_origin (tenant_id, legal_entity_id, id, source_type, source_id, product_id, reference_number, direction, line_source_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, tenantID, doc.legalEntityID, originID, doc.sourceType, sourceID, r.product, reference, string(dir), line); err != nil {
		return "", err
	}
	status := string(pl.Status)
	var issue, receipt *string
	if dir == domain.IssueDirection {
		issue = &status
	} else {
		receipt = &status
	}
	value := pl.Value.String()
	date := doc.date.Format(dateLayout)
	// The financial date and posted amount are set only when a financial
	// status is reached (FR-16.11); a physical receipt or deduction (vendor
	// consignment) carries neither.
	var financialDate, posted *string
	if domain.IsFinancial(pl.Status) {
		financialDate, posted = &date, &value
	}
	var corrects *string
	if pl.Corrects != "" {
		corrects = &pl.Corrects
	}
	var cwQty any
	if pl.Line.CWQty > 0 {
		cwQty = pl.Line.CWQty
	}
	if _, err := tx.Exec(ctx, `insert into inventory_transaction (tenant_id, legal_entity_id, id, origin_id, product_id, dimension_set_hash, qty, entered_qty,
		entered_unit, cw_qty, issue_status, receipt_status, physical_date, financial_date, currency, cost_amount_physical, cost_amount_posted, corrects_transaction_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		tenantID, doc.legalEntityID, txnID, originID, r.product, r.hash, pl.Line.Qty.String(), pl.Line.EnteredQty.String(), pl.Line.EnteredUnit, cwQty,
		issue, receipt, date, financialDate, doc.currency, value, posted, corrects); err != nil {
		return "", err
	}
	data := invevents.TransactionPostedData{
		TransactionID: txnID, LegalEntityID: doc.legalEntityID, SourceType: doc.sourceType, OriginID: originID, ProductID: r.product,
		DimensionSet: map[string]string(r.dims), Qty: pl.Line.Qty.String(), IssueStatus: issue, ReceiptStatus: receipt,
		PhysicalDate: date, FinancialDate: financialDate, Currency: doc.currency, CostAmountPhysical: value, CostAmountPosted: posted,
		CorrectsTransactionID: corrects,
	}
	if pl.Line.CWQty > 0 {
		n := pl.Line.CWQty
		data.CWQty = &n
	}
	if doc.journalID != "" {
		data.JournalID = &doc.journalID
	}
	data.ReferenceID = reference
	if doc.statusChangeID != "" {
		data.StatusChangeID = &doc.statusChangeID
	}
	return txnID, enqueue(ctx, tx, "transaction", txnID, invevents.TransactionPosted, tenantID, data)
}

func onHandFrom(b [12]string) domain.OnHand {
	d := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }
	return domain.OnHand{PostedQty: d(b[0]), PostedValue: d(b[1]), Received: d(b[2]), Registered: d(b[3]), Arrived: d(b[4]),
		Ordered: d(b[5]), Deducted: d(b[6]), Picked: d(b[7]), ReservedPhysical: d(b[8]), HeldPhysical: d(b[9]), ReservedOrdered: d(b[10]), OnOrder: d(b[11])}
}

func journalPosted(j domain.Journal, entry domain.JournalEntry) invevents.JournalPostedData {
	out := invevents.JournalPostedData{JournalID: j.ID, LegalEntityID: j.LegalEntityID, JournalType: string(j.Type),
		PostingDate: j.PostingDate.Format(dateLayout), Currency: entry.Currency, LineCount: len(j.Lines), GLLines: []invevents.GLLineData{}}
	if j.ReversesJournalID != "" {
		out.ReversesJournalID = &j.ReversesJournalID
	}
	for _, l := range entry.Lines {
		out.GLLines = append(out.GLLines, invevents.GLLineData{Account: l.Account, Debit: l.Debit.String(), Credit: l.Credit.String()})
	}
	return out
}
