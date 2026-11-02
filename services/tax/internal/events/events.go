package events

const Source = "/passage/tax/tax"

const ProviderFallback = "dev.passage.tax.provider.fallback.v1"

type ProviderFallbackData struct {
	LegalEntityID string `json:"legalEntityId"`
	RecommitID    string `json:"recommitId"`
	Provider      string `json:"provider"`
	Reason        string `json:"reason"`
}
