package domain

import "fmt"

var (
	// CategoryRank is used to keep a released product from being more alive than its product (BR-8.1).
	CategoryRank = map[string]int{
		StateDraft:    0,
		StateActive:   1,
		StatePhaseOut: 2,
		StateObsolete: 3,
	}

	// SeededProcesses are the FR-8.14 business processes.
	SeededProcesses = []string{
		"purchase_requisition", "purchase_order", "sales_quotation", "sales_order",
		"transfer_order", "production_order", "bom_line_use", "master_planning",
		"inventory_receipt", "inventory_issue", "counting", "return_order",
	}
)

// CanTransition reports whether a category move is legal (BR-8.1).
func CanTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case StateDraft:
		return to == StateActive
	case StateActive:
		return to == StatePhaseOut
	case StatePhaseOut:
		return to == StateActive || to == StateObsolete
	default:
		return false
	}
}

// AssertLifecycleTransition rejects illegal category moves and "more alive than product" (BR-8.1).
func AssertLifecycleTransition(from, to, productCategory string) error {
	if !CanTransition(from, to) {
		return errf(CodeLifecycleIllegal, "illegal lifecycle transition "+from+" → "+to)
	}
	if productCategory != "" && CategoryRank[to] < CategoryRank[productCategory] {
		return errf(CodeLifecycleIllegal, "a released product cannot be more alive than its product")
	}
	return nil
}

// SeededLifecycleStates mirrors the D365 defaults in FR-8.14.
func SeededLifecycleStates() []LifecycleState {
	allowAll := map[string]string{}
	for _, p := range SeededProcesses {
		allowAll[p] = "allow"
	}
	prototype := map[string]string{}
	for _, p := range SeededProcesses {
		prototype[p] = "block"
	}
	prototype["bom_line_use"] = "allow"
	prototype["master_planning"] = "allow"

	phase := map[string]string{}
	for _, p := range SeededProcesses {
		phase[p] = "allow"
	}
	phase["purchase_order"] = "block"
	phase["master_planning"] = "block"
	phase["sales_order"] = "warn"

	obsolete := map[string]string{}
	for _, p := range SeededProcesses {
		obsolete[p] = "block"
	}
	obsolete["inventory_issue"] = "allow"
	obsolete["counting"] = "allow"
	obsolete["return_order"] = "allow"

	return []LifecycleState{
		{Code: "PROTOTYPE", Name: "Prototype", Category: StateDraft, DefaultForNew: true, AppliesTo: []string{"product"}, Policies: prototype},
		{Code: "ACTIVE", Name: "Active", Category: StateActive, DefaultForNew: true, AppliesTo: []string{"released_product"}, Policies: allowAll},
		{Code: "PHASE_OUT", Name: "Phase out", Category: StatePhaseOut, AppliesTo: []string{"product", "released_product"}, Policies: phase},
		{Code: "OBSOLETE", Name: "Obsolete", Category: StateObsolete, AppliesTo: []string{"product", "released_product"}, Policies: obsolete},
	}
}

func restrictiveness(policy string) int {
	switch policy {
	case "block":
		return 2
	case "warn":
		return 1
	default:
		return 0
	}
}

func mostRestrictive(policies ...string) string {
	best := "allow"
	for _, p := range policies {
		if restrictiveness(p) > restrictiveness(best) {
			best = p
		}
	}
	return best
}

func policyOf(states []LifecycleState, code, process string) string {
	for _, s := range states {
		if s.Code == code || s.Category == code {
			if p, ok := s.Policies[process]; ok {
				return p
			}
		}
	}
	return "allow"
}

// EvaluateProcessPolicy applies BR-8.12: FR-8.10 block flags win, then the most restrictive state policy.
func EvaluateProcessPolicy(process string, released ReleasedProduct, productState string, states []LifecycleState) ProcessDecision {
	if flagBlocks(process, released) {
		return ProcessDecision{Policy: "block", Code: CodeLCBlocked, Detail: fmt.Sprintf("%s is blocked for state %s", process, released.LifecycleState)}
	}
	releasedPolicy := policyOf(states, released.LifecycleState, process)
	productPolicy := policyOf(states, productState, process)
	policy := mostRestrictive(releasedPolicy, productPolicy)
	switch policy {
	case "block":
		return ProcessDecision{Policy: "block", Code: CodeLCBlocked, Detail: fmt.Sprintf("%s is blocked for state %s", process, released.LifecycleState)}
	case "warn":
		return ProcessDecision{Policy: "warn", Code: CodeLCWarn, Detail: fmt.Sprintf("%s is discouraged for state %s", process, released.LifecycleState)}
	default:
		return ProcessDecision{Policy: "allow"}
	}
}

func flagBlocks(process string, rp ReleasedProduct) bool {
	switch process {
	case "purchase_requisition", "purchase_order":
		return rp.BlockedPurchase
	case "sales_quotation", "sales_order", "return_order":
		return rp.BlockedSales
	case "inventory_receipt", "inventory_issue", "counting", "transfer_order":
		return rp.BlockedInventory
	case "production_order", "bom_line_use", "master_planning":
		return rp.BlockedProduction
	default:
		return false
	}
}
