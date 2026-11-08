package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

// dutyLegalEntityWrite is the duty the copy route requires; the copy runs tenant-wide because it
// reads the source entity's sites and writes the new entity's.
const dutyLegalEntityWrite = "org.legal-entity.write"

// LegalEntityCopyInput is a legal-entity copy request (FR-6.15). The user supplies code, name,
// country and accounting currency; the time zone and language default to the source's.
type LegalEntityCopyInput struct {
	SourceID           string
	Code               string
	Name               string
	Country            string
	Currency           string
	TimeZone           string
	DefaultLanguage    string
	RegistrationNumber *string
	Sites              bool // sites and their warehouses
	Locations          bool // locations of the copied warehouses; needs Sites
	RoundingRules      bool // currency rounding rules the source entity overrides
}

// LegalEntityCopy is what a copy created.
type LegalEntityCopy struct {
	LegalEntity    LegalEntity `json:"legalEntity"`
	SourceID       string      `json:"sourceId"`
	Sites          int         `json:"sites"`
	Warehouses     int         `json:"warehouses"`
	Locations      int         `json:"locations"`
	RoundingRules  int         `json:"roundingRules"`
	CalendarCopied bool        `json:"calendarCopied"`
	Replayed       bool        `json:"-"`
}

func (c *LegalEntityCopy) fromRow(row pgx.Row) error {
	return row.Scan(&c.SourceID, &c.Sites, &c.Warehouses, &c.Locations, &c.RoundingRules, &c.CalendarCopied)
}

// copyRequestHash fingerprints a copy request as the store received it, so an Idempotency-Key
// replay with any other body (another option, time zone or registration number) is refused.
func copyRequestHash(in LegalEntityCopyInput) string {
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func calendarAssignable(ctx context.Context, tx pgx.Tx, tenantID string, calendarID *string) (bool, error) {
	if calendarID == nil {
		return false, nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `select exists (select 1 from fiscal_calendar c where c.tenant_id = $1 and c.id = $2 and c.status = 'active'
		and exists (select 1 from fiscal_period p where p.tenant_id = $1 and p.calendar_id = c.id))`, tenantID, *calendarID).Scan(&ok)
	return ok, err
}

// CopyLegalEntity creates a legal entity from an existing one in a single transaction: the new
// entity, its fiscal calendar assignment, and optionally the active sites, warehouses and
// locations (codes kept: they are unique per legal entity, so the new entity cannot collide) and
// the currency rounding rules the source overrides. Every copied row gets a new id and no
// transactional data is read. Any failure rolls the whole copy back (BR-6.14).
func (s *Postgres) CopyLegalEntity(ctx context.Context, in LegalEntityCopyInput, idempotencyKey string) (LegalEntityCopy, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out LegalEntityCopy
	hash := copyRequestHash(in)
	err := dbx.WithTenantWideAdminTx(ctx, s.pool, p, dutyLegalEntityWrite, func(tx pgx.Tx) error {
		if key != nil {
			if err := lockIdempotencyKey(ctx, tx, "legal_entity_copy", p.TenantID, *key); err != nil {
				return err
			}
			var targetID, storedHash string
			err := tx.QueryRow(ctx, `select target_id, request_hash from legal_entity_copy where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&targetID, &storedHash)
			if err == nil {
				if storedHash != hash {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
				}
				return replayLegalEntityCopy(ctx, tx, p.TenantID, targetID, &out)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		source, err := scanLegalEntity(tx.QueryRow(ctx, `select `+legalEntityColumns+` from legal_entity where tenant_id = $1 and id = $2 for share`, p.TenantID, in.SourceID))
		if errors.Is(err, pgx.ErrNoRows) {
			return legalEntityNotFound()
		}
		if err != nil {
			return err
		}
		if in.TimeZone == "" {
			in.TimeZone = source.TimeZone
		}
		if in.DefaultLanguage == "" {
			in.DefaultLanguage = source.DefaultLanguage
		}
		if err := checkLegalEntityLimit(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := checkActiveReferences(ctx, tx, p.TenantID, in.Country, in.Currency, in.DefaultLanguage); err != nil {
			return err
		}
		out = LegalEntityCopy{SourceID: source.ID}
		if out.CalendarCopied, err = calendarAssignable(ctx, tx, p.TenantID, source.FiscalCalendarID); err != nil {
			return err
		}
		var calendar *string
		if out.CalendarCopied {
			calendar = source.FiscalCalendarID
		}
		out.LegalEntity, err = scanLegalEntity(tx.QueryRow(ctx, `insert into legal_entity (tenant_id, id, code, name, country, currency, time_zone, default_language, registration_number, fiscal_calendar_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) returning `+legalEntityColumns,
			p.TenantID, ids.New(), in.Code, in.Name, in.Country, in.Currency, in.TimeZone, in.DefaultLanguage, in.RegistrationNumber, calendar))
		if err != nil {
			return err
		}
		target := out.LegalEntity
		if _, err := tx.Exec(ctx, `insert into legal_entity_name (tenant_id, legal_entity_id, name) values ($1, $2, $3)`, p.TenantID, target.ID, target.Name); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "legalentity", target.ID, orgevents.LegalEntityCreated, p.TenantID, orgevents.LegalEntityCreatedData{
			ID: target.ID, Code: target.Code, Name: target.Name, CountryCode: target.Country, AccountingCurrency: target.Currency, TimeZone: target.TimeZone,
		}); err != nil {
			return err
		}
		if in.Sites {
			if err := copyStructure(ctx, tx, p.TenantID, source.ID, target.ID, in.Locations, &out); err != nil {
				return err
			}
		}
		if in.RoundingRules {
			if err := copyRoundingRules(ctx, tx, p.TenantID, source.ID, target.ID, &out); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `insert into legal_entity_copy (tenant_id, id, source_id, target_id, idempotency_key, request_hash, sites, warehouses, locations, rounding_rules, calendar_copied)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, p.TenantID, ids.New(), source.ID, target.ID, key, hash, out.Sites, out.Warehouses, out.Locations, out.RoundingRules, out.CalendarCopied); err != nil {
			return err
		}
		return enqueue(ctx, tx, "legalentity", target.ID, orgevents.LegalEntityCopied, p.TenantID, orgevents.LegalEntityCopiedData{
			SourceID: source.ID, TargetID: target.ID, Sites: out.Sites, Warehouses: out.Warehouses, Locations: out.Locations, RoundingRules: out.RoundingRules, CalendarCopied: out.CalendarCopied,
		})
	})
	if errors.Is(err, dbx.ErrNotTenantWide) {
		return LegalEntityCopy{}, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "copying a legal entity needs the tenant.administer duty and no legal-entity scope")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.TableName == "legal_entity" {
		return LegalEntityCopy{}, httpx.NewProblem(http.StatusConflict, CodeLegalEntityCode, "a legal entity with this code already exists")
	}
	if err != nil {
		return LegalEntityCopy{}, err
	}
	return out, nil
}

func replayLegalEntityCopy(ctx context.Context, tx pgx.Tx, tenantID, targetID string, out *LegalEntityCopy) error {
	target, err := scanLegalEntity(tx.QueryRow(ctx, `select `+legalEntityColumns+` from legal_entity where tenant_id = $1 and id = $2`, tenantID, targetID))
	if err != nil {
		return err
	}
	out.LegalEntity = target
	if err := out.fromRow(tx.QueryRow(ctx, `select source_id, sites, warehouses, locations, rounding_rules, calendar_copied from legal_entity_copy where tenant_id = $1 and target_id = $2`, tenantID, targetID)); err != nil {
		return err
	}
	out.Replayed = true
	return nil
}

func copyStructure(ctx context.Context, tx pgx.Tx, tenantID, sourceID, targetID string, locations bool, out *LegalEntityCopy) error {
	rows, err := tx.Query(ctx, `select `+siteColumns+` from site where tenant_id = $1 and legal_entity_id = $2 and status = 'active' order by code`, tenantID, sourceID)
	if err != nil {
		return err
	}
	var sites []Site
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			rows.Close()
			return err
		}
		sites = append(sites, site)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	siteIDs := map[string]string{}
	var created []Site
	for _, old := range sites {
		calendar := (*string)(nil)
		if ok, err := calendarAssignable(ctx, tx, tenantID, old.FiscalCalendarID); err != nil {
			return err
		} else if ok {
			calendar = old.FiscalCalendarID
		}
		site, err := scanSite(tx.QueryRow(ctx, `insert into site (tenant_id, id, legal_entity_id, code, name, time_zone, fiscal_calendar_id)
			values ($1, $2, $3, $4, $5, $6, $7) returning `+siteColumns, tenantID, ids.New(), targetID, old.Code, old.Name, old.TimeZone, calendar))
		if err != nil {
			return err
		}
		siteIDs[old.ID] = site.ID
		created = append(created, site)
	}
	out.Sites = len(created)

	whRows, err := tx.Query(ctx, `select `+warehouseColumns+` from warehouse where tenant_id = $1 and legal_entity_id = $2 and status = 'active' order by code`, tenantID, sourceID)
	if err != nil {
		return err
	}
	var warehouses []Warehouse
	for whRows.Next() {
		w, err := scanWarehouse(whRows)
		if err != nil {
			whRows.Close()
			return err
		}
		warehouses = append(warehouses, w)
	}
	whRows.Close()
	if err := whRows.Err(); err != nil {
		return err
	}
	warehouseIDs := map[string]string{}
	var createdWarehouses []Warehouse
	for _, old := range warehouses {
		siteID, ok := siteIDs[old.SiteID]
		if !ok {
			continue
		}
		w, err := scanWarehouse(tx.QueryRow(ctx, `insert into warehouse (tenant_id, id, legal_entity_id, site_id, code, name, type, time_zone)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+warehouseColumns, tenantID, ids.New(), targetID, siteID, old.Code, old.Name, old.Type, old.TimeZone))
		if err != nil {
			return err
		}
		warehouseIDs[old.ID] = w.ID
		createdWarehouses = append(createdWarehouses, w)
	}
	out.Warehouses = len(createdWarehouses)

	for i, old := range sites {
		if old.DefaultWarehouseID == nil {
			continue
		}
		if id, ok := warehouseIDs[*old.DefaultWarehouseID]; ok {
			if _, err := tx.Exec(ctx, `update site set default_warehouse_id = $3 where tenant_id = $1 and id = $2`, tenantID, created[i].ID, id); err != nil {
				return err
			}
			created[i].DefaultWarehouseID = &id
		}
	}
	for _, site := range created {
		if err := enqueue(ctx, tx, "site", site.ID, orgevents.SiteCreated, tenantID, orgevents.SiteCreatedData{ID: site.ID, LegalEntityID: site.LegalEntityID, DefaultWarehouseID: site.DefaultWarehouseID}); err != nil {
			return err
		}
	}
	for _, w := range createdWarehouses {
		if err := enqueueWarehouseCreated(ctx, tx, tenantID, w); err != nil {
			return err
		}
	}
	if !locations {
		return nil
	}
	locRows, err := tx.Query(ctx, `select `+locationColumns+` from location where tenant_id = $1 and legal_entity_id = $2 and status = 'active' order by warehouse_id, code`, tenantID, sourceID)
	if err != nil {
		return err
	}
	var oldLocations []Location
	for locRows.Next() {
		l, err := scanLocation(locRows)
		if err != nil {
			locRows.Close()
			return err
		}
		oldLocations = append(oldLocations, l)
	}
	locRows.Close()
	if err := locRows.Err(); err != nil {
		return err
	}
	// Locations can run to thousands: queue each insert and its created event into a batch so a chunk
	// costs one round trip instead of two per location.
	batch := &pgx.Batch{}
	queued := queuedTx{Tx: tx, batch: batch}
	for _, old := range oldLocations {
		warehouseID, ok := warehouseIDs[old.WarehouseID]
		if !ok {
			continue
		}
		l := Location{ID: ids.New(), LegalEntityID: targetID, WarehouseID: warehouseID, Type: old.Type}
		batch.Queue(`insert into location (tenant_id, id, legal_entity_id, warehouse_id, code, name, type) values ($1, $2, $3, $4, $5, $6, $7)`,
			tenantID, l.ID, targetID, warehouseID, old.Code, old.Name, old.Type)
		if err := enqueue(ctx, queued, "location", l.ID, orgevents.LocationCreated, tenantID,
			orgevents.LocationCreatedData{ID: l.ID, LegalEntityID: l.LegalEntityID, ParentID: &l.WarehouseID, Type: &l.Type}); err != nil {
			return err
		}
		out.Locations++
		if batch.Len() >= copyBatchStatements {
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return err
			}
			batch = &pgx.Batch{}
			queued.batch = batch
		}
	}
	if batch.Len() > 0 {
		return tx.SendBatch(ctx, batch).Close()
	}
	return nil
}

// copyBatchStatements bounds one pipelined batch of location inserts and their outbox rows.
const copyBatchStatements = 1000

// queuedTx queues Exec calls into a batch instead of running them, so outbox.Enqueue (which only
// Execs) can ride in the same pipelined batch as the rows it announces.
type queuedTx struct {
	pgx.Tx
	batch *pgx.Batch
}

func (q queuedTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.batch.Queue(sql, args...)
	return pgconn.CommandTag{}, nil
}

func copyRoundingRules(ctx context.Context, tx pgx.Tx, tenantID, sourceID, targetID string, out *LegalEntityCopy) error {
	rows, err := tx.Query(ctx, `select currency_code, purpose, unit::text, method from currency_rounding where tenant_id = $1 and legal_entity_id = $2 order by currency_code, purpose`, tenantID, sourceID)
	if err != nil {
		return err
	}
	type rule struct{ currency, purpose, unit, method string }
	var rules []rule
	for rows.Next() {
		var r rule
		if err := rows.Scan(&r.currency, &r.purpose, &r.unit, &r.method); err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range rules {
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into currency_rounding (tenant_id, id, currency_code, purpose, legal_entity_id, unit, method) values ($1, $2, $3, $4, $5, $6, $7)`,
			tenantID, id, r.currency, r.purpose, targetID, r.unit, r.method); err != nil {
			return err
		}
		out.RoundingRules++
		if err := enqueue(ctx, tx, "currencyrounding", id, orgevents.CurrencyRoundingChanged, tenantID, orgevents.CurrencyRoundingChangedData{
			CurrencyCode: r.currency, Purpose: r.purpose, LegalEntityID: &targetID, Unit: r.unit, Method: r.method}); err != nil {
			return err
		}
	}
	return nil
}
