package store

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
)

// MergeSource is one stored batch to merge. The register keeps no quantity or
// cost (the wave-16 inventory ledger does), so the caller passes them, with the
// warehouse and catch-weight unit the merge rule compares. Version is the batch
// version the caller read.
type MergeSource struct {
	BatchID     string
	Version     int
	WarehouseID string
	CWUnit      string
	Qty         decimal.Decimal
	UnitCost    decimal.Decimal
}

// MergeInput is the merge command: the batch number of the new batch, its
// sources and the text attributes whose disagreement is allowed.
type MergeInput struct {
	TargetNumber string
	Sources      []MergeSource
	Override     map[string]bool
}

// GenealogyLink is one parent or child of a batch.
type GenealogyLink struct {
	BatchID     string          `json:"batchId"`
	BatchNumber string          `json:"batchNumber"`
	ItemID      string          `json:"itemId"`
	Relation    string          `json:"relation"`
	Qty         decimal.Decimal `json:"qty"`
	UnitCost    decimal.Decimal `json:"unitCost"`
	CreatedBy   string          `json:"createdBy"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// MergeResult is the answer of the merge command: the new batch with its
// merged attributes, the quantity and weighted-average cost per unit, and its
// parents.
type MergeResult struct {
	Batch    Batch           `json:"batch"`
	Qty      decimal.Decimal `json:"qty"`
	UnitCost decimal.Decimal `json:"unitCost"`
	Parents  []GenealogyLink `json:"parents"`
}

// Genealogy is where a batch came from and where it went.
type Genealogy struct {
	BatchID  string          `json:"batchId"`
	Parents  []GenealogyLink `json:"parents"`
	Children []GenealogyLink `json:"children"`
	// Reworks are the rework orders of the batch, as the batch reworked or as
	// the output (FR-24.18, AC-24.18).
	Reworks []ReworkOrder `json:"reworks"`
}

// MaxMergeSources bounds one merge.
const MaxMergeSources = 50

// domainValues turns the current recorded attributes of a batch into the values
// the merge rule works on.
func domainValues(attrs []BatchAttribute) (map[string]domain.Value, error) {
	out := make(map[string]domain.Value, len(attrs))
	for _, a := range attrs {
		v := domain.Value{Num: a.Num, Text: a.Text}
		if a.Date != nil {
			t, err := time.Parse(dateLayout, *a.Date)
			if err != nil {
				return nil, err
			}
			v.Date = &t
		}
		out[a.Name] = v
	}
	return out, nil
}

// MergeBatches merges two or more stored batches of one item into a new batch
// (FR-24.16, AC-24.12). Under one transaction it locks the sources, checks the
// versions the caller read, runs domain.Merge on their recorded attributes and
// dispositions (so a hold cannot be bypassed by restating a batch), registers
// the target with the merged values (checked against the item's definitions)
// and the earliest expiry, writes one genealogy row per source and the
// BatchMerged event. Posting the transfers is the inventory ledger's job; the
// event carries what it needs. Each source's version moves by one, so a second
// merge (or a disposition change) based on the version read before this merge
// is refused as stale; a source stays registered and can be merged again from
// its new version, because the register holds no quantity (a partial merge
// leaves stock behind; the on-hand is the inventory ledger's).
func (s *Postgres) MergeBatches(ctx context.Context, in MergeInput, idem Idem) (MergeResult, bool, error) {
	var out MergeResult
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		// Lock in id order so two merges over the same batches cannot deadlock.
		order := make([]int, len(in.Sources))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, b int) bool { return in.Sources[order[a]].BatchID < in.Sources[order[b]].BatchID })
		loaded := make([]Batch, len(in.Sources))
		for _, i := range order {
			src := in.Sources[i]
			b, err := loadBatch(ctx, tx, src.BatchID, " for update")
			if err != nil {
				return err
			}
			if b.Version != src.Version {
				return staleBatch(b, src.Version)
			}
			loaded[i] = b
		}
		first := loaded[0]
		srcs := make([]domain.Batch, len(in.Sources))
		for i, b := range loaded {
			if b.LegalEntityID != first.LegalEntityID {
				return &domain.Error{Code: domain.CodeMergeRefused,
					Detail: fmt.Sprintf("merge refused: batch %s belongs to another legal entity than %s", b.BatchNumber, first.BatchNumber)}
			}
			vals, err := domainValues(b.Attributes)
			if err != nil {
				return err
			}
			src := in.Sources[i]
			srcs[i] = domain.Batch{ID: b.ID, ItemID: b.ItemID, WarehouseID: src.WarehouseID, CWUnit: src.CWUnit,
				Disposition: b.Disposition, Qty: src.Qty, UnitCost: src.UnitCost, Values: vals}
		}
		res, err := domain.Merge(srcs, in.Override)
		if err != nil {
			return err
		}
		sourceIDs := make([]string, len(loaded))
		for i, b := range loaded {
			sourceIDs[i] = b.ID
		}
		if res.Disposition == domain.DispositionAvailable {
			// The target is born available, so the release gate applies to what it
			// is made of (PMF-0015): a source that should have had an approved
			// batch record cannot be merged into a releasable batch.
			if err := releaseGate(ctx, tx, first.LegalEntityID, first.ItemID, sourceIDs, in.TargetNumber); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update batch_record set version = version + 1, updated_at = now() where id = any($1::uuid[])`, sourceIDs); err != nil {
			return err
		}
		defs, err := loadDefs(ctx, tx, first.ItemID, MaxList)
		if err != nil {
			return err
		}
		dd := make([]domain.AttributeDef, len(defs))
		kinds := map[string]string{}
		for i, d := range defs {
			dd[i] = toDomainDef(d)
			kinds[d.Name] = d.Kind
		}
		checks, err := domain.CheckAttributes(dd, res.Values)
		if err != nil {
			return err
		}
		var expiry *time.Time
		if v, ok := res.Values[domain.ExpiryAttribute]; ok {
			expiry = v.Date
		}
		p := principal(ctx)
		target := Batch{}
		id := ids.New()
		err = tx.QueryRow(ctx, `insert into batch_record (tenant_id, id, legal_entity_id, item_id, batch_number, disposition, expiry_date, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+batchCols,
			p.TenantID, id, first.LegalEntityID, first.ItemID, in.TargetNumber, res.Disposition, expiry, p.UserID).
			Scan(&target.ID, &target.LegalEntityID, &target.ItemID, &target.BatchNumber, &target.BatchOrderID, &target.Disposition, &target.ExpiryDate,
				&target.Version, &target.CreatedBy, &target.CreatedAt, &target.UpdatedAt)
		if err != nil {
			return mapDuplicate(err, CodeBatchDuplicate, fmt.Sprintf("batch %s of item %s already exists", in.TargetNumber, first.ItemID))
		}
		for _, c := range checks {
			v := res.Values[c.Name]
			if _, err := tx.Exec(ctx, `insert into batch_attribute_history (tenant_id, batch_id, legal_entity_id, name, kind, num_value, text_value, date_value, pass, detail, recorded_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				p.TenantID, target.ID, target.LegalEntityID, c.Name, kinds[c.Name], v.Num, v.Text, v.Date, c.Pass, c.Detail, p.UserID); err != nil {
				return err
			}
		}
		evt := pmfevents.BatchMergedData{BatchID: target.ID, LegalEntityID: target.LegalEntityID, ItemID: target.ItemID,
			BatchNumber: target.BatchNumber, Disposition: target.Disposition, WarehouseID: in.Sources[0].WarehouseID, CWUnit: in.Sources[0].CWUnit,
			Qty: res.Qty.String(), UnitCost: res.UnitCost.String(),
			Parents: make([]pmfevents.MergedParent, len(loaded))}
		if target.ExpiryDate != nil {
			evt.ExpiryDate = *target.ExpiryDate
		}
		parents := make([]GenealogyLink, len(loaded))
		for i, b := range loaded {
			src := in.Sources[i]
			if _, err := tx.Exec(ctx, `insert into batch_genealogy (tenant_id, legal_entity_id, child_batch_id, parent_batch_id, relation, qty, unit_cost, created_by)
				values ($1, $2, $3, $4, 'merge', $5, $6, $7)`, p.TenantID, target.LegalEntityID, target.ID, b.ID, src.Qty, src.UnitCost, p.UserID); err != nil {
				return err
			}
			parents[i] = GenealogyLink{BatchID: b.ID, BatchNumber: b.BatchNumber, ItemID: b.ItemID, Relation: "merge",
				Qty: src.Qty, UnitCost: src.UnitCost, CreatedBy: p.UserID, CreatedAt: target.CreatedAt}
			evt.Parents[i] = pmfevents.MergedParent{BatchID: b.ID, BatchNumber: b.BatchNumber, Qty: src.Qty.String(), UnitCost: src.UnitCost.String()}
		}
		if err := enqueue(ctx, tx, "batch", target.ID, pmfevents.BatchMerged, evt); err != nil {
			return err
		}
		fresh, err := loadBatch(ctx, tx, target.ID, "")
		if err != nil {
			return err
		}
		out, created = MergeResult{Batch: fresh, Qty: res.Qty, UnitCost: res.UnitCost, Parents: parents}, true
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, created, err
}

// GetGenealogy returns the batches a batch was merged from (or reworked from)
// and the batches it went into, with the rework orders of the batch. A batch outside the caller's scope is PMF-0108.
func (s *Postgres) GetGenealogy(ctx context.Context, id string) (Genealogy, error) {
	out := Genealogy{BatchID: id}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from batch_record where id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound(CodeBatchNotFound, "batch", id)
		}
		var err error
		if out.Parents, err = genealogyLinks(ctx, tx, `join batch_record b on b.tenant_id = g.tenant_id and b.id = g.parent_batch_id where g.child_batch_id = $1`, id); err != nil {
			return err
		}
		if out.Children, err = genealogyLinks(ctx, tx, `join batch_record b on b.tenant_id = g.tenant_id and b.id = g.child_batch_id where g.parent_batch_id = $1`, id); err != nil {
			return err
		}
		out.Reworks, err = batchReworks(ctx, tx, id)
		return err
	})
	return out, err
}

func genealogyLinks(ctx context.Context, tx pgx.Tx, joinWhere, id string) ([]GenealogyLink, error) {
	rows, err := tx.Query(ctx, `select b.id::text, b.batch_number, b.item_id::text, g.relation, g.qty, g.unit_cost, g.created_by, g.created_at
		from batch_genealogy g `+joinWhere+` order by g.created_at, b.batch_number limit `+fmt.Sprint(maxHistory), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GenealogyLink{}
	for rows.Next() {
		var l GenealogyLink
		if err := rows.Scan(&l.BatchID, &l.BatchNumber, &l.ItemID, &l.Relation, &l.Qty, &l.UnitCost, &l.CreatedBy, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
