package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	qevents "github.com/BenjaminSRussell/passage/services/quality/internal/events"
)

func scanCoA(row pgx.Row) (domain.CoA, string, error) {
	var raw []byte
	var orderID string
	if err := row.Scan(&raw, &orderID); err != nil {
		return domain.CoA{}, "", err
	}
	var doc domain.CoA
	return doc, orderID, json.Unmarshal(raw, &doc)
}

// IssueCoA stores the next revision of a certificate of analysis (FR-33.9,
// BR-33.10). The order's row lock serialises revisions of the same chain; build
// receives the latest revision of the (order, shipment) chain, nil for the first,
// and returns the document. The document is immutable once written, and
// coa.issued goes out in the same transaction. A replayed key answers the stored
// revision; the same key on another order is refused.
func (s *Postgres) IssueCoA(ctx context.Context, req domain.CoARequest, key string, build func(prev *domain.CoA) (domain.CoA, error)) (domain.CoA, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.CoA
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			doc, orderID, err := scanCoA(tx.QueryRow(ctx, `select document, quality_order_id::text from quality_coa where `+ownTenant+` and idempotency_key = $1`, key))
			if err == nil {
				if orderID != req.QualityOrderID {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeKeyReuse, "Idempotency-Key was already used for a CoA of another order")
				}
				out = doc
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if _, err := readOrder(ctx, tx, req.QualityOrderID, true); err != nil {
			return err
		}
		var prev *domain.CoA
		latest, _, err := scanCoA(tx.QueryRow(ctx, `select document, quality_order_id::text from quality_coa where `+ownTenant+`
			and quality_order_id = $1 and shipment_key = $2 order by revision desc limit 1`, req.QualityOrderID, req.ShipmentID))
		switch {
		case err == nil:
			prev = &latest
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		doc, err := build(prev)
		if err != nil {
			return err
		}
		doc.ID = ids.New()
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into quality_coa (tenant_id, id, quality_order_id, shipment_key, revision, supersedes, reason,
			document, signed_by, issued_by, issued_at, idempotency_key)
			values ($1, $2, $3, $4, $5, nullif($6, '')::uuid, $7, $8::jsonb, $9, $10, $11, $12)`,
			p.TenantID, doc.ID, doc.QualityOrderID, doc.ShipmentID, doc.Revision, doc.Supersedes, doc.Reason, string(raw),
			doc.SignedBy, doc.IssuedBy, doc.IssuedAt, keyArg(key))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeWriteRace, "a CoA revision of this order is being issued concurrently; retry")
		}
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "coa", doc.ID, qevents.CoAIssued, p.TenantID, qevents.CoAIssuedData{
			CoAID: doc.ID, QualityOrderID: doc.QualityOrderID, ShipmentID: doc.ShipmentID, CustomerID: doc.CustomerID, ItemID: doc.ItemID,
			Batch: doc.Batch, Revision: doc.Revision, Supersedes: doc.Supersedes, AcceptQty: doc.AcceptQty.String(),
			DeviationQty: doc.DeviationQty.String(), SignedBy: doc.SignedBy,
		}); err != nil {
			return err
		}
		out, inserted = doc, true
		return nil
	})
	return out, inserted, err
}

// ListCoAs returns every CoA revision of an order: the batch chain first, then
// each shipment's, oldest revision first.
func (s *Postgres) ListCoAs(ctx context.Context, orderID string) ([]domain.CoA, error) {
	out := []domain.CoA{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select document, quality_order_id::text from quality_coa where `+ownTenant+`
			and quality_order_id = $1 order by shipment_key, revision`, orderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			doc, _, err := scanCoA(rows)
			if err != nil {
				return err
			}
			out = append(out, doc)
		}
		return rows.Err()
	})
	return out, err
}
