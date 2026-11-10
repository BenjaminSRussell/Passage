package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

type EntitySetting struct {
	HomeCountry string `json:"home_country"`
	VATID       string `json:"vat_id"`
}

func (s *Store) GetEntitySetting(ctx context.Context) (EntitySetting, error) {
	p, err := scope(ctx)
	if err != nil {
		return EntitySetting{}, err
	}
	var out EntitySetting
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = loadEntitySetting(ctx, tx, p)
		return err
	})
	return out, err
}

func loadEntitySetting(ctx context.Context, tx pgx.Tx, p tenant.Principal) (EntitySetting, error) {
	var out EntitySetting
	err := tx.QueryRow(ctx, `select home_country, vat_id from tax_entity_setting where tenant_id = $1 and legal_entity_id = $2`,
		p.TenantID, p.LegalEntityID).Scan(&out.HomeCountry, &out.VATID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNoEntitySettings
	}
	if err != nil {
		return out, fmt.Errorf("read entity settings: %w", err)
	}
	return out, nil
}

func (s *Store) PutEntitySetting(ctx context.Context, e EntitySetting) error {
	p, err := scope(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into tax_entity_setting (tenant_id, legal_entity_id, home_country, vat_id) values ($1, $2, $3, $4)
			on conflict (tenant_id, legal_entity_id) do update set home_country = excluded.home_country, vat_id = excluded.vat_id, updated_at = now()`,
			p.TenantID, p.LegalEntityID, e.HomeCountry, e.VATID); err != nil {
			return fmt.Errorf("save entity settings: %w", err)
		}
		return nil
	})
}

func (s *Store) ListBoxes(ctx context.Context) ([]domain.Box, error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Box
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = listBoxes(ctx, tx, p.TenantID)
		return err
	})
	return out, err
}

func listBoxes(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.Box, error) {
	rows, err := tx.Query(ctx, `select box, label, kind, sort from tax_vat_box where tenant_id = $1 order by sort, box`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list vat boxes: %w", err)
	}
	out := []domain.Box{}
	index := map[string]int{}
	for rows.Next() {
		var b domain.Box
		if err := rows.Scan(&b.Box, &b.Label, &b.Kind, &b.Sort); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan vat box: %w", err)
		}
		b.Codes = []domain.BoxCode{}
		index[b.Box] = len(out)
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	codes, err := tx.Query(ctx, `select box, code, direction from tax_vat_box_code where tenant_id = $1 order by box, code, direction`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list vat box codes: %w", err)
	}
	defer codes.Close()
	for codes.Next() {
		var box string
		var c domain.BoxCode
		if err := codes.Scan(&box, &c.Code, &c.Direction); err != nil {
			return nil, fmt.Errorf("scan vat box code: %w", err)
		}
		out[index[box]].Codes = append(out[index[box]].Codes, c)
	}
	return out, codes.Err()
}

// PutBoxes replaces the whole box set. Every mapped tax code must exist.
func (s *Store) PutBoxes(ctx context.Context, boxes []domain.Box) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		codes, err := listCodes(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, c := range codes {
			known[c.Code.Code] = true
		}
		for i, b := range boxes {
			for _, c := range b.Codes {
				if !known[c.Code] {
					return &domain.Error{Code: domain.CodeValidation, Field: fmt.Sprintf("boxes[%d].codes", i), Detail: fmt.Sprintf("tax code %q does not exist", c.Code)}
				}
			}
		}
		if _, err := tx.Exec(ctx, `delete from tax_vat_box where tenant_id = $1`, p.TenantID); err != nil {
			return fmt.Errorf("clear vat boxes: %w", err)
		}
		for _, b := range boxes {
			if _, err := tx.Exec(ctx, `insert into tax_vat_box (tenant_id, box, label, kind, sort) values ($1, $2, $3, $4, $5)`,
				p.TenantID, b.Box, b.Label, b.Kind, b.Sort); err != nil {
				return fmt.Errorf("save vat box: %w", err)
			}
			for _, c := range b.Codes {
				if _, err := tx.Exec(ctx, `insert into tax_vat_box_code (tenant_id, box, code, direction) values ($1, $2, $3, $4)`,
					p.TenantID, b.Box, c.Code, c.Direction); err != nil {
					return fmt.Errorf("save vat box code: %w", err)
				}
			}
		}
		return nil
	})
}

func vatTxns(ctx context.Context, tx pgx.Tx, p tenant.Principal, period domain.Period) ([]domain.VATTxn, error) {
	rows, err := tx.Query(ctx, `select code, direction, tax_date, amount, base_amount, coalesce(counterparty_country, ''),
			coalesce(counterparty_vat_id, ''), coalesce(supply_kind, '') from tax_transaction
		where tenant_id = $1 and legal_entity_id = $2 and tax_date between $3 and $4 order by tax_date, id`,
		p.TenantID, p.LegalEntityID, period.From.Time, period.To.Time)
	if err != nil {
		return nil, fmt.Errorf("read tax transactions: %w", err)
	}
	defer rows.Close()
	var out []domain.VATTxn
	for rows.Next() {
		var t domain.VATTxn
		var base *decimal.Decimal
		if err := rows.Scan(&t.Code, &t.Direction, &t.TaxDate, &t.Amount, &base, &t.CounterpartyCtry, &t.CounterpartyVAT, &t.SupplyKind); err != nil {
			return nil, fmt.Errorf("scan tax transaction: %w", err)
		}
		t.Base = base
		out = append(out, t)
	}
	return out, rows.Err()
}

// VATReturn computes the return boxes of the request's legal entity over the period.
func (s *Store) VATReturn(ctx context.Context, period domain.Period) (domain.VATReturn, error) {
	p, err := scope(ctx)
	if err != nil {
		return domain.VATReturn{}, err
	}
	var out domain.VATReturn
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		boxes, err := listBoxes(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		txns, err := vatTxns(ctx, tx, p, period)
		if err != nil {
			return err
		}
		out = domain.ComputeVATReturn(period, boxes, txns)
		return nil
	})
	return out, err
}

// ESL computes the EU sales list of the request's legal entity over the period.
func (s *Store) ESL(ctx context.Context, period domain.Period) (domain.ESL, error) {
	p, err := scope(ctx)
	if err != nil {
		return domain.ESL{}, err
	}
	var out domain.ESL
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		setting, err := loadEntitySetting(ctx, tx, p)
		if err != nil {
			return err
		}
		txns, err := vatTxns(ctx, tx, p, period)
		if err != nil {
			return err
		}
		out = domain.ComputeESL(period, setting.HomeCountry, setting.VATID, txns)
		return nil
	})
	return out, err
}
