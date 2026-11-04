package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// DescriptionTemplate is a default description keyed by scope, source type and
// language (FR-11.17). Scope "legal_entity" overrides scope "tenant" for the
// same source type and language.
type DescriptionTemplate struct {
	ID            string            `json:"id"`
	Scope         string            `json:"scope"`
	LegalEntityID *string           `json:"legalEntityId"`
	SourceType    string            `json:"sourceType"`
	Language      string            `json:"language"`
	Template      string            `json:"template"`
	FieldBindings map[string]string `json:"fieldBindings"`
	UpdatedAt     time.Time         `json:"updatedAt"`
	// LockVersion is the entity tag: PUT of an existing template sends it in If-Match.
	LockVersion int `json:"lockVersion"`
}

// Description template scopes.
const (
	ScopeTenant      = "tenant"
	ScopeLegalEntity = "legal_entity"
)

const descriptionColumns = `id::text, scope, legal_entity_id::text, source_type, language, template, field_bindings, updated_at, lock_version`

func scanDescription(row pgx.Row, extra ...any) (DescriptionTemplate, error) {
	var d DescriptionTemplate
	var bindings []byte
	err := row.Scan(append([]any{&d.ID, &d.Scope, &d.LegalEntityID, &d.SourceType, &d.Language, &d.Template, &bindings,
		&d.UpdatedAt, &d.LockVersion}, extra...)...)
	if err != nil {
		return d, err
	}
	d.FieldBindings = map[string]string{}
	if len(bindings) > 0 {
		if err := json.Unmarshal(bindings, &d.FieldBindings); err != nil {
			return d, err
		}
	}
	return d, nil
}

func outOfScope() error {
	return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity")
}

// ListDescriptionTemplates returns the tenant templates and the legal entity
// templates inside the caller's scope (row-level security hides the rest).
func (s *Postgres) ListDescriptionTemplates(ctx context.Context) ([]DescriptionTemplate, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []DescriptionTemplate{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+descriptionColumns+` from description_template where tenant_id = $1
			order by source_type, language, legal_entity_id nulls first`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDescription(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// GetDescriptionTemplate returns one template or 404 LEDGER_DESCRIPTION_TEMPLATE_NOT_FOUND
// (also for another legal entity's template outside the caller's scope).
func (s *Postgres) GetDescriptionTemplate(ctx context.Context, id string) (DescriptionTemplate, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return DescriptionTemplate{}, err
	}
	var out DescriptionTemplate
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		d, err := scanDescription(tx.QueryRow(ctx, `select `+descriptionColumns+` from description_template where tenant_id = $1 and id = $2`,
			p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "LEDGER_DESCRIPTION_TEMPLATE_NOT_FOUND", "description template not found")
		}
		out = d
		return err
	})
	return out, err
}

// mayWriteDescription is the fail-closed write rule: a legal-entity template
// needs its entity inside the caller's scope; a tenant template is tenant-level
// reference data and only a tenant administrator (no legal-entity claim and the
// tenant.administer duty) may write it.
func mayWriteDescription(p tenant.Principal, in DescriptionTemplate) bool {
	if in.Scope == ScopeTenant {
		return tenant.IsTenantAdmin(p)
	}
	return in.LegalEntityID != nil && tenant.InLegalEntityScope(p, *in.LegalEntityID)
}

// PutDescriptionTemplate creates or replaces a template by id; created reports
// whether the id is new. A second template for the same scope, legal entity,
// source type and language is refused with 409
// LEDGER_DESCRIPTION_TEMPLATE_DUPLICATE. A caller scoped to one legal entity
// may write only that entity's templates: tenant templates apply to every
// legal entity, so changing or taking over one is 403 AUTH-0003.
func (s *Postgres) PutDescriptionTemplate(ctx context.Context, id string, ifMatch int, in DescriptionTemplate) (DescriptionTemplate, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return DescriptionTemplate{}, false, err
	}
	if !mayWriteDescription(p, in) {
		return DescriptionTemplate{}, false, outOfScope()
	}
	if in.FieldBindings == nil {
		in.FieldBindings = map[string]string{}
	}
	bindings, err := json.Marshal(in.FieldBindings)
	if err != nil {
		return DescriptionTemplate{}, false, err
	}
	var out DescriptionTemplate
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existingScope string
		var current int
		err := tx.QueryRow(ctx, `select scope, lock_version from description_template where tenant_id = $1 and id = $2 for update`,
			p.TenantID, id).Scan(&existingScope, &current)
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if found && p.LegalEntityID != "" && existingScope == ScopeTenant {
			return outOfScope()
		}
		if err := checkLock(found, current, ifMatch, "description template"); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `insert into description_template (tenant_id, id, scope, legal_entity_id, source_type, language,
			template, field_bindings)
			values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
			on conflict (tenant_id, id) do update set scope = excluded.scope, legal_entity_id = excluded.legal_entity_id,
				source_type = excluded.source_type, language = excluded.language, template = excluded.template,
				field_bindings = excluded.field_bindings,
				`+lockedUpdate("description_template", 9, "scope", "legal_entity_id", "source_type", "language", "template", "field_bindings")+`
			returning `+descriptionColumns+`, (xmax = 0)`, p.TenantID, id, in.Scope, in.LegalEntityID, in.SourceType,
			in.Language, in.Template, string(bindings), ifMatch)
		var scanErr error
		out, scanErr = scanDescription(row, &created)
		var pgErr *pgconn.PgError
		if errors.As(scanErr, &pgErr) {
			switch pgErr.Code {
			case "23505": // description_template_scope_uq
				return httpx.NewProblem(http.StatusConflict, "LEDGER_DESCRIPTION_TEMPLATE_DUPLICATE",
					"a template already exists for this scope, legal entity, source type and language")
			case "42501": // the id belongs to a legal entity outside the caller's scope
				return outOfScope()
			}
		}
		if errors.Is(scanErr, pgx.ErrNoRows) && !found {
			// The upsert met a row the read above did not see: one created since, or another legal entity's
			// template, which row-level security hides from this caller (taking it over is out of scope).
			var visible bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from description_template where tenant_id = $1 and id = $2)`,
				p.TenantID, id).Scan(&visible); err != nil {
				return err
			}
			if !visible {
				return outOfScope()
			}
		}
		return lockLost(scanErr, ifMatch, "description template")
	})
	if err != nil {
		return DescriptionTemplate{}, false, err
	}
	return out, created, nil
}

// loadDescriptionTemplate returns the template for a source type and language,
// preferring the legal entity's own template over the tenant template. A nil
// result means no template applies.
func loadDescriptionTemplate(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, sourceType, language string) (*DescriptionTemplate, error) {
	d, err := scanDescription(tx.QueryRow(ctx, `select `+descriptionColumns+` from description_template
		where tenant_id = $1 and source_type = $2 and language = $3 and (legal_entity_id is null or legal_entity_id = $4::uuid)
		order by legal_entity_id nulls last limit 1`, tenantID, sourceType, language, legalEntityID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// applyDefaultDescriptions runs the default-description stage of the posting
// pipeline (FR-11.17): when the request omits its description or a line omits
// its text, the template for the source type (legal entity first, then tenant)
// is expanded and fills the gaps. Explicit text is never replaced. It returns
// the entry description and, when the expansion was cut to 255 characters, the
// LEDGER_DESCRIPTION_TRUNCATED notice. lines is updated in place.
func applyDefaultDescriptions(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, in PostInput,
	lines []domain.ValidLine) (string, []Notice, error) {
	needed := in.Description == ""
	for _, ln := range lines {
		needed = needed || ln.Text == ""
	}
	if !needed {
		return in.Description, nil, nil
	}
	var tpl *DescriptionTemplate
	var err error
	for _, language := range domain.DescriptionLanguages(in.DescriptionLanguage) {
		if tpl, err = loadDescriptionTemplate(ctx, tx, tenantID, ledger.LegalEntityID, in.Source.Type, language); err != nil || tpl != nil {
			break
		}
	}
	if err != nil || tpl == nil {
		return in.Description, nil, err
	}
	text, truncated := domain.ExpandDescription(tpl.Template, tpl.FieldBindings, descriptionValues(ledger, in, lines))
	description := in.Description
	if description == "" {
		description = text
	}
	for i := range lines {
		if lines[i].Text == "" {
			lines[i].Text = text
		}
	}
	var notices []Notice
	if truncated {
		notices = append(notices, Notice{Code: domain.CodeDescriptionTruncated,
			Message: "the default description was truncated to 255 characters; shorten the template"})
	}
	return description, notices, nil
}

// descriptionValues are the named fields a template can bind (FR-11.17).
// voucher is the voucher writeEntry will assign: the ledger row is locked, so
// next_sequence cannot move before the insert. amount_txn is the entry's debit
// total in the ledger currency (wave 11 is single-currency). party_name stays
// blank until source documents carry parties (wave 7 and the subledgers).
func descriptionValues(ledger Ledger, in PostInput, lines []domain.ValidLine) map[string]string {
	debit := decimal.Zero
	for _, ln := range lines {
		if ln.Side == domain.SideDebit {
			debit = debit.Add(ln.Amount)
		}
	}
	values := map[string]string{
		"voucher":       voucherOf(ledger.Code, ledger.NextSequence),
		"party_name":    "",
		"document_date": "",
		"amount_txn":    debit.StringFixed(money.MinorUnits(ledger.Currency)),
		"currency_txn":  ledger.Currency,
		"source_id":     in.Source.ID,
		"period":        domain.PeriodKey(in.PostingDate),
	}
	if in.DocumentDate != nil {
		values["document_date"] = in.DocumentDate.Format(time.DateOnly)
	}
	return values
}
