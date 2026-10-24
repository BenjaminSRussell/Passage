package generated

type Note struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	Body string `json:"body,omitempty"`
	AuthorId string `json:"authorId,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
