package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

var (
	ErrNotFound         = errors.New("tax: not found")
	ErrNoEntitySettings = errors.New("tax: no entity settings for this legal entity")
)

func tenantOf(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return tenant.Principal{}, errors.New("tax: no principal in context")
	}
	return p, nil
}

func inUse(field, format string, a ...any) *domain.Error {
	return &domain.Error{Code: domain.CodeConfigInUse, Field: field, Detail: fmt.Sprintf(format, a...)}
}

func isForeignKeyViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23503"
}

// ---- jurisdictions

func listJurisdictions(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.Jurisdiction, error) {
	rows, err := tx.Query(ctx, `select code, name, kind, country, coalesce(parent_code, '') from tax_jurisdiction where tenant_id = $1 order by code`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list jurisdictions: %w", err)
	}
	defer rows.Close()
	out := []domain.Jurisdiction{}
	for rows.Next() {
		var j domain.Jurisdiction
		if err := rows.Scan(&j.Code, &j.Name, &j.Kind, &j.Country, &j.ParentCode); err != nil {
			return nil, fmt.Errorf("scan jurisdiction: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) ListJurisdictions(ctx context.Context) ([]domain.Jurisdiction, error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Jurisdiction
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = listJurisdictions(ctx, tx, p.TenantID)
		return err
	})
	return out, err
}

// PutJurisdiction creates or replaces a jurisdiction; the tree it joins is checked in the same transaction.
func (s *Store) PutJurisdiction(ctx context.Context, j domain.Jurisdiction) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1 || ':tax_jurisdiction'))`, p.TenantID); err != nil {
			return fmt.Errorf("lock jurisdiction tree: %w", err)
		}
		all, err := listJurisdictions(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		byCode := map[string]domain.Jurisdiction{}
		for _, e := range all {
			byCode[e.Code] = e
		}
		if err := domain.ValidateJurisdiction(j, byCode); err != nil {
			return err
		}
		// Moving a node below one of its own descendants would loop: walk down from j.
		for at := j.ParentCode; at != ""; at = byCode[at].ParentCode {
			if at == j.Code {
				return &domain.Error{Code: domain.CodeValidation, Field: "parent_code", Detail: "the parent is below this jurisdiction"}
			}
		}
		if _, err := tx.Exec(ctx, `insert into tax_jurisdiction (tenant_id, code, name, kind, country, parent_code)
			values ($1, $2, $3, $4, $5, nullif($6, ''))
			on conflict (tenant_id, code) do update set name = excluded.name, kind = excluded.kind, country = excluded.country,
				parent_code = excluded.parent_code, updated_at = now()`,
			p.TenantID, j.Code, j.Name, j.Kind, j.Country, j.ParentCode); err != nil {
			return fmt.Errorf("save jurisdiction: %w", err)
		}
		return nil
	})
}

func (s *Store) DeleteJurisdiction(ctx context.Context, code string) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from tax_jurisdiction where tenant_id = $1 and code = $2`, p.TenantID, code)
		if isForeignKeyViolation(err) {
			return inUse("code", "Jurisdiction %s still has child jurisdictions or tax codes. Move or remove them first.", code)
		}
		if err != nil {
			return fmt.Errorf("delete jurisdiction: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ---- tax codes

func listCodes(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.ConfigCode, error) {
	rows, err := tx.Query(ctx, `select code, name, jurisdiction_code, mechanism, calc_basis, rounding, compound_sequence, recoverable_pct::text, rates
		from tax_code where tenant_id = $1 order by code`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list tax codes: %w", err)
	}
	defer rows.Close()
	out := []domain.ConfigCode{}
	for rows.Next() {
		var c domain.ConfigCode
		var recoverable *string
		var rates []byte
		if err := rows.Scan(&c.Code.Code, &c.Name, &c.Jurisdiction, &c.Mechanism, &c.CalcBasis, &c.Rounding, &c.CompoundSequence, &recoverable, &rates); err != nil {
			return nil, fmt.Errorf("scan tax code: %w", err)
		}
		if recoverable != nil {
			d, err := parseDec(*recoverable)
			if err != nil {
				return nil, err
			}
			c.RecoverablePct = &d
		}
		if err := json.Unmarshal(rates, &c.Rates); err != nil {
			return nil, fmt.Errorf("decode rates of %s: %w", c.Code.Code, err)
		}
		if c.Rates == nil {
			c.Rates = []domain.Rate{}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func parseDec(s string) (domain.Dec, error) {
	var d domain.Dec
	if err := json.Unmarshal([]byte(fmt.Sprintf("%q", s)), &d); err != nil {
		return d, fmt.Errorf("decode decimal %q: %w", s, err)
	}
	return d, nil
}

func (s *Store) ListCodes(ctx context.Context) ([]domain.ConfigCode, error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.ConfigCode
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = listCodes(ctx, tx, p.TenantID)
		return err
	})
	return out, err
}

func (s *Store) PutCode(ctx context.Context, c domain.ConfigCode) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if c.Mechanism == "" {
		c.Mechanism = domain.MechStandard
	}
	if c.CalcBasis == "" {
		c.CalcBasis = domain.BasisLine
	}
	if c.Rounding == "" {
		c.Rounding = domain.RoundPerLine
	}
	if c.Rates == nil {
		c.Rates = []domain.Rate{}
	}
	rates, err := json.Marshal(c.Rates)
	if err != nil {
		return err
	}
	var recoverable any
	if c.RecoverablePct != nil {
		recoverable = c.RecoverablePct.Decimal
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into tax_code (tenant_id, code, name, jurisdiction_code, mechanism, calc_basis, rounding, compound_sequence, recoverable_pct, rates)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			on conflict (tenant_id, code) do update set name = excluded.name, jurisdiction_code = excluded.jurisdiction_code,
				mechanism = excluded.mechanism, calc_basis = excluded.calc_basis, rounding = excluded.rounding,
				compound_sequence = excluded.compound_sequence, recoverable_pct = excluded.recoverable_pct, rates = excluded.rates, updated_at = now()`,
			p.TenantID, c.Code.Code, c.Name, c.Jurisdiction, c.Mechanism, c.CalcBasis, c.Rounding, c.CompoundSequence, recoverable, rates)
		if isForeignKeyViolation(err) {
			return &domain.Error{Code: domain.CodeValidation, Field: "jurisdiction", Detail: fmt.Sprintf("jurisdiction %s does not exist", c.Jurisdiction)}
		}
		if err != nil {
			return fmt.Errorf("save tax code: %w", err)
		}
		return nil
	})
}

func (s *Store) DeleteCode(ctx context.Context, code string) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var groups, boxes, certs, policies int
		if err := tx.QueryRow(ctx, `select (select count(*) from tax_group where tenant_id = $1 and $2 = any(tax_codes)),
			(select count(*) from tax_vat_box_code where tenant_id = $1 and code = $2),
			(select count(*) from tax_exemption_certificate where tenant_id = $1 and $2 = any(codes)),
			(select count(*) from tax_recovery_policy where tenant_id = $1 and code = $2)`, p.TenantID, code).Scan(&groups, &boxes, &certs, &policies); err != nil {
			return fmt.Errorf("check tax code use: %w", err)
		}
		if groups > 0 || boxes > 0 || certs > 0 || policies > 0 {
			return inUse("code", "Tax code %s is used by %d tax groups, %d VAT return boxes, %d exemption certificates and %d recovery policies. Remove it from them first.", code, groups, boxes, certs, policies)
		}
		tag, err := tx.Exec(ctx, `delete from tax_code where tenant_id = $1 and code = $2`, p.TenantID, code)
		if err != nil {
			return fmt.Errorf("delete tax code: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ---- tax groups

func listGroups(ctx context.Context, tx pgx.Tx, tenantID, kind string) ([]domain.Group, error) {
	rows, err := tx.Query(ctx, `select kind, code, name, tax_codes from tax_group where tenant_id = $1 and ($2 = '' or kind = $2) order by kind, code`, tenantID, kind)
	if err != nil {
		return nil, fmt.Errorf("list tax groups: %w", err)
	}
	defer rows.Close()
	out := []domain.Group{}
	for rows.Next() {
		var g domain.Group
		if err := rows.Scan(&g.Kind, &g.Code, &g.Name, &g.TaxCodes); err != nil {
			return nil, fmt.Errorf("scan tax group: %w", err)
		}
		if g.TaxCodes == nil {
			g.TaxCodes = []string{}
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListGroups returns the groups of one kind, or of both kinds when kind is empty.
func (s *Store) ListGroups(ctx context.Context, kind string) ([]domain.Group, error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Group
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = listGroups(ctx, tx, p.TenantID, kind)
		return err
	})
	return out, err
}

func (s *Store) PutGroup(ctx context.Context, g domain.Group) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if g.TaxCodes == nil {
		g.TaxCodes = []string{}
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
		if err := domain.ValidateGroup(g, known); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into tax_group (tenant_id, kind, code, name, tax_codes) values ($1, $2, $3, $4, $5)
			on conflict (tenant_id, kind, code) do update set name = excluded.name, tax_codes = excluded.tax_codes, updated_at = now()`,
			p.TenantID, g.Kind, g.Code, g.Name, g.TaxCodes); err != nil {
			return fmt.Errorf("save tax group: %w", err)
		}
		return nil
	})
}

func (s *Store) DeleteGroup(ctx context.Context, kind, code string) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from tax_group where tenant_id = $1 and kind = $2 and code = $3`, p.TenantID, kind, code)
		if err != nil {
			return fmt.Errorf("delete tax group: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// LoadConfig assembles the engine's configuration from what is stored, so a calculation can use
// it instead of an inline config.
func (s *Store) LoadConfig(ctx context.Context) (domain.Config, error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return domain.Config{}, err
	}
	var cfg domain.Config
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		codes, err := listCodes(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		groups, err := listGroups(ctx, tx, p.TenantID, "")
		if err != nil {
			return err
		}
		cfg = domain.AssembleConfig(codes, groups, "")
		certs, err := listCertificates(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		for _, c := range certs {
			cfg.Exemptions = append(cfg.Exemptions, c.Exemption)
		}
		if cfg.Recovery, err = listRecovery(ctx, tx, p.TenantID); err != nil {
			return err
		}
		return nil
	})
	return cfg, err
}

// ---- exemption certificates

func listCertificates(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.Certificate, error) {
	rows, err := tx.Query(ctx, `select number, party_ref, note, codes, valid_from, valid_to from tax_exemption_certificate where tenant_id = $1 order by number`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list exemption certificates: %w", err)
	}
	defer rows.Close()
	out := []domain.Certificate{}
	for rows.Next() {
		var c domain.Certificate
		var from time.Time
		var to *time.Time
		if err := rows.Scan(&c.Number, &c.PartyRef, &c.Note, &c.Codes, &from, &to); err != nil {
			return nil, fmt.Errorf("scan exemption certificate: %w", err)
		}
		c.ValidFrom = domain.Date{Time: from}
		if to != nil {
			c.ValidTo = domain.Date{Time: *to}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ListCertificates(ctx context.Context) ([]domain.Certificate, error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Certificate
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = listCertificates(ctx, tx, p.TenantID)
		return err
	})
	return out, err
}

func (s *Store) PutCertificate(ctx context.Context, c domain.Certificate) error {
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
		for _, code := range codes {
			known[code.Code.Code] = true
		}
		if err := domain.ValidateCertificate(c, known); err != nil {
			return err
		}
		var to any
		if !c.ValidTo.IsZero() {
			to = c.ValidTo.Time
		}
		if _, err := tx.Exec(ctx, `insert into tax_exemption_certificate (tenant_id, number, party_ref, note, codes, valid_from, valid_to)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, number) do update set party_ref = excluded.party_ref, note = excluded.note, codes = excluded.codes,
				valid_from = excluded.valid_from, valid_to = excluded.valid_to, updated_at = now()`,
			p.TenantID, c.Number, c.PartyRef, c.Note, c.Codes, c.ValidFrom.Time, to); err != nil {
			return fmt.Errorf("save exemption certificate: %w", err)
		}
		return nil
	})
}

func (s *Store) DeleteCertificate(ctx context.Context, number string) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from tax_exemption_certificate where tenant_id = $1 and number = $2`, p.TenantID, number)
		if err != nil {
			return fmt.Errorf("delete exemption certificate: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
