package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	CodeTemplateNotFound    = "AUTH-0404"
	CodeTemplateVersionUsed = "MIG-TEMPLATE-VERSION-EXISTS"
)

type DataTemplate struct {
	ID           string            `json:"id"`
	Entity       string            `json:"entity"`
	Name         string            `json:"name"`
	Version      int               `json:"version"`
	Checksum     string            `json:"checksum"`
	DefaultCount int               `json:"defaultCount"`
	Defaults     map[string]string `json:"defaults"`
	CreatedAt    time.Time         `json:"createdAt"`
}

// LedgerRow is one row of a run's lineage; TemplateFields are the targets filled from the data template.
type LedgerRow struct {
	SourceKey      string   `json:"sourceKey"`
	TargetID       string   `json:"targetId,omitempty"`
	Action         string   `json:"action"`
	TemplateFields []string `json:"defaultFromTemplate"`
}

func templateNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeTemplateNotFound, "data template not found")
}

func templateVersionUsed() error {
	return httpx.NewProblem(http.StatusConflict, CodeTemplateVersionUsed, "this entity, name and version already hold a different template; publish a higher version")
}

func sameTemplate(a, b DataTemplate) bool {
	return a.Entity == b.Entity && a.Name == b.Name && a.Version == b.Version && a.Checksum == b.Checksum
}

const templateColumns = `id, entity, name, version, checksum, default_count, defaults, created_at`

func scanTemplate(row pgx.Row) (DataTemplate, error) {
	var t DataTemplate
	var defaults []byte
	if err := row.Scan(&t.ID, &t.Entity, &t.Name, &t.Version, &t.Checksum, &t.DefaultCount, &defaults, &t.CreatedAt); err != nil {
		return DataTemplate{}, err
	}
	return t, json.Unmarshal(defaults, &t.Defaults)
}

func (s *Postgres) SaveDataTemplate(ctx context.Context, tmpl DataTemplate, idempotencyKey string) (DataTemplate, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	defaults, err := json.Marshal(tmpl.Defaults)
	if err != nil {
		return DataTemplate{}, false, err
	}
	tmpl.ID = ids.New()
	var saved DataTemplate
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into migration.data_template (tenant_id, id, entity, name, version, checksum, default_count, defaults, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) on conflict do nothing`,
			p.TenantID, tmpl.ID, tmpl.Entity, tmpl.Name, tmpl.Version, tmpl.Checksum, len(tmpl.Defaults), defaults, key)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		if created {
			saved, err = scanTemplate(tx.QueryRow(ctx, `select `+templateColumns+` from migration.data_template where tenant_id = $1 and id = $2`, p.TenantID, tmpl.ID))
			return err
		}
		if key != nil {
			saved, err = scanTemplate(tx.QueryRow(ctx, `select `+templateColumns+` from migration.data_template where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				if !sameTemplate(saved, tmpl) {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSpecKeyReuse, "Idempotency-Key was already used with a different request")
				}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		saved, err = scanTemplate(tx.QueryRow(ctx, `select `+templateColumns+` from migration.data_template where tenant_id = $1 and entity = $2 and name = $3 and version = $4`,
			p.TenantID, tmpl.Entity, tmpl.Name, tmpl.Version))
		if err != nil {
			return err
		}
		if saved.Checksum != tmpl.Checksum {
			return templateVersionUsed()
		}
		return nil
	})
	return saved, created, err
}

func (s *Postgres) GetDataTemplate(ctx context.Context, id string) (DataTemplate, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return DataTemplate{}, templateNotFound()
	}
	var t DataTemplate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		t, err = scanTemplate(tx.QueryRow(ctx, `select `+templateColumns+` from migration.data_template where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DataTemplate{}, templateNotFound()
	}
	return t, err
}

func (s *Postgres) ListDataTemplates(ctx context.Context, entity string, limit int) ([]DataTemplate, error) {
	p, _ := tenant.FromContext(ctx)
	var out []DataTemplate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+templateColumns+` from migration.data_template
			where tenant_id = $1 and ($2 = '' or entity = $2)
			order by entity, name, version desc limit $3`, p.TenantID, entity, clampLimit(limit))
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

func (s *Postgres) RunLedger(ctx context.Context, runID string, templatedOnly bool, limit int) ([]LedgerRow, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(runID) {
		return nil, notFound()
	}
	out := []LedgerRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from migration.run where tenant_id = $1 and id = $2)`, p.TenantID, runID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound()
		}
		rows, err := tx.Query(ctx, `select source_key, coalesce(target_id::text, ''), action, template_fields from migration.run_ledger
			where tenant_id = $1 and run_id = $2 and (not $3 or cardinality(template_fields) > 0)
			order by source_key limit $4`, p.TenantID, runID, templatedOnly, clampLimit(limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l LedgerRow
			if err := rows.Scan(&l.SourceKey, &l.TargetID, &l.Action, &l.TemplateFields); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

func (m *Mem) SaveDataTemplate(_ context.Context, tmpl DataTemplate, idempotencyKey string) (DataTemplate, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.SaveErr != nil {
		return DataTemplate{}, false, m.SaveErr
	}
	if id, ok := m.templateKey[idempotencyKey]; ok && idempotencyKey != "" {
		for _, t := range m.templates {
			if t.ID == id {
				if !sameTemplate(t, tmpl) {
					return DataTemplate{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSpecKeyReuse, "Idempotency-Key was already used with a different request")
				}
				return t, false, nil
			}
		}
	}
	for _, t := range m.templates {
		if t.Entity == tmpl.Entity && t.Name == tmpl.Name && t.Version == tmpl.Version {
			if t.Checksum != tmpl.Checksum {
				return DataTemplate{}, false, templateVersionUsed()
			}
			return t, false, nil
		}
	}
	tmpl.ID, tmpl.CreatedAt, tmpl.DefaultCount = ids.New(), time.Now().UTC(), len(tmpl.Defaults)
	m.templates = append(m.templates, tmpl)
	if idempotencyKey != "" {
		m.templateKey[idempotencyKey] = tmpl.ID
	}
	return tmpl, true, nil
}

func (m *Mem) GetDataTemplate(_ context.Context, id string) (DataTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.templates {
		if t.ID == id {
			return t, nil
		}
	}
	return DataTemplate{}, templateNotFound()
}

func (m *Mem) ListDataTemplates(_ context.Context, entity string, limit int) ([]DataTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []DataTemplate
	for _, t := range m.templates {
		if entity == "" || t.Entity == entity {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b DataTemplate) int {
		return cmp.Or(strings.Compare(a.Entity, b.Entity), strings.Compare(a.Name, b.Name), b.Version-a.Version)
	})
	return out[:min(len(out), clampLimit(limit))], nil
}

func (m *Mem) RunLedger(_ context.Context, runID string, templatedOnly bool, limit int) ([]LedgerRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.find(runID)
	if run.ID == "" {
		return nil, notFound()
	}
	out := []LedgerRow{}
	for _, e := range run.Report.Ledger {
		if templatedOnly && len(e.TemplateFields) == 0 {
			continue
		}
		fields := e.TemplateFields
		if fields == nil {
			fields = []string{}
		}
		out = append(out, LedgerRow{SourceKey: e.SourceKey, TargetID: e.TargetID, Action: e.Action, TemplateFields: fields})
	}
	slices.SortFunc(out, func(a, b LedgerRow) int { return strings.Compare(a.SourceKey, b.SourceKey) })
	return out[:min(len(out), clampLimit(limit))], nil
}
