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

// RecordWithholding records the withholding of payments for the request's legal entity. An id is recorded once: an identical replay is
// a counted duplicate, a replay with other content is refused.
func (s *Store) RecordWithholding(ctx context.Context, entries []domain.WithholdingEntry) (int, error) {
	p, err := scope(ctx)
	if err != nil {
		return 0, err
	}
	inserted := 0
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, e := range entries {
			tag, err := tx.Exec(ctx, `insert into tax_withholding_entry (tenant_id, legal_entity_id, id, party_type, party_ref, code, payment_date, currency,
					gross_amount, taxable_base, withheld_amount)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) on conflict (tenant_id, legal_entity_id, id) do nothing`,
				p.TenantID, p.LegalEntityID, e.ID, e.PartyType, e.PartyRef, e.Code, e.PaymentDate.Time, e.Currency, e.Gross.Decimal, e.TaxableBase.Decimal, e.Withheld.Decimal)
			if err != nil {
				return fmt.Errorf("record withholding %s: %w", e.ID, err)
			}
			if tag.RowsAffected() == 1 {
				inserted++
				continue
			}
			var same bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from tax_withholding_entry where tenant_id = $1 and legal_entity_id = $2 and id = $3
				and party_type = $4 and party_ref = $5 and code = $6 and payment_date = $7 and currency = $8 and gross_amount = $9 and taxable_base = $10 and withheld_amount = $11)`,
				p.TenantID, p.LegalEntityID, e.ID, e.PartyType, e.PartyRef, e.Code, e.PaymentDate.Time, e.Currency, e.Gross.Decimal, e.TaxableBase.Decimal, e.Withheld.Decimal).Scan(&same); err != nil {
				return fmt.Errorf("compare withholding %s: %w", e.ID, err)
			}
			if !same {
				return &domain.Error{Code: domain.CodeValidation, Field: "entries", Detail: fmt.Sprintf("entry %s was already recorded with different content", e.ID)}
			}
		}
		return nil
	})
	return inserted, err
}

// RemittanceRun is one remittance run with the certificates it issued.
type RemittanceRun struct {
	ID           string                          `json:"id"`
	Number       string                          `json:"number"`
	PeriodFrom   domain.Date                     `json:"period_from"`
	PeriodTo     domain.Date                     `json:"period_to"`
	Lines        []domain.RemittanceLine         `json:"lines"`
	Certificates []domain.WithholdingCertificate `json:"certificates,omitempty"`
	CreatedAt    time.Time                       `json:"created_at"`
}

func nextNumber(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, kind string, year int) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `insert into tax_number_counter (tenant_id, legal_entity_id, kind, year, last_value) values ($1, $2, $3, $4, 1)
		on conflict (tenant_id, legal_entity_id, kind, year) do update set last_value = tax_number_counter.last_value + 1 returning last_value`,
		tenantID, legalEntityID, kind, year).Scan(&n)
	return n, err
}

// RunRemittance remits the withholding of the period: it locks the entries not yet remitted, totals them per code and currency, issues a
// certificate per payee, code and currency, and marks the entries, all in one transaction so a failure leaves no number used and no
// entry marked. Nothing to remit is TAX-0014.
func (s *Store) RunRemittance(ctx context.Context, period domain.Period) (RemittanceRun, error) {
	p, err := scope(ctx)
	if err != nil {
		return RemittanceRun{}, err
	}
	var out RemittanceRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, party_type, party_ref, code, payment_date, currency, gross_amount::text, taxable_base::text, withheld_amount::text
			from tax_withholding_entry where tenant_id = $1 and legal_entity_id = $2 and remittance_run_id is null and payment_date between $3 and $4
			order by payment_date, id for update`, p.TenantID, p.LegalEntityID, period.From.Time, period.To.Time)
		if err != nil {
			return fmt.Errorf("lock withholding entries: %w", err)
		}
		var entries []domain.WithholdingEntry
		for rows.Next() {
			var e domain.WithholdingEntry
			var date time.Time
			var gross, base, withheld string
			if err := rows.Scan(&e.ID, &e.PartyType, &e.PartyRef, &e.Code, &date, &e.Currency, &gross, &base, &withheld); err != nil {
				rows.Close()
				return fmt.Errorf("scan withholding entry: %w", err)
			}
			e.PaymentDate = domain.Date{Time: date}
			for dst, src := range map[*domain.Dec]string{&e.Gross: gross, &e.TaxableBase: base, &e.Withheld: withheld} {
				if *dst, err = parseDec(src); err != nil {
					rows.Close()
					return err
				}
			}
			entries = append(entries, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(entries) == 0 {
			return &domain.Error{Code: domain.CodeNothingToRemit, Detail: "no withholding is waiting to be remitted in the period"}
		}
		lines, groups := domain.PlanRemittance(entries)
		year := period.To.Year()
		seq, err := nextNumber(ctx, tx, p.TenantID, p.LegalEntityID, "wht-run", year)
		if err != nil {
			return fmt.Errorf("number the remittance run: %w", err)
		}
		out = RemittanceRun{ID: ids.New(), Number: domain.RemittanceNumber(year, seq), PeriodFrom: period.From, PeriodTo: period.To, Lines: lines}
		raw, err := json.Marshal(lines)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `insert into tax_withholding_run (tenant_id, legal_entity_id, id, number, period_from, period_to, lines) values ($1, $2, $3, $4, $5, $6, $7)
			returning created_at`, p.TenantID, p.LegalEntityID, out.ID, out.Number, period.From.Time, period.To.Time, raw).Scan(&out.CreatedAt); err != nil {
			return fmt.Errorf("insert remittance run: %w", err)
		}
		for _, g := range groups {
			n, err := nextNumber(ctx, tx, p.TenantID, p.LegalEntityID, "wht-certificate", year)
			if err != nil {
				return fmt.Errorf("number the certificate: %w", err)
			}
			cert := domain.WithholdingCertificate{ID: ids.New(), Number: domain.CertificateNumber(year, n), RunID: out.ID, PartyType: g.PartyType, PartyRef: g.PartyRef,
				Code: g.Code, Currency: g.Currency, PeriodFrom: period.From, PeriodTo: period.To, Gross: g.Gross, TaxableBase: g.TaxableBase, Withheld: g.Withheld, Entries: len(g.EntryIDs)}
			if _, err := tx.Exec(ctx, `insert into tax_withholding_certificate (tenant_id, legal_entity_id, id, number, run_id, party_type, party_ref, code, currency, period_from, period_to,
					gross_amount, taxable_base, withheld_amount, entry_count) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
				p.TenantID, p.LegalEntityID, cert.ID, cert.Number, out.ID, cert.PartyType, cert.PartyRef, cert.Code, cert.Currency, period.From.Time, period.To.Time,
				cert.Gross, cert.TaxableBase, cert.Withheld, cert.Entries); err != nil {
				return fmt.Errorf("insert certificate: %w", err)
			}
			if _, err := tx.Exec(ctx, `update tax_withholding_entry set remittance_run_id = $4, certificate_id = $5 where tenant_id = $1 and legal_entity_id = $2 and id = any($3::uuid[])`,
				p.TenantID, p.LegalEntityID, g.EntryIDs, out.ID, cert.ID); err != nil {
				return fmt.Errorf("mark entries remitted: %w", err)
			}
			out.Certificates = append(out.Certificates, cert)
		}
		return nil
	})
	return out, err
}

func scanCertificate(row pgx.Row) (domain.WithholdingCertificate, error) {
	var c domain.WithholdingCertificate
	var from, to time.Time
	if err := row.Scan(&c.ID, &c.Number, &c.RunID, &c.PartyType, &c.PartyRef, &c.Code, &c.Currency, &from, &to, &c.Gross, &c.TaxableBase, &c.Withheld, &c.Entries); err != nil {
		return c, err
	}
	c.PeriodFrom, c.PeriodTo = domain.Date{Time: from}, domain.Date{Time: to}
	return c, nil
}

const certificateColumns = `id::text, number, run_id::text, party_type, party_ref, code, currency::text, period_from, period_to, gross_amount, taxable_base, withheld_amount, entry_count`

// ListRemittanceRuns lists the runs of the request's legal entity, newest first.
func (s *Store) ListRemittanceRuns(ctx context.Context) ([]RemittanceRun, error) {
	p, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	out := []RemittanceRun{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, number, period_from, period_to, lines, created_at from tax_withholding_run
			where tenant_id = $1 and legal_entity_id = $2 order by created_at desc, number desc limit 200`, p.TenantID, p.LegalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func scanRun(row pgx.Row) (RemittanceRun, error) {
	var r RemittanceRun
	var from, to time.Time
	var raw []byte
	if err := row.Scan(&r.ID, &r.Number, &from, &to, &raw, &r.CreatedAt); err != nil {
		return r, err
	}
	r.PeriodFrom, r.PeriodTo = domain.Date{Time: from}, domain.Date{Time: to}
	if err := json.Unmarshal(raw, &r.Lines); err != nil {
		return r, fmt.Errorf("decode remittance lines: %w", err)
	}
	return r, nil
}

// GetRemittanceRun reads one run with its certificates.
func (s *Store) GetRemittanceRun(ctx context.Context, id string) (RemittanceRun, error) {
	p, err := scope(ctx)
	if err != nil {
		return RemittanceRun{}, err
	}
	var out RemittanceRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = scanRun(tx.QueryRow(ctx, `select id::text, number, period_from, period_to, lines, created_at from tax_withholding_run
			where tenant_id = $1 and legal_entity_id = $2 and id = $3`, p.TenantID, p.LegalEntityID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		certs, err := listCertificatesWhere(ctx, tx, p.TenantID, p.LegalEntityID, `run_id = $3`, id)
		out.Certificates = certs
		return err
	})
	return out, err
}

func listCertificatesWhere(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, where string, arg any) ([]domain.WithholdingCertificate, error) {
	rows, err := tx.Query(ctx, `select `+certificateColumns+` from tax_withholding_certificate where tenant_id = $1 and legal_entity_id = $2 and `+where+` order by number`, tenantID, legalEntityID, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.WithholdingCertificate{}
	for rows.Next() {
		c, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListWithholdingCertificates lists the certificates issued to a payee (every payee when partyRef is empty), oldest first.
func (s *Store) ListWithholdingCertificates(ctx context.Context, partyRef string) ([]domain.WithholdingCertificate, error) {
	p, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.WithholdingCertificate
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = listCertificatesWhere(ctx, tx, p.TenantID, p.LegalEntityID, `($3 = '' or party_ref = $3)`, partyRef)
		return err
	})
	return out, err
}
