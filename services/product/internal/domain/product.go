package domain

import (
	"fmt"
	"regexp"
	"strings"
)

var productNumberRE = regexp.MustCompile(ProductNumberPattern)

// ValidateProductNumber enforces FR-8.1.
func ValidateProductNumber(n string) error {
	if !productNumberRE.MatchString(n) {
		return errf(CodeNumFormat, "product number "+n+" does not match the pattern")
	}
	return nil
}

// AssertNumberMutable rejects a number change after the product is released.
func AssertNumberMutable(current Product, nextNumber string) error {
	if current.LifecycleState != StateDraft && current.LifecycleState != "" && current.ProductNumber != nextNumber {
		return errf(CodeNumImmutable, "product number cannot change after release")
	}
	return nil
}

// AssertMasterDimensionChange rejects changing a master's dimension group once variants exist.
func AssertMasterDimensionChange(master Product, variantCount int, nextGroupID string) error {
	if master.Subtype != SubtypeMaster {
		return nil
	}
	if variantCount > 0 && nextGroupID != master.DimensionGroupID {
		return errf(CodeDimGroupFrozen, "a master cannot change its dimension group once any variant exists")
	}
	return nil
}

// ServiceNumber formats FR-8.17: SVC-{cat}-{#####}.
func ServiceNumber(category string, seq int) string {
	cat := strings.ToUpper(strings.TrimSpace(category))
	if cat == "" {
		cat = "SVC"
	}
	return fmt.Sprintf("SVC-%s-%05d", cat, seq)
}

// AssertServiceNotStocked rejects storage/tracking groups or a stocked model group on a service (BR-8.17).
func AssertServiceNotStocked(p Product, model ItemModelGroup, storageGroupID, trackingGroupID string) error {
	if p.ProductType != TypeService {
		return nil
	}
	if storageGroupID != "" || trackingGroupID != "" || model.Stocked {
		return errf(CodeSvcStocked, "service products cannot be stocked")
	}
	return nil
}

// SearchName returns the typeahead key, falling back to name (BR-8.10).
func SearchName(p Product) string {
	if s := strings.TrimSpace(p.SearchName); s != "" {
		return s
	}
	return strings.TrimSpace(p.Name)
}

// TranslationFallback picks language, then tenant base, then the product name.
func TranslationFallback(name, language, tenantBase, productName string) string {
	for _, s := range []string{name, language, tenantBase, productName} {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return productName
}
