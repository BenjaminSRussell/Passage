package generated

type User struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	Subject string `json:"subject,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Email *string `json:"email,omitempty"`
	PrincipalType string `json:"principalType,omitempty"`
	Active bool `json:"active,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
