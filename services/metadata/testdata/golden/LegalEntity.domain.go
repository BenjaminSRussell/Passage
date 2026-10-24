package generated

type LegalEntity struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	Code string `json:"code,omitempty"`
	Name string `json:"name,omitempty"`
	BaseCurrency string `json:"baseCurrency,omitempty"`
	Country string `json:"country,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
