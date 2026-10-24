package generated

type JournalLineStub struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	LegalEntityID string `json:"legalEntityId"`
	JournalEntryId string `json:"journalEntryId,omitempty"`
	Account string `json:"account,omitempty"`
	Amount string `json:"amount,omitempty"`
	PostingDate string `json:"postingDate,omitempty"`
	Memo *string `json:"memo,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}
