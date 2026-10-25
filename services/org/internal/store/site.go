package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

const (
	CodeSiteNotFound           = "ORG-SITE-NOT-FOUND"
	CodeSiteCodeDuplicate      = "ORG-SITE-CODE-DUPLICATE"
	CodeSiteLegalEntityInvalid = "ORG-SITE-LEGAL-ENTITY-INVALID"
	siteColumns                = "id, legal_entity_id, code, name, time_zone, status, version, created_at, updated_at"
)

type Site struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	TimeZone      string    `json:"timeZone"`
	Status        string    `json:"status"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type SitePatch struct {
	Name     *string `json:"name"`
	TimeZone *string `json:"timeZone"`
	Status   *string `json:"status"`
}

type SiteFilter struct{ LegalEntityID, Status string }

type SiteKey struct{ Code, ID string }

type SitePage struct {
	Items []Site
	Next  *SiteKey
}

func scanSite(row pgx.Row, extra ...any) (Site, error) {
	var s Site
	err := row.Scan(append([]any{&s.ID, &s.LegalEntityID, &s.Code, &s.Name, &s.TimeZone, &s.Status, &s.Version, &s.CreatedAt, &s.UpdatedAt}, extra...)...)
	return s, err
}

func siteNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeSiteNotFound, "site not found")
}

func activeLegalEntity(ctx context.Context, tx pgx.Tx, tenantID, id string) (string, bool, error) {
	var status, timeZone string
	err := tx.QueryRow(ctx, `select status, time_zone from legal_entity where tenant_id = $1 and id = $2`, tenantID, id).Scan(&status, &timeZone)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return timeZone, err == nil && status == "active", err
}

func (s *Postgres) CreateSite(ctx context.Context, in Site, idempotencyKey string) (Site, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var site Site
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanSite(tx.QueryRow(ctx, `select `+siteColumns+` from site where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				site = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		legalEntityZone, ok, err := activeLegalEntity(ctx, tx, p.TenantID, in.LegalEntityID)
		if err != nil {
			return err
		}
		if !ok {
			bad := httpx.NewProblem(http.StatusUnprocessableEntity, CodeSiteLegalEntityInvalid, "legal entity is unknown or inactive")
			bad.Errors = map[string]string{"legalEntityId": "must be an active legal entity visible to the caller"}
			return bad
		}
		zone := in.TimeZone
		if zone == "" {
			zone = legalEntityZone
		}
		var inserted bool
		site, err = scanSite(tx.QueryRow(ctx, `insert into site (tenant_id, id, legal_entity_id, code, name, time_zone, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, idempotency_key) do update set version = site.version
			returning `+siteColumns+`, (xmax = 0)`, p.TenantID, ids.New(), in.LegalEntityID, in.Code, in.Name, zone, key), &inserted)
		if err != nil || !inserted {
			return err
		}
		return enqueue(ctx, tx, "site", site.ID, orgevents.SiteCreated, p.TenantID, orgevents.SiteCreatedData{ID: site.ID, LegalEntityID: site.LegalEntityID})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Site{}, httpx.NewProblem(http.StatusConflict, CodeSiteCodeDuplicate, "a site with this code already exists in the legal entity")
	}
	if err == nil && key != nil && (site.LegalEntityID != in.LegalEntityID || site.Code != in.Code || site.Name != in.Name || (in.TimeZone != "" && site.TimeZone != in.TimeZone)) {
		return Site{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return site, err
}

func (s *Postgres) GetSite(ctx context.Context, id string) (Site, error) {
	p, _ := tenant.FromContext(ctx)
	var site Site
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		site, err = scanSite(tx.QueryRow(ctx, `select `+siteColumns+` from site where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Site{}, siteNotFound()
	}
	return site, err
}

func (s *Postgres) ListSites(ctx context.Context, f SiteFilter, after SiteKey, limit int) (SitePage, error) {
	p, _ := tenant.FromContext(ctx)
	if after.ID == "" {
		after.ID = "00000000-0000-0000-0000-000000000000"
	}
	page := SitePage{Items: []Site{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+siteColumns+` from site where tenant_id = $1 and (code, id) > ($2, $3::uuid)
			and ($4::text = '' or legal_entity_id::text = lower($4)) and ($5::text = '' or status = $5)
			order by code, id limit $6`, p.TenantID, after.Code, after.ID, f.LegalEntityID, f.Status, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			site, err := scanSite(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, site)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = &SiteKey{Code: last.Code, ID: last.ID}
	}
	return page, err
}

func (in SitePatch) apply(site Site) (Site, []string) {
	var changed []string
	if in.Name != nil && *in.Name != site.Name {
		site.Name = *in.Name
		changed = append(changed, "name")
	}
	if in.Status != nil && *in.Status != site.Status {
		site.Status = *in.Status
		changed = append(changed, "status")
	}
	if in.TimeZone != nil && *in.TimeZone != site.TimeZone {
		site.TimeZone = *in.TimeZone
		changed = append(changed, "time_zone")
	}
	return site, changed
}

func (s *Postgres) UpdateSite(ctx context.Context, id string, version int, in SitePatch) (Site, error) {
	p, _ := tenant.FromContext(ctx)
	var site Site
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanSite(tx.QueryRow(ctx, `select `+siteColumns+` from site where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return siteNotFound()
		}
		if err != nil {
			return err
		}
		site = current
		if current.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("current version is %d", current.Version))
		}
		next, changed := in.apply(current)
		if len(changed) == 0 {
			return nil
		}
		site, err = scanSite(tx.QueryRow(ctx, `update site set name = $3, status = $4, time_zone = $5, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+siteColumns, p.TenantID, id, next.Name, next.Status, next.TimeZone))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "site", id, orgevents.SiteUpdated, p.TenantID, orgevents.SiteUpdatedData{ID: id, ChangedFields: changed, Version: site.Version})
	})
	if err != nil {
		return Site{}, err
	}
	return site, nil
}
