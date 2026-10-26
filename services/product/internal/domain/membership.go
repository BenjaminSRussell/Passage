package domain

import "fmt"

type OrgRef struct {
	ID            string
	LegalEntityID string
	SiteID        string
}

func CheckSite(field, legalEntityID string, site *OrgRef) error {
	if site == nil {
		return errf(CodeReleaseSiteInvalid, fmt.Sprintf("%s: site does not exist", field))
	}
	if site.LegalEntityID != legalEntityID {
		return errf(CodeReleaseSiteInvalid, fmt.Sprintf("%s: site belongs to another legal entity", field))
	}
	return nil
}

func CheckWarehouse(field, legalEntityID, siteID string, warehouse *OrgRef) error {
	if warehouse == nil {
		return errf(CodeReleaseWarehouseInvalid, fmt.Sprintf("%s: warehouse does not exist", field))
	}
	if warehouse.LegalEntityID != legalEntityID {
		return errf(CodeReleaseWarehouseInvalid, fmt.Sprintf("%s: warehouse belongs to another legal entity", field))
	}
	if siteID != "" && warehouse.SiteID != siteID {
		return errf(CodeReleaseWarehouseInvalid, fmt.Sprintf("%s: warehouse is not in site %s", field, siteID))
	}
	return nil
}
