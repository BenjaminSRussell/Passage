package domain

type ConfigurationRef struct {
	ID               string `json:"id"`
	ModelID          string `json:"modelId"`
	MasterID         string `json:"masterId"`
	Status           string `json:"status"`
	BelongsToProduct bool   `json:"belongsToProduct"`
}
