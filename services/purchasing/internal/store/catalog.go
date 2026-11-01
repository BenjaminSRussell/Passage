package store

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Problem codes raised by the catalog store.
const (
	CodeCatalogNotFound      = "PRC-CAT-NOT-FOUND"
	CodeCatalogEntryNotFound = "PRC-CAT-ENTRY-NOT-FOUND"
	CodeCatalogTooManyEntry  = "PRC-CAT-007"
)

// CatalogSummary is a list row: the header and the entry count.
type CatalogSummary struct {
	ID            string      `json:"id"`
	Number        string      `json:"number"`
	LegalEntityID string      `json:"legalEntityId"`
	Name          string      `json:"name"`
	Status        string      `json:"status"`
	ValidFrom     domain.Day  `json:"validFrom"`
	ValidTo       *domain.Day `json:"validTo,omitempty"`
	EntryCount    int         `json:"entryCount"`
	Version       int         `json:"version"`
	CreatedAt     time.Time   `json:"createdAt"`
}

// CatalogFilter narrows ListCatalogs; empty fields do not filter.
type CatalogFilter struct {
	Status        string
	LegalEntityID string
}

// CatalogHit is one picker result: an entry with the catalog it belongs to.
type CatalogHit struct {
	domain.CatalogEntry
	CatalogID     string `json:"catalogId"`
	CatalogNumber string `json:"catalogNumber"`
	CatalogName   string `json:"catalogName"`
}

// CatalogSearch is the requisition picker query (FR-17.18): entries of catalogs
// published on Date to OrgUnitID (or to every unit) in LegalEntityID.
type CatalogSearch struct {
	LegalEntityID string
	OrgUnitID     string
	Date          domain.Day
	Text          string
	Category      string
	VendorID      string
}

const catalogColumns = `c.id, c.number, c.legal_entity_id, c.name, c.description, c.status, c.valid_from, c.valid_to,
	c.org_unit_ids::text[], c.row_version, c.created_at, c.updated_at`

func scanCatalog(row pgx.Row, c *domain.Catalog) error {
	var from time.Time
	var to *time.Time
	if err := row.Scan(&c.ID, &c.Number, &c.LegalEntityID, &c.Name, &c.Description, &c.Status, &from, &to, &c.OrgUnitIDs,
		&c.Version, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return err
	}
	c.ValidFrom, c.ValidTo = day(from), dayOrNil(to)
	if c.OrgUnitIDs == nil {
		c.OrgUnitIDs = []string{}
	}
	return nil
}

const catalogEntryColumns = `e.id, e.entry_no, e.vendor_id, e.vendor_part_no, coalesce(e.product_id::text, ''), e.name, e.description,
	e.category, e.unit, e.unit_price, e.currency, e.lead_time_days, e.preferred, e.image_url`

func catalogEntryDest(e *domain.CatalogEntry) []any {
	return []any{&e.ID, &e.EntryNo, &e.VendorID, &e.VendorPartNo, &e.ProductID, &e.Name, &e.Description, &e.Category, &e.Unit,
		&e.UnitPrice, &e.Currency, &e.LeadTimeDays, &e.Preferred, &e.ImageURL}
}

func loadCatalog(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.Catalog, error) {
	p, _ := tenant.FromContext(ctx)
	q := `select ` + catalogColumns + ` from procurement_catalog c where c.tenant_id = $1 and c.id = $2`
	if lock {
		q += ` for update`
	}
	var c domain.Catalog
	if err := scanCatalog(tx.QueryRow(ctx, q, p.TenantID, id), &c); err != nil {
		return domain.Catalog{}, notFound(err, CodeCatalogNotFound, "procurement catalog not found")
	}
	rows, err := tx.Query(ctx, `select `+catalogEntryColumns+` from procurement_catalog_entry e
		where e.tenant_id = $1 and e.catalog_id = $2 order by e.entry_no`, p.TenantID, id)
	if err != nil {
		return domain.Catalog{}, err
	}
	c.Entries, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.CatalogEntry, error) {
		var e domain.CatalogEntry
		return e, row.Scan(catalogEntryDest(&e)...)
	})
	if c.Entries == nil {
		c.Entries = []domain.CatalogEntry{}
	}
	return c, err
}

func insertCatalogEntry(ctx context.Context, tx pgx.Tx, catalogID, legalEntityID string, no int, e domain.CatalogEntry) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `insert into procurement_catalog_entry (tenant_id, id, catalog_id, legal_entity_id, entry_no, vendor_id,
		vendor_part_no, product_id, name, description, category, unit, unit_price, currency, lead_time_days, preferred, image_url)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		p.TenantID, ids.New(), catalogID, legalEntityID, no, e.VendorID, e.VendorPartNo, nullable(e.ProductID), e.Name, e.Description,
		e.Category, e.Unit, e.UnitPrice, e.Currency, e.LeadTimeDays, e.Preferred, e.ImageURL)
	return err
}

// CreateCatalog stores a DRAFT catalog with a fresh CAT- number and its entries.
// A replayed Idempotency-Key returns the catalog created by the first request.
func (s *Postgres) CreateCatalog(ctx context.Context, in domain.Catalog, idem Idem) (domain.Catalog, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	if in.OrgUnitIDs == nil {
		in.OrgUnitIDs = []string{}
	}
	var out domain.Catalog
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadCatalog(ctx, tx, existing, false)
			return err
		}
		var seq int64
		if err := tx.QueryRow(ctx, `select nextval('procurement_catalog_number_seq')`).Scan(&seq); err != nil {
			return err
		}
		in.Number = fmt.Sprintf("CAT-%08d", seq)
		if _, err := tx.Exec(ctx, `insert into procurement_catalog (tenant_id, id, number, legal_entity_id, name, description, valid_from,
			valid_to, org_unit_ids, created_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9::uuid[], $10)`,
			p.TenantID, in.ID, in.Number, in.LegalEntityID, in.Name, in.Description, in.ValidFrom.Time, dayPtr(in.ValidTo),
			in.OrgUnitIDs, userOrNil(p.UserID)); err != nil {
			return err
		}
		for i, e := range in.Entries {
			if err := insertCatalogEntry(ctx, tx, in.ID, in.LegalEntityID, i+1, e); err != nil {
				return err
			}
		}
		out, err = loadCatalog(ctx, tx, in.ID, false)
		return err
	})
	return out, err
}

// GetCatalog returns one catalog with its entries.
func (s *Postgres) GetCatalog(ctx context.Context, id string) (domain.Catalog, error) {
	var out domain.Catalog
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadCatalog(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListCatalogs returns catalog headers newest first, after the cursor id.
func (s *Postgres) ListCatalogs(ctx context.Context, f CatalogFilter, after string, limit int) (Page[CatalogSummary], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []CatalogSummary
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select c.id, c.number, c.legal_entity_id, c.name, c.status, c.valid_from, c.valid_to,
			(select count(*) from procurement_catalog_entry e where e.tenant_id = c.tenant_id and e.catalog_id = c.id),
			c.row_version, c.created_at
			from procurement_catalog c
			where c.tenant_id = $1 and ($2 = '' or c.status = $2) and ($3 = '' or c.legal_entity_id::text = $3)
			and ($4::uuid is null or c.id < $4::uuid)
			order by c.id desc limit $5`, p.TenantID, f.Status, f.LegalEntityID, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (CatalogSummary, error) {
			var c CatalogSummary
			var from time.Time
			var to *time.Time
			err := row.Scan(&c.ID, &c.Number, &c.LegalEntityID, &c.Name, &c.Status, &from, &to, &c.EntryCount, &c.Version, &c.CreatedAt)
			c.ValidFrom, c.ValidTo = day(from), dayOrNil(to)
			return c, err
		})
		return err
	})
	if err != nil {
		return Page[CatalogSummary]{}, err
	}
	if items == nil {
		items = []CatalogSummary{}
	}
	return pageOf(items, limit, func(c CatalogSummary) string { return c.ID }), nil
}

func catalogConflict(cur, want int) error {
	return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
		fmt.Sprintf("catalog is at version %d, not %d; reload and retry", cur, want))
}

// editDraftCatalog locks the catalog, checks the version and that it is still a
// DRAFT, runs change and bumps the version. A replayed key returns the catalog as is.
func (s *Postgres) editDraftCatalog(ctx context.Context, id string, ifMatch int, idem Idem, change func(tx pgx.Tx, c domain.Catalog) error) (domain.Catalog, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Catalog
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadCatalog(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil || replay {
			out = cur
			return err
		}
		if cur.Version != ifMatch {
			return catalogConflict(cur.Version, ifMatch)
		}
		if cur.Status != domain.CatalogDraft {
			return httpx.NewProblem(http.StatusConflict, domain.CodeCatalogState, "entries can only change while the catalog is a draft")
		}
		if err := change(tx, cur); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update procurement_catalog set row_version = row_version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		out, err = loadCatalog(ctx, tx, id, false)
		return err
	})
	return out, err
}

// AddCatalogEntries appends entries to a DRAFT catalog under If-Match.
func (s *Postgres) AddCatalogEntries(ctx context.Context, id string, ifMatch int, entries []domain.CatalogEntry, idem Idem) (domain.Catalog, error) {
	return s.editDraftCatalog(ctx, id, ifMatch, idem, func(tx pgx.Tx, c domain.Catalog) error {
		if len(c.Entries)+len(entries) > domain.MaxCatalogEntries {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCatalogTooManyEntry,
				fmt.Sprintf("a catalog holds at most %d entries", domain.MaxCatalogEntries))
		}
		next := 0
		for _, e := range c.Entries {
			next = max(next, e.EntryNo)
		}
		for i, e := range entries {
			if err := insertCatalogEntry(ctx, tx, id, c.LegalEntityID, next+i+1, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveCatalogEntry deletes one entry of a DRAFT catalog under If-Match.
func (s *Postgres) RemoveCatalogEntry(ctx context.Context, id, entryID string, ifMatch int) (domain.Catalog, error) {
	p, _ := tenant.FromContext(ctx)
	return s.editDraftCatalog(ctx, id, ifMatch, Idem{}, func(tx pgx.Tx, _ domain.Catalog) error {
		tag, err := tx.Exec(ctx, `delete from procurement_catalog_entry where tenant_id = $1 and catalog_id = $2 and id = $3`,
			p.TenantID, id, entryID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeCatalogEntryNotFound, "catalog entry not found")
		}
		return nil
	})
}

// ActOnCatalog applies publish or retire (domain.NextCatalogStatus) under If-Match and
// writes catalog.published.v1 or catalog.retired.v1 in the same transaction.
func (s *Postgres) ActOnCatalog(ctx context.Context, id string, ifMatch int, action string, idem Idem) (domain.Catalog, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Catalog
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadCatalog(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil || replay {
			out = cur
			return err
		}
		if cur.Version != ifMatch {
			return catalogConflict(cur.Version, ifMatch)
		}
		next, err := domain.NextCatalogStatus(cur.Status, action, len(cur.Entries))
		if err != nil {
			if de, ok := err.(*domain.Error); ok {
				status := http.StatusConflict
				if de.Code == domain.CodeCatalogEmpty {
					status = http.StatusUnprocessableEntity
				}
				return httpx.NewProblem(status, de.Code, de.Detail)
			}
			return err
		}
		if _, err := tx.Exec(ctx, `update procurement_catalog set status = $3, row_version = row_version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id, next); err != nil {
			return err
		}
		if out, err = loadCatalog(ctx, tx, id, false); err != nil {
			return err
		}
		evt := purchasingevents.CatalogPublished
		if action == domain.CatalogRetire {
			evt = purchasingevents.CatalogRetired
		}
		data := purchasingevents.CatalogData{CatalogID: out.ID, Number: out.Number, LegalEntityID: out.LegalEntityID, Name: out.Name,
			Status: out.Status, ValidFrom: out.ValidFrom.String(), OrgUnitIDs: out.OrgUnitIDs, EntryCount: len(out.Entries), Version: out.Version}
		if out.ValidTo != nil {
			data.ValidTo = out.ValidTo.String()
		}
		return enqueue(ctx, tx, "catalog", out.ID, evt, data)
	})
	return out, err
}

// likePattern escapes LIKE wildcards so the picker text matches literally.
func likePattern(text string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(text) + "%"
}

// SearchCatalogEntries is the requisition picker query: entries of PUBLISHED catalogs
// valid on f.Date for the legal entity and unit, newest first by entry id.
func (s *Postgres) SearchCatalogEntries(ctx context.Context, f CatalogSearch, after string, limit int) (Page[CatalogHit], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	pattern := ""
	if f.Text != "" {
		pattern = likePattern(f.Text)
	}
	var items []CatalogHit
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+catalogEntryColumns+`, c.id, c.number, c.name
			from procurement_catalog_entry e
			join procurement_catalog c on c.tenant_id = e.tenant_id and c.id = e.catalog_id
			where e.tenant_id = $1 and c.legal_entity_id = $2 and c.status = 'PUBLISHED'
			and c.valid_from <= $3 and (c.valid_to is null or c.valid_to >= $3)
			and (cardinality(c.org_unit_ids) = 0 or ($4::uuid is not null and $4::uuid = any(c.org_unit_ids)))
			and ($5 = '' or e.name ilike $5 or e.description ilike $5 or e.vendor_part_no ilike $5)
			and ($6 = '' or e.category = $6) and ($7 = '' or e.vendor_id::text = $7)
			and ($8::uuid is null or e.id < $8::uuid)
			order by e.id desc limit $9`, p.TenantID, f.LegalEntityID, f.Date.Time, nullable(f.OrgUnitID), pattern, f.Category, f.VendorID,
			nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (CatalogHit, error) {
			var h CatalogHit
			return h, row.Scan(append(catalogEntryDest(&h.CatalogEntry), &h.CatalogID, &h.CatalogNumber, &h.CatalogName)...)
		})
		return err
	})
	if err != nil {
		return Page[CatalogHit]{}, err
	}
	if items == nil {
		items = []CatalogHit{}
	}
	return pageOf(items, limit, func(h CatalogHit) string { return h.ID }), nil
}

// GetCatalogEntry returns one entry and the header of its catalog (no entries), for
// checking a requisition line that names it.
func (s *Postgres) GetCatalogEntry(ctx context.Context, entryID string) (domain.Catalog, domain.CatalogEntry, error) {
	p, _ := tenant.FromContext(ctx)
	var c domain.Catalog
	var e domain.CatalogEntry
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var from time.Time
		var to *time.Time
		dest := append(catalogEntryDest(&e), &c.ID, &c.Number, &c.LegalEntityID, &c.Status, &from, &to, &c.OrgUnitIDs)
		err := tx.QueryRow(ctx, `select `+catalogEntryColumns+`, c.id, c.number, c.legal_entity_id, c.status, c.valid_from, c.valid_to,
			c.org_unit_ids::text[]
			from procurement_catalog_entry e join procurement_catalog c on c.tenant_id = e.tenant_id and c.id = e.catalog_id
			where e.tenant_id = $1 and e.id = $2`, p.TenantID, entryID).Scan(dest...)
		if err != nil {
			return notFound(err, CodeCatalogEntryNotFound, "catalog entry "+entryID+" not found")
		}
		c.ValidFrom, c.ValidTo = day(from), dayOrNil(to)
		return nil
	})
	return c, e, err
}
