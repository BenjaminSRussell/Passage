package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Problem codes raised by the service line store (FR-17.23).
const (
	// CodeServiceSpecNotFound is the 404 for a line without a service specification.
	CodeServiceSpecNotFound = "PRC-SRV-NOT-FOUND"
	// CodeModelServiceNotFound is the 404 for a model service specification that is not visible.
	CodeModelServiceNotFound = "PRC-SRV-MODEL-NOT-FOUND"
	// CodeModelServiceExists: the legal entity already has a model with this code.
	CodeModelServiceExists = "PRC-SRV-006"
	// CodeModelServiceStale: If-Match is not the model's version.
	CodeModelServiceStale = "PRC-SRV-007"
)

// ServiceInput is a requested service specification: the limits and either an outline or the
// model that fills it (never both; the API refuses that).
type ServiceInput struct {
	Terms   domain.ServiceTerms
	Outline []domain.OutlineNode
	ModelID string
}

// ServiceResult is a service specification with the PO row version after the call: writes bump
// the PO's row version, so the next PO or specification write must carry the new ETag.
type ServiceResult struct {
	domain.ServiceSpec
	PORowVersion int `json:"poRowVersion"`
}

// ModelServiceFilter narrows ListModelServiceSpecs; empty fields do not filter.
type ModelServiceFilter struct {
	LegalEntityID string
	Status        string
	Text          string
}

// PutServiceSpec saves the service specification of one PO line (FR-17.23), replacing any earlier
// one. The PO must be DRAFT or REJECTED (PRC-SRV-001); the outline must be consistent
// (PRC-SRV-004), within the line amount and the limits coherent (PRC-SRV-002). ifMatch is the PO
// row version the caller read (BR-17.8); the write bumps it. Writes servicespec.changed.v1.
func (s *Postgres) PutServiceSpec(ctx context.Context, poID string, lineNo, ifMatch int, in ServiceInput) (ServiceResult, error) {
	var out ServiceResult
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		po, line, err := lockPlanLine(ctx, tx, poID, lineNo, ifMatch)
		if err != nil {
			return err
		}
		if err := domain.CheckServiceTarget(po.ApprovalStatus, line.Status); err != nil {
			return err
		}
		nodes := in.Outline
		if in.ModelID != "" {
			m, err := loadModelService(ctx, tx, in.ModelID)
			if err != nil {
				return err
			}
			if err := domain.CheckModelApplies(m, po.LegalEntityID, po.Currency); err != nil {
				return err
			}
			nodes = domain.ToTree(m.Outline)
		}
		outline, err := domain.BuildOutline(nodes, money.MinorUnits(po.Currency))
		if err != nil {
			return err
		}
		if err := domain.ValidateServiceTerms(in.Terms, outline.PlannedTotal, line.LineAmount); err != nil {
			return err
		}
		if in.Terms.ContractAgreementLineID != "" {
			agr, err := loadAgreementLine(ctx, tx, in.Terms.ContractAgreementLineID, false)
			if err != nil {
				return err
			}
			if err := domain.CheckContractLine(agr, po.LegalEntityID, po.VendorID, po.Currency); err != nil {
				return err
			}
		}
		if err := deleteServiceSpec(ctx, tx, poID, lineNo); err != nil {
			return err
		}
		p, _ := tenant.FromContext(ctx)
		if _, err := tx.Exec(ctx, `insert into purchase_service_spec (tenant_id, purchase_order_id, line_no, value_limit, expected_value,
			contract_agreement_line_id, model_id, planned_total, updated_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.TenantID, poID, lineNo, in.Terms.ValueLimit, in.Terms.ExpectedValue, nullable(in.Terms.ContractAgreementLineID),
			nullable(in.ModelID), outline.PlannedTotal, userOrNil(p.UserID)); err != nil {
			return err
		}
		for _, r := range outline.Rows {
			if _, err := tx.Exec(ctx, `insert into purchase_service_item (tenant_id, purchase_order_id, line_no, seq, parent_seq, depth, node_kind,
				description, product_id, qty, unit, unit_price, amount) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				p.TenantID, poID, lineNo, r.Seq, nullableInt(r.ParentSeq), r.Depth, r.Kind, r.Description, nullable(r.ProductID),
				r.Qty, r.Unit, r.UnitPrice, r.Amount); err != nil {
				return err
			}
		}
		spec, err := loadServiceSpec(ctx, tx, poID, lineNo)
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "servicespec", poID, purchasingevents.ServiceSpecChanged, serviceSpecData(po, spec, false, p.UserID)); err != nil {
			return err
		}
		if _, err := bumpPORowVersion(ctx, tx, poID); err != nil {
			return err
		}
		return serviceResult(ctx, tx, poID, spec, &out)
	})
	return out, err
}

// DeleteServiceSpec removes the specification of one PO line, which becomes a plain line again;
// same preconditions as PutServiceSpec. It returns the new PO row version.
func (s *Postgres) DeleteServiceSpec(ctx context.Context, poID string, lineNo, ifMatch int) (int, error) {
	var rowVersion int
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		po, line, err := lockPlanLine(ctx, tx, poID, lineNo, ifMatch)
		if err != nil {
			return err
		}
		if err := domain.CheckServiceTarget(po.ApprovalStatus, line.Status); err != nil {
			return err
		}
		spec, err := loadServiceSpec(ctx, tx, poID, lineNo)
		if err != nil {
			return err
		}
		if err := deleteServiceSpec(ctx, tx, poID, lineNo); err != nil {
			return err
		}
		p, _ := tenant.FromContext(ctx)
		if err := enqueue(ctx, tx, "servicespec", poID, purchasingevents.ServiceSpecChanged, serviceSpecData(po, spec, true, p.UserID)); err != nil {
			return err
		}
		rowVersion, err = bumpPORowVersion(ctx, tx, poID)
		return err
	})
	return rowVersion, err
}

// GetServiceSpec returns the specification of one PO line; 404 when the PO is not visible or the
// line has none.
func (s *Postgres) GetServiceSpec(ctx context.Context, poID string, lineNo int) (ServiceResult, error) {
	var out ServiceResult
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkVisible(ctx, tx, poID); err != nil {
			return err
		}
		spec, err := loadServiceSpec(ctx, tx, poID, lineNo)
		if err != nil {
			return err
		}
		return serviceResult(ctx, tx, poID, spec, &out)
	})
	return out, err
}

// ListServiceSpecs returns the specifications of a PO in line order (the "services per purchase
// order" inquiry, 3.17.14); 404 when the PO is not visible.
func (s *Postgres) ListServiceSpecs(ctx context.Context, poID string) ([]domain.ServiceSpec, error) {
	out := []domain.ServiceSpec{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkVisible(ctx, tx, poID); err != nil {
			return err
		}
		p, _ := tenant.FromContext(ctx)
		rows, err := tx.Query(ctx, `select line_no from purchase_service_spec where tenant_id = $1 and purchase_order_id = $2 order by line_no`,
			p.TenantID, poID)
		if err != nil {
			return err
		}
		nos, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil {
			return err
		}
		for _, n := range nos {
			spec, err := loadServiceSpec(ctx, tx, poID, n)
			if err != nil {
				return err
			}
			out = append(out, spec)
		}
		return nil
	})
	return out, err
}

// CheckServiceLimit answers whether an entry of unplanned services fits the line's limits
// (BR-17.19, AC-17.17): unplanned is what the caller (wave 18's entry sheets) has already entered
// on the line. The answer is a domain.LimitCheck; exceeding the value limit or the contract limit
// is Allowed false with PRC-SRV-003, not an error. A line without a specification is 404
// PRC-SRV-NOT-FOUND (no unplanned services are allowed on it); a cancelled order or line takes no
// entry (PRC-SRV-001). It reads only: the caller keeps the entered amounts and enforces the
// answer's figures under its own lock.
func (s *Postgres) CheckServiceLimit(ctx context.Context, poID string, lineNo int, unplanned, entry decimal.Decimal) (domain.LimitCheck, error) {
	var out domain.LimitCheck
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		po, err := loadPurchaseOrder(ctx, tx, poID)
		if err != nil {
			return err
		}
		spec, err := loadServiceSpec(ctx, tx, poID, lineNo)
		if err != nil {
			return err
		}
		if po.ApprovalStatus == domain.POStatusCancelled {
			return &domain.Error{Code: domain.CodeServiceTarget, Detail: "the purchase order is cancelled and takes no service entry"}
		}
		for _, l := range po.Lines {
			if l.LineNo == lineNo && l.Status == domain.POLineCancelled {
				return &domain.Error{Code: domain.CodeServiceTarget, Detail: "line is cancelled and takes no service entry"}
			}
		}
		out, err = domain.CheckUnplanned(spec.ValueLimit, unplanned, entry, spec.ContractRemaining, lineNo)
		out.Currency = spec.Currency
		return err
	})
	return out, err
}

// checkServiceSpecsWithinLines is PRC-SRV-002 when a live line's planned outline is worth more
// than the line (the line amount fell after the outline was saved); the confirm gate of PO
// confirmation.
func checkServiceSpecsWithinLines(ctx context.Context, tx pgx.Tx, po PurchaseOrder) error {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, `select line_no, planned_total from purchase_service_spec where tenant_id = $1 and purchase_order_id = $2`,
		p.TenantID, po.ID)
	if err != nil {
		return err
	}
	type planned struct {
		lineNo int
		total  decimal.Decimal
	}
	specs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (planned, error) {
		var x planned
		return x, r.Scan(&x.lineNo, &x.total)
	})
	if err != nil {
		return err
	}
	for _, sp := range specs {
		for _, l := range po.Lines {
			if l.LineNo != sp.lineNo || l.Status == domain.POLineCancelled {
				continue
			}
			if err := domain.CheckPlannedWithinLine(sp.total, l.LineAmount); err != nil {
				if de, ok := err.(*domain.Error); ok {
					de.Detail = fmt.Sprintf("line %d: %s", l.LineNo, de.Detail)
				}
				return err
			}
		}
	}
	return nil
}

func serviceSpecData(po PurchaseOrder, spec domain.ServiceSpec, removed bool, by string) purchasingevents.ServiceSpecData {
	return purchasingevents.ServiceSpecData{POID: po.ID, Number: po.Number, LineNo: spec.LineNo, Currency: po.Currency,
		ValueLimit: spec.ValueLimit.String(), ExpectedValue: spec.ExpectedValue.String(), PlannedTotal: spec.PlannedTotal.String(),
		OutlineItems: len(spec.Outline), ContractAgreementLineID: spec.ContractAgreementLineID, ModelID: spec.ModelID,
		Removed: removed, ChangedBy: by}
}

func deleteServiceSpec(ctx context.Context, tx pgx.Tx, poID string, lineNo int) error {
	p, _ := tenant.FromContext(ctx)
	if _, err := tx.Exec(ctx, `delete from purchase_service_item where tenant_id = $1 and purchase_order_id = $2 and line_no = $3`,
		p.TenantID, poID, lineNo); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `delete from purchase_service_spec where tenant_id = $1 and purchase_order_id = $2 and line_no = $3`,
		p.TenantID, poID, lineNo)
	return err
}

// serviceResult fills out with the specification and the PO's current row version.
func serviceResult(ctx context.Context, tx pgx.Tx, poID string, spec domain.ServiceSpec, out *ServiceResult) error {
	p, _ := tenant.FromContext(ctx)
	var v int
	if err := tx.QueryRow(ctx, `select row_version from purchase_order where tenant_id = $1 and id = $2`, p.TenantID, poID).Scan(&v); err != nil {
		return err
	}
	*out = ServiceResult{ServiceSpec: spec, PORowVersion: v}
	return nil
}

// loadServiceSpec reads the specification of a line with its outline and, when it names a contract
// line, that line's remaining value; 404 PRC-SRV-NOT-FOUND when the line has none.
func loadServiceSpec(ctx context.Context, tx pgx.Tx, poID string, lineNo int) (domain.ServiceSpec, error) {
	p, _ := tenant.FromContext(ctx)
	spec := domain.ServiceSpec{PurchaseOrderID: poID, LineNo: lineNo, Outline: []domain.OutlineRow{}}
	err := tx.QueryRow(ctx, `select o.currency, l.line_amount, s.value_limit, s.expected_value, s.planned_total,
		coalesce(s.contract_agreement_line_id::text, ''), coalesce(s.model_id::text, ''), coalesce(s.updated_by::text, ''), s.updated_at
		from purchase_service_spec s
		join purchase_order_line l on l.tenant_id = s.tenant_id and l.purchase_order_id = s.purchase_order_id and l.line_no = s.line_no
		join purchase_order o on o.tenant_id = s.tenant_id and o.id = s.purchase_order_id
		where s.tenant_id = $1 and s.purchase_order_id = $2 and s.line_no = $3`, p.TenantID, poID, lineNo).
		Scan(&spec.Currency, &spec.LineAmount, &spec.ValueLimit, &spec.ExpectedValue, &spec.PlannedTotal,
			&spec.ContractAgreementLineID, &spec.ModelID, &spec.UpdatedBy, &spec.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return spec, httpx.NewProblem(http.StatusNotFound, CodeServiceSpecNotFound, fmt.Sprintf("line %d has no service specification", lineNo))
	}
	if err != nil {
		return spec, err
	}
	rows, err := tx.Query(ctx, `select seq, coalesce(parent_seq, 0), depth, node_kind, description, coalesce(product_id::text, ''), qty, unit, unit_price, amount
		from purchase_service_item where tenant_id = $1 and purchase_order_id = $2 and line_no = $3 order by seq`, p.TenantID, poID, lineNo)
	if err != nil {
		return spec, err
	}
	if spec.Outline, err = collectOutline(rows); err != nil {
		return spec, err
	}
	if spec.ContractAgreementLineID != "" {
		agr, err := loadAgreementLine(ctx, tx, spec.ContractAgreementLineID, false)
		if err != nil {
			return spec, err
		}
		rem := domain.ContractRemaining(agr.Lines[0], money.MinorUnits(spec.Currency))
		spec.ContractRemaining = &rem
	}
	return spec, nil
}

func collectOutline(rows pgx.Rows) ([]domain.OutlineRow, error) {
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.OutlineRow, error) {
		var x domain.OutlineRow
		return x, r.Scan(&x.Seq, &x.ParentSeq, &x.Depth, &x.Kind, &x.Description, &x.ProductID, &x.Qty, &x.Unit, &x.UnitPrice, &x.Amount)
	})
	if out == nil {
		out = []domain.OutlineRow{}
	}
	return out, err
}

// CreateModelServiceSpec stores a model service specification (version 1) and writes
// modelservicespec.created.v1 in the same transaction. A replayed Idempotency-Key returns the
// model the first request created; a second model with the same code in the legal entity is 409
// PRC-SRV-006.
func (s *Postgres) CreateModelServiceSpec(ctx context.Context, in domain.ModelService, nodes []domain.OutlineNode, idem Idem) (domain.ModelService, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out domain.ModelService
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadModelService(ctx, tx, existing)
			return err
		}
		outline, err := domain.BuildOutline(nodes, money.MinorUnits(in.Currency))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into purchase_model_service_spec (tenant_id, id, legal_entity_id, code, name, currency, status,
			planned_total, created_by, updated_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`,
			p.TenantID, in.ID, in.LegalEntityID, in.Code, in.Name, in.Currency, in.Status, outline.PlannedTotal, userOrNil(p.UserID)); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "purchase_model_service_spec_unique" {
				return httpx.NewProblem(http.StatusConflict, CodeModelServiceExists,
					"the legal entity already has a model service specification "+in.Code+"; change that one instead")
			}
			return err
		}
		if err := writeModelItems(ctx, tx, in.ID, outline.Rows); err != nil {
			return err
		}
		if out, err = loadModelService(ctx, tx, in.ID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "modelservicespec", out.ID, purchasingevents.ModelServiceSpecCreated, modelServiceData(out, p.UserID))
	})
	return out, err
}

// UpdateModelServiceSpec replaces a model's name, currency, status and outline under If-Match
// (the code and the legal entity do not change) and writes modelservicespec.changed.v1. A model
// already copied into a PO line is not touched: the line keeps its own copy.
func (s *Postgres) UpdateModelServiceSpec(ctx context.Context, id string, ifMatch int, in domain.ModelService, nodes []domain.OutlineNode) (domain.ModelService, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ModelService
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadModelService(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != ifMatch {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeModelServiceStale,
				fmt.Sprintf("model service specification %s is at version %d, not %d; reload and reapply the change", cur.Code, cur.Version, ifMatch))
		}
		outline, err := domain.BuildOutline(nodes, money.MinorUnits(in.Currency))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update purchase_model_service_spec set name = $3, currency = $4, status = $5, planned_total = $6,
			row_version = row_version + 1, updated_by = $7, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id, in.Name, in.Currency, in.Status, outline.PlannedTotal, userOrNil(p.UserID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from purchase_model_service_item where tenant_id = $1 and model_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if err := writeModelItems(ctx, tx, id, outline.Rows); err != nil {
			return err
		}
		if out, err = loadModelService(ctx, tx, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "modelservicespec", out.ID, purchasingevents.ModelServiceSpecChanged, modelServiceData(out, p.UserID))
	})
	return out, err
}

// GetModelServiceSpec returns one model with its outline.
func (s *Postgres) GetModelServiceSpec(ctx context.Context, id string) (domain.ModelService, error) {
	var out domain.ModelService
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadModelService(ctx, tx, id)
		return err
	})
	return out, err
}

// ListModelServiceSpecs returns models newest first, after the cursor id, without their outlines.
func (s *Postgres) ListModelServiceSpecs(ctx context.Context, f ModelServiceFilter, after string, limit int) (Page[domain.ModelService], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	items := []domain.ModelService{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+modelServiceColumns+` from purchase_model_service_spec
			where tenant_id = $1 and ($2::uuid is null or legal_entity_id = $2::uuid) and ($3 = '' or status = $3)
			and ($4 = '' or code ilike '%' || $4 || '%' or name ilike '%' || $4 || '%') and ($5::uuid is null or id < $5::uuid)
			order by id desc limit $6`, p.TenantID, nullable(f.LegalEntityID), f.Status, f.Text, nullable(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m domain.ModelService
			if err := scanModelService(rows, &m); err != nil {
				return err
			}
			m.Outline = []domain.OutlineRow{}
			items = append(items, m)
		}
		return rows.Err()
	})
	if err != nil {
		return Page[domain.ModelService]{}, err
	}
	return pageOf(items, limit, func(m domain.ModelService) string { return m.ID }), nil
}

const modelServiceColumns = `id, legal_entity_id, code, name, currency, status, planned_total, row_version,
	coalesce(created_by::text, ''), created_at, coalesce(updated_by::text, ''), updated_at`

func scanModelService(row pgx.Row, m *domain.ModelService) error {
	return row.Scan(&m.ID, &m.LegalEntityID, &m.Code, &m.Name, &m.Currency, &m.Status, &m.PlannedTotal, &m.Version,
		&m.CreatedBy, &m.CreatedAt, &m.UpdatedBy, &m.UpdatedAt)
}

func loadModelService(ctx context.Context, tx pgx.Tx, id string) (domain.ModelService, error) {
	p, _ := tenant.FromContext(ctx)
	var m domain.ModelService
	err := scanModelService(tx.QueryRow(ctx, `select `+modelServiceColumns+` from purchase_model_service_spec where tenant_id = $1 and id = $2`,
		p.TenantID, id), &m)
	if err != nil {
		return m, notFound(err, CodeModelServiceNotFound, "model service specification not found")
	}
	rows, err := tx.Query(ctx, `select seq, coalesce(parent_seq, 0), depth, node_kind, description, coalesce(product_id::text, ''), qty, unit, unit_price, amount
		from purchase_model_service_item where tenant_id = $1 and model_id = $2 order by seq`, p.TenantID, id)
	if err != nil {
		return m, err
	}
	m.Outline, err = collectOutline(rows)
	return m, err
}

func writeModelItems(ctx context.Context, tx pgx.Tx, id string, rows []domain.OutlineRow) error {
	p, _ := tenant.FromContext(ctx)
	for _, r := range rows {
		if _, err := tx.Exec(ctx, `insert into purchase_model_service_item (tenant_id, model_id, seq, parent_seq, depth, node_kind, description,
			product_id, qty, unit, unit_price, amount) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			p.TenantID, id, r.Seq, nullableInt(r.ParentSeq), r.Depth, r.Kind, r.Description, nullable(r.ProductID),
			r.Qty, r.Unit, r.UnitPrice, r.Amount); err != nil {
			return err
		}
	}
	return nil
}

func modelServiceData(m domain.ModelService, by string) purchasingevents.ModelServiceSpecData {
	return purchasingevents.ModelServiceSpecData{ModelID: m.ID, LegalEntityID: m.LegalEntityID, Code: m.Code, Name: m.Name,
		Currency: m.Currency, Status: m.Status, PlannedTotal: m.PlannedTotal.String(), OutlineItems: len(m.Outline),
		Version: m.Version, ChangedBy: by}
}
