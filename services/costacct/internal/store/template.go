package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	ccevents "github.com/BenjaminSRussell/passage/services/costacct/internal/events"
)

// Template and measure problem codes (listed in the service README).
const (
	CodeFormulaInvalid      = domain.CodeFormulaError // CO-0621
	CodeNegativeReceiver    = "CST-0015"
	CodeMeasureNotFound     = "CST-0016"
	CodeTemplateNotFound    = "CST-0017"
	CodeTemplateVersion     = "CST-0018"
	CodeTemplateRunNotFound = "CST-0019"
	oneTemplateRun          = "template_run_one_completed"
)

// StatisticalMeasure is a driver quantity per receiver and period (FR-38.17 input).
type StatisticalMeasure struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Unit          string `json:"unit"`
}

// MeasureValue is one receiver's value of a measure in a period.
type MeasureValue struct {
	CostObjectID string          `json:"costObjectId"`
	Value        decimal.Decimal `json:"value"`
	UpdatedAt    string          `json:"updatedAt,omitempty"`
}

// MeasureValuesRequest loads values of one measure for one period. Loads are upserts.
type MeasureValuesRequest struct {
	MeasureID     string
	LegalEntityID string
	Period        string
	Values        []MeasureValue
}

// TemplateLine is one receiver line: the formula gives the amount debited to the
// receiver and credited to the sender on the secondary element.
type TemplateLine struct {
	ReceiverCostObjectID string `json:"receiverCostObjectId"`
	SecondaryElementID   string `json:"secondaryElementId"`
	Formula              string `json:"formula"`
}

// AllocationTemplate is a driver-formula allocation template (FR-38.17).
type AllocationTemplate struct {
	ID                 string         `json:"id"`
	LegalEntityID      string         `json:"legalEntityId"`
	Code               string         `json:"code"`
	Name               string         `json:"name"`
	Currency           string         `json:"currency"`
	SenderCostObjectID string         `json:"senderCostObjectId"`
	AllowNegative      bool           `json:"allowNegative"`
	Lines              []TemplateLine `json:"lines"`
	Version            int            `json:"version"`
	UpdatedAt          string         `json:"updatedAt"`
}

// TemplateRunRequest selects a template and period for a run or a dry run.
type TemplateRunRequest struct {
	TemplateID string
	Period     string
}

// TemplateRun is a stored template allocation run with its cents-exact lines.
type TemplateRun struct {
	ID                 string              `json:"id"`
	LegalEntityID      string              `json:"legalEntityId"`
	TemplateID         string              `json:"templateId"`
	TemplateVersion    int                 `json:"templateVersion"`
	Period             string              `json:"period"`
	Currency           string              `json:"currency"`
	SenderCostObjectID string              `json:"senderCostObjectId"`
	Total              decimal.Decimal     `json:"total"`
	Lines              []domain.LineResult `json:"lines"`
	Warnings           []string            `json:"warnings"`
	Status             string              `json:"status"`
	CreatedAt          string              `json:"createdAt"`
}

// DryRunResult is a template evaluation that posted nothing.
type DryRunResult struct {
	TemplateID      string `json:"templateId"`
	TemplateVersion int    `json:"templateVersion"`
	Period          string `json:"period"`
	domain.TemplateResult
}

// CreateMeasure stores a statistical measure for a legal entity.
func (s *Postgres) CreateMeasure(ctx context.Context, key, hash string, in StatisticalMeasure) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		in.ID = ids.New()
		tag, err := tx.Exec(ctx, `insert into statistical_measure (tenant_id, id, legal_entity_id, code, name, unit) values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, legal_entity_id, code) do nothing`, p.TenantID, in.ID, in.LegalEntityID, in.Code, in.Name, in.Unit)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() == 0 {
			return 0, nil, problem(http.StatusConflict, CodeDuplicateCode, "statistical measure code already exists")
		}
		return http.StatusCreated, in, nil
	})
}

// LoadMeasureValues upserts the receivers' values of a measure for a period.
func (s *Postgres) LoadMeasureValues(ctx context.Context, key, hash string, in MeasureValuesRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from statistical_measure where tenant_id = $1 and legal_entity_id = $2 and id = $3)`,
			p.TenantID, in.LegalEntityID, in.MeasureID).Scan(&exists); err != nil {
			return 0, nil, err
		}
		if !exists {
			return 0, nil, problem(http.StatusNotFound, CodeMeasureNotFound, "statistical measure not found in this legal entity")
		}
		objectIDs := make([]string, len(in.Values))
		for i, v := range in.Values {
			objectIDs[i] = v.CostObjectID
		}
		if err := requireObjects(ctx, tx, p.TenantID, in.LegalEntityID, objectIDs); err != nil {
			return 0, nil, err
		}
		for _, v := range in.Values {
			if _, err := tx.Exec(ctx, `insert into statistical_measure_value (tenant_id, id, legal_entity_id, measure_id, cost_object_id, period_key, value)
				values ($1, $2, $3, $4, $5, $6, $7)
				on conflict (tenant_id, measure_id, cost_object_id, period_key) do update set value = excluded.value, updated_at = now()`,
				p.TenantID, ids.New(), in.LegalEntityID, in.MeasureID, v.CostObjectID, in.Period, v.Value.StringFixed(4)); err != nil {
				return 0, nil, err
			}
		}
		return http.StatusOK, map[string]any{"measureId": in.MeasureID, "period": in.Period, "loaded": len(in.Values)}, nil
	})
}

// SaveTemplate stores a template. A new code is version 1 and needs
// expectedVersion 0; saving an existing code needs the version the caller read
// and creates the next one (optimistic versioning, CST-0018). Every formula must
// parse and reference only measures and cost elements of the legal entity
// (CO-0621), so a template that saved is one a run can evaluate.
func (s *Postgres) SaveTemplate(ctx context.Context, key, hash string, in AllocationTemplate, expectedVersion int) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		// Serialize saves of one code so two first saves cannot both create version 1.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('allocation_template:' || $1 || ':' || $2 || ':' || $3, 0))`,
			p.TenantID, in.LegalEntityID, in.Code); err != nil {
			return 0, nil, err
		}
		receivers := make([]string, 0, len(in.Lines))
		elements := make([]string, 0, len(in.Lines))
		for _, l := range in.Lines {
			receivers = append(receivers, l.ReceiverCostObjectID)
			elements = append(elements, l.SecondaryElementID)
		}
		if err := requireObjects(ctx, tx, p.TenantID, in.LegalEntityID, uniq(append([]string{in.SenderCostObjectID}, receivers...))); err != nil {
			return 0, nil, err
		}
		if err := requireSecondaries(ctx, tx, p.TenantID, in.LegalEntityID, uniq(elements)); err != nil {
			return 0, nil, err
		}
		if err := checkReferences(ctx, tx, p.TenantID, in); err != nil {
			return 0, nil, err
		}
		lines, err := json.Marshal(in.Lines)
		if err != nil {
			return 0, nil, err
		}
		var id string
		var version int
		status := http.StatusCreated
		err = tx.QueryRow(ctx, `select id::text, version from allocation_template where tenant_id = $1 and legal_entity_id = $2 and code = $3 for update`,
			p.TenantID, in.LegalEntityID, in.Code).Scan(&id, &version)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if expectedVersion != 0 {
				return 0, nil, problem(http.StatusConflict, CodeTemplateVersion, fmt.Sprintf("template %s does not exist; save it with expectedVersion 0", in.Code))
			}
			in.ID, in.Version = ids.New(), 1
			if _, err := tx.Exec(ctx, `insert into allocation_template (tenant_id, id, legal_entity_id, code, name, currency, sender_cost_object_id,
				allow_negative, lines, version) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1)`,
				p.TenantID, in.ID, in.LegalEntityID, in.Code, in.Name, in.Currency, in.SenderCostObjectID, in.AllowNegative, lines); err != nil {
				return 0, nil, err
			}
		case err != nil:
			return 0, nil, err
		default:
			if expectedVersion != version {
				return 0, nil, problem(http.StatusConflict, CodeTemplateVersion,
					fmt.Sprintf("template %s is at version %d, not %d; reload it and save again", in.Code, version, expectedVersion))
			}
			status = http.StatusOK
			in.ID, in.Version = id, version+1
			if _, err := tx.Exec(ctx, `update allocation_template set name = $3, currency = $4, sender_cost_object_id = $5,
				allow_negative = $6, lines = $7, version = $8, updated_at = now() where tenant_id = $1 and id = $2`,
				p.TenantID, in.ID, in.Name, in.Currency, in.SenderCostObjectID, in.AllowNegative, lines, in.Version); err != nil {
				return 0, nil, err
			}
		}
		if _, err := tx.Exec(ctx, `insert into allocation_template_version (tenant_id, template_id, version, legal_entity_id, sender_cost_object_id, allow_negative, lines)
			values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, in.ID, in.Version, in.LegalEntityID, in.SenderCostObjectID, in.AllowNegative, lines); err != nil {
			return 0, nil, err
		}
		saved, err := loadTemplate(ctx, tx, p.TenantID, in.ID, false)
		return status, saved, err
	})
}

// checkReferences rejects a formula that reads a measure (m.<code>) or a cost
// element balance (b.<code>) the legal entity does not have. A typo would
// otherwise evaluate to zero silently.
func checkReferences(ctx context.Context, tx pgx.Tx, tenantID string, t AllocationTemplate) error {
	measures, err := codeSet(ctx, tx, `select code from statistical_measure where tenant_id = $1 and legal_entity_id = $2`, tenantID, t.LegalEntityID)
	if err != nil {
		return err
	}
	elements, err := codeSet(ctx, tx, `select code from cost_element where tenant_id = $1 and legal_entity_id = $2`, tenantID, t.LegalEntityID)
	if err != nil {
		return err
	}
	for i, l := range t.Lines {
		f, err := domain.ParseFormula(l.Formula)
		if err != nil {
			return formulaProblem(t.Code, i, err.Error())
		}
		for _, ref := range f.Refs() {
			kind, name, _ := strings.Cut(ref, ".")
			switch {
			case kind == "m" && !measures[name]:
				return formulaProblem(t.Code, i, fmt.Sprintf("%s is not a statistical measure of this legal entity", ref))
			case kind == "b" && !elements[name]:
				return formulaProblem(t.Code, i, fmt.Sprintf("%s is not a cost element of this legal entity", ref))
			}
		}
	}
	return nil
}

// formulaProblem is CO-0621 in the PRD's message form; line is 0-based.
func formulaProblem(template string, line int, detail string) error {
	return problem(http.StatusUnprocessableEntity, CodeFormulaInvalid, fmt.Sprintf("Formula error in template %s line %d: %s", template, line+1, detail))
}

func codeSet(ctx context.Context, tx pgx.Tx, q string, args ...any) (map[string]bool, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out[c] = true
	}
	return out, rows.Err()
}

// RunTemplate evaluates a template for a period, stores the run and posts its
// entries (origin Allocation) in one transaction. Each line debits the receiver
// and credits the sender on the line's element, so the run nets to zero
// (FR-38.17, FR-38.7 conservation). The template row is locked, so two runs of
// one template and period cannot both complete (CST-0002).
func (s *Postgres) RunTemplate(ctx context.Context, key, hash string, in TemplateRunRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, err := loadTemplate(ctx, tx, p.TenantID, in.TemplateID, true)
		if err != nil {
			return 0, nil, err
		}
		var done bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from template_run where tenant_id = $1 and template_id = $2 and period_key = $3 and status = 'Completed')`,
			p.TenantID, t.ID, in.Period).Scan(&done); err != nil {
			return 0, nil, err
		}
		if done {
			return 0, nil, problem(http.StatusConflict, CodePeriodAllocated, "this template already has a completed run for the period")
		}
		res, err := evaluateTemplate(ctx, tx, p.TenantID, t, in.Period)
		if err != nil {
			return 0, nil, err
		}
		resLines, err := json.Marshal(res.Lines)
		if err != nil {
			return 0, nil, err
		}
		warnings, err := json.Marshal(res.Warnings)
		if err != nil {
			return 0, nil, err
		}
		runID := ids.New()
		if _, err := tx.Exec(ctx, `insert into template_run (tenant_id, id, legal_entity_id, template_id, template_version, period_key, currency,
			sender_cost_object_id, total, lines, warnings, status) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'Completed')`,
			p.TenantID, runID, t.LegalEntityID, t.ID, t.Version, in.Period, t.Currency, t.SenderCostObjectID, res.TotalIn.StringFixed(4), resLines, warnings); err != nil {
			return 0, nil, err
		}
		for _, ln := range res.Lines {
			if ln.Amount.IsZero() {
				continue
			}
			if err := postEntry(ctx, tx, p.TenantID, t.LegalEntityID, ln.ReceiverID, ln.ElementID, in.Period, t.Currency, ln.Amount, runID, "template_run_id"); err != nil {
				return 0, nil, err
			}
			if err := postEntry(ctx, tx, p.TenantID, t.LegalEntityID, t.SenderCostObjectID, ln.ElementID, in.Period, t.Currency, ln.Amount.Neg(), runID, "template_run_id"); err != nil {
				return 0, nil, err
			}
		}
		if err := enqueue(ctx, tx, "template_run", runID, ccevents.AllocationCompleted, p.TenantID, ccevents.AllocationCompletedData{
			RunID: runID, LegalEntityID: t.LegalEntityID, TemplateID: t.ID, Period: in.Period, Method: "Template", Converged: true,
			TotalIn: res.TotalIn, TotalOut: res.TotalIn, Residual: decimal.Zero, Swept: decimal.Zero,
		}); err != nil {
			return 0, nil, err
		}
		run, err := loadTemplateRun(ctx, tx, p.TenantID, runID)
		return http.StatusCreated, run, err
	})
}

// DryRunTemplate evaluates a template for a period without storing anything.
func (s *Postgres) DryRunTemplate(ctx context.Context, in TemplateRunRequest) (DryRunResult, error) {
	var out DryRunResult
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		t, err := loadTemplate(ctx, tx, tenantID, in.TemplateID, false)
		if err != nil {
			return err
		}
		res, err := evaluateTemplate(ctx, tx, tenantID, t, in.Period)
		out = DryRunResult{TemplateID: t.ID, TemplateVersion: t.Version, Period: in.Period, TemplateResult: res}
		return err
	})
	return out, err
}

// ListMeasures lists a legal entity's measures by code (every legal entity in scope when empty).
func (s *Postgres) ListMeasures(ctx context.Context, legalEntityID string) ([]StatisticalMeasure, error) {
	out := []StatisticalMeasure{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, code, name, unit from statistical_measure
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) order by code limit $3`, tenantID, legalEntityID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m StatisticalMeasure
			if err := rows.Scan(&m.ID, &m.LegalEntityID, &m.Code, &m.Name, &m.Unit); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// MeasureValues lists one measure's values for a period, or CST-0016.
func (s *Postgres) MeasureValues(ctx context.Context, measureID, period string) ([]MeasureValue, error) {
	out := []MeasureValue{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from statistical_measure where tenant_id = $1 and id = $2)`, tenantID, measureID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeMeasureNotFound, "statistical measure not found")
		}
		rows, err := tx.Query(ctx, `select cost_object_id::text, value::text, updated_at from statistical_measure_value
			where tenant_id = $1 and measure_id = $2 and period_key = $3 order by cost_object_id limit $4`, tenantID, measureID, period, maxListValues)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v MeasureValue
			var value string
			var updated time.Time
			if err := rows.Scan(&v.CostObjectID, &value, &updated); err != nil {
				return err
			}
			v.Value, v.UpdatedAt = parseDec(value), stamp(updated)
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

const maxListValues = 5000

// ListTemplates lists templates by code.
func (s *Postgres) ListTemplates(ctx context.Context, legalEntityID string) ([]AllocationTemplate, error) {
	out := []AllocationTemplate{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+templateColumns+` from allocation_template
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) order by code limit $3`, tenantID, legalEntityID, listLimit)
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

// GetTemplate returns one template or CST-0017.
func (s *Postgres) GetTemplate(ctx context.Context, id string) (AllocationTemplate, error) {
	var t AllocationTemplate
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		t, err = loadTemplate(ctx, tx, tenantID, id, false)
		return err
	})
	return t, err
}

// ListTemplateRuns lists completed template runs, newest first, optionally of one template.
func (s *Postgres) ListTemplateRuns(ctx context.Context, legalEntityID, templateID string) ([]TemplateRun, error) {
	out := []TemplateRun{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+templateRunColumns+` from template_run
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or template_id::text = $3)
			order by created_at desc, id limit $4`, tenantID, legalEntityID, templateID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanTemplateRun(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetTemplateRun returns one template run or CST-0019.
func (s *Postgres) GetTemplateRun(ctx context.Context, id string) (TemplateRun, error) {
	var r TemplateRun
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		r, err = loadTemplateRun(ctx, tx, tenantID, id)
		return err
	})
	return r, err
}

const templateColumns = `id::text, legal_entity_id::text, code, name, currency, sender_cost_object_id::text, allow_negative, lines, version, updated_at`

func scanTemplate(row pgx.Row) (AllocationTemplate, error) {
	var t AllocationTemplate
	var lines []byte
	var updated time.Time
	if err := row.Scan(&t.ID, &t.LegalEntityID, &t.Code, &t.Name, &t.Currency, &t.SenderCostObjectID, &t.AllowNegative, &lines, &t.Version, &updated); err != nil {
		return t, err
	}
	t.UpdatedAt = stamp(updated)
	return t, json.Unmarshal(lines, &t.Lines)
}

// loadTemplate reads a template of the tenant; lock takes FOR UPDATE on its row.
func loadTemplate(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (AllocationTemplate, error) {
	q := `select ` + templateColumns + ` from allocation_template where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	t, err := scanTemplate(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, problem(http.StatusNotFound, CodeTemplateNotFound, "allocation template not found")
	}
	return t, err
}

const templateRunColumns = `id::text, legal_entity_id::text, template_id::text, template_version, period_key, currency, sender_cost_object_id::text,
	total::text, lines, warnings, status, created_at`

func scanTemplateRun(row pgx.Row) (TemplateRun, error) {
	var r TemplateRun
	var total string
	var lines, warnings []byte
	var created time.Time
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.TemplateID, &r.TemplateVersion, &r.Period, &r.Currency, &r.SenderCostObjectID,
		&total, &lines, &warnings, &r.Status, &created); err != nil {
		return r, err
	}
	r.Total, r.CreatedAt = parseDec(total), stamp(created)
	if err := json.Unmarshal(lines, &r.Lines); err != nil {
		return r, err
	}
	return r, json.Unmarshal(warnings, &r.Warnings)
}

func loadTemplateRun(ctx context.Context, tx pgx.Tx, tenantID, id string) (TemplateRun, error) {
	r, err := scanTemplateRun(tx.QueryRow(ctx, `select `+templateRunColumns+` from template_run where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, problem(http.StatusNotFound, CodeTemplateRunNotFound, "template allocation run not found")
	}
	return r, err
}

// evaluateTemplate checks the template's objects and elements, builds each
// receiver's environment for the period and evaluates the lines (BR-38.12).
func evaluateTemplate(ctx context.Context, tx pgx.Tx, tenantID string, t AllocationTemplate, period string) (domain.TemplateResult, error) {
	receivers := make([]string, 0, len(t.Lines))
	elements := make([]string, 0, len(t.Lines))
	lines := make([]domain.TemplateLine, 0, len(t.Lines))
	for i, l := range t.Lines {
		f, err := domain.ParseFormula(l.Formula)
		if err != nil {
			return domain.TemplateResult{}, formulaProblem(t.Code, i, err.Error())
		}
		receivers = append(receivers, l.ReceiverCostObjectID)
		elements = append(elements, l.SecondaryElementID)
		lines = append(lines, domain.TemplateLine{ReceiverID: l.ReceiverCostObjectID, ElementID: l.SecondaryElementID, Formula: f})
	}
	if err := requireObjects(ctx, tx, tenantID, t.LegalEntityID, uniq(append([]string{t.SenderCostObjectID}, receivers...))); err != nil {
		return domain.TemplateResult{}, err
	}
	if err := requireSecondaries(ctx, tx, tenantID, t.LegalEntityID, uniq(elements)); err != nil {
		return domain.TemplateResult{}, err
	}
	envs, err := receiverEnvs(ctx, tx, tenantID, t.LegalEntityID, period, t.Currency, uniq(receivers))
	if err != nil {
		return domain.TemplateResult{}, err
	}
	res, err := domain.EvaluateTemplate(lines, envs, money.MinorUnits(t.Currency), t.AllowNegative)
	switch {
	case errors.Is(err, domain.ErrNegativeReceiver):
		return domain.TemplateResult{}, problem(http.StatusUnprocessableEntity, CodeNegativeReceiver, err.Error()+" and the template does not allow negative amounts")
	case errors.Is(err, domain.ErrTemplateConservation):
		return domain.TemplateResult{}, problem(http.StatusInternalServerError, CodeConservation, "template totals differ; the run was rolled back")
	}
	return res, err
}

// receiverEnvs builds the m.<measure> and b.<element> references of each
// receiver: measure values loaded for the period, and the receiver's primary
// cost (Journal and Manual entries) per element code in the period and the
// template currency. Allocated cost is not a driver, so a run never feeds on
// another run. Missing values are zero.
func receiverEnvs(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, period, currency string, receivers []string) (map[string]domain.Env, error) {
	envs := make(map[string]domain.Env, len(receivers))
	for _, r := range receivers {
		envs[r] = domain.Env{}
	}
	if err := fillEnv(ctx, tx, envs, "m.", `select sm.code, smv.cost_object_id::text, smv.value::text
		from statistical_measure_value smv join statistical_measure sm on sm.tenant_id = smv.tenant_id and sm.id = smv.measure_id
		where smv.tenant_id = $1 and smv.legal_entity_id = $2 and smv.period_key = $3 and smv.cost_object_id = any($4::uuid[])`,
		tenantID, legalEntityID, period, receivers); err != nil {
		return nil, err
	}
	if err := fillEnv(ctx, tx, envs, "b.", `select ce.code, e.cost_object_id::text, sum(e.amount)::text
		from cost_entry e join cost_element ce on ce.tenant_id = e.tenant_id and ce.id = e.cost_element_id
		where e.tenant_id = $1 and e.legal_entity_id = $2 and e.period_key = $3 and e.cost_object_id = any($4::uuid[])
		and e.currency = $5 and e.origin in ('Journal', 'Manual')
		group by ce.code, e.cost_object_id`, tenantID, legalEntityID, period, receivers, currency); err != nil {
		return nil, err
	}
	return envs, nil
}

func fillEnv(ctx context.Context, tx pgx.Tx, envs map[string]domain.Env, prefix, q string, args ...any) error {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var code, objectID, value string
		if err := rows.Scan(&code, &objectID, &value); err != nil {
			return err
		}
		if env, ok := envs[objectID]; ok {
			env[prefix+code] = parseDec(value)
		}
	}
	return rows.Err()
}

func requireSecondaries(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, elementIDs []string) error {
	var found int
	if err := tx.QueryRow(ctx, `select count(distinct id) from cost_element where tenant_id = $1 and legal_entity_id = $2
		and kind = 'Secondary' and id = any($3::uuid[])`, tenantID, legalEntityID, elementIDs).Scan(&found); err != nil {
		return err
	}
	if found != len(elementIDs) {
		return problem(http.StatusUnprocessableEntity, CodeSecondaryNotFound, "every template line needs a secondary cost element of the legal entity")
	}
	return nil
}

// uniq returns ss without repeated values, sorted, so lookups are deterministic.
func uniq(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
