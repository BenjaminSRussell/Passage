package domain

const (
	CheckAuto   = "auto"
	CheckManual = "manual"

	CheckPending       = "pending"
	CheckPassed        = "passed"
	CheckFailed        = "failed"
	CheckNotApplicable = "not_applicable"

	ReadinessPending = "readiness_pending"
	ReleasedActive   = "Active"

	// CodeLifecycleBlocked is the FR-8.14 process-policy code a sales order query returns
	// while the released product is still readiness_pending (AC-9.12).
	CodeLifecycleBlocked = "PRD-LC-BLOCKED"
)

// ReadinessPolicy is FR-9.17: automated and manual checks that must pass before a
// released product becomes Active.
type ReadinessPolicy struct {
	ID          string
	Code        string
	Checks      []ReadinessCheck
	TargetState string
	Continuous  bool
}

// ReadinessCheck is one policy row.
type ReadinessCheck struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Mandatory bool   `json:"mandatory"`
	Assignee  string `json:"assignee,omitempty"`
	DueDays   int    `json:"dueDays,omitempty"`
}

// ReadinessFacts are the automated observations at evaluation time. Unset hooks of
// waves that are not live stay false and evaluate to not_applicable.
type ReadinessFacts struct {
	ItemModelGroupSet bool
	StorageGroupSet   bool
	TrackingGroupSet  bool
	OrderSettingsSet  bool
	ActiveBOM         bool
	ActiveRoute       bool
	TaxGroupSet       bool
	CoverageGroupSet  bool
	PurchasePriceSet  bool
	SalesPriceSet     bool
	WarehouseSetup    bool
}

// ReadinessCheckResult is the persisted outcome of one check for one released product.
type ReadinessCheckResult struct {
	Key      string
	Kind     string
	Status   string
	SignedBy string
}

var autoKeys = map[string]func(ReadinessFacts) (observed, implemented bool){
	"item_model_group":       func(f ReadinessFacts) (bool, bool) { return f.ItemModelGroupSet, true },
	"storage_group":          func(f ReadinessFacts) (bool, bool) { return f.StorageGroupSet, true },
	"tracking_group":         func(f ReadinessFacts) (bool, bool) { return f.TrackingGroupSet, true },
	"default_order_settings": func(f ReadinessFacts) (bool, bool) { return f.OrderSettingsSet, true },
	"active_bom":             func(f ReadinessFacts) (bool, bool) { return f.ActiveBOM, true },
	"active_route":           func(f ReadinessFacts) (bool, bool) { return f.ActiveRoute, true },
	"tax_group":              func(f ReadinessFacts) (bool, bool) { return f.TaxGroupSet, false },
	"coverage_group":         func(f ReadinessFacts) (bool, bool) { return f.CoverageGroupSet, false },
	"purchase_price":         func(f ReadinessFacts) (bool, bool) { return f.PurchasePriceSet, false },
	"sales_price":            func(f ReadinessFacts) (bool, bool) { return f.SalesPriceSet, false },
	"warehouse_setup":        func(f ReadinessFacts) (bool, bool) { return f.WarehouseSetup, false },
}

// EvaluateReadiness re-runs automated checks. Manual results are kept unless missing.
func EvaluateReadiness(policy ReadinessPolicy, facts ReadinessFacts, previous []ReadinessCheckResult) []ReadinessCheckResult {
	prev := map[string]ReadinessCheckResult{}
	for _, r := range previous {
		prev[r.Key] = r
	}
	out := make([]ReadinessCheckResult, 0, len(policy.Checks))
	for _, c := range policy.Checks {
		row := ReadinessCheckResult{Key: c.Key, Kind: c.Kind, Status: CheckPending}
		if old, ok := prev[c.Key]; ok {
			row = old
		}
		if c.Kind == CheckAuto {
			fn, known := autoKeys[c.Key]
			if !known {
				row.Status = CheckNotApplicable
			} else if observed, implemented := fn(facts); !implemented {
				row.Status = CheckNotApplicable
			} else if observed {
				row.Status = CheckPassed
			} else {
				row.Status = CheckPending
			}
		}
		out = append(out, row)
	}
	return out
}

// SignManualCheck records a passed manual check. The user who created the release session cannot sign (BR-9.14).
func SignManualCheck(results []ReadinessCheckResult, key, signer, sessionCreator string) ([]ReadinessCheckResult, error) {
	if signer == "" {
		return nil, errf(CodeReadinessSelfSign, "a readiness check needs the signing user")
	}
	if signer == sessionCreator {
		return nil, errf(CodeReadinessSelfSign, "the user who created the release session cannot sign a readiness check")
	}
	out := append([]ReadinessCheckResult(nil), results...)
	for i := range out {
		if out[i].Key == key {
			if out[i].Kind != CheckManual {
				return nil, errf("COM-0001", "check %s is not manual", key)
			}
			out[i].Status, out[i].SignedBy = CheckPassed, signer
			return out, nil
		}
	}
	return nil, errf("COM-0001", "unknown readiness check %s", key)
}

// ReadinessState is Active when every mandatory check is passed or not_applicable (BR-9.14).
func ReadinessState(policy ReadinessPolicy, results []ReadinessCheckResult) (state string, pending int) {
	byKey := map[string]ReadinessCheckResult{}
	for _, r := range results {
		byKey[r.Key] = r
	}
	target := policy.TargetState
	if target == "" {
		target = ReleasedActive
	}
	for _, c := range policy.Checks {
		if !c.Mandatory {
			continue
		}
		r := byKey[c.Key]
		if r.Status == CheckPassed || r.Status == CheckNotApplicable {
			continue
		}
		pending++
	}
	if pending > 0 {
		return ReadinessPending, pending
	}
	return target, 0
}

// ProcessPolicy answers a FR-8.14 process query. Sales orders are blocked while readiness is pending.
func ProcessPolicy(state, process string) string {
	if state == ReadinessPending && process == "sales_order" {
		return CodeLifecycleBlocked
	}
	return ""
}
