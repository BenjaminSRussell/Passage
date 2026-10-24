package generated

type VendorStub struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	LegalEntityID string `json:"legalEntityId"`
	Account string `json:"account,omitempty"`
	Name string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	CreditLimit *string `json:"creditLimit,omitempty"`
	CreditLimitCurrency *string `json:"creditLimitCurrency,omitempty"`
	TaxId *string `json:"taxId,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
