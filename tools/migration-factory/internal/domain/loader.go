package domain

import (
	"fmt"
	"strings"
	"time"
)

// XrefKey is the natural key of a legacy record: (source_system, source_company, source_table, source_id).
func XrefKey(system SourceSystem, company, table, sourceID string) string {
	return strings.Join([]string{string(system), company, table, sourceID}, "|")
}

// LoadPlan is the idempotent result of comparing an extract with the legacy_xref set (FR-49.10).
type LoadPlan struct {
	ToLoad         []Record
	AlreadyPresent int
}

// PlanLoad keeps only records whose xref key is not yet present. A re-run of an extract that was
// already loaded therefore loads zero records; MIG-1505 reports the count (AC-49.4).
func PlanLoad(records []Record, system SourceSystem, company, table string, existing map[string]struct{}) LoadPlan {
	var plan LoadPlan
	for _, r := range records {
		if _, ok := existing[XrefKey(system, company, table, r.LegacyID)]; ok {
			plan.AlreadyPresent++
			continue
		}
		plan.ToLoad = append(plan.ToLoad, r)
	}
	return plan
}

// AlreadyPresentMessage renders MIG-1505 for a re-run that loaded nothing new.
func AlreadyPresentMessage(n int) string {
	return fmt.Sprintf("Re-run loaded 0 new records; all %d already present by cross-reference.", n)
}

// Xref is one legacy_xref row (FR-49.3): the legacy record and the Passage record it became.
type Xref struct {
	XrefID        string
	SourceSystem  SourceSystem
	SourceCompany string
	SourceTable   string
	SourceID      string
	PassageEntity string
	PassageID     string
	RunID         string
	CreatedAt     time.Time
}
