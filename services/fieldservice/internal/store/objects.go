package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// Problem codes of the installed base (listed in the service README).
const (
	CodeObjectDuplicate = "SVC-OBJ-001"
	CodeObjectNotFound  = "SVC-OBJ-002"
	CodeObjectParent    = "SVC-OBJ-003"
	CodeObjectInstalled = "SVC-OBJ-004"
	CodeObjectUnknown   = "SVC-OBJ-005"
)

// ServiceObject is an installed-base unit at a customer site (FR-45.1) with its
// warranty term (FR-45.2). WarrantyStartsOn stays empty until the trigger date
// is known; WarrantyEndsOn is the first day the warranty no longer covers.
type ServiceObject struct {
	ID               string              `json:"id"`
	ItemID           string              `json:"itemId"`
	Serial           string              `json:"serial"`
	CustomerPartyID  string              `json:"customerPartyId"`
	LegalEntityID    string              `json:"legalEntityId"`
	SiteAddressID    *string             `json:"siteAddressId,omitempty"`
	ParentID         *string             `json:"parentId,omitempty"`
	ShipmentID       *string             `json:"shipmentId,omitempty"`
	ShippedOn        *string             `json:"shippedOn,omitempty"`
	InstalledOn      *string             `json:"installedOn,omitempty"`
	Status           string              `json:"status"`
	Warranty         domain.WarrantyTerm `json:"warranty"`
	WarrantyStartsOn *string             `json:"warrantyStartsOn,omitempty"`
	WarrantyEndsOn   *string             `json:"warrantyEndsOn,omitempty"`
	CreatedAt        time.Time           `json:"createdAt"`
	Version          int                 `json:"version"`
}

// ShipmentLine is one serialized item line of a confirmed shipment (wave 28).
type ShipmentLine struct {
	ItemID   string              `json:"itemId"`
	Serials  []string            `json:"serials"`
	Warranty domain.WarrantyTerm `json:"warranty"`
}

// ShipmentReceipt is a confirmed serialized shipment that creates service
// objects (FR-45.1, AC-45.8). It stands in for the wave-28 shipment event.
type ShipmentReceipt struct {
	ShipmentID      string         `json:"shipmentId"`
	ShipDate        time.Time      `json:"shipDate"`
	CustomerPartyID string         `json:"customerPartyId"`
	LegalEntityID   string         `json:"legalEntityId"`
	SiteAddressID   *string        `json:"siteAddressId,omitempty"`
	Lines           []ShipmentLine `json:"lines"`
}

// ReceiptResult says how many objects a shipment created and how many of its
// serials already had one.
type ReceiptResult struct {
	Created  int `json:"created"`
	Existing int `json:"existing"`
}

// ObjectFilter narrows the installed-base list. Empty strings match everything.
type ObjectFilter struct {
	Customer string
	Item     string
	Serial   string
	Parent   string
	Status   string
	Before   string
	Limit    int
}

const serviceObjectColumns = `o.id::text, o.item_id::text, o.serial, o.customer_party_id::text, o.legal_entity_id::text,
	o.site_address_id::text, o.parent_id::text, o.shipment_id::text, o.shipped_on::text, o.installed_on::text, o.status,
	o.warranty_trigger, o.warranty_months, o.parts_covered, o.labor_covered, o.travel_covered, o.warranty_starts_on::text,
	o.created_at, o.version`

func scanServiceObject(row pgx.Row) (ServiceObject, error) {
	var o ServiceObject
	if err := row.Scan(&o.ID, &o.ItemID, &o.Serial, &o.CustomerPartyID, &o.LegalEntityID, &o.SiteAddressID, &o.ParentID,
		&o.ShipmentID, &o.ShippedOn, &o.InstalledOn, &o.Status, &o.Warranty.StartTrigger, &o.Warranty.Months,
		&o.Warranty.PartsCovered, &o.Warranty.LaborCovered, &o.Warranty.TravelCovered, &o.WarrantyStartsOn,
		&o.CreatedAt, &o.Version); err != nil {
		return o, err
	}
	if start := parseDay(o.WarrantyStartsOn); start != nil {
		if end := domain.WarrantyEnd(o.Warranty, start); end != nil {
			text := end.Format(dateLayout)
			o.WarrantyEndsOn = &text
		}
	}
	return o, nil
}

// warrantyStartText is the stored start day of a warranty, or nil while unknown.
func warrantyStartText(term domain.WarrantyTerm, shipped, installed *time.Time, registered time.Time) *string {
	start, ok := domain.WarrantyStart(term.StartTrigger, shipped, installed, registered)
	if !ok {
		return nil
	}
	text := start.Format(dateLayout)
	return &text
}

// CreateServiceObject registers one service object by hand (FR-45.1). A parent
// must belong to the same customer. A replay with the same Idempotency-Key
// returns the stored object.
func (s *Postgres) CreateServiceObject(ctx context.Context, in ServiceObject, key, requestHash string) (ServiceObject, bool, error) {
	tid := tenantOf(ctx)
	var out ServiceObject
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_object", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getServiceObject(ctx, tx, tid, id)
			return err
		}
		if in.ParentID != nil {
			var customer string
			err := tx.QueryRow(ctx, `select customer_party_id::text from service_object where tenant_id = $1 and id = $2`, tid, *in.ParentID).Scan(&customer)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && customer != in.CustomerPartyID {
				return unprocessable(CodeObjectParent, "the parent service object does not exist or belongs to another customer")
			}
			if err != nil {
				return err
			}
		}
		newID := ids.New()
		starts := warrantyStartText(in.Warranty, parseDay(in.ShippedOn), parseDay(in.InstalledOn), time.Now())
		_, err = tx.Exec(ctx, `insert into service_object (tenant_id, id, item_id, serial, customer_party_id, legal_entity_id,
			site_address_id, parent_id, shipped_on, installed_on, status, warranty_trigger, warranty_months,
			warranty_starts_on, parts_covered, labor_covered, travel_covered, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10::date, 'ACTIVE', $11, $12, $13::date, $14, $15, $16, $17, $18)`,
			tid, newID, in.ItemID, in.Serial, in.CustomerPartyID, in.LegalEntityID, in.SiteAddressID, in.ParentID,
			in.ShippedOn, in.InstalledOn, in.Warranty.StartTrigger, in.Warranty.Months, starts,
			in.Warranty.PartsCovered, in.Warranty.LaborCovered, in.Warranty.TravelCovered, nullable(key), requestHash)
		if err != nil {
			return err
		}
		out, err = getServiceObject(ctx, tx, tid, newID)
		return err
	})
	if isUnique(err) {
		return ServiceObject{}, false, httpx.NewProblem(http.StatusConflict, CodeObjectDuplicate, "this item and serial already have a service object")
	}
	return out, replayed, err
}

// ReceiveShipment creates one ACTIVE service object per shipped serial, its
// warranty starting on the ship day for the SHIP trigger (FR-45.1, AC-45.8). A
// serial that already has an object is counted as existing and left as it is,
// so a re-delivered shipment creates nothing twice.
func (s *Postgres) ReceiveShipment(ctx context.Context, in ShipmentReceipt) (ReceiptResult, error) {
	tid := tenantOf(ctx)
	var res ReceiptResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		shipped := domain.Day(in.ShipDate)
		shippedText := shipped.Format(dateLayout)
		now := time.Now()
		for _, line := range in.Lines {
			starts := warrantyStartText(line.Warranty, &shipped, nil, now)
			for _, serial := range line.Serials {
				tag, err := tx.Exec(ctx, `insert into service_object (tenant_id, id, item_id, serial, customer_party_id, legal_entity_id,
					site_address_id, shipment_id, shipped_on, status, warranty_trigger, warranty_months, warranty_starts_on,
					parts_covered, labor_covered, travel_covered)
					values ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, 'ACTIVE', $10, $11, $12::date, $13, $14, $15)
					on conflict (tenant_id, item_id, serial) do nothing`,
					tid, ids.New(), line.ItemID, serial, in.CustomerPartyID, in.LegalEntityID, in.SiteAddressID,
					in.ShipmentID, shippedText, line.Warranty.StartTrigger, line.Warranty.Months, starts,
					line.Warranty.PartsCovered, line.Warranty.LaborCovered, line.Warranty.TravelCovered)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 1 {
					res.Created++
				} else {
					res.Existing++
				}
			}
		}
		return nil
	})
	return res, err
}

// InstallServiceObject records the installation day of an object (FR-45.1). An
// INSTALL warranty that had not started starts that day (FR-45.2). An object is
// installed once; version must be the version the caller read.
func (s *Postgres) InstallServiceObject(ctx context.Context, id string, version int, installedOn string, siteAddressID *string) (ServiceObject, error) {
	tid := tenantOf(ctx)
	var out ServiceObject
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanServiceObject(tx.QueryRow(ctx, `select `+serviceObjectColumns+` from service_object o where o.tenant_id = $1 and o.id = $2 for update`, tid, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeObjectNotFound, "service object not found")
		}
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionConflict(cur.Version)
		}
		if cur.InstalledOn != nil {
			return unprocessable(CodeObjectInstalled, "the service object was already installed on "+*cur.InstalledOn)
		}
		starts := cur.WarrantyStartsOn
		if starts == nil {
			starts = warrantyStartText(cur.Warranty, parseDay(cur.ShippedOn), parseDay(&installedOn), cur.CreatedAt)
		}
		if _, err := tx.Exec(ctx, `update service_object set installed_on = $3::date, warranty_starts_on = $4::date,
			site_address_id = coalesce($5, site_address_id), version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, tid, id, installedOn, starts, siteAddressID); err != nil {
			return err
		}
		out, err = getServiceObject(ctx, tx, tid, id)
		return err
	})
	return out, err
}

// GetServiceObject loads one service object; a missing one is a 404.
func (s *Postgres) GetServiceObject(ctx context.Context, id string) (ServiceObject, error) {
	tid := tenantOf(ctx)
	var o ServiceObject
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		o, err = getServiceObject(ctx, tx, tid, id)
		return err
	})
	return o, err
}

// ListServiceObjects returns the installed base newest first, inside the
// caller's legal-entity scope (PRD 8.45.6 "Installed base explorer").
func (s *Postgres) ListServiceObjects(ctx context.Context, f ObjectFilter) (Page[ServiceObject], error) {
	return listPage(ctx, s, f.Limit, func(o ServiceObject) string { return o.ID }, scanServiceObject,
		`select `+serviceObjectColumns+` from service_object o
		where o.tenant_id = $1 and ($2 = '' or o.customer_party_id::text = $2) and ($3 = '' or o.item_id::text = $3)
			and ($4 = '' or o.serial = $4) and ($5 = '' or o.parent_id::text = $5) and ($6 = '' or o.status = $6)
			and `+keyset("o", "service_object", 7)+`
		order by o.created_at desc, o.id desc limit $8`,
		tenantOf(ctx), f.Customer, f.Item, f.Serial, f.Parent, f.Status, f.Before)
}

func getServiceObject(ctx context.Context, tx pgx.Tx, tenantID, id string) (ServiceObject, error) {
	o, err := scanServiceObject(tx.QueryRow(ctx, `select `+serviceObjectColumns+` from service_object o where o.tenant_id = $1 and o.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return o, httpx.NewProblem(http.StatusNotFound, CodeObjectNotFound, "service object not found")
	}
	return o, err
}

// checkCaseObject verifies that the service object a case names exists and
// belongs to the case's customer, so a case never draws on another customer's
// warranty or object-specific agreement line.
func checkCaseObject(ctx context.Context, tx pgx.Tx, tenantID, objectID, customer string) error {
	var owner string
	err := tx.QueryRow(ctx, `select customer_party_id::text from service_object where tenant_id = $1 and id = $2`, tenantID, objectID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && owner != customer {
		return unprocessable(CodeObjectUnknown, "the service object does not exist or belongs to another customer")
	}
	return err
}

// warrantyCover evaluates the warranty of the case's service object on the
// service day: the work order's completion, else today (FR-45.2, BR-45.4). A
// work order without a case or a case without an object has no warranty, and
// an object of another customer never covers.
func warrantyCover(ctx context.Context, tx pgx.Tx, tenantID string, wo WorkOrder) (domain.WarrantyCover, error) {
	if wo.CaseID == nil {
		return domain.WarrantyCover{}, nil
	}
	var starts *string
	var term domain.WarrantyTerm
	err := tx.QueryRow(ctx, `select so.warranty_starts_on::text, so.warranty_months, so.parts_covered, so.labor_covered, so.travel_covered
		from service_case c join service_object so on so.tenant_id = c.tenant_id and so.id = c.service_object_id
			and so.customer_party_id = c.customer_party_id
		where c.tenant_id = $1 and c.id = $2`, tenantID, *wo.CaseID).Scan(&starts, &term.Months, &term.PartsCovered, &term.LaborCovered, &term.TravelCovered)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WarrantyCover{}, nil
	}
	if err != nil {
		return domain.WarrantyCover{}, err
	}
	on := time.Now()
	if wo.CompletedAt != nil {
		on = *wo.CompletedAt
	}
	return domain.WarrantyCoverOn(term, parseDay(starts), on), nil
}

func parseDay(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t, err := time.Parse(dateLayout, *s)
	if err != nil {
		return nil
	}
	return &t
}
