package domain

import (
	"slices"
	"strings"
)

// Reasons and codes for the row-level scope mirror (FR-46.7, BR-46.9).
const (
	ReasonNoScope = "NO_SCOPE"
	CodeUnmapped  = "ANA-0103"
)

// Scope is the legal-entity scope the caller's policy decision grants.
// Unrestricted means the caller holds every legal entity ("*").
type Scope struct {
	Unrestricted   bool
	LegalEntityIDs []string
}

// ScopeFromPrincipal maps the gateway's legal-entity header to a scope: no
// header means the whole tenant, one id narrows to that entity.
func ScopeFromPrincipal(legalEntityID string) Scope {
	if legalEntityID == "" {
		return Scope{Unrestricted: true}
	}
	return Scope{LegalEntityIDs: []string{legalEntityID}}
}

// Filter is a Cube queryRewrite member filter.
type Filter struct {
	Member   string   `json:"member"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

// modelScopeColumns names, per Cube model, the column that carries the legal
// entity. A model without an entry cannot be queried (SCOPE_UNMAPPED).
func modelScopeColumns(model string) (string, bool) {
	switch model {
	case "fin_journal_line", "sales_line", "prod_resource_shift":
		return "legal_entity_id", true
	}
	return "", false
}

// ScopeResult is the filter set for one model. When Reason is NO_SCOPE the
// query must not run: an empty filter list would otherwise mean "no filter".
type ScopeResult struct {
	Filters []Filter
	Reason  string
}

// CompileScope turns a scope into the filters every Cube query on model carries.
// Unknown models fail closed with ANA-0103; an empty scope yields NO_SCOPE.
func CompileScope(model string, s Scope) (ScopeResult, *Error) {
	column, ok := modelScopeColumns(model)
	if !ok {
		return ScopeResult{}, &Error{Code: CodeUnmapped, Status: 403, Detail: "model " + model + " has no security mapping"}
	}
	if s.Unrestricted {
		return ScopeResult{}, nil
	}
	if len(s.LegalEntityIDs) == 0 {
		return ScopeResult{Reason: ReasonNoScope}, nil
	}
	ids := slices.Clone(s.LegalEntityIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return ScopeResult{Filters: []Filter{{Member: model + "." + column, Operator: "equals", Values: ids}}}, nil
}

// ScopeWithinGrant reports whether every value in filters is one the scope
// grants. It backs the contract test that Cube results stay inside OLTP scope.
func ScopeWithinGrant(filters []Filter, s Scope) bool {
	if s.Unrestricted {
		return true
	}
	for _, f := range filters {
		for _, v := range f.Values {
			if !slices.ContainsFunc(s.LegalEntityIDs, func(id string) bool { return strings.EqualFold(id, v) }) {
				return false
			}
		}
	}
	return true
}
