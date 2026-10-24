package domain_test

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

func TestApplyExtensionAddsFieldEnumValueAndHook(t *testing.T) {
	base := vendor(t)
	ext := domain.Extension{
		Name:   "customer.vendor-preferences",
		Entity: "Vendor",
		Fields: []domain.Field{{Name: "account_manager", Type: "string", Length: 80}},
		EnumValues: []domain.EnumExtension{{
			Enum:   "Status",
			Values: []domain.EnumValue{{Name: "preferred", Code: 1000, Label: "Preferred"}},
		}},
		Hooks: []domain.Hook{{Name: "validateWrite", Expr: "status != \"blocked\""}},
	}

	effective, err := domain.ApplyExtension(base, ext)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Fields) != 4 || len(base.Enums[0].Values) != 2 {
		t.Fatal("ApplyExtension mutated the published base definition")
	}
	if got := effective.Fields[len(effective.Fields)-1].Name; got != "account_manager" {
		t.Fatalf("last field = %q, want account_manager", got)
	}
	if got := effective.Enums[0].Values[len(effective.Enums[0].Values)-1]; got.Name != "preferred" || got.Code != 1000 {
		t.Fatalf("extension enum value = %#v", got)
	}
	if len(effective.Hooks) != 1 {
		t.Fatalf("hooks = %#v", effective.Hooks)
	}
	if err := domain.CompareVersions(base, effective); err != nil {
		t.Fatalf("additive extension rejected: %v", err)
	}
}

func TestApplyExtensionRejectsCollisionsAndSystemEnumCodes(t *testing.T) {
	tests := []struct {
		name string
		ext  domain.Extension
		want string
	}{
		{
			name: "field collision",
			ext:  domain.Extension{Name: "customer.collision", Entity: "Vendor", Fields: []domain.Field{{Name: "code", Type: "text"}}},
			want: "MET-0102",
		},
		{
			name: "system enum range",
			ext: domain.Extension{Name: "customer.bad-enum", Entity: "Vendor", EnumValues: []domain.EnumExtension{{
				Enum: "Status", Values: []domain.EnumValue{{Name: "preferred", Code: 999}},
			}}},
			want: "MET-0103",
		},
		{
			name: "wrong entity",
			ext:  domain.Extension{Name: "customer.wrong", Entity: "Customer"},
			want: "MET-0101",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.ApplyExtension(vendor(t), tt.ext)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %s", err, tt.want)
			}
		})
	}
}
