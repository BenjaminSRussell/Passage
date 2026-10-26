package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

func TestResolveName(t *testing.T) {
	both := map[string]string{"fr": "Société", "fr-CA": "Société QC"}
	tests := []struct {
		name         string
		translations map[string]string
		tag          string
		wantName     string
		wantLanguage string
		wantSource   string
	}{
		{"exact tag", both, "fr-CA", "Société QC", "fr-CA", domain.SourceTranslation},
		{"primary subtag fallback", both, "fr-FR", "Société", "fr", domain.SourceTranslation},
		{"primary subtag asked directly", both, "fr", "Société", "fr", domain.SourceTranslation},
		{"no matching language", both, "de", "Company", "", domain.SourceBase},
		{"empty tag", both, "", "Company", "", domain.SourceBase},
		{"region-only stored, primary asked", map[string]string{"fr-CA": "Société QC"}, "fr", "Company", "", domain.SourceBase},
		{"no translations", nil, "fr-CA", "Company", "", domain.SourceBase},
		{"empty tag with an empty-key translation", map[string]string{"": "x"}, "", "Company", "", domain.SourceBase},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, language, source := domain.ResolveName("Company", tt.translations, tt.tag)
			if name != tt.wantName || language != tt.wantLanguage || source != tt.wantSource {
				t.Fatalf("got (%q, %q, %q); want (%q, %q, %q)", name, language, source, tt.wantName, tt.wantLanguage, tt.wantSource)
			}
		})
	}
}
