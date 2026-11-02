package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	CodeHoldInvalid = "PRV-1220"
	CodeHoldState   = "PRV-1222"
)

type DataClass string

const (
	ClassAPPayee         DataClass = "AP_PAYEE"
	ClassFinancialRecord DataClass = "FINANCIAL_RECORD"
	ClassEmployment      DataClass = "EMPLOYMENT"
	ClassCandidate       DataClass = "CANDIDATE"
	ClassSystemLog       DataClass = "SYSTEM_LOG"
)

type RetentionPolicy struct {
	DataClass DataClass `json:"dataClass"`
	Months    int       `json:"months"`
	Anchor    string    `json:"anchor"`
	Source    string    `json:"source"`
}

var defaultRetention = []RetentionPolicy{
	{ClassAPPayee, 84, "last activity", "owner decision 2026-11: 7 years after last activity for AP and payee records"},
	{ClassFinancialRecord, 84, "last activity", "owner decision 2026-11: 7 years after last activity for financial records"},
	{ClassEmployment, 72, "termination", "PRD FR-48.11: HR 6 years post-termination"},
	{ClassCandidate, 6, "rejection", "PRD wave 42 recruitment: candidate data deleted 6 months after rejection"},
	{ClassSystemLog, 12, "created", "PRD FR-48.11: logs 1 year"},
}

func DefaultRetention() []RetentionPolicy {
	return append([]RetentionPolicy(nil), defaultRetention...)
}

func ValidDataClass(c DataClass) bool {
	for _, p := range defaultRetention {
		if p.DataClass == c {
			return true
		}
	}
	return false
}

func RetentionEnds(class DataClass, anchor time.Time) (time.Time, error) {
	for _, p := range defaultRetention {
		if p.DataClass == class {
			return anchor.UTC().AddDate(0, p.Months, 0), nil
		}
	}
	return time.Time{}, &Error{Code: CodeHoldInvalid, Detail: fmt.Sprintf("data class %q has no retention policy", class)}
}

type HoldScope struct {
	LegalEntityID string `json:"legalEntityId"`
	SubjectRef    string `json:"subjectRef,omitempty"`
	DataClass     string `json:"dataClass,omitempty"`
	RecordRef     string `json:"recordRef,omitempty"`
}

func (s HoldScope) Validate() error {
	if s.DataClass != "" && !ValidDataClass(DataClass(s.DataClass)) {
		return &Error{Code: CodeHoldInvalid, Detail: fmt.Sprintf("data class %q is not one of AP_PAYEE, FINANCIAL_RECORD, EMPLOYMENT, CANDIDATE, SYSTEM_LOG", s.DataClass)}
	}
	return nil
}

func (s HoldScope) ValidHold() error {
	if strings.TrimSpace(s.SubjectRef) == "" && s.DataClass == "" && strings.TrimSpace(s.RecordRef) == "" {
		return &Error{Code: CodeHoldInvalid, Detail: "a hold needs a subjectRef, a dataClass or a recordRef"}
	}
	return s.Validate()
}

func ValidHoldReason(reason string) error {
	if len(strings.TrimSpace(reason)) < privacyJustifyMin {
		return &Error{Code: CodeHoldInvalid, Detail: "a reason of at least 10 characters is required"}
	}
	return nil
}

func ErasureBlocked(holdIDs []string) error { return privacyHoldError(holdIDs) }
