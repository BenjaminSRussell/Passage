package generated

type BenefitElection struct {
	ID string `json:"id"`
	TenantID string `json:"tenantId"`
	PlanCode string `json:"planCode,omitempty"`
	EmployeeRef string `json:"employeeRef,omitempty"`
	CoverageTier *string `json:"coverageTier,omitempty"`
	MedicalLeaveReason *string `json:"medicalLeaveReason,omitempty"`
	AccommodationNote *[]byte `json:"accommodationNote,omitempty"`
	PayrollMemo *string `json:"payrollMemo,omitempty"`
	Ext map[string]any `json:"ext"`
	Version int `json:"version"`
}

// BenefitElectionPHIFields are the JSON names of the phi fields; every one is stored sealed with fieldcrypt.
func BenefitElectionPHIFields() []string { return []string{"coverageTier", "medicalLeaveReason", "accommodationNote"} }
