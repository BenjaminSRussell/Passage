// Package config reads the reporting tunables from the environment (PRD
// §2.15.10 and §2.15.13). Every value has a default and an allowed range; an
// out-of-range value fails startup, so a typo never silently changes a
// statement's lag gate or retention.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Reporting holds the process-wide defaults of the §2.15.13 parameters. The
// per-tenant and per-legal-entity overrides need the platform parameter store
// (NOT-IMPLEMENTED.md).
type Reporting struct {
	MaxLagSeconds        int // run blocked above this lag unless allow_stale
	PageSize             int // drill level 2 page size (BR-15.4)
	CellKeyLimit         int // keys stored before a cell becomes a query spec (BR-15.11)
	RetentionClosedYears int // library retention for closed-period runs (FR-15.8)
	RetentionAdhocDays   int // ad-hoc run retention
	AttachmentLimitMB    int // attach versus link (FR-15.8)
	ShareLinkExpiryDays  int // shared link lifetime
	PDFConcurrencyPerPod int // renderer queue width
	OPACacheSeconds      int // dimension security cache (BR-15.7)
}

// setting is one variable: its name, default and inclusive range.
type setting struct {
	env           string
	def, min, max int
	dst           *int
}

func (cfg *Reporting) settings() []setting {
	return []setting{
		{"REPORTING_MAX_LAG_S", 30, 0, 600, &cfg.MaxLagSeconds},
		{"REPORTING_PAGE_SIZE", 500, 100, 1000, &cfg.PageSize},
		{"REPORTING_CELL_KEY_LIMIT", 50000, 1000, 200000, &cfg.CellKeyLimit},
		{"REPORTING_RETENTION_CLOSED_YEARS", 7, 1, 30, &cfg.RetentionClosedYears},
		{"REPORTING_RETENTION_ADHOC_DAYS", 90, 7, 730, &cfg.RetentionAdhocDays},
		{"REPORTING_EMAIL_ATTACHMENT_LIMIT_MB", 20, 1, 25, &cfg.AttachmentLimitMB},
		{"REPORTING_SHARE_LINK_EXPIRY_DAYS", 14, 1, 90, &cfg.ShareLinkExpiryDays},
		{"REPORTING_PDF_CONCURRENCY_PER_POD", 4, 1, 16, &cfg.PDFConcurrencyPerPod},
		{"REPORTING_OPA_CACHE_SECONDS", 60, 5, 600, &cfg.OPACacheSeconds},
	}
}

// FromEnv reads every setting through getenv; an unset or empty value takes
// the default. It reports every problem at once, in the order above, so one
// restart shows every typo.
func FromEnv(getenv func(string) string) (Reporting, error) {
	var cfg Reporting
	var problems []string
	for _, s := range cfg.settings() {
		raw := strings.TrimSpace(getenv(s.env))
		if raw == "" {
			*s.dst = s.def
			continue
		}
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s must be an integer, got %q", s.env, raw))
		case n < s.min || n > s.max:
			problems = append(problems, fmt.Sprintf("%s must be between %d and %d, got %d", s.env, s.min, s.max, n))
		default:
			*s.dst = n
		}
	}
	if len(problems) > 0 {
		return Reporting{}, fmt.Errorf("invalid reporting configuration: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}
