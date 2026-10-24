package generated

type Role struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	Code string `json:"code,omitempty"`
	Name string `json:"name,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
