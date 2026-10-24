package generated

type Address struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	Line1 string `json:"line1,omitempty"`
	Line2 *string `json:"line2,omitempty"`
	City *string `json:"city,omitempty"`
	PostalCode *string `json:"postalCode,omitempty"`
	Country string `json:"country,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
