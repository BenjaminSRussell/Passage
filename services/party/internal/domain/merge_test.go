package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

func TestSurvive(t *testing.T) {
	older, newer := time.Unix(1000, 0), time.Unix(2000, 0)
	cases := []struct {
		name          string
		winner, loser domain.Side
		rule          domain.Rule
		want          string
	}{
		{"default keeps winner", domain.Side{Values: map[string]string{"time_zone": "UTC"}}, domain.Side{Values: map[string]string{"time_zone": "CET"}}, "", "UTC"},
		{"keepWinner", domain.Side{Values: map[string]string{"time_zone": "UTC"}}, domain.Side{Values: map[string]string{"time_zone": "CET"}}, domain.KeepWinner, "UTC"},
		{"keepLoser", domain.Side{Values: map[string]string{"time_zone": "UTC"}}, domain.Side{Values: map[string]string{"time_zone": "CET"}}, domain.KeepLoser, "CET"},
		{"keepLoser with empty loser keeps winner", domain.Side{Values: map[string]string{"time_zone": "UTC"}}, domain.Side{}, domain.KeepLoser, "UTC"},
		{"newest loser", domain.Side{Values: map[string]string{"time_zone": "UTC"}, UpdatedAt: older}, domain.Side{Values: map[string]string{"time_zone": "CET"}, UpdatedAt: newer}, domain.Newest, "CET"},
		{"newest winner", domain.Side{Values: map[string]string{"time_zone": "UTC"}, UpdatedAt: newer}, domain.Side{Values: map[string]string{"time_zone": "CET"}, UpdatedAt: older}, domain.Newest, "UTC"},
		{"nonEmpty fills from loser", domain.Side{}, domain.Side{Values: map[string]string{"time_zone": "CET"}}, domain.NonEmpty, "CET"},
		{"nonEmpty keeps winner", domain.Side{Values: map[string]string{"time_zone": "UTC"}}, domain.Side{Values: map[string]string{"time_zone": "CET"}}, domain.NonEmpty, "UTC"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.Survive(c.winner, c.loser, map[string]domain.Rule{"time_zone": c.rule})
			if err != nil {
				t.Fatal(err)
			}
			if got["time_zone"] != c.want {
				t.Fatalf("time_zone = %q, want %q", got["time_zone"], c.want)
			}
		})
	}
}

func TestSurvive_RejectsUnknownFieldAndRule(t *testing.T) {
	var ruleErr *domain.RuleError
	if _, err := domain.Survive(domain.Side{}, domain.Side{}, map[string]domain.Rule{"party_number": domain.KeepLoser}); !errors.As(err, &ruleErr) {
		t.Fatalf("unknown field: got %v, want RuleError", err)
	}
	if _, err := domain.Survive(domain.Side{}, domain.Side{}, map[string]domain.Rule{"name": "coinflip"}); !errors.As(err, &ruleErr) {
		t.Fatalf("unknown rule: got %v, want RuleError", err)
	}
}
