package domain

import "fmt"

// ResolveSiteDefault applies FR-8.4 cascade: per-site override, else entity default, else reject.
func ResolveSiteDefault(defaults ReleaseDefaults, siteID string) (SiteDefault, error) {
	if siteID != "" {
		if over, ok := defaults.SiteDefaults[siteID]; ok {
			if over.WarehouseID == "" {
				return SiteDefault{}, errf(CodeReleaseDefault, missingField("warehouse", defaults.LegalEntityID, siteID))
			}
			if over.SiteID == "" {
				over.SiteID = siteID
			}
			return over, nil
		}
	}
	if defaults.DefaultSiteID == "" {
		return SiteDefault{}, errf(CodeReleaseDefault, missingField("site", defaults.LegalEntityID, siteID))
	}
	if defaults.DefaultWarehouseID == "" {
		return SiteDefault{}, errf(CodeReleaseDefault, missingField("warehouse", defaults.LegalEntityID, defaults.DefaultSiteID))
	}
	site := defaults.DefaultSiteID
	if siteID != "" {
		site = siteID
	}
	return SiteDefault{
		SiteID:       site,
		WarehouseID:  defaults.DefaultWarehouseID,
		MinQty:       defaults.SiteDefaults[""].MinQty,
		MultipleQty:  defaults.SiteDefaults[""].MultipleQty,
		StdQty:       defaults.SiteDefaults[""].StdQty,
		LeadTimeDays: defaults.SiteDefaults[""].LeadTimeDays,
	}, nil
}

func missingField(field, entity, site string) string {
	if site == "" {
		site = "*"
	}
	return fmt.Sprintf("missing %s for %s/%s", field, entity, site)
}

// AssertReleasable rejects releasing a draft product (FR-8.4).
func AssertReleasable(p Product) error {
	if p.LifecycleState == StateDraft || p.LifecycleState == "" {
		return errf(CodeReleaseDraft, "draft products cannot be released")
	}
	return nil
}

// AssertDimensionGroupUnlocked rejects a group change after inventory transactions (FR-8.3, AC-8.5).
func AssertDimensionGroupUnlocked(hasFinancialTxn bool) error {
	if hasFinancialTxn {
		return errf(CodeDimGroupLocked, "dimension group is locked by inventory transactions")
	}
	return nil
}

// ApplyReleaseProfile copies defaults at release time (BR-8.4). Later profile edits do not propagate.
func ApplyReleaseProfile(product Product, defaults ReleaseDefaults, itemNumber string) (ReleasedProduct, error) {
	if err := AssertReleasable(product); err != nil {
		return ReleasedProduct{}, err
	}
	if defaults.ItemGroupID == "" {
		return ReleasedProduct{}, errf(CodeReleaseDefault, missingField("item_group", defaults.LegalEntityID, ""))
	}
	if defaults.ItemModelGroupID == "" {
		return ReleasedProduct{}, errf(CodeReleaseDefault, missingField("item_model_group", defaults.LegalEntityID, ""))
	}
	if itemNumber == "" {
		itemNumber = product.ProductNumber
	}
	site, err := ResolveSiteDefault(defaults, defaults.DefaultSiteID)
	if err != nil {
		return ReleasedProduct{}, err
	}
	rp := ReleasedProduct{
		ProductID:          product.ID,
		LegalEntityID:      defaults.LegalEntityID,
		ItemNumber:         itemNumber,
		ItemGroupID:        defaults.ItemGroupID,
		ItemModelGroupID:   defaults.ItemModelGroupID,
		DefaultSiteID:      site.SiteID,
		DefaultWarehouseID: site.WarehouseID,
		LifecycleState:     StateActive,
		SiteDefaults:       map[string]SiteDefault{},
		Version:            1,
	}
	for k, v := range defaults.SiteDefaults {
		if k == "" {
			continue
		}
		rp.SiteDefaults[k] = v
	}
	return rp, nil
}
