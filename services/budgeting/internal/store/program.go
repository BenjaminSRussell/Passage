package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	budgetevents "github.com/BenjaminSRussell/passage/services/budgeting/internal/events"
)

// Investment program problem codes (listed in the service README).
const (
	CodePositionCodeInUse = "BUD-0026"
	CodeProgramNotFound   = "BUD-0030"
	CodeProgramDuplicate  = "BUD-0031"
	CodeCarryForwardYear  = "BUD-0032"
)

// NewProgram creates an investment program for one approval year (FR-38.21).
type NewProgram struct {
	LegalEntityID string
	Code          string
	Name          string
	ApprovalYear  int
	Currency      string
	Budget        decimal.Decimal
}

// Program is an investment program. Its budget is the root of the distribution;
// Distributed is what its top-level positions hold.
type Program struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	ApprovalYear  int             `json:"approvalYear"`
	ProgramType   string          `json:"programType"`
	Currency      string          `json:"currency"`
	Budget        decimal.Decimal `json:"budget"`
	Distributed   decimal.Decimal `json:"distributed"`
	Status        string          `json:"status"`
	Version       int64           `json:"version"`
	CreatedAt     string          `json:"createdAt"`
}

// Assignment is a capex request, asset under construction or project on a position.
type Assignment struct {
	ID         string          `json:"id"`
	PositionID string          `json:"positionId"`
	ObjectType string          `json:"objectType"`
	ObjectRef  string          `json:"objectRef"`
	Plan       decimal.Decimal `json:"plan"`
	Actual     decimal.Decimal `json:"actual"`
	Commitment decimal.Decimal `json:"commitment"`
	UpdatedAt  string          `json:"updatedAt"`
}

// Position is one program position. Plan, Actual and Commitment roll up the
// position's own assignments and those of every descendant; Available is
// budget − actual − commitment over the subtree.
type Position struct {
	ID             string          `json:"id"`
	ProgramID      string          `json:"programId"`
	ParentID       string          `json:"parentId,omitempty"`
	Code           string          `json:"code"`
	Level          int             `json:"level"`
	Category       string          `json:"category"`
	Budget         decimal.Decimal `json:"budget"`
	Distributed    decimal.Decimal `json:"distributed"`
	Undistributed  decimal.Decimal `json:"undistributed"`
	Plan           decimal.Decimal `json:"plan"`
	Actual         decimal.Decimal `json:"actual"`
	Commitment     decimal.Decimal `json:"commitment"`
	Available      decimal.Decimal `json:"available"`
	CarriedForward decimal.Decimal `json:"carriedForward"`
	Assignments    []Assignment    `json:"assignments"`
	own            domain.Amounts
}

// ProgramView is a program with its rolled-up totals and every position, parents
// before children.
type ProgramView struct {
	Program
	Plan           decimal.Decimal `json:"plan"`
	Actual         decimal.Decimal `json:"actual"`
	Commitment     decimal.Decimal `json:"commitment"`
	Available      decimal.Decimal `json:"available"`
	CarriedForward decimal.Decimal `json:"carriedForward"`
	Positions      []Position      `json:"positions"`
}

// Allocation sets the budget of one child position, creating it when the code is new.
type Allocation struct {
	PositionCode string
	Category     string // empty keeps the category, or Capex for a new position
	Budget       decimal.Decimal
}

// DistributeRequest distributes budget under a parent position ("" = program root).
type DistributeRequest struct {
	ParentPositionID string
	Allocations      []Allocation
}

// AssignRequest puts an object on a position with the amounts its owning service reports.
type AssignRequest struct {
	PositionID string
	ObjectType string
	ObjectRef  string
	Plan       decimal.Decimal
	Actual     decimal.Decimal
	Commitment decimal.Decimal
}

// CarriedPosition is one position's carried amount.
type CarriedPosition struct {
	PositionID string          `json:"positionId"`
	Code       string          `json:"code"`
	Amount     decimal.Decimal `json:"amount"`
}

// CarryForwardResponse reports a carry-forward of a program into the next year.
type CarryForwardResponse struct {
	ProgramID string            `json:"programId"`
	FromYear  int               `json:"fromYear"`
	ToYear    int               `json:"toYear"`
	Amount    decimal.Decimal   `json:"amount"`
	Positions []CarriedPosition `json:"positions"`
}

func programNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeProgramNotFound, "investment program not found")
}

const programColumns = `id::text, legal_entity_id::text, code, name, approval_year, program_type, currency, budget::text,
	(select coalesce(sum(pp.budget), 0)::text from program_position pp where pp.tenant_id = p.tenant_id and pp.program_id = p.id and pp.parent_id is null),
	status, version, created_at`

func scanProgram(row pgx.Row) (Program, error) {
	var pr Program
	var budget, distributed string
	var created time.Time
	err := row.Scan(&pr.ID, &pr.LegalEntityID, &pr.Code, &pr.Name, &pr.ApprovalYear, &pr.ProgramType, &pr.Currency, &budget, &distributed,
		&pr.Status, &pr.Version, &created)
	pr.Budget, pr.Distributed, pr.CreatedAt = parseDec(budget), parseDec(distributed), stamp(created)
	return pr, err
}

// loadProgram reads a program of the tenant; lock takes FOR UPDATE on its row so
// every command on one program is serialized.
func loadProgram(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Program, error) {
	q := `select ` + programColumns + ` from investment_program p where p.tenant_id = $1 and p.id = $2`
	if lock {
		q += ` for update of p`
	}
	pr, err := scanProgram(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return pr, programNotFound()
	}
	return pr, err
}

func bumpProgram(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	_, err := tx.Exec(ctx, `update investment_program set version = version + 1 where tenant_id = $1 and id = $2`, tenantID, id)
	return err
}

// CreateProgram stores a program; the code is unique per legal entity and approval year.
func (s *Postgres) CreateProgram(ctx context.Context, key, hash string, in NewProgram) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var id string
		err := tx.QueryRow(ctx, `insert into investment_program (tenant_id, id, legal_entity_id, code, name, approval_year, currency, budget)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, legal_entity_id, code, approval_year) do nothing returning id::text`,
			p.TenantID, ids.New(), in.LegalEntityID, in.Code, in.Name, in.ApprovalYear, in.Currency, in.Budget.StringFixed(4)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, httpx.NewProblem(http.StatusConflict, CodeProgramDuplicate, "a program with this code already exists for the approval year")
		}
		if err != nil {
			return 0, nil, err
		}
		view, err := loadView(ctx, tx, p.TenantID, id)
		return http.StatusCreated, view, err
	})
}

// ListPrograms lists programs, newest approval year first. An empty
// legalEntityID lists every legal entity in the caller's scope.
func (s *Postgres) ListPrograms(ctx context.Context, legalEntityID string) ([]Program, error) {
	out := []Program{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+programColumns+` from investment_program p
			where p.tenant_id = $1 and ($2 = '' or p.legal_entity_id::text = $2)
			order by p.approval_year desc, p.code limit $3`, tenantID, legalEntityID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			pr, err := scanProgram(rows)
			if err != nil {
				return err
			}
			out = append(out, pr)
		}
		return rows.Err()
	})
	return out, err
}

// GetProgram returns a program with its rolled-up positions, or BUD-0030.
func (s *Postgres) GetProgram(ctx context.Context, id string) (ProgramView, error) {
	var view ProgramView
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		view, err = loadView(ctx, tx, tenantID, id)
		return err
	})
	return view, err
}

// DistributePositions sets child budgets under one parent, creating positions
// for new codes, so that the children never exceed the parent (BR-38.15). The
// program row is locked first, so concurrent distributions of the program
// serialize and the invariant holds for every committed state. The response is
// the program after the change.
func (s *Postgres) DistributePositions(ctx context.Context, key, hash, programID string, req DistributeRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		prog, err := loadProgram(ctx, tx, p.TenantID, programID, true)
		if err != nil {
			return 0, nil, err
		}
		positions, err := loadPositions(ctx, tx, p.TenantID, programID)
		if err != nil {
			return 0, nil, err
		}
		parentBudget, parentLevel := prog.Budget, 0
		if req.ParentPositionID != "" {
			parent, ok := findPosition(positions, req.ParentPositionID)
			if !ok {
				return 0, nil, httpx.NewProblem(http.StatusNotFound, domain.CodePositionNotFound, "parent position not found in this program")
			}
			parentBudget, parentLevel = parent.Budget, parent.Level
		}
		byCode := make(map[string]Position, len(positions))
		var kids []domain.Child
		var created []Position
		for _, pos := range positions {
			byCode[pos.Code] = pos
			if pos.ParentID == req.ParentPositionID {
				kids = append(kids, domain.Child{ID: pos.ID, Budget: pos.Budget, Distributed: pos.Distributed})
			}
		}
		categories := map[string]string{}
		for _, a := range req.Allocations {
			pos, ok := byCode[a.PositionCode]
			switch {
			case ok && pos.ParentID != req.ParentPositionID:
				return 0, nil, httpx.NewProblem(http.StatusConflict, CodePositionCodeInUse,
					fmt.Sprintf("position %s already exists under another parent in this program", a.PositionCode))
			case !ok:
				if err := domain.ValidateLevel(parentLevel + 1); err != nil {
					return 0, nil, domainProblem(err)
				}
				category := a.Category
				if category == "" {
					category = domain.CategoryCapex
				}
				pos = Position{ID: ids.New(), ProgramID: programID, ParentID: req.ParentPositionID, Code: a.PositionCode, Level: parentLevel + 1, Category: category}
				byCode[a.PositionCode] = pos
				created = append(created, pos)
				kids = append(kids, domain.Child{ID: pos.ID})
			}
			if a.Category != "" {
				categories[pos.ID] = a.Category
			}
			if kids, err = domain.Distribute(parentBudget, kids, pos.ID, a.Budget); err != nil {
				return 0, nil, domainProblem(err)
			}
		}
		for _, c := range created {
			if _, err := tx.Exec(ctx, `insert into program_position (tenant_id, id, legal_entity_id, program_id, parent_id, code, level, category)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, c.ID, prog.LegalEntityID, programID, nullIfEmpty(c.ParentID), c.Code, c.Level, c.Category); err != nil {
				return 0, nil, err
			}
		}
		for _, k := range kids {
			if _, err := tx.Exec(ctx, `update program_position set budget = $3, category = coalesce($4::text, category) where tenant_id = $1 and id = $2`,
				p.TenantID, k.ID, k.Budget.StringFixed(4), nullIfEmpty(categories[k.ID])); err != nil {
				return 0, nil, err
			}
		}
		if req.ParentPositionID != "" {
			if _, err := tx.Exec(ctx, `update program_position set distributed = $3 where tenant_id = $1 and id = $2`,
				p.TenantID, req.ParentPositionID, domain.Total(kids).StringFixed(4)); err != nil {
				return 0, nil, err
			}
		}
		if err := bumpProgram(ctx, tx, p.TenantID, programID); err != nil {
			return 0, nil, err
		}
		view, err := loadView(ctx, tx, p.TenantID, programID)
		return http.StatusOK, view, err
	})
}

// AssignObject puts a capex request, asset under construction or project on a
// position of the program with its reported plan, actual and commitment. A
// re-assignment of the same object replaces its amounts and may move it to
// another position. The positions' own totals are recomputed from their
// assignments in the same transaction.
func (s *Postgres) AssignObject(ctx context.Context, key, hash, programID string, req AssignRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		prog, err := loadProgram(ctx, tx, p.TenantID, programID, true)
		if err != nil {
			return 0, nil, err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from program_position where tenant_id = $1 and program_id = $2 and id = $3)`,
			p.TenantID, programID, req.PositionID).Scan(&exists); err != nil {
			return 0, nil, err
		}
		if !exists {
			return 0, nil, httpx.NewProblem(http.StatusNotFound, domain.CodePositionNotFound, "position not found in this program")
		}
		var previous string
		err = tx.QueryRow(ctx, `select position_id::text from program_assignment where tenant_id = $1 and program_id = $2 and object_type = $3 and object_ref = $4`,
			p.TenantID, programID, req.ObjectType, req.ObjectRef).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `insert into program_assignment (tenant_id, id, legal_entity_id, program_id, position_id, object_type, object_ref, plan, actual, commitment)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			on conflict (tenant_id, program_id, object_type, object_ref) do update
			set position_id = excluded.position_id, plan = excluded.plan, actual = excluded.actual, commitment = excluded.commitment, updated_at = now()`,
			p.TenantID, ids.New(), prog.LegalEntityID, programID, req.PositionID, req.ObjectType, req.ObjectRef,
			req.Plan.StringFixed(4), req.Actual.StringFixed(4), req.Commitment.StringFixed(4)); err != nil {
			return 0, nil, err
		}
		for _, id := range uniqueNonEmpty(req.PositionID, previous) {
			if _, err := tx.Exec(ctx, `update program_position pp set
				plan = coalesce((select sum(a.plan) from program_assignment a where a.tenant_id = pp.tenant_id and a.position_id = pp.id), 0),
				actual = coalesce((select sum(a.actual) from program_assignment a where a.tenant_id = pp.tenant_id and a.position_id = pp.id), 0),
				commitment = coalesce((select sum(a.commitment) from program_assignment a where a.tenant_id = pp.tenant_id and a.position_id = pp.id), 0)
				where pp.tenant_id = $1 and pp.id = $2`, p.TenantID, id); err != nil {
				return 0, nil, err
			}
		}
		if err := bumpProgram(ctx, tx, p.TenantID, programID); err != nil {
			return 0, nil, err
		}
		view, err := loadView(ctx, tx, p.TenantID, programID)
		return http.StatusOK, view, err
	})
}

// CarryForward records each position's unused budget for the next approval year
// (FR-38.21): its own undistributed slice less the actuals and commitments of its
// own assignments, so no money is counted twice across levels. Each position is
// carried once (BR-38.15); a rerun carries only positions not yet carried and is
// refused with BUD-0021 when there are none. The program row is locked so the
// figures are consistent, and the carriedforward event goes through the outbox.
func (s *Postgres) CarryForward(ctx context.Context, key, hash, programID string, toYear int) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		prog, err := loadProgram(ctx, tx, p.TenantID, programID, true)
		if err != nil {
			return 0, nil, err
		}
		if toYear != prog.ApprovalYear+1 {
			return 0, nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeCarryForwardYear,
				fmt.Sprintf("unused budget of approval year %d carries forward to %d only", prog.ApprovalYear, prog.ApprovalYear+1))
		}
		positions, err := loadPositions(ctx, tx, p.TenantID, programID)
		if err != nil {
			return 0, nil, err
		}
		resp := CarryForwardResponse{ProgramID: programID, FromYear: prog.ApprovalYear, ToYear: toYear, Amount: decimal.Zero, Positions: []CarriedPosition{}}
		already := 0
		for _, pos := range positions {
			if pos.CarriedForward.Sign() > 0 {
				already++
				continue
			}
			amount := domain.Unused(pos.Budget, pos.Distributed, pos.own.Actual, pos.own.Commitment)
			if amount.IsZero() {
				continue
			}
			if _, err := tx.Exec(ctx, `insert into investment_carry_forward (tenant_id, id, legal_entity_id, program_id, position_id, from_year, to_year, amount)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, ids.New(), prog.LegalEntityID, programID, pos.ID, prog.ApprovalYear, toYear, amount.StringFixed(4)); err != nil {
				return 0, nil, err
			}
			resp.Amount = resp.Amount.Add(amount)
			resp.Positions = append(resp.Positions, CarriedPosition{PositionID: pos.ID, Code: pos.Code, Amount: amount})
		}
		if len(resp.Positions) == 0 {
			if already > 0 {
				return 0, nil, httpx.NewProblem(http.StatusConflict, domain.CodeCarryForwardRepeated, "unused budget of this program was already carried forward")
			}
			return http.StatusOK, resp, nil
		}
		data := budgetevents.ProgramCarriedForwardData{ProgramID: programID, FromYear: resp.FromYear, ToYear: resp.ToYear, Amount: resp.Amount.String(),
			Positions: make([]budgetevents.CarriedPositionData, 0, len(resp.Positions))}
		for _, c := range resp.Positions {
			data.Positions = append(data.Positions, budgetevents.CarriedPositionData{PositionID: c.PositionID, Code: c.Code, Amount: c.Amount.String()})
		}
		if err := enqueue(ctx, tx, "investment_program", programID, budgetevents.ProgramCarriedForward, p.TenantID, data); err != nil {
			return 0, nil, err
		}
		if err := bumpProgram(ctx, tx, p.TenantID, programID); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, resp, nil
	})
}

func findPosition(positions []Position, id string) (Position, bool) {
	for _, pos := range positions {
		if pos.ID == id {
			return pos, true
		}
	}
	return Position{}, false
}

func uniqueNonEmpty(a, b string) []string {
	switch {
	case b == "" || b == a:
		return []string{a}
	default:
		return []string{a, b}
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// loadPositions reads every position of a program, parents before children,
// with its own (unrolled) amounts and any carried-forward amount.
func loadPositions(ctx context.Context, tx pgx.Tx, tenantID, programID string) ([]Position, error) {
	rows, err := tx.Query(ctx, `select pp.id::text, pp.program_id::text, coalesce(pp.parent_id::text, ''), pp.code, pp.level, pp.category,
			pp.budget::text, pp.distributed::text, pp.plan::text, pp.actual::text, pp.commitment::text, coalesce(cf.amount, 0)::text
		from program_position pp
		left join investment_carry_forward cf on cf.tenant_id = pp.tenant_id and cf.position_id = pp.id
		where pp.tenant_id = $1 and pp.program_id = $2 order by pp.level, pp.code`, tenantID, programID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Position{}
	for rows.Next() {
		var pos Position
		var budget, distributed, plan, actual, commitment, carried string
		if err := rows.Scan(&pos.ID, &pos.ProgramID, &pos.ParentID, &pos.Code, &pos.Level, &pos.Category,
			&budget, &distributed, &plan, &actual, &commitment, &carried); err != nil {
			return nil, err
		}
		pos.Budget, pos.Distributed, pos.CarriedForward = parseDec(budget), parseDec(distributed), parseDec(carried)
		pos.own = domain.Amounts{Plan: parseDec(plan), Actual: parseDec(actual), Commitment: parseDec(commitment)}
		pos.Assignments = []Assignment{}
		out = append(out, pos)
	}
	return out, rows.Err()
}

// loadView builds the program view: positions with their assignments and the
// rollup of plan, actual and commitment over each subtree.
func loadView(ctx context.Context, tx pgx.Tx, tenantID, programID string) (ProgramView, error) {
	prog, err := loadProgram(ctx, tx, tenantID, programID, false)
	if err != nil {
		return ProgramView{}, err
	}
	positions, err := loadPositions(ctx, tx, tenantID, programID)
	if err != nil {
		return ProgramView{}, err
	}
	index := make(map[string]int, len(positions))
	nodes := make([]domain.Node, len(positions))
	for i, pos := range positions {
		index[pos.ID] = i
		nodes[i] = domain.Node{ID: pos.ID, ParentID: pos.ParentID, Own: pos.own}
	}
	rows, err := tx.Query(ctx, `select id::text, position_id::text, object_type, object_ref, plan::text, actual::text, commitment::text, updated_at
		from program_assignment where tenant_id = $1 and program_id = $2 order by object_type, object_ref`, tenantID, programID)
	if err != nil {
		return ProgramView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Assignment
		var plan, actual, commitment string
		var updated time.Time
		if err := rows.Scan(&a.ID, &a.PositionID, &a.ObjectType, &a.ObjectRef, &plan, &actual, &commitment, &updated); err != nil {
			return ProgramView{}, err
		}
		a.Plan, a.Actual, a.Commitment, a.UpdatedAt = parseDec(plan), parseDec(actual), parseDec(commitment), stamp(updated)
		if i, ok := index[a.PositionID]; ok {
			positions[i].Assignments = append(positions[i].Assignments, a)
		}
	}
	if err := rows.Err(); err != nil {
		return ProgramView{}, err
	}
	rolled := domain.Rollup(nodes)
	view := ProgramView{Program: prog, Plan: decimal.Zero, Actual: decimal.Zero, Commitment: decimal.Zero, CarriedForward: decimal.Zero, Positions: positions}
	for i := range positions {
		pos := &positions[i]
		r := rolled[pos.ID]
		pos.Plan, pos.Actual, pos.Commitment = r.Plan, r.Actual, r.Commitment
		pos.Undistributed = pos.Budget.Sub(pos.Distributed)
		pos.Available = pos.Budget.Sub(r.Actual).Sub(r.Commitment)
		view.Plan, view.Actual, view.Commitment = view.Plan.Add(pos.own.Plan), view.Actual.Add(pos.own.Actual), view.Commitment.Add(pos.own.Commitment)
		view.CarriedForward = view.CarriedForward.Add(pos.CarriedForward)
	}
	view.Available = prog.Budget.Sub(view.Actual).Sub(view.Commitment)
	return view, nil
}
