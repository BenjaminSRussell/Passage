package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

// ---- master data

func listMaster(ctx context.Context, tx pgx.Tx, tenantID, kind string) ([]domain.MasterEntry, error) {
	rows, err := tx.Query(ctx, `select kind, code, description, attrs from tax_intrastat_master
		where tenant_id = $1 and ($2 = '' or kind = $2) order by kind, code`, tenantID, kind)
	if err != nil {
		return nil, fmt.Errorf("list intrastat master data: %w", err)
	}
	defer rows.Close()
	out := []domain.MasterEntry{}
	for rows.Next() {
		var e domain.MasterEntry
		var attrs []byte
		if err := rows.Scan(&e.Kind, &e.Code, &e.Description, &attrs); err != nil {
			return nil, fmt.Errorf("scan intrastat master entry: %w", err)
		}
		if err := json.Unmarshal(attrs, &e.Attrs); err != nil {
			return nil, fmt.Errorf("decode intrastat attrs: %w", err)
		}
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListMaster(ctx context.Context, kind string) ([]domain.MasterEntry, error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.MasterEntry
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = listMaster(ctx, tx, p.TenantID, kind)
		return err
	})
	return out, err
}

func (s *Store) PutMaster(ctx context.Context, e domain.MasterEntry) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	attrs, err := json.Marshal(e.Attrs)
	if err != nil {
		return err
	}
	if e.Attrs == nil {
		attrs = []byte("{}")
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into tax_intrastat_master (tenant_id, kind, code, description, attrs) values ($1, $2, $3, $4, $5)
			on conflict (tenant_id, kind, code) do update set description = excluded.description, attrs = excluded.attrs, updated_at = now()`,
			p.TenantID, e.Kind, e.Code, e.Description, attrs); err != nil {
			return fmt.Errorf("save intrastat master entry: %w", err)
		}
		return nil
	})
}

// ImportMaster adds the entries a tenant does not have yet; an existing entry is kept as the tenant edited it unless overwrite is
// true. It returns how many entries were added or replaced and how many were left alone.
func (s *Store) ImportMaster(ctx context.Context, entries []domain.MasterEntry, overwrite bool) (res ImportResult, err error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	conflict := "do nothing"
	if overwrite {
		conflict = "do update set description = excluded.description, attrs = excluded.attrs, updated_at = now()"
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res = ImportResult{}
		for _, e := range entries {
			attrs, err := json.Marshal(e.Attrs)
			if err != nil {
				return err
			}
			if e.Attrs == nil {
				attrs = []byte("{}")
			}
			tag, err := tx.Exec(ctx, `insert into tax_intrastat_master (tenant_id, kind, code, description, attrs) values ($1, $2, $3, $4, $5)
				on conflict (tenant_id, kind, code) `+conflict, p.TenantID, e.Kind, e.Code, e.Description, attrs)
			if err != nil {
				return fmt.Errorf("import intrastat master entry: %w", err)
			}
			if tag.RowsAffected() == 1 {
				res.Added++
			} else {
				res.Skipped++
			}
		}
		return nil
	})
	return res, err
}

// ImportResult counts a master data import.
type ImportResult struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
}

func (s *Store) DeleteMaster(ctx context.Context, kind, code string) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from tax_intrastat_master where tenant_id = $1 and kind = $2 and code = $3`, p.TenantID, kind, code)
		if err != nil {
			return fmt.Errorf("delete intrastat master entry: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ---- journals

type Journal struct {
	ID           string                 `json:"id"`
	Flow         string                 `json:"flow"`
	PeriodFrom   domain.Date            `json:"period_from"`
	Status       string                 `json:"status"`
	CorrectionOf string                 `json:"correction_of"`
	CreatedAt    time.Time              `json:"created_at"`
	ExportedAt   *time.Time             `json:"exported_at"`
	LineCount    int                    `json:"line_count"`
	ErrorCount   int                    `json:"error_count"`
	Lines        []domain.IntrastatLine `json:"lines,omitempty"`
}

type TransferResult struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
}

const journalColumns = `id::text, flow, period_from, status, coalesce(correction_of::text, ''), created_at, exported_at,
	(select count(*) from tax_intrastat_line l where l.tenant_id = j.tenant_id and l.journal_id = j.id),
	(select count(*) from tax_intrastat_line l where l.tenant_id = j.tenant_id and l.journal_id = j.id and l.errors <> '[]'::jsonb)`

func scanJournal(row pgx.Row) (Journal, error) {
	var j Journal
	err := row.Scan(&j.ID, &j.Flow, &j.PeriodFrom.Time, &j.Status, &j.CorrectionOf, &j.CreatedAt, &j.ExportedAt, &j.LineCount, &j.ErrorCount)
	return j, err
}

func lockJournal(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, forUpdate bool) (Journal, error) {
	if !ids.Valid(id) {
		return Journal{}, ErrNotFound
	}
	q := `select ` + journalColumns + ` from tax_intrastat_journal j where tenant_id = $1 and legal_entity_id = $2 and id = $3`
	if forUpdate {
		q += ` for update`
	}
	j, err := scanJournal(tx.QueryRow(ctx, q, p.TenantID, p.LegalEntityID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, fmt.Errorf("read intrastat journal: %w", err)
	}
	return j, nil
}

func lockOpenJournal(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (Journal, error) {
	j, err := lockJournal(ctx, tx, p, id, true)
	if err != nil {
		return j, err
	}
	if j.Status == domain.JournalExported {
		return j, &domain.Error{Code: domain.CodeValidation, Field: "status", Detail: "This journal has been exported and can no longer change. Start a correction instead."}
	}
	return j, nil
}

func readLines(ctx context.Context, tx pgx.Tx, tenantID, journalID string) ([]domain.IntrastatLine, error) {
	rows, err := tx.Query(ctx, `select line_no, source_ref, partner_country, commodity_code, transaction_code, transport_mode, delivery_terms,
			port, origin_country, net_mass, supplementary_qty, invoice_value, statistical_value, compressed_from, errors
		from tax_intrastat_line where tenant_id = $1 and journal_id = $2 order by line_no`, tenantID, journalID)
	if err != nil {
		return nil, fmt.Errorf("read intrastat lines: %w", err)
	}
	defer rows.Close()
	out := []domain.IntrastatLine{}
	for rows.Next() {
		var l domain.IntrastatLine
		var errs []byte
		if err := rows.Scan(&l.LineNo, &l.SourceRef, &l.PartnerCountry, &l.CommodityCode, &l.TransactionCode, &l.TransportMode, &l.DeliveryTerms,
			&l.Port, &l.OriginCountry, &l.NetMass.Decimal, &l.SupplementaryQty.Decimal, &l.InvoiceValue.Decimal, &l.StatisticalValue.Decimal,
			&l.CompressedFrom, &errs); err != nil {
			return nil, fmt.Errorf("scan intrastat line: %w", err)
		}
		if err := json.Unmarshal(errs, &l.Errors); err != nil {
			return nil, fmt.Errorf("decode line errors: %w", err)
		}
		if l.Errors == nil {
			l.Errors = []string{}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func insertLine(ctx context.Context, tx pgx.Tx, tenantID, journalID string, l domain.IntrastatLine) error {
	errs, err := json.Marshal(append([]string{}, l.Errors...))
	if err != nil {
		return err
	}
	if l.CompressedFrom < 1 {
		l.CompressedFrom = 1
	}
	if _, err := tx.Exec(ctx, `insert into tax_intrastat_line (tenant_id, journal_id, line_no, source_ref, partner_country, commodity_code, transaction_code,
			transport_mode, delivery_terms, port, origin_country, net_mass, supplementary_qty, invoice_value, statistical_value, compressed_from, errors)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		tenantID, journalID, l.LineNo, l.SourceRef, l.PartnerCountry, l.CommodityCode, l.TransactionCode, l.TransportMode, l.DeliveryTerms,
		l.Port, l.OriginCountry, l.NetMass.Decimal, l.SupplementaryQty.Decimal, l.InvoiceValue.Decimal, l.StatisticalValue.Decimal, l.CompressedFrom, errs); err != nil {
		return fmt.Errorf("save intrastat line: %w", err)
	}
	return nil
}

func replaceLines(ctx context.Context, tx pgx.Tx, tenantID, journalID string, lines []domain.IntrastatLine) error {
	if _, err := tx.Exec(ctx, `delete from tax_intrastat_line where tenant_id = $1 and journal_id = $2`, tenantID, journalID); err != nil {
		return fmt.Errorf("clear intrastat lines: %w", err)
	}
	for i, l := range lines {
		l.LineNo = i + 1
		if err := insertLine(ctx, tx, tenantID, journalID, l); err != nil {
			return err
		}
	}
	return nil
}

func setStatus(ctx context.Context, tx pgx.Tx, tenantID, id, status string) error {
	if _, err := tx.Exec(ctx, `update tax_intrastat_journal set status = $3 where tenant_id = $1 and id = $2`, tenantID, id, status); err != nil {
		return fmt.Errorf("set journal status: %w", err)
	}
	return nil
}

func (s *Store) CreateJournal(ctx context.Context, flow string, periodFrom domain.Date) (Journal, error) {
	p, err := scope(ctx)
	if err != nil {
		return Journal{}, err
	}
	var out Journal
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into tax_intrastat_journal (tenant_id, id, legal_entity_id, flow, period_from) values ($1, $2, $3, $4, $5)`,
			p.TenantID, id, p.LegalEntityID, flow, periodFrom.Time); err != nil {
			return fmt.Errorf("create intrastat journal: %w", err)
		}
		out, err = lockJournal(ctx, tx, p, id, false)
		return err
	})
	return out, err
}

func (s *Store) ListJournals(ctx context.Context) ([]Journal, error) {
	p, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	out := []Journal{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+journalColumns+` from tax_intrastat_journal j where tenant_id = $1 and legal_entity_id = $2
			order by period_from desc, created_at desc limit 200`, p.TenantID, p.LegalEntityID)
		if err != nil {
			return fmt.Errorf("list intrastat journals: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJournal(rows)
			if err != nil {
				return fmt.Errorf("scan intrastat journal: %w", err)
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) GetJournal(ctx context.Context, id string) (Journal, error) {
	p, err := scope(ctx)
	if err != nil {
		return Journal{}, err
	}
	var out Journal
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = lockJournal(ctx, tx, p, id, false)
		if err != nil {
			return err
		}
		out.Lines, err = readLines(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// Transfer adds source lines (shipments or receipts) to an open journal. A source_ref that was
// transferred before, even if its line has since been compressed, is skipped.
func (s *Store) Transfer(ctx context.Context, id string, lines []domain.IntrastatLine) (TransferResult, error) {
	p, err := scope(ctx)
	if err != nil {
		return TransferResult{}, err
	}
	var res TransferResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockOpenJournal(ctx, tx, p, id); err != nil {
			return err
		}
		var next int
		if err := tx.QueryRow(ctx, `select coalesce(max(line_no), 0) from tax_intrastat_line where tenant_id = $1 and journal_id = $2`, p.TenantID, id).Scan(&next); err != nil {
			return fmt.Errorf("read last line number: %w", err)
		}
		for _, l := range lines {
			tag, err := tx.Exec(ctx, `insert into tax_intrastat_transferred (tenant_id, journal_id, source_ref) values ($1, $2, $3) on conflict do nothing`, p.TenantID, id, l.SourceRef)
			if err != nil {
				return fmt.Errorf("record transferred source: %w", err)
			}
			if tag.RowsAffected() == 0 {
				res.Skipped++
				continue
			}
			next++
			l.LineNo, l.CompressedFrom, l.Errors = next, 1, nil
			if err := insertLine(ctx, tx, p.TenantID, id, l); err != nil {
				return err
			}
			res.Added++
		}
		if res.Added > 0 {
			return setStatus(ctx, tx, p.TenantID, id, domain.JournalDraft)
		}
		return nil
	})
	return res, err
}

// Validate checks every line against the master data and the entity's country, stores the
// problems on the lines and marks the journal validated when there are none.
func (s *Store) Validate(ctx context.Context, id string) (Journal, error) {
	p, err := scope(ctx)
	if err != nil {
		return Journal{}, err
	}
	var out Journal
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := lockOpenJournal(ctx, tx, p, id)
		if err != nil {
			return err
		}
		setting, err := loadEntitySetting(ctx, tx, p)
		if err != nil {
			return err
		}
		entries, err := listMaster(ctx, tx, p.TenantID, "")
		if err != nil {
			return err
		}
		lines, err := readLines(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		bad := domain.ValidateIntrastatLines(j.Flow, setting.HomeCountry, lines, domain.NewMaster(entries))
		if err := replaceLines(ctx, tx, p.TenantID, id, lines); err != nil {
			return err
		}
		status := domain.JournalDraft
		if bad == 0 && len(lines) > 0 {
			status = domain.JournalValidated
		}
		if err := setStatus(ctx, tx, p.TenantID, id, status); err != nil {
			return err
		}
		out, err = lockJournal(ctx, tx, p, id, false)
		if err != nil {
			return err
		}
		out.Lines = lines
		return nil
	})
	return out, err
}

// Compress merges lines that share every classifying field. The journal goes back to draft.
func (s *Store) Compress(ctx context.Context, id string) (Journal, error) {
	p, err := scope(ctx)
	if err != nil {
		return Journal{}, err
	}
	var out Journal
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockOpenJournal(ctx, tx, p, id); err != nil {
			return err
		}
		lines, err := readLines(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		lines = domain.CompressIntrastatLines(lines)
		if err := replaceLines(ctx, tx, p.TenantID, id, lines); err != nil {
			return err
		}
		if err := setStatus(ctx, tx, p.TenantID, id, domain.JournalDraft); err != nil {
			return err
		}
		out, err = lockJournal(ctx, tx, p, id, false)
		if err != nil {
			return err
		}
		out.Lines = lines
		return nil
	})
	return out, err
}

// PutLine replaces the content of one line of an open journal.
func (s *Store) PutLine(ctx context.Context, id string, lineNo int, l domain.IntrastatLine) error {
	p, err := scope(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockOpenJournal(ctx, tx, p, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update tax_intrastat_line set partner_country = $4, commodity_code = $5, transaction_code = $6, transport_mode = $7,
				delivery_terms = $8, port = $9, origin_country = $10, net_mass = $11, supplementary_qty = $12, invoice_value = $13, statistical_value = $14, errors = '[]'
			where tenant_id = $1 and journal_id = $2 and line_no = $3`,
			p.TenantID, id, lineNo, l.PartnerCountry, l.CommodityCode, l.TransactionCode, l.TransportMode, l.DeliveryTerms, l.Port, l.OriginCountry,
			l.NetMass.Decimal, l.SupplementaryQty.Decimal, l.InvoiceValue.Decimal, l.StatisticalValue.Decimal)
		if err != nil {
			return fmt.Errorf("update intrastat line: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return setStatus(ctx, tx, p.TenantID, id, domain.JournalDraft)
	})
}

// DeleteLine removes one line of an open journal and renumbers the rest.
func (s *Store) DeleteLine(ctx context.Context, id string, lineNo int) error {
	p, err := scope(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockOpenJournal(ctx, tx, p, id); err != nil {
			return err
		}
		lines, err := readLines(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		kept := lines[:0]
		found := false
		for _, l := range lines {
			if l.LineNo == lineNo {
				found = true
				continue
			}
			kept = append(kept, l)
		}
		if !found {
			return ErrNotFound
		}
		if err := replaceLines(ctx, tx, p.TenantID, id, kept); err != nil {
			return err
		}
		return setStatus(ctx, tx, p.TenantID, id, domain.JournalDraft)
	})
}

// Correct opens a correction of an exported journal: a new draft with the same flow and period
// and a copy of its lines, to be edited, validated and exported again.
func (s *Store) Correct(ctx context.Context, id string) (Journal, error) {
	p, err := scope(ctx)
	if err != nil {
		return Journal{}, err
	}
	var out Journal
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		orig, err := lockJournal(ctx, tx, p, id, true)
		if err != nil {
			return err
		}
		if orig.Status != domain.JournalExported {
			return &domain.Error{Code: domain.CodeValidation, Field: "status", Detail: "Only an exported journal needs a correction. Edit this one directly."}
		}
		var open int
		if err := tx.QueryRow(ctx, `select count(*) from tax_intrastat_journal where tenant_id = $1 and correction_of = $2 and status <> 'exported'`, p.TenantID, id).Scan(&open); err != nil {
			return fmt.Errorf("check open corrections: %w", err)
		}
		if open > 0 {
			return &domain.Error{Code: domain.CodeValidation, Field: "status", Detail: "This journal already has a correction in progress."}
		}
		lines, err := readLines(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		newID := ids.New()
		if _, err := tx.Exec(ctx, `insert into tax_intrastat_journal (tenant_id, id, legal_entity_id, flow, period_from, correction_of) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, newID, p.LegalEntityID, orig.Flow, orig.PeriodFrom.Time, id); err != nil {
			return fmt.Errorf("create correction journal: %w", err)
		}
		for i := range lines {
			lines[i].SourceRef, lines[i].Errors = "", nil
		}
		if err := replaceLines(ctx, tx, p.TenantID, newID, lines); err != nil {
			return err
		}
		out, err = lockJournal(ctx, tx, p, newID, false)
		return err
	})
	return out, err
}

type ExportData struct {
	Journal     Journal
	HomeCountry string
	Master      domain.Master
}

// Export hands out a validated journal for filing and locks it. An exported journal can be
// downloaded again; a draft cannot be exported.
func (s *Store) Export(ctx context.Context, id string) (ExportData, error) {
	p, err := scope(ctx)
	if err != nil {
		return ExportData{}, err
	}
	var out ExportData
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := lockJournal(ctx, tx, p, id, true)
		if err != nil {
			return err
		}
		if j.Status == domain.JournalDraft {
			return &domain.Error{Code: domain.CodeValidation, Field: "status", Detail: "Validate the journal before exporting it; it has unchecked or invalid lines."}
		}
		setting, err := loadEntitySetting(ctx, tx, p)
		if err != nil {
			return err
		}
		entries, err := listMaster(ctx, tx, p.TenantID, "")
		if err != nil {
			return err
		}
		if j.Status == domain.JournalValidated {
			if _, err := tx.Exec(ctx, `update tax_intrastat_journal set status = 'exported', exported_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
				return fmt.Errorf("mark journal exported: %w", err)
			}
		}
		if j, err = lockJournal(ctx, tx, p, id, false); err != nil {
			return err
		}
		if j.Lines, err = readLines(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		out = ExportData{Journal: j, HomeCountry: setting.HomeCountry, Master: domain.NewMaster(entries)}
		return nil
	})
	return out, err
}
