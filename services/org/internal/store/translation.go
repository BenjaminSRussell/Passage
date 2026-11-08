package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

const (
	CodeTranslationLanguageInvalid = "ORG-TRANSLATION-LANGUAGE-INVALID"
	CodeTranslationNotFound        = "ORG-TRANSLATION-NOT-FOUND"

	translationColumns = `owner_type, owner_id::text, language, name, created_at, updated_at`
)

type NameTranslation struct {
	OwnerType string    `json:"ownerType"`
	OwnerID   string    `json:"ownerId"`
	Language  string    `json:"language"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Changed   bool      `json:"-"`
}

type NameResolution struct {
	OwnerType string `json:"ownerType"`
	OwnerID   string `json:"ownerId"`
	Language  string `json:"language,omitempty"`
	Name      string `json:"name"`
	Source    string `json:"source"`
}

var ownerNotFound = map[string]func() error{
	"legal_entity":   legalEntityNotFound,
	"operating_unit": operatingUnitNotFound,
	"site":           siteNotFound,
	"warehouse":      warehouseNotFound,
}

var baseNameQueries = map[string]string{
	"legal_entity":   `select name from legal_entity where tenant_id = $1 and id = $2`,
	"operating_unit": `select name from operating_unit where tenant_id = $1 and id = $2`,
	"site":           `select name from site where tenant_id = $1 and id = $2`,
	"warehouse":      `select name from warehouse where tenant_id = $1 and id = $2`,
}

func scanNameTranslation(row pgx.Row) (NameTranslation, error) {
	var t NameTranslation
	err := row.Scan(&t.OwnerType, &t.OwnerID, &t.Language, &t.Name, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func resolveTranslationOwner(ctx context.Context, tx pgx.Tx, tenantID, ownerType, ownerID string) (*string, error) {
	var legalEntityID *string
	err := tx.QueryRow(ctx, ownerLookups[ownerType], tenantID, ownerID).Scan(&legalEntityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ownerNotFound[ownerType]()
	}
	return legalEntityID, err
}

func (s *Postgres) PutNameTranslation(ctx context.Context, ownerType, ownerID, language, name string) (NameTranslation, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out NameTranslation
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		legalEntityID, err := resolveTranslationOwner(ctx, tx, p.TenantID, ownerType, ownerID)
		if err != nil {
			return err
		}
		languages, err := queryReference(ctx, tx, KindLanguage, p.TenantID, language[:2], ReferenceFilter{})
		if err != nil {
			return err
		}
		if len(languages) == 0 || languages[0].Status != "active" {
			problem := httpx.NewProblem(http.StatusUnprocessableEntity, CodeTranslationLanguageInvalid, "language is unknown or inactive")
			problem.Errors = map[string]string{"language": "must be an active language"}
			return problem
		}
		var inserted bool
		err = tx.QueryRow(ctx, `insert into org_unit_translation (tenant_id, owner_type, owner_id, legal_entity_id, language, name)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, owner_type, owner_id, language)
			do update set name = excluded.name, updated_at = now() where org_unit_translation.name <> excluded.name
			returning `+translationColumns+`, xmax = 0`, p.TenantID, ownerType, ownerID, legalEntityID, language, name).
			Scan(&out.OwnerType, &out.OwnerID, &out.Language, &out.Name, &out.CreatedAt, &out.UpdatedAt, &inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			out, err = scanNameTranslation(tx.QueryRow(ctx, `select `+translationColumns+` from org_unit_translation
				where tenant_id = $1 and owner_type = $2 and owner_id = $3 and language = $4`, p.TenantID, ownerType, ownerID, language))
			return err
		}
		if err != nil {
			return err
		}
		out.Changed, created = true, inserted
		return nil
	})
	if err != nil {
		return NameTranslation{}, false, err
	}
	return out, created, nil
}

// ListNameTranslations lists the translations of one owner; an empty ownerType lists every
// translation of the tenant (the organization package export).
func (s *Postgres) ListNameTranslations(ctx context.Context, ownerType, ownerID string) ([]NameTranslation, error) {
	p, _ := tenant.FromContext(ctx)
	items := []NameTranslation{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		query, args := `select `+translationColumns+` from org_unit_translation where tenant_id = $1 order by owner_type, owner_id, language`, []any{p.TenantID}
		if ownerType != "" {
			if _, err := resolveTranslationOwner(ctx, tx, p.TenantID, ownerType, ownerID); err != nil {
				return err
			}
			query = `select ` + translationColumns + ` from org_unit_translation where tenant_id = $1 and owner_type = $2 and owner_id = $3 order by language`
			args = []any{p.TenantID, ownerType, ownerID}
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanNameTranslation(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *Postgres) DeleteNameTranslation(ctx context.Context, ownerType, ownerID, language string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := resolveTranslationOwner(ctx, tx, p.TenantID, ownerType, ownerID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from org_unit_translation where tenant_id = $1 and owner_type = $2 and owner_id = $3 and language = $4`,
			p.TenantID, ownerType, ownerID, language)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeTranslationNotFound, "translation not found")
		}
		return nil
	})
}

func (s *Postgres) ResolveOrgUnitName(ctx context.Context, ownerType, ownerID, tag string) (NameResolution, error) {
	p, _ := tenant.FromContext(ctx)
	out := NameResolution{OwnerType: ownerType, OwnerID: ownerID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := resolveTranslationOwner(ctx, tx, p.TenantID, ownerType, ownerID); err != nil {
			return err
		}
		var base string
		if err := tx.QueryRow(ctx, baseNameQueries[ownerType], p.TenantID, ownerID).Scan(&base); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select language, name from org_unit_translation where tenant_id = $1 and owner_type = $2 and owner_id = $3`, p.TenantID, ownerType, ownerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		translations := map[string]string{}
		for rows.Next() {
			var language, name string
			if err := rows.Scan(&language, &name); err != nil {
				return err
			}
			translations[language] = name
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.Name, out.Language, out.Source = domain.ResolveName(base, translations, tag)
		return nil
	})
	return out, err
}
