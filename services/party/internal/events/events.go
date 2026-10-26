package events

import (
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

const Context = "party"

var (
	PartyCreated = events.Type(Context, "party", "created", 1)
	PartyUpdated = events.Type(Context, "party", "updated", 1)
)

func All() []string {
	return []string{PartyCreated, PartyUpdated, domain.PartyMergedType}
}

type PartyCreatedData struct {
	PartyID     string `json:"party_id"`
	PartyNumber string `json:"party_number"`
	PartyType   string `json:"party_type"`
	Name        string `json:"name"`
}

type PartyUpdatedData struct {
	PartyID       string   `json:"party_id"`
	ChangedFields []string `json:"changed_fields"`
	Version       int      `json:"version"`
}
