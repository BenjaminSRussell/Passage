package domain

// CodeCategoryTypes refuses a valuation category whose type list breaks the
// split valuation rules (FR-20.16, BR-20.14).
const CodeCategoryTypes = "CST-VAL-003"

// ValuationTypeSpec is one valuation type of a category with the inventory
// account that holds its stock (the valuation class equivalent, FR-20.16).
type ValuationTypeSpec struct {
	Code    string
	Account string
}

// CheckCategoryTypes applies the category rules. A BATCH category creates its
// valuation types at batch receipt (BR-20.14), so it lists none; any other
// category lists at least one type, each code once.
// Code and account formats are request validation, checked by the caller.
func CheckCategoryTypes(batch bool, types []ValuationTypeSpec) error {
	if batch {
		if len(types) > 0 {
			return &Error{CodeCategoryTypes, "a batch valuation category takes no fixed valuation types: each batch becomes its own type at receipt"}
		}
		return nil
	}
	if len(types) == 0 {
		return &Error{CodeCategoryTypes, "a valuation category needs at least one valuation type, or must be a batch category"}
	}
	seen := make(map[string]bool, len(types))
	for _, t := range types {
		if seen[t.Code] {
			return &Error{CodeCategoryTypes, "valuation type " + t.Code + " is listed twice"}
		}
		seen[t.Code] = true
	}
	return nil
}
