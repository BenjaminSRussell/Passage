package api

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/phiaudit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	entityWorkerBenefits = "hr.worker.benefits"
	// codePHIAuditUnavailable is returned instead of the data when the read cannot be recorded (fail closed).
	codePHIAuditUnavailable = "HCM-PHI-AUDIT-UNAVAILABLE"
	maxPurposeLength        = 500
	selfServicePurpose      = "self-service"
)

// healthPlanTypes are the benefit plan types whose enrollment says something about a person's health coverage.
var healthPlanTypes = map[string]bool{"MEDICAL": true, "DENTAL": true, "VISION": true, "HSA": true, "FSA": true}

// benefitPHIFields names the phi fields a benefits response carries: health-plan elections and the life events that
// open enrollment (birth, adoption, loss of coverage). A response with neither carries none, so nothing is recorded.
func benefitPHIFields(out store.WorkerBenefits) []string {
	var fields []string
	for _, e := range out.Enrollments {
		if healthPlanTypes[e.PlanType] {
			fields = append(fields, "enrollments.planCode", "enrollments.optionCode", "enrollments.coverageLevel",
				"enrollments.dependentCount", "enrollments.endReason", "enrollments.cobraQualifyingEvent")
			break
		}
	}
	if len(out.LifeEvents) > 0 {
		fields = append(fields, "lifeEvents.kind", "lifeEvents.eventDate")
	}
	return fields
}

// auditPHIRead records one read of a record that returned the phi fields, before the response is written. It returns
// false after writing a problem. With auditing off, or no phi fields in the response, it does nothing.
func (a *API) auditPHIRead(w http.ResponseWriter, r *http.Request, entity, recordID string, fields []string, selfRead bool) bool {
	if a.phiAudit == nil || len(fields) == 0 {
		return true
	}
	purpose := strings.TrimSpace(r.URL.Query().Get("purpose"))
	if purpose == "" && selfRead {
		purpose = selfServicePurpose
	}
	if purpose == "" {
		validationFailed(w, r, map[string]string{"purpose": "is required to read protected health information: give a reason code or a short reason"})
		return false
	}
	if utf8.RuneCountInString(purpose) > maxPurposeLength {
		validationFailed(w, r, map[string]string{"purpose": fmt.Sprintf("must be at most %d characters", maxPurposeLength)})
		return false
	}
	if err := a.phiAudit.RecordRead(r.Context(), phiaudit.Read{Entity: entity, RecordID: recordID, Fields: fields, Purpose: purpose}); err != nil {
		logFailure(r, "record phi read", err)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codePHIAuditUnavailable,
			"this record has protected health information and the read could not be recorded, so it was not returned"))
		return false
	}
	a.phiReads.Add(1)
	return true
}
