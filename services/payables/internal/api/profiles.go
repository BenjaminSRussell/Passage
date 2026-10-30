package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	EnvPostingProfiles     = "PAYABLES_POSTING_PROFILES"
	EnvPostingProfilesFile = "PAYABLES_POSTING_PROFILES_FILE"
)

type PostingProfile struct {
	LedgerID        string               `json:"ledgerId"`
	BankAccount     string               `json:"bankAccount"`
	ControlAccount  string               `json:"controlAccount"`
	DiscountAccount string               `json:"discountAccount"`
	File            domain.RunFileConfig `json:"file"`
}

type PostingProfiles map[string]PostingProfile

func (p PostingProfiles) For(legalEntityID string) (PostingProfile, bool) {
	canonical, _ := ids.Canonical(legalEntityID)
	profile, ok := p[canonical]
	return profile, ok
}

// LoadPostingProfiles reads the per-legal-entity run configuration (ledger, GL accounts and the paying
// bank account) from PAYABLES_POSTING_PROFILES or the mounted file in PAYABLES_POSTING_PROFILES_FILE.
// A request can never supply these; it returns nil, nil when unset so payment runs answer 422.
func LoadPostingProfiles(getenv func(string) string) (PostingProfiles, error) {
	raw := strings.TrimSpace(getenv(EnvPostingProfiles))
	if file := getenv(EnvPostingProfilesFile); raw == "" && file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", EnvPostingProfilesFile, err)
		}
		raw = strings.TrimSpace(string(b))
	}
	if raw == "" {
		return nil, nil
	}
	var parsed map[string]PostingProfile
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("%s: %w", EnvPostingProfiles, err)
	}
	out := make(PostingProfiles, len(parsed))
	for le, p := range parsed {
		canonical, ok := ids.Canonical(le)
		if !ok || !ids.Valid(p.LedgerID) || p.BankAccount == "" || p.ControlAccount == "" ||
			len(p.BankAccount) > maxAccountLen || len(p.ControlAccount) > maxAccountLen || len(p.DiscountAccount) > maxAccountLen {
			return nil, errors.New(EnvPostingProfiles + ": every legal entity needs a UUID key, a ledger UUID, a bank account and a control account of at most 20 characters")
		}
		out[canonical] = p
	}
	return out, nil
}
