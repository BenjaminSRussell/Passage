package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// DocStore is the persistence port of docgen (#46): templates, text blocks and output
// rules. Postgres implements it next to Store; the API reads it through a type assertion,
// so the label and job code does not depend on it.
type DocStore interface {
	PutTemplate(ctx context.Context, t domain.DocumentTemplate) (domain.DocumentTemplate, error)
	GetTemplate(ctx context.Context, code string) (domain.DocumentTemplate, error)
	DefaultTemplate(ctx context.Context, documentType string) (*domain.DocumentTemplate, error)
	ListTemplates(ctx context.Context, documentType string) ([]domain.DocumentTemplate, error)
	PutTextBlock(ctx context.Context, b domain.TextBlock) (domain.TextBlock, error)
	TextBlockVersions(ctx context.Context, code string) ([]domain.TextBlock, error)
	ListTextBlocks(ctx context.Context) ([]domain.TextBlock, error)
	CreateOutputRule(ctx context.Context, r domain.OutputRule, idempotencyKey string) (domain.OutputRule, bool, error)
	ListOutputRules(ctx context.Context) ([]domain.OutputRule, error)
	DeleteOutputRule(ctx context.Context, id string) error
}

var _ DocStore = (*Postgres)(nil)

const templateColumns = "code, name, document_type, is_default, version, config"

func scanTemplate(row pgx.Row) (domain.DocumentTemplate, error) {
	var t domain.DocumentTemplate
	var raw []byte
	if err := row.Scan(&t.Code, &t.Name, &t.DocumentType, &t.Default, &t.Version, &raw); err != nil {
		return t, err
	}
	return t, json.Unmarshal(raw, &t.Config)
}

// PutTemplate creates or replaces the template with t.Code and raises its version. A
// default template takes the default flag from the type's previous default.
func (s *Postgres) PutTemplate(ctx context.Context, t domain.DocumentTemplate) (domain.DocumentTemplate, error) {
	p, _ := tenant.FromContext(ctx)
	cfg, err := json.Marshal(t.Config)
	if err != nil {
		return domain.DocumentTemplate{}, err
	}
	var out domain.DocumentTemplate
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existing string
		switch err := tx.QueryRow(ctx, `select document_type from print_document_template where tenant_id = $1 and code = $2 for update`, p.TenantID, t.Code).Scan(&existing); {
		case err == nil && existing != t.DocumentType:
			return httpx.NewProblem(http.StatusConflict, domain.CodeInvalid, "the document type of a template cannot change")
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if t.Default {
			if _, err := tx.Exec(ctx, `update print_document_template set is_default = false where tenant_id = $1 and document_type = $2 and code <> $3`, p.TenantID, t.DocumentType, t.Code); err != nil {
				return err
			}
		}
		var err error
		out, err = scanTemplate(tx.QueryRow(ctx, `insert into print_document_template (tenant_id, code, name, document_type, is_default, config)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, code) do update set name = excluded.name, is_default = excluded.is_default, config = excluded.config,
				version = print_document_template.version + 1, updated_at = now()
			returning `+templateColumns, p.TenantID, t.Code, t.Name, t.DocumentType, t.Default, cfg))
		return err
	})
	return out, err
}

// GetTemplate returns one template, or a 404 problem.
func (s *Postgres) GetTemplate(ctx context.Context, code string) (domain.DocumentTemplate, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.DocumentTemplate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanTemplate(tx.QueryRow(ctx, `select `+templateColumns+` from print_document_template where tenant_id = $1 and code = $2`, p.TenantID, code))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DocumentTemplate{}, httpx.NewProblem(http.StatusNotFound, domain.CodeTemplateNotFound, "document template not found")
	}
	return out, err
}

// DefaultTemplate returns the default template of a document type, or nil.
func (s *Postgres) DefaultTemplate(ctx context.Context, documentType string) (*domain.DocumentTemplate, error) {
	p, _ := tenant.FromContext(ctx)
	var out *domain.DocumentTemplate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := scanTemplate(tx.QueryRow(ctx, `select `+templateColumns+` from print_document_template where tenant_id = $1 and document_type = $2 and is_default`, p.TenantID, documentType))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		out = &t
		return err
	})
	return out, err
}

// ListTemplates returns the tenant's templates by code, optionally of one document type.
func (s *Postgres) ListTemplates(ctx context.Context, documentType string) ([]domain.DocumentTemplate, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.DocumentTemplate{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+templateColumns+` from print_document_template where tenant_id = $1 and ($2 = '' or document_type = $2) order by code`, p.TenantID, documentType)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTemplate(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

const blockColumns = "code, language, body, version"

func scanBlock(row pgx.Row) (domain.TextBlock, error) {
	var b domain.TextBlock
	err := row.Scan(&b.Code, &b.Language, &b.Body, &b.Version)
	b.Placeholders = domain.Placeholders(b.Body)
	return b, err
}

// PutTextBlock creates or replaces one language version of a text block.
func (s *Postgres) PutTextBlock(ctx context.Context, b domain.TextBlock) (domain.TextBlock, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.TextBlock
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanBlock(tx.QueryRow(ctx, `insert into print_text_block (tenant_id, code, language, body) values ($1, $2, $3, $4)
			on conflict (tenant_id, code, language) do update set body = excluded.body, version = print_text_block.version + 1, updated_at = now()
			returning `+blockColumns, p.TenantID, b.Code, b.Language, b.Body))
		return err
	})
	return out, err
}

func (s *Postgres) queryBlocks(ctx context.Context, code string) ([]domain.TextBlock, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.TextBlock{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+blockColumns+` from print_text_block where tenant_id = $1 and ($2 = '' or code = $2) order by code, language`, p.TenantID, code)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBlock(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// TextBlockVersions returns every language version of one block.
func (s *Postgres) TextBlockVersions(ctx context.Context, code string) ([]domain.TextBlock, error) {
	return s.queryBlocks(ctx, code)
}

// ListTextBlocks returns every block version of the tenant.
func (s *Postgres) ListTextBlocks(ctx context.Context) ([]domain.TextBlock, error) {
	return s.queryBlocks(ctx, "")
}

const outputColumns = "id::text, document_type, coalesce(legal_entity_id, ''), coalesce(account_id, ''), copy_kind, channel, destination, copies, sequence"

func scanOutput(row pgx.Row) (domain.OutputRule, error) {
	var r domain.OutputRule
	err := row.Scan(&r.ID, &r.DocumentType, &r.LegalEntityID, &r.AccountID, &r.CopyKind, &r.Channel, &r.Destination, &r.Copies, &r.Sequence)
	return r, err
}

// CreateOutputRule stores a rule. A repeated Idempotency-Key returns the stored rule
// (inserted=false); a different body under that key is refused.
func (s *Postgres) CreateOutputRule(ctx context.Context, in domain.OutputRule, idempotencyKey string) (domain.OutputRule, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.OutputRule
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanOutput(tx.QueryRow(ctx, `insert into print_output_rule (tenant_id, id, document_type, legal_entity_id, account_id, copy_kind, channel, destination, copies, sequence, idempotency_key)
			values ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, $7, $8, $9, $10, $11)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+outputColumns, p.TenantID, ids.New(), in.DocumentType, in.LegalEntityID, in.AccountID, in.CopyKind, in.Channel, in.Destination, in.Copies, in.Sequence, idempotencyKey))
		if err == nil {
			inserted = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scanOutput(tx.QueryRow(ctx, `select `+outputColumns+` from print_output_rule where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		return err
	})
	if err != nil {
		return domain.OutputRule{}, false, err
	}
	same := out.DocumentType == in.DocumentType && out.LegalEntityID == in.LegalEntityID && out.AccountID == in.AccountID &&
		out.CopyKind == in.CopyKind && out.Channel == in.Channel && out.Destination == in.Destination && out.Copies == in.Copies && out.Sequence == in.Sequence
	if !inserted && !same {
		return domain.OutputRule{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return out, inserted, nil
}

// ListOutputRules returns every output rule of the tenant by sequence.
func (s *Postgres) ListOutputRules(ctx context.Context) ([]domain.OutputRule, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.OutputRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+outputColumns+` from print_output_rule where tenant_id = $1 order by sequence, id`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanOutput(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteOutputRule removes a rule.
func (s *Postgres) DeleteOutputRule(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from print_output_rule where tenant_id = $1 and id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusNotFound, domain.CodeOutputRuleNotFound, "output rule not found")
		}
		return nil
	})
}
