package config

import (
	"strconv"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnv_DefaultsMatchPRD(t *testing.T) {
	cfg, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Reporting{
		MaxLagSeconds: 30, PageSize: 500, CellKeyLimit: 50000,
		RetentionClosedYears: 7, RetentionAdhocDays: 90, AttachmentLimitMB: 20,
		ShareLinkExpiryDays: 14, PDFConcurrencyPerPod: 4, OPACacheSeconds: 60,
	}
	if cfg != want {
		t.Fatalf("defaults = %+v, want %+v", cfg, want)
	}
}

func TestFromEnv_OverridesWithinRange(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{
		"REPORTING_MAX_LAG_S":               "0",
		"REPORTING_PAGE_SIZE":               " 250 ",
		"REPORTING_CELL_KEY_LIMIT":          "200000",
		"REPORTING_RETENTION_ADHOC_DAYS":    "730",
		"REPORTING_PDF_CONCURRENCY_PER_POD": "16",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxLagSeconds != 0 || cfg.CellKeyLimit != 200000 || cfg.RetentionAdhocDays != 730 || cfg.PDFConcurrencyPerPod != 16 {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if cfg.PageSize != 250 {
		t.Fatalf("PageSize = %d, want 250 (whitespace trimmed)", cfg.PageSize)
	}
}

func TestFromEnv_RejectsOutOfRangeAndNonInteger(t *testing.T) {
	cases := []struct {
		name, env, value, want string
	}{
		{"lag above max", "REPORTING_MAX_LAG_S", "601", "REPORTING_MAX_LAG_S must be between 0 and 600, got 601"},
		{"page below min", "REPORTING_PAGE_SIZE", "99", "REPORTING_PAGE_SIZE must be between 100 and 1000, got 99"},
		{"retention zero", "REPORTING_RETENTION_CLOSED_YEARS", "0", "REPORTING_RETENTION_CLOSED_YEARS must be between 1 and 30"},
		{"not an integer", "REPORTING_OPA_CACHE_SECONDS", "1m", `REPORTING_OPA_CACHE_SECONDS must be an integer, got "1m"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FromEnv(env(map[string]string{tc.env: tc.value}))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestFromEnv_ReportsEveryProblemInOrder(t *testing.T) {
	_, err := FromEnv(env(map[string]string{
		"REPORTING_PAGE_SIZE":      "1",
		"REPORTING_MAX_LAG_S":      "x",
		"REPORTING_CELL_KEY_LIMIT": "5",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	lag := strings.Index(msg, "REPORTING_MAX_LAG_S")
	page := strings.Index(msg, "REPORTING_PAGE_SIZE")
	cell := strings.Index(msg, "REPORTING_CELL_KEY_LIMIT")
	if cell < 0 || lag < 0 || page < 0 {
		t.Fatalf("missing one of the three problems: %s", msg)
	}
	if !(lag < page && page < cell) {
		t.Fatalf("problems are not in a stable order: %s", msg)
	}
}

// Each bound is inclusive: min and max are accepted, one past either is refused.
func TestFromEnv_EveryBoundIsInclusive(t *testing.T) {
	var probe Reporting
	for _, s := range probe.settings() {
		for _, tc := range []struct {
			n  int
			ok bool
		}{{s.min, true}, {s.max, true}, {s.min - 1, false}, {s.max + 1, false}} {
			_, err := FromEnv(env(map[string]string{s.env: strconv.Itoa(tc.n)}))
			if (err == nil) != tc.ok {
				t.Errorf("%s=%d: error %v, want ok=%v", s.env, tc.n, err, tc.ok)
			}
		}
	}
}
