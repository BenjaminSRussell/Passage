package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Problem codes raised by the vendor import store.
const (
	CodeImportNotFound    = "PRC-CAT-IMPORT-NOT-FOUND"
	CodeImportRowNotFound = "PRC-CAT-IMPORT-ROW-NOT-FOUND"
	CodeImportDuplicate   = "PRC-CAT-IMPORT-DUPLICATE"
)

// fileHash fingerprints a vendor file by its vendor and rows (raw values under
// canonical column names), so the same file is staged once whatever its format.
func fileHash(vendorID string, rows []domain.ImportRow) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(vendorID)))
	for _, r := range rows {
		raw, _ := json.Marshal(r.Raw)
		h.Write([]byte{'\n'})
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func duplicateFile(existing string) error {
	return httpx.NewProblem(http.StatusConflict, CodeImportDuplicate,
		"this vendor file is already staged or committed for the catalog as import "+existing+"; cancel that import to stage it again")
}

// ImportInput is a validated vendor file ready to stage: one ImportRow per file row.
type ImportInput struct {
	VendorID string
	Format   string
	FileName string
	Rows     []domain.ImportRow
}

// ImportFilter narrows ListCatalogImports; empty fields do not filter.
type ImportFilter struct {
	CatalogID string
	Status    string
}

// ImportDecisions are the buyer's choices on staged rows: AcceptAll accepts every
// ACCEPTABLE row, then Accept and Reject name rows by number.
type ImportDecisions struct {
	AcceptAll bool
	Accept    []int
	Reject    []int
}

// ImportRowPage is a page of staged rows; Next is the last row number, 0 on the last page.
type ImportRowPage struct {
	Items []domain.ImportRow
	Next  int
}

const importSelect = `select i.id, i.catalog_id, i.legal_entity_id, i.vendor_id, i.format, i.file_name, i.status, i.entries_added,
	i.row_version, i.created_at, i.updated_at, n.total, n.acceptable, n.error, n.accepted, n.rejected
	from procurement_catalog_import i
	cross join lateral (select count(*) as total, count(*) filter (where r.status = 'ACCEPTABLE') as acceptable,
		count(*) filter (where r.status = 'ERROR') as error, count(*) filter (where r.status = 'ACCEPTED') as accepted,
		count(*) filter (where r.status = 'REJECTED') as rejected
		from procurement_catalog_import_row r where r.tenant_id = i.tenant_id and r.import_id = i.id) n`

func scanImport(row pgx.Row, c *domain.CatalogImport) error {
	return row.Scan(&c.ID, &c.CatalogID, &c.LegalEntityID, &c.VendorID, &c.Format, &c.FileName, &c.Status, &c.EntriesAdded,
		&c.Version, &c.CreatedAt, &c.UpdatedAt, &c.TotalRows, &c.AcceptableRows, &c.ErrorRows, &c.AcceptedRows, &c.RejectedRows)
}

func loadImport(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.CatalogImport, error) {
	p, _ := tenant.FromContext(ctx)
	var c domain.CatalogImport
	if lock {
		// Lock the header alone: FOR UPDATE cannot sit on a query with aggregates.
		var locked string
		if err := tx.QueryRow(ctx, `select id::text from procurement_catalog_import where tenant_id = $1 and id = $2 for update`,
			p.TenantID, id).Scan(&locked); err != nil {
			return c, notFound(err, CodeImportNotFound, "catalog import not found")
		}
	}
	err := scanImport(tx.QueryRow(ctx, importSelect+` where i.tenant_id = $1 and i.id = $2`, p.TenantID, id), &c)
	return c, notFound(err, CodeImportNotFound, "catalog import not found")
}

func importConflict(cur, want int) error {
	return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
		fmt.Sprintf("catalog import is at version %d, not %d; reload and retry", cur, want))
}

func importProblem(err error, status int) error {
	if de, ok := err.(*domain.Error); ok {
		return httpx.NewProblem(status, de.Code, de.Detail)
	}
	return err
}

// StageVendorImport stores a vendor file against a DRAFT or PUBLISHED catalog: each row
// is ACCEPTABLE or ERROR, a vendor part number already in the catalog or repeated in the
// file is an ERROR, and nothing reaches the catalog until the commit. A replayed
// Idempotency-Key returns the import created by the first request.
func (s *Postgres) StageVendorImport(ctx context.Context, catalogID string, in ImportInput, idem Idem) (domain.CatalogImport, error) {
	p, _ := tenant.FromContext(ctx)
	if len(in.Rows) == 0 || len(in.Rows) > domain.MaxImportRows {
		return domain.CatalogImport{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeImportTooBig,
			fmt.Sprintf("a vendor import holds 1 to %d rows", domain.MaxImportRows))
	}
	id, hash := ids.New(), fileHash(in.VendorID, in.Rows)
	var out domain.CatalogImport
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, id)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadImport(ctx, tx, existing, false)
			return err
		}
		var legalEntityID, status string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text, status from procurement_catalog where tenant_id = $1 and id = $2`,
			p.TenantID, catalogID).Scan(&legalEntityID, &status); err != nil {
			return notFound(err, CodeCatalogNotFound, "procurement catalog not found")
		}
		if status == domain.CatalogRetired {
			return httpx.NewProblem(http.StatusConflict, domain.CodeCatalogState, "a retired catalog takes no import")
		}
		var same string
		err = tx.QueryRow(ctx, `select id::text from procurement_catalog_import where tenant_id = $1 and catalog_id = $2 and vendor_id = $3
			and file_hash = $4 and status <> 'CANCELLED'`, p.TenantID, catalogID, in.VendorID, hash).Scan(&same)
		if err == nil {
			return duplicateFile(same)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := tx.Query(ctx, `select lower(vendor_part_no) from procurement_catalog_entry
			where tenant_id = $1 and catalog_id = $2 and vendor_id = $3 and vendor_part_no <> ''`, p.TenantID, catalogID, in.VendorID)
		if err != nil {
			return err
		}
		held := map[string]bool{}
		for rows.Next() {
			var part string
			if err := rows.Scan(&part); err != nil {
				return err
			}
			held[part] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		staged := append([]domain.ImportRow(nil), in.Rows...)
		domain.FlagDuplicates(staged, held)
		if _, err := tx.Exec(ctx, `insert into procurement_catalog_import (tenant_id, id, catalog_id, legal_entity_id, vendor_id, format,
			file_name, file_hash, created_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.TenantID, id, catalogID, legalEntityID, in.VendorID, in.Format, in.FileName, hash, userOrNil(p.UserID)); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "procurement_catalog_import_file" {
				return duplicateFile("staged at the same time")
			}
			return err
		}
		batch := &pgx.Batch{}
		for _, r := range staged {
			raw, _ := json.Marshal(r.Raw)
			if r.Entry == nil || r.Status == domain.RowError {
				batch.Queue(`insert into procurement_catalog_import_row (tenant_id, import_id, row_no, legal_entity_id, status, reason, raw)
					values ($1, $2, $3, $4, $5, $6, $7::jsonb)`, p.TenantID, id, r.RowNo, legalEntityID, r.Status, r.Reason, string(raw))
				continue
			}
			e := r.Entry
			batch.Queue(`insert into procurement_catalog_import_row (tenant_id, import_id, row_no, legal_entity_id, status, reason, raw,
				vendor_part_no, product_id, name, description, category, unit, unit_price, currency, lead_time_days, image_url)
				values ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
				p.TenantID, id, r.RowNo, legalEntityID, domain.RowAcceptable, r.Reason, string(raw), e.VendorPartNo, nullable(e.ProductID),
				e.Name, e.Description, e.Category, e.Unit, e.UnitPrice, e.Currency, e.LeadTimeDays, e.ImageURL)
		}
		br := tx.SendBatch(ctx, batch)
		for i := 0; i < batch.Len(); i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
		out, err = loadImport(ctx, tx, id, false)
		return err
	})
	return out, err
}

// GetCatalogImport returns one import header with its row counts.
func (s *Postgres) GetCatalogImport(ctx context.Context, id string) (domain.CatalogImport, error) {
	var out domain.CatalogImport
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadImport(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListCatalogImports returns import headers newest first, after the cursor id.
func (s *Postgres) ListCatalogImports(ctx context.Context, f ImportFilter, after string, limit int) (Page[domain.CatalogImport], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []domain.CatalogImport
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, importSelect+` where i.tenant_id = $1 and ($2 = '' or i.catalog_id::text = $2)
			and ($3 = '' or i.status = $3) and ($4::uuid is null or i.id < $4::uuid) order by i.id desc limit $5`,
			p.TenantID, f.CatalogID, f.Status, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.CatalogImport, error) {
			var c domain.CatalogImport
			return c, scanImport(row, &c)
		})
		return err
	})
	if err != nil {
		return Page[domain.CatalogImport]{}, err
	}
	if items == nil {
		items = []domain.CatalogImport{}
	}
	return pageOf(items, limit, func(c domain.CatalogImport) string { return c.ID }), nil
}

// ListImportRows returns the staged rows of an import in file order after row number
// afterRow, optionally of one status.
func (s *Postgres) ListImportRows(ctx context.Context, importID, status string, afterRow, limit int) (ImportRowPage, error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []domain.ImportRow
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := loadImport(ctx, tx, importID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select row_no, status, reason, raw::text, vendor_part_no, coalesce(product_id::text, ''), name, description,
			category, unit, unit_price, currency, lead_time_days, image_url
			from procurement_catalog_import_row where tenant_id = $1 and import_id = $2 and ($3 = '' or status = $3) and row_no > $4
			order by row_no limit $5`, p.TenantID, importID, status, afterRow, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.ImportRow, error) {
			var r domain.ImportRow
			var raw string
			var part, name, desc, cat, unit, cur, img *string
			var product string
			var lead *int
			var price decimal.NullDecimal
			if err := row.Scan(&r.RowNo, &r.Status, &r.Reason, &raw, &part, &product, &name, &desc, &cat, &unit, &price, &cur, &lead, &img); err != nil {
				return r, err
			}
			if err := json.Unmarshal([]byte(raw), &r.Raw); err != nil {
				return r, err
			}
			if name != nil {
				r.Entry = &domain.CatalogEntry{VendorPartNo: deref(part), ProductID: product, Name: *name, Description: deref(desc),
					Category: deref(cat), Unit: deref(unit), Currency: deref(cur), ImageURL: deref(img)}
				if lead != nil {
					r.Entry.LeadTimeDays = *lead
				}
				r.Entry.UnitPrice = price.Decimal
			}
			return r, nil
		})
		return err
	})
	if err != nil {
		return ImportRowPage{}, err
	}
	if items == nil {
		items = []domain.ImportRow{}
	}
	page := ImportRowPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.Next = items[limit-1].RowNo
	}
	return page, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// DecideImportRows records the buyer's accept and reject choices on a STAGED import
// under If-Match. An ERROR row cannot be accepted (PRC-CAT-005) and an unknown row
// number is a 404. A replayed Idempotency-Key returns the import as it is now.
func (s *Postgres) DecideImportRows(ctx context.Context, id string, ifMatch int, d ImportDecisions, idem Idem) (domain.CatalogImport, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.CatalogImport
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadImport(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil || replay {
			out = cur
			return err
		}
		if cur.Version != ifMatch {
			return importConflict(cur.Version, ifMatch)
		}
		if cur.Status != domain.ImportStaged {
			return httpx.NewProblem(http.StatusConflict, domain.CodeImportState, "rows can only be decided while the import is staged")
		}
		if d.AcceptAll {
			if _, err := tx.Exec(ctx, `update procurement_catalog_import_row set status = 'ACCEPTED'
				where tenant_id = $1 and import_id = $2 and status = 'ACCEPTABLE'`, p.TenantID, id); err != nil {
				return err
			}
		}
		for _, step := range []struct {
			decision string
			rows     []int
		}{{"ACCEPT", d.Accept}, {"REJECT", d.Reject}} {
			if len(step.rows) == 0 {
				continue
			}
			rows, err := tx.Query(ctx, `select row_no, status from procurement_catalog_import_row
				where tenant_id = $1 and import_id = $2 and row_no = any($3)`, p.TenantID, id, step.rows)
			if err != nil {
				return err
			}
			status := map[int]string{}
			for rows.Next() {
				var n int
				var st string
				if err := rows.Scan(&n, &st); err != nil {
					return err
				}
				status[n] = st
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			var change []int
			var to string
			for _, n := range step.rows {
				st, ok := status[n]
				if !ok {
					return httpx.NewProblem(http.StatusNotFound, CodeImportRowNotFound, "import row "+strconv.Itoa(n)+" not found")
				}
				next, err := domain.Decide(n, st, step.decision)
				if err != nil {
					return importProblem(err, http.StatusUnprocessableEntity)
				}
				if next != st {
					change, to = append(change, n), next
				}
			}
			if len(change) > 0 {
				if _, err := tx.Exec(ctx, `update procurement_catalog_import_row set status = $4
					where tenant_id = $1 and import_id = $2 and row_no = any($3)`, p.TenantID, id, change, to); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `update procurement_catalog_import set row_version = row_version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		out, err = loadImport(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ActOnCatalogImport applies commit or cancel to a STAGED import under If-Match. The
// commit adds the ACCEPTED rows to the catalog as entries (rows not accepted stay out),
// raises the catalog version and writes catalogimport.committed.v1 in the same
// transaction. The catalog must be a DRAFT or PUBLISHED one, with room under the entry cap.
func (s *Postgres) ActOnCatalogImport(ctx context.Context, id string, ifMatch int, action string, idem Idem) (domain.CatalogImport, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.CatalogImport
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadImport(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil || replay {
			out = cur
			return err
		}
		if cur.Version != ifMatch {
			return importConflict(cur.Version, ifMatch)
		}
		next, err := domain.NextImportStatus(cur.Status, action)
		if err != nil {
			return importProblem(err, http.StatusConflict)
		}
		added := 0
		if action == domain.ImportCommit {
			cat, maxNo, err := loadCatalogHeader(ctx, tx, cur.CatalogID, true)
			if err != nil {
				return err
			}
			if cat.Status == domain.CatalogRetired {
				return httpx.NewProblem(http.StatusConflict, domain.CodeCatalogState, "a retired catalog takes no import")
			}
			if cur.AcceptedRows == 0 {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeImportNothing, "accept at least one row before the commit")
			}
			if cat.EntryCount+cur.AcceptedRows > domain.MaxCatalogEntries {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCatalogTooManyEntry,
					fmt.Sprintf("a catalog holds at most %d entries", domain.MaxCatalogEntries))
			}
			// A part number may have reached the catalog after staging (another import
			// committed first); the catalog lock above serialises commits.
			var dupRow int
			var dupPart string
			err = tx.QueryRow(ctx, `select r.row_no, r.vendor_part_no from procurement_catalog_import_row r
				where r.tenant_id = $1 and r.import_id = $2 and r.status = 'ACCEPTED' and r.vendor_part_no <> ''
				and exists (select 1 from procurement_catalog_entry e where e.tenant_id = r.tenant_id and e.catalog_id = $3
					and e.vendor_id = $4 and lower(e.vendor_part_no) = lower(r.vendor_part_no))
				order by r.row_no limit 1`, p.TenantID, id, cat.ID, cur.VendorID).Scan(&dupRow, &dupPart)
			if err == nil {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeImportRow, fmt.Sprintf(
					"row %d: vendorPartNo %s was added to the catalog after this file was staged; reject the row and commit again", dupRow, dupPart))
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// One statement adds every accepted row in file order, numbered after the
			// catalog's last entry, with UUIDv7 ids made here so the picker's newest-first
			// order holds.
			entryIDs := make([]string, cur.AcceptedRows)
			for i := range entryIDs {
				entryIDs[i] = ids.New()
			}
			tag, err := tx.Exec(ctx, `insert into procurement_catalog_entry (tenant_id, id, catalog_id, legal_entity_id, entry_no, vendor_id,
				vendor_part_no, product_id, name, description, category, unit, unit_price, currency, lead_time_days, preferred, image_url)
				select r.tenant_id, ($5::uuid[])[r.n], $3, $4, $6 + r.n, $7, coalesce(r.vendor_part_no, ''), r.product_id, r.name,
					coalesce(r.description, ''), r.category, r.unit, r.unit_price, r.currency, coalesce(r.lead_time_days, 0), false,
					coalesce(r.image_url, '')
				from (select x.*, row_number() over (order by x.row_no) as n from procurement_catalog_import_row x
					where x.tenant_id = $1 and x.import_id = $2 and x.status = 'ACCEPTED') r`,
				p.TenantID, id, cat.ID, cat.LegalEntityID, entryIDs, maxNo, cur.VendorID)
			if err != nil {
				return err
			}
			if int(tag.RowsAffected()) != cur.AcceptedRows {
				return fmt.Errorf("catalog import %s: added %d entries, want %d", id, tag.RowsAffected(), cur.AcceptedRows)
			}
			added = cur.AcceptedRows
			if _, err := tx.Exec(ctx, `update procurement_catalog set row_version = row_version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, cat.ID); err != nil {
				return err
			}
			if err := enqueue(ctx, tx, "catalogimport", id, purchasingevents.CatalogImportCommitted, purchasingevents.CatalogImportData{
				ImportID: id, CatalogID: cat.ID, CatalogNumber: cat.Number, LegalEntityID: cat.LegalEntityID, VendorID: cur.VendorID,
				TotalRows: cur.TotalRows, AcceptedRows: added, RejectedRows: cur.RejectedRows, ErrorRows: cur.ErrorRows,
				SkippedRows: cur.AcceptableRows, CatalogVersion: cat.Version + 1}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update procurement_catalog_import set status = $3, entries_added = $4, row_version = row_version + 1,
			updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, next, added); err != nil {
			return err
		}
		out, err = loadImport(ctx, tx, id, false)
		return err
	})
	return out, err
}
