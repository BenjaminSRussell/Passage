package generated

type Attachment struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	OwnerId string `json:"ownerId,omitempty"`
	FileName string `json:"fileName,omitempty"`
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
