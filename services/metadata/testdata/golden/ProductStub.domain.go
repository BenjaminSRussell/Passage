package generated

type ProductStub struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	Sku string `json:"sku,omitempty"`
	Description *string `json:"description,omitempty"`
	Uom string `json:"uom,omitempty"`
	Active bool `json:"active,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
