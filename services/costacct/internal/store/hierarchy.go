package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	ccevents "github.com/BenjaminSRussell/passage/services/costacct/internal/events"
)

// Problem codes of cost object hierarchies (FR-38.18), listed in the README.
const (
	CodeHierarchyNotFound    = "CST-0021"
	CodeNodeNotFound         = "CST-0022"
	CodeNodeNotCollecting    = "CST-0023"
	CodeNothingToDistribute  = "CST-0024"
	CodeNodeExists           = "CST-0025"
	CodeInvalidDistribution  = "CST-0026"
	CodeHierarchyRunNotFound = "CST-0027"
	CodeCollectionBelowZero  = "CST-0028"
	CodeUndistributed        = "CO-0645"
	hierarchyCodeOnce        = "cost_object_hierarchy_code_once"
	nodeObjectOnce           = "cost_hierarchy_node_object_once"
)

// Hierarchy is a cost object hierarchy of one legal entity (FR-38.18).
type Hierarchy struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	NodeCount     int             `json:"nodeCount"`
	CreatedAt     string          `json:"createdAt"`
	Nodes         []HierarchyNode `json:"nodes,omitempty"`
}

// HierarchyNode places one cost object in a hierarchy. Only a collecting node
// takes costs that no lower object can carry, and only it has a basis.
type HierarchyNode struct {
	ID                string `json:"id"`
	HierarchyID       string `json:"hierarchyId"`
	ParentNodeID      string `json:"parentNodeId,omitempty"`
	CostObjectID      string `json:"costObjectId"`
	Collects          bool   `json:"collects"`
	DistributionBasis string `json:"distributionBasis,omitempty"`
	CreatedAt         string `json:"createdAt"`
	legalEntityID     string
}

// CollectRequest collects a cost on a node (SAP KKP4 collective entry). A
// negative amount corrects an earlier collection and may not take the node's
// balance for the element, period and currency below zero.
type CollectRequest struct {
	NodeID        string
	Period        string
	Currency      string
	CostElementID string
	Amount        decimal.Decimal
}

// Collection is the cost entry a collection created and the node's new balance.
type Collection struct {
	EntryID       string          `json:"entryId"`
	NodeID        string          `json:"nodeId"`
	CostObjectID  string          `json:"costObjectId"`
	CostElementID string          `json:"costElementId"`
	Period        string          `json:"period"`
	Currency      string          `json:"currency"`
	Amount        decimal.Decimal `json:"amount"`
	Behavior      string          `json:"behavior"`
	Balance       decimal.Decimal `json:"balance"`
}

// DistributionRequest distributes what a collecting node holds for one element,
// period and currency to some or all of its child nodes.
type DistributionRequest struct {
	NodeID        string
	Period        string
	Currency      string
	CostElementID string
	Children      []domain.HierarchyChild
}

// DistributionShare is what one child node received.
type DistributionShare struct {
	NodeID       string          `json:"nodeId"`
	CostObjectID string          `json:"costObjectId"`
	Basis        decimal.Decimal `json:"basis"`
	Amount       decimal.Decimal `json:"amount"`
}

// DistributionRun is a stored hierarchy distribution. Distributed + Residual
// = Amount; a positive residual stays on the node and raises CO-0645.
type DistributionRun struct {
	ID            string              `json:"id"`
	LegalEntityID string              `json:"legalEntityId"`
	HierarchyID   string              `json:"hierarchyId"`
	NodeID        string              `json:"nodeId"`
	CostElementID string              `json:"costElementId"`
	Period        string              `json:"period"`
	Currency      string              `json:"currency"`
	Basis         string              `json:"basis"`
	Amount        decimal.Decimal     `json:"amount"`
	Distributed   decimal.Decimal     `json:"distributed"`
	Residual      decimal.Decimal     `json:"residual"`
	Shares        []DistributionShare `json:"shares"`
	Warnings      []Warning           `json:"warnings"`
	Status        string              `json:"status"`
	CreatedAt     string              `json:"createdAt"`
}

// NodeBalance is what a node holds for one element and currency in a period:
// its collections, plus what it received from its parent, less what it
// distributed.
type NodeBalance struct {
	NodeID        string          `json:"nodeId"`
	CostElementID string          `json:"costElementId"`
	Currency      string          `json:"currency"`
	Amount        decimal.Decimal `json:"amount"`
}

// CreateHierarchy stores a hierarchy; codes are unique per legal entity.
func (s *Postgres) CreateHierarchy(ctx context.Context, key, hash string, in Hierarchy) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from cost_object_hierarchy where tenant_id = $1 and legal_entity_id = $2 and code = $3)`,
			p.TenantID, in.LegalEntityID, in.Code).Scan(&exists); err != nil {
			return 0, nil, err
		}
		if exists {
			return 0, nil, problem(http.StatusConflict, CodeDuplicateCode, "a hierarchy with this code already exists")
		}
		created := time.Now().UTC()
		h := Hierarchy{ID: ids.New(), LegalEntityID: in.LegalEntityID, Code: in.Code, Name: in.Name, Type: in.Type, CreatedAt: stamp(created)}
		if _, err := tx.Exec(ctx, `insert into cost_object_hierarchy (tenant_id, id, legal_entity_id, code, name, type, created_at)
			values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, h.ID, h.LegalEntityID, h.Code, h.Name, h.Type, created); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, h, nil
	})
}

// CreateHierarchyNode adds a node for a cost object of the hierarchy's legal
// entity. A cost object appears once per hierarchy; the parent is a node of
// the same hierarchy, so no cycle can form. Only a Collective hierarchy has
// collecting nodes (FR-38.18).
func (s *Postgres) CreateHierarchyNode(ctx context.Context, key, hash string, in HierarchyNode) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var le, kind string
		err := tx.QueryRow(ctx, `select legal_entity_id::text, type from cost_object_hierarchy where tenant_id = $1 and id = $2`,
			p.TenantID, in.HierarchyID).Scan(&le, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, problem(http.StatusNotFound, CodeHierarchyNotFound, "cost object hierarchy not found")
		}
		if err != nil {
			return 0, nil, err
		}
		if in.Collects && kind != domain.HierarchyCollective {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeNodeNotCollecting, "only a Collective hierarchy has collecting nodes; this one is "+kind)
		}
		if err := requireObjects(ctx, tx, p.TenantID, le, []string{in.CostObjectID}); err != nil {
			return 0, nil, err
		}
		if in.ParentNodeID != "" {
			var found bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from cost_hierarchy_node where tenant_id = $1 and hierarchy_id = $2 and id = $3)`,
				p.TenantID, in.HierarchyID, in.ParentNodeID).Scan(&found); err != nil {
				return 0, nil, err
			}
			if !found {
				return 0, nil, problem(http.StatusUnprocessableEntity, CodeNodeNotFound, "the parent node is not a node of this hierarchy")
			}
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from cost_hierarchy_node where tenant_id = $1 and hierarchy_id = $2 and cost_object_id = $3)`,
			p.TenantID, in.HierarchyID, in.CostObjectID).Scan(&exists); err != nil {
			return 0, nil, err
		}
		if exists {
			return 0, nil, problem(http.StatusConflict, CodeNodeExists, "this cost object already has a node in the hierarchy")
		}
		created := time.Now().UTC()
		n := HierarchyNode{ID: ids.New(), HierarchyID: in.HierarchyID, ParentNodeID: in.ParentNodeID, CostObjectID: in.CostObjectID,
			Collects: in.Collects, DistributionBasis: in.DistributionBasis, CreatedAt: stamp(created)}
		if _, err := tx.Exec(ctx, `insert into cost_hierarchy_node (tenant_id, id, legal_entity_id, hierarchy_id, parent_node_id, cost_object_id,
			collects, distribution_basis, created_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.TenantID, n.ID, le, n.HierarchyID, nullIfEmpty(n.ParentNodeID), n.CostObjectID, n.Collects, nullIfEmpty(n.DistributionBasis), created); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, n, nil
	})
}

// lockNode reads one node with its legal entity, holding
// the row lock so collections and distributions of the node are serialised.
func lockNode(ctx context.Context, tx pgx.Tx, tenantID, id string) (HierarchyNode, error) {
	var n HierarchyNode
	var created time.Time
	err := tx.QueryRow(ctx, `select id::text, hierarchy_id::text, coalesce(parent_node_id::text, ''), cost_object_id::text, collects,
			coalesce(distribution_basis, ''), created_at, legal_entity_id::text
		from cost_hierarchy_node where tenant_id = $1 and id = $2 for update`, tenantID, id).
		Scan(&n.ID, &n.HierarchyID, &n.ParentNodeID, &n.CostObjectID, &n.Collects, &n.DistributionBasis, &created, &n.legalEntityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, problem(http.StatusNotFound, CodeNodeNotFound, "hierarchy node not found")
	}
	n.CreatedAt = stamp(created)
	return n, err
}

// nodeBalance sums the entries a node owns for one element, period and currency.
func nodeBalance(ctx context.Context, tx pgx.Tx, tenantID, nodeID, elementID, period, currency string) (decimal.Decimal, error) {
	var s string
	err := tx.QueryRow(ctx, `select coalesce(sum(amount), 0)::text from cost_entry where tenant_id = $1 and hierarchy_node_id = $2
		and cost_element_id = $3 and period_key = $4 and currency = $5`, tenantID, nodeID, elementID, period, currency).Scan(&s)
	return parseDec(s), err
}

// requireElement checks that a cost element exists in the legal entity.
func requireElement(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from cost_element where tenant_id = $1 and legal_entity_id = $2 and id = $3)`,
		tenantID, legalEntityID, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return problem(http.StatusUnprocessableEntity, CodeSecondaryNotFound, "cost element not found in the node's legal entity")
	}
	return nil
}

// postNodeEntry writes one entry owned by a node and enqueues costentry.created.
func postNodeEntry(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, node HierarchyNode, elementID, period, currency string,
	amount decimal.Decimal, origin, behavior, runID string) (string, error) {
	entryID := ids.New()
	source := entryID
	if runID != "" {
		source = runID
	}
	if _, err := tx.Exec(ctx, `insert into cost_entry (tenant_id, id, legal_entity_id, cost_object_id, cost_element_id, period_key, currency,
		amount, origin, behavior, source_id, hierarchy_node_id, hierarchy_run_id) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		tenantID, entryID, legalEntityID, node.CostObjectID, elementID, period, currency, amount.StringFixed(4), origin, behavior, source,
		node.ID, nullIfEmpty(runID)); err != nil {
		return "", err
	}
	return entryID, enqueue(ctx, tx, "cost_entry", entryID, ccevents.CostEntryCreated, tenantID, ccevents.CostEntryCreatedData{
		EntryID: entryID, CostObjectID: node.CostObjectID, ElementID: elementID, Period: period, Amount: amount, Origin: origin,
	})
}

// CollectAtNode posts a cost to a collecting node's cost object (origin Manual),
// classified Fixed or Variable by the FR-38.16 rules like a journal line.
func (s *Postgres) CollectAtNode(ctx context.Context, key, hash string, in CollectRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		node, err := lockNode(ctx, tx, p.TenantID, in.NodeID)
		if err != nil {
			return 0, nil, err
		}
		if !node.Collects {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeNodeNotCollecting, "this hierarchy node does not collect costs")
		}
		if err := requireElement(ctx, tx, p.TenantID, node.legalEntityID, in.CostElementID); err != nil {
			return 0, nil, err
		}
		balance, err := nodeBalance(ctx, tx, p.TenantID, node.ID, in.CostElementID, in.Period, in.Currency)
		if err != nil {
			return 0, nil, err
		}
		balance = balance.Add(in.Amount)
		if balance.IsNegative() {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeCollectionBelowZero,
				fmt.Sprintf("the correction would leave the node at %s for the element and period", balance.StringFixed(2)))
		}
		classify, err := behaviorClassifier(ctx, tx, p.TenantID, node.legalEntityID, []string{node.CostObjectID})
		if err != nil {
			return 0, nil, err
		}
		behavior := classify(in.CostElementID, node.CostObjectID, in.Period)
		entryID, err := postNodeEntry(ctx, tx, p.TenantID, node.legalEntityID, node, in.CostElementID, in.Period, in.Currency, in.Amount, "Manual", behavior, "")
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, Collection{EntryID: entryID, NodeID: node.ID, CostObjectID: node.CostObjectID, CostElementID: in.CostElementID,
			Period: in.Period, Currency: in.Currency, Amount: in.Amount, Behavior: behavior, Balance: balance}, nil
	})
}

// RunHierarchyDistribution distributes what a collecting node holds for one
// element, period and currency to its child nodes by the node's basis
// (FR-38.18). The node is credited with the distributed total and each child
// debited its share on the same element (origin HierarchyDistribution), so the
// run nets to zero and the GL reconciliation is unchanged. A residual stays on
// the node with warning CO-0645; a later run may distribute it.
func (s *Postgres) RunHierarchyDistribution(ctx context.Context, key, hash string, in DistributionRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		node, err := lockNode(ctx, tx, p.TenantID, in.NodeID)
		if err != nil {
			return 0, nil, err
		}
		if !node.Collects {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeNodeNotCollecting, "this hierarchy node does not collect costs")
		}
		if err := requireElement(ctx, tx, p.TenantID, node.legalEntityID, in.CostElementID); err != nil {
			return 0, nil, err
		}
		childIDs := make([]string, len(in.Children))
		for i, c := range in.Children {
			childIDs[i] = c.NodeID
		}
		kids := map[string]HierarchyNode{}
		rows, err := tx.Query(ctx, `select id::text, cost_object_id::text from cost_hierarchy_node
			where tenant_id = $1 and hierarchy_id = $2 and parent_node_id = $3 and id = any($4::uuid[])`, p.TenantID, node.HierarchyID, node.ID, childIDs)
		if err != nil {
			return 0, nil, err
		}
		for rows.Next() {
			var k HierarchyNode
			if err := rows.Scan(&k.ID, &k.CostObjectID); err != nil {
				rows.Close()
				return 0, nil, err
			}
			kids[k.ID] = k
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, nil, err
		}
		if len(kids) != len(childIDs) {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeInvalidDistribution, "every receiver must be a child node of the distributing node")
		}
		held, err := nodeBalance(ctx, tx, p.TenantID, node.ID, in.CostElementID, in.Period, in.Currency)
		if err != nil {
			return 0, nil, err
		}
		if !held.IsPositive() {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeNothingToDistribute, "the node holds no cost for this element, period and currency")
		}
		split, err := domain.SplitHierarchy(held, node.DistributionBasis, in.Children, money.MinorUnits(in.Currency))
		if errors.Is(err, domain.ErrConservation) {
			return 0, nil, problem(http.StatusInternalServerError, CodeConservation, "hierarchy distribution totals differ; the run was rolled back")
		}
		if err != nil {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeInvalidDistribution, err.Error())
		}
		created := time.Now().UTC()
		run := DistributionRun{ID: ids.New(), LegalEntityID: node.legalEntityID, HierarchyID: node.HierarchyID, NodeID: node.ID,
			CostElementID: in.CostElementID, Period: in.Period, Currency: in.Currency, Basis: node.DistributionBasis, Amount: held,
			Distributed: split.Distributed, Residual: split.Residual, Shares: make([]DistributionShare, len(split.Shares)), Status: "Completed",
			CreatedAt: stamp(created)}
		for i, sh := range split.Shares {
			run.Shares[i] = DistributionShare{NodeID: sh.NodeID, CostObjectID: kids[sh.NodeID].CostObjectID, Basis: sh.Basis, Amount: sh.Amount}
		}
		run.Warnings = distributionWarnings(run)
		shares, err := json.Marshal(run.Shares)
		if err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `insert into hierarchy_distribution_run (tenant_id, id, legal_entity_id, hierarchy_id, node_id, period_key, currency,
			cost_element_id, basis, amount, distributed, residual, shares, status, created_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'Completed', $14)`,
			p.TenantID, run.ID, run.LegalEntityID, run.HierarchyID, run.NodeID, run.Period, run.Currency, run.CostElementID, run.Basis,
			held.StringFixed(4), split.Distributed.StringFixed(4), split.Residual.StringFixed(4), shares, created); err != nil {
			return 0, nil, err
		}
		if split.Distributed.IsPositive() {
			if _, err := postNodeEntry(ctx, tx, p.TenantID, run.LegalEntityID, node, run.CostElementID, run.Period, run.Currency,
				split.Distributed.Neg(), "HierarchyDistribution", "Unclassified", run.ID); err != nil {
				return 0, nil, err
			}
		}
		records := make([]ccevents.HierarchyShareRecord, 0, len(run.Shares))
		for _, sh := range run.Shares {
			if sh.Amount.IsZero() {
				continue
			}
			if _, err := postNodeEntry(ctx, tx, p.TenantID, run.LegalEntityID, kids[sh.NodeID], run.CostElementID, run.Period, run.Currency,
				sh.Amount, "HierarchyDistribution", "Unclassified", run.ID); err != nil {
				return 0, nil, err
			}
			records = append(records, ccevents.HierarchyShareRecord{NodeID: sh.NodeID, CostObjectID: sh.CostObjectID, Amount: sh.Amount})
		}
		if err := enqueue(ctx, tx, "hierarchy_distribution_run", run.ID, ccevents.HierarchyDistributed, p.TenantID, ccevents.HierarchyDistributedData{
			RunID: run.ID, LegalEntityID: run.LegalEntityID, HierarchyID: run.HierarchyID, NodeID: run.NodeID, CostElementID: run.CostElementID,
			Period: run.Period, Currency: run.Currency, Basis: run.Basis, Amount: held, Distributed: split.Distributed, Residual: split.Residual,
			Shares: records,
		}); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, run, nil
	})
}

// distributionWarnings raises CO-0645 when a run left cost on its node.
func distributionWarnings(r DistributionRun) []Warning {
	if !r.Residual.IsPositive() {
		return []Warning{}
	}
	return []Warning{{Code: CodeUndistributed, Detail: fmt.Sprintf("Hierarchy node %s keeps %s %s undistributed", r.NodeID, r.Residual.StringFixed(2), r.Currency)}}
}

const hierarchyColumns = `h.id::text, h.legal_entity_id::text, h.code, h.name, h.type, h.created_at,
	(select count(*) from cost_hierarchy_node n where n.tenant_id = h.tenant_id and n.hierarchy_id = h.id)`

func scanHierarchy(row pgx.Row) (Hierarchy, error) {
	var h Hierarchy
	var created time.Time
	if err := row.Scan(&h.ID, &h.LegalEntityID, &h.Code, &h.Name, &h.Type, &created, &h.NodeCount); err != nil {
		return h, err
	}
	h.CreatedAt = stamp(created)
	return h, nil
}

// ListHierarchies lists hierarchies by code; legalEntityID narrows it when set.
func (s *Postgres) ListHierarchies(ctx context.Context, legalEntityID string) ([]Hierarchy, error) {
	out := []Hierarchy{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+hierarchyColumns+` from cost_object_hierarchy h
			where h.tenant_id = $1 and ($2 = '' or h.legal_entity_id::text = $2) order by h.code limit $3`, tenantID, legalEntityID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			h, err := scanHierarchy(rows)
			if err != nil {
				return err
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

// GetHierarchy returns one hierarchy with its nodes, parents before children
// where they were created first, or CST-0021.
func (s *Postgres) GetHierarchy(ctx context.Context, id string) (Hierarchy, error) {
	var h Hierarchy
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		h, err = scanHierarchy(tx.QueryRow(ctx, `select `+hierarchyColumns+` from cost_object_hierarchy h where h.tenant_id = $1 and h.id = $2`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeHierarchyNotFound, "cost object hierarchy not found")
		}
		if err != nil {
			return err
		}
		h.Nodes = []HierarchyNode{}
		rows, err := tx.Query(ctx, `select id::text, hierarchy_id::text, coalesce(parent_node_id::text, ''), cost_object_id::text, collects,
			coalesce(distribution_basis, ''), created_at from cost_hierarchy_node where tenant_id = $1 and hierarchy_id = $2 order by created_at, id`,
			tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n HierarchyNode
			var created time.Time
			if err := rows.Scan(&n.ID, &n.HierarchyID, &n.ParentNodeID, &n.CostObjectID, &n.Collects, &n.DistributionBasis, &created); err != nil {
				return err
			}
			n.CreatedAt = stamp(created)
			h.Nodes = append(h.Nodes, n)
		}
		return rows.Err()
	})
	return h, err
}

// HierarchyBalances lists, for one period, the non-zero balance every node of
// the hierarchy holds per element and currency.
func (s *Postgres) HierarchyBalances(ctx context.Context, hierarchyID, period string) ([]NodeBalance, error) {
	out := []NodeBalance{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from cost_object_hierarchy where tenant_id = $1 and id = $2)`, tenantID, hierarchyID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeHierarchyNotFound, "cost object hierarchy not found")
		}
		rows, err := tx.Query(ctx, `select e.hierarchy_node_id::text, e.cost_element_id::text, e.currency, sum(e.amount)::text
			from cost_entry e join cost_hierarchy_node n on n.tenant_id = e.tenant_id and n.id = e.hierarchy_node_id
			where e.tenant_id = $1 and n.hierarchy_id = $2 and e.period_key = $3
			group by e.hierarchy_node_id, e.cost_element_id, e.currency having sum(e.amount) <> 0
			order by e.hierarchy_node_id, e.cost_element_id, e.currency`, tenantID, hierarchyID, period)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b NodeBalance
			var amount string
			if err := rows.Scan(&b.NodeID, &b.CostElementID, &b.Currency, &amount); err != nil {
				return err
			}
			b.Amount = parseDec(amount)
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

const distributionColumns = `id::text, legal_entity_id::text, hierarchy_id::text, node_id::text, cost_element_id::text, period_key, currency, basis,
	amount::text, distributed::text, residual::text, shares, status, created_at`

func scanDistribution(row pgx.Row) (DistributionRun, error) {
	var r DistributionRun
	var amount, distributed, residual string
	var shares []byte
	var created time.Time
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.HierarchyID, &r.NodeID, &r.CostElementID, &r.Period, &r.Currency, &r.Basis,
		&amount, &distributed, &residual, &shares, &r.Status, &created); err != nil {
		return r, err
	}
	r.Amount, r.Distributed, r.Residual, r.CreatedAt = parseDec(amount), parseDec(distributed), parseDec(residual), stamp(created)
	if err := json.Unmarshal(shares, &r.Shares); err != nil {
		return r, err
	}
	r.Warnings = distributionWarnings(r)
	return r, nil
}

// ListDistributionRuns lists distribution runs, newest period first, narrowed
// by legal entity and hierarchy when set.
func (s *Postgres) ListDistributionRuns(ctx context.Context, legalEntityID, hierarchyID string) ([]DistributionRun, error) {
	out := []DistributionRun{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+distributionColumns+` from hierarchy_distribution_run
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or hierarchy_id::text = $3)
			order by period_key desc, created_at desc limit $4`, tenantID, legalEntityID, hierarchyID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanDistribution(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetDistributionRun returns one distribution run, or CST-0027.
func (s *Postgres) GetDistributionRun(ctx context.Context, id string) (DistributionRun, error) {
	var r DistributionRun
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		r, err = scanDistribution(tx.QueryRow(ctx, `select `+distributionColumns+` from hierarchy_distribution_run where tenant_id = $1 and id = $2`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeHierarchyRunNotFound, "hierarchy distribution run not found")
		}
		return err
	})
	return r, err
}
