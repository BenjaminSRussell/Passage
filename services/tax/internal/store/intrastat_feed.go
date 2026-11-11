package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

func (s *Store) ListIntrastatProducts(ctx context.Context) ([]domain.IntrastatProduct, error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.IntrastatProduct{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = readProducts(ctx, tx, p.TenantID, nil)
		return err
	})
	return out, err
}

func readProducts(ctx context.Context, tx pgx.Tx, tenantID string, only []string) ([]domain.IntrastatProduct, error) {
	rows, err := tx.Query(ctx, `select product_id::text, commodity_code, origin_country, net_mass_per_unit::text, supplementary_per_unit::text from tax_intrastat_product
		where tenant_id = $1 and ($2::text[] is null or product_id::text = any($2)) order by product_id`, tenantID, only)
	if err != nil {
		return nil, fmt.Errorf("read intrastat products: %w", err)
	}
	defer rows.Close()
	out := []domain.IntrastatProduct{}
	for rows.Next() {
		var p domain.IntrastatProduct
		var mass, supp string
		if err := rows.Scan(&p.ProductID, &p.CommodityCode, &p.OriginCountry, &mass, &supp); err != nil {
			return nil, err
		}
		if p.NetMassPerUnit, err = parseDec(mass); err != nil {
			return nil, err
		}
		if p.SupplementaryPerUnit, err = parseDec(supp); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PutIntrastatProduct(ctx context.Context, p domain.IntrastatProduct) error {
	t, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into tax_intrastat_product (tenant_id, product_id, commodity_code, origin_country, net_mass_per_unit, supplementary_per_unit)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, product_id) do update set commodity_code = excluded.commodity_code, origin_country = excluded.origin_country,
				net_mass_per_unit = excluded.net_mass_per_unit, supplementary_per_unit = excluded.supplementary_per_unit, updated_at = now()`,
			t.TenantID, p.ProductID, p.CommodityCode, p.OriginCountry, p.NetMassPerUnit.Decimal, p.SupplementaryPerUnit.Decimal)
		return err
	})
}

func (s *Store) DeleteIntrastatProduct(ctx context.Context, productID string) error {
	t, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from tax_intrastat_product where tenant_id = $1 and product_id = $2`, t.TenantID, productID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// StageMovements records the goods movements fulfillment and receiving report. A source reference is staged once: an identical replay
// is a counted duplicate, a replay with other content is refused.
func (s *Store) StageMovements(ctx context.Context, movements []domain.Movement) (int, error) {
	p, err := scope(ctx)
	if err != nil {
		return 0, err
	}
	staged := 0
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, m := range movements {
			raw, err := json.Marshal(m.Lines)
			if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `insert into tax_intrastat_movement (tenant_id, legal_entity_id, id, flow, source_ref, movement_date, partner_country, transaction_code,
					transport_mode, delivery_terms, port, lines)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) on conflict (tenant_id, legal_entity_id, source_ref) do nothing`,
				p.TenantID, p.LegalEntityID, ids.New(), m.Flow, m.SourceRef, m.MovementDate.Time, m.PartnerCountry, m.TransactionCode, m.TransportMode, m.DeliveryTerms, m.Port, raw)
			if err != nil {
				return fmt.Errorf("stage movement %s: %w", m.SourceRef, err)
			}
			if tag.RowsAffected() == 1 {
				staged++
				continue
			}
			var same bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from tax_intrastat_movement where tenant_id = $1 and legal_entity_id = $2 and source_ref = $3 and flow = $4
				and movement_date = $5 and partner_country = $6 and transaction_code = $7 and transport_mode = $8 and delivery_terms = $9 and port = $10 and lines = $11::jsonb)`,
				p.TenantID, p.LegalEntityID, m.SourceRef, m.Flow, m.MovementDate.Time, m.PartnerCountry, m.TransactionCode, m.TransportMode, m.DeliveryTerms, m.Port, raw).Scan(&same); err != nil {
				return fmt.Errorf("compare movement %s: %w", m.SourceRef, err)
			}
			if !same {
				return &domain.Error{Code: domain.CodeValidation, Field: "movements", Detail: fmt.Sprintf("movement %s was already staged with different content", m.SourceRef)}
			}
		}
		return nil
	})
	return staged, err
}

// ListMovements lists the staged movements of the legal entity, newest first; status filters when not empty.
func (s *Store) ListMovements(ctx context.Context, status string) ([]domain.Movement, error) {
	p, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.Movement{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = readMovements(ctx, tx, p.TenantID, p.LegalEntityID, status, "", 500, false)
		return err
	})
	return out, err
}

func readMovements(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, status, flow string, limit int, lock bool) ([]domain.Movement, error) {
	q := `select id::text, flow, source_ref, movement_date, partner_country, transaction_code, transport_mode, delivery_terms, port, lines, status from tax_intrastat_movement
		where tenant_id = $1 and legal_entity_id = $2 and ($3 = '' or status = $3) and ($4 = '' or flow = $4) order by movement_date desc, source_ref limit $5`
	if lock {
		q += ` for update`
	}
	rows, err := tx.Query(ctx, q, tenantID, legalEntityID, status, flow, limit)
	if err != nil {
		return nil, fmt.Errorf("read movements: %w", err)
	}
	defer rows.Close()
	out := []domain.Movement{}
	for rows.Next() {
		var m domain.Movement
		var date time.Time
		var raw []byte
		if err := rows.Scan(&m.ID, &m.Flow, &m.SourceRef, &date, &m.PartnerCountry, &m.TransactionCode, &m.TransportMode, &m.DeliveryTerms, &m.Port, &raw, &m.Status); err != nil {
			return nil, err
		}
		m.MovementDate = domain.Date{Time: date}
		if err := json.Unmarshal(raw, &m.Lines); err != nil {
			return nil, fmt.Errorf("decode movement lines: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MovementTransfer is what transferring the staged movements into a journal did.
type MovementTransfer struct {
	Added    int                   `json:"added"`
	Skipped  int                   `json:"skipped"`
	Ignored  int                   `json:"ignored"`
	Complete int                   `json:"complete_movements"`
	Unmapped []domain.UnmappedLine `json:"unmapped"`
}

// TransferMovements fills a draft journal from the staged movements of its flow and month (13 deliverable 6): each line of a movement to
// or from another EU member state whose product has an Intrastat mapping becomes a journal line; movements all of whose lines were taken
// are marked transferred, movements outside Intrastat (own country, non-EU partner) are marked ignored, and a movement with a product
// that has no mapping stays staged, its lines named in unmapped, so mapping the product and transferring again completes it.
func (s *Store) TransferMovements(ctx context.Context, journalID string, d domain.MovementDefaults) (MovementTransfer, error) {
	p, err := scope(ctx)
	if err != nil {
		return MovementTransfer{}, err
	}
	out := MovementTransfer{Unmapped: []domain.UnmappedLine{}}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := lockOpenJournal(ctx, tx, p, journalID)
		if err != nil {
			return err
		}
		setting, err := loadEntitySetting(ctx, tx, p)
		if err != nil {
			return err
		}
		movements, err := readMovements(ctx, tx, p.TenantID, p.LegalEntityID, "staged", j.Flow, 2000, true)
		if err != nil {
			return err
		}
		wanted := map[string]bool{}
		for _, m := range movements {
			for _, l := range m.Lines {
				wanted[l.ProductID] = true
			}
		}
		only := make([]string, 0, len(wanted))
		for id := range wanted {
			only = append(only, id)
		}
		list, err := readProducts(ctx, tx, p.TenantID, only)
		if err != nil {
			return err
		}
		products := map[string]domain.IntrastatProduct{}
		for _, pr := range list {
			products[pr.ProductID] = pr
		}
		lines, unmapped, complete, ignored := domain.MovementLines(j.Flow, setting.HomeCountry, j.PeriodFrom.Time, movements, products, d)
		res, err := transferLines(ctx, tx, p.TenantID, journalID, lines)
		if err != nil {
			return err
		}
		done := append(append([]string{}, complete...), ignored...)
		if len(done) > 0 {
			if _, err := tx.Exec(ctx, `update tax_intrastat_movement set status = case when id::text = any($4::text[]) then 'ignored' else 'transferred' end
				where tenant_id = $1 and legal_entity_id = $2 and id::text = any($3::text[])`, p.TenantID, p.LegalEntityID, done, ignored); err != nil {
				return fmt.Errorf("mark movements: %w", err)
			}
		}
		out = MovementTransfer{Added: res.Added, Skipped: res.Skipped, Ignored: len(ignored), Complete: len(complete), Unmapped: append([]domain.UnmappedLine{}, unmapped...)}
		return nil
	})
	return out, err
}
