package generated

type Tenant struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	Slug string `json:"slug,omitempty"`
	Region string `json:"region,omitempty"`
	Tier string `json:"tier,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
