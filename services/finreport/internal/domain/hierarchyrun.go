package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Slice-local catalog codes for statement runs over a hierarchy (FR-15.4,
// FR-15.13); they are not in the PRD catalog yet.
const (
	// CodeHierarchyRun refuses a run that cannot use the hierarchy as its rows.
	CodeHierarchyRun = "REPORT-0033"
	// CodeHierarchyRunUnassigned is the warning stored with a run whose
	// hierarchy leaves members without a node; they show in the Unassigned row.
	CodeHierarchyRunUnassigned = "REPORT-0034"
)

// HierarchyRunOptions are the choices of a run that takes its rows from a
// hierarchy. The definition supplies the columns, the kind and the flags; its
// own rows are replaced by one row per hierarchy node.
type HierarchyRunOptions struct {
	// NormalBalance is how the rows present amounts: debit (default) or credit.
	NormalBalance string
	// AccountFrom and AccountTo narrow a dimension hierarchy to a range of
	// accounts (such as the P&L accounts of a cost center report). Both or none.
	AccountFrom string
	AccountTo   string
}

// HierarchyObserved lists what the read model holds for the hierarchy's
// subject in the ledger: the distinct accounts, or the distinct values of the
// dimension attribute. HasBlank is true when some balances carry no value for
// the dimension attribute; they are shown in the Unassigned row.
type HierarchyObserved struct {
	Members  []string
	HasBlank bool
}

// HierarchyRows is the expanded definition of a hierarchy run and the members
// the hierarchy did not place (sorted; a blank dimension value is not listed).
type HierarchyRows struct {
	Definition Definition
	Unassigned []string
}

// ExpandHierarchy builds the definition a run evaluates: one row per node of
// the published hierarchy h, depth first in the order the nodes are declared.
// A leaf sums its members, a parent totals its children, an "Unassigned" row
// comes last for members no leaf claims, and a grand total row closes the
// statement, so a trial balance still sums to zero. Empty nodes are left out.
//
// stored is the membership materialized when h was published; it wins for
// members it knows (it holds the attributes the rules may have used). Members
// first seen in the ledger are placed by the rules on their key. The subjects
// main_account and dimension_value can be run; categories need master data
// this service does not have yet.
func ExpandHierarchy(base Definition, h Hierarchy, stored Membership, seen HierarchyObserved, opts HierarchyRunOptions) (HierarchyRows, error) {
	bad := func(format string, args ...any) (HierarchyRows, error) {
		return HierarchyRows{}, errf(CodeHierarchyRun, format, args...)
	}
	if base.Kind == KindCashFlow {
		return bad("a cash flow definition cannot take its rows from a hierarchy.")
	}
	if h.Subject == SubjectCategory {
		return bad("hierarchy %s classifies main account categories; only main account and dimension value hierarchies can drive a run.", h.Code)
	}
	if opts.NormalBalance == "" {
		opts.NormalBalance = "debit"
	}
	if opts.NormalBalance != "debit" && opts.NormalBalance != "credit" {
		return bad("normalBalance must be debit or credit.")
	}
	if (opts.AccountFrom == "") != (opts.AccountTo == "") || opts.AccountFrom > opts.AccountTo {
		return bad("accountFrom and accountTo must both be set, with accountFrom not above accountTo.")
	}
	if h.Subject == SubjectAccount && opts.AccountFrom != "" {
		return bad("accountFrom and accountTo narrow dimension hierarchies; an account hierarchy already lists the accounts.")
	}

	leaves := map[string]bool{}
	children := map[string][]string{}
	roots := []string{}
	byCode := map[string]HierarchyNode{}
	for _, n := range h.Nodes {
		byCode[n.Code] = n
		if n.Parent == "" {
			roots = append(roots, n.Code)
		} else {
			children[n.Parent] = append(children[n.Parent], n.Code)
		}
	}
	for _, n := range h.Nodes {
		if len(children[n.Code]) == 0 {
			leaves[n.Code] = true
		}
	}

	placed := map[string][]string{}
	var unassigned []string
	var fresh []Member
	stillUnassigned := map[string]bool{}
	for _, id := range stored.Unassigned {
		stillUnassigned[id] = true
	}
	members := append([]string(nil), seen.Members...)
	sort.Strings(members)
	for _, id := range members {
		if leaf, ok := stored.Assigned[id]; ok && leaves[leaf] {
			placed[leaf] = append(placed[leaf], id)
		} else if stillUnassigned[id] {
			unassigned = append(unassigned, id)
		} else {
			fresh = append(fresh, Member{ID: id, Key: id})
		}
	}
	if len(fresh) > 0 {
		m, err := h.Materialize(fresh)
		if err != nil {
			return HierarchyRows{}, err
		}
		for id, leaf := range m.Assigned {
			placed[leaf] = append(placed[leaf], id)
		}
		unassigned = append(unassigned, m.Unassigned...)
	}
	// Map order must not reach the stored definition: the same inputs give the
	// same expanded rows.
	for leaf := range placed {
		sort.Strings(placed[leaf])
	}
	sort.Strings(unassigned)

	taken := map[string]bool{}
	for _, n := range h.Nodes {
		taken[n.Code] = true
	}
	free := func(code string) string {
		for taken[code] {
			code += "_"
		}
		taken[code] = true
		return code
	}
	unassignedCode, totalCode := free("UNASSIGNED"), free("TOTAL")

	leafRow := func(code, label string, indent int, ids []string, blank bool) Row {
		r := Row{Code: code, Label: label, NormalBalance: opts.NormalBalance, Format: Format{Indent: indent}}
		if h.Subject == SubjectAccount {
			r.Kind, r.Accounts = RowAccountRange, ids
			return r
		}
		values := append([]string(nil), ids...)
		if blank {
			values = append(values, "")
		}
		r.Kind, r.DimensionFilter = RowDimensionFilter, map[string][]string{h.DimensionAttribute: values}
		r.AccountFrom, r.AccountTo = opts.AccountFrom, opts.AccountTo
		return r
	}

	var rows []Row
	var emit func(code string, depth int) (string, bool)
	emit = func(code string, depth int) (string, bool) {
		n := byCode[code]
		indent := min(depth, MaxIndent)
		if leaves[code] {
			if len(placed[code]) == 0 {
				return "", false
			}
			rows = append(rows, leafRow(code, n.Label, indent, placed[code], false))
			return code, true
		}
		var refs []string
		for _, c := range children[code] {
			if ref, ok := emit(c, depth+1); ok {
				refs = append(refs, ref)
			}
		}
		if len(refs) == 0 {
			return "", false
		}
		rows = append(rows, Row{Code: code, Kind: RowTotal, Label: n.Label, References: refs, NormalBalance: opts.NormalBalance,
			Format: Format{Bold: true, Indent: indent}})
		return code, true
	}
	var top []string
	for _, r := range roots {
		if ref, ok := emit(r, 0); ok {
			top = append(top, ref)
		}
	}
	if len(unassigned) > 0 || seen.HasBlank {
		rows = append(rows, leafRow(unassignedCode, "Unassigned", 0, unassigned, seen.HasBlank))
		top = append(top, unassignedCode)
	}
	if len(top) == 0 {
		return bad("hierarchy %s places no member that has balances in this ledger.", h.Code)
	}
	rows = append(rows, Row{Code: totalCode, Kind: RowTotal, Label: "Total", References: top, NormalBalance: opts.NormalBalance,
		Format: Format{Bold: true, PrintZero: true}})

	def := base
	def.Name = strings.TrimSpace(base.Name + " by " + h.Name)
	def.Rows = rows
	if issues := def.Validate(); len(issues) > 0 {
		return bad("the hierarchy does not fit a statement: %s: %s", issues[0].Field, issues[0].Detail)
	}
	return HierarchyRows{Definition: def, Unassigned: unassigned}, nil
}

// HierarchyRunCheck is the warning a run over a hierarchy carries when members
// have no node (FR-15.13: the Unassigned row is footnoted). Nil when none.
func HierarchyRunCheck(h Hierarchy, version int, unassigned []string, blank bool) *Check {
	if len(unassigned) == 0 && !blank {
		return nil
	}
	detail := fmt.Sprintf("%d members have no node in %s version %d and are shown in the Unassigned row", len(unassigned), h.Code, version)
	if len(unassigned) > 0 {
		sample := unassigned
		if len(sample) > hierarchySample {
			sample = sample[:hierarchySample]
		}
		detail += ": " + strings.Join(sample, ", ")
	}
	if blank {
		detail += "; some balances carry no value for " + h.DimensionAttribute
	}
	return &Check{Code: CodeHierarchyRunUnassigned, Severity: SeverityWarning, Detail: detail + "."}
}

// ValidReportCode reports whether s has the shape of a definition or hierarchy code.
func ValidReportCode(s string) bool { return codePattern.MatchString(s) }
