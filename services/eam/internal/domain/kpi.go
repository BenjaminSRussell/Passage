package domain

// KPI is the reliability summary for one scope and window (BR-44.8).
type KPI struct {
	Failures     int     `json:"failures"`
	MTBFHours    float64 `json:"mtbfHours"`
	MTTRHours    float64 `json:"mttrHours"`
	Availability float64 `json:"availability"`
}

// Reliability computes MTBF = uptime ÷ failures, MTTR = Σ repair hours ÷
// failures and availability = 1 − downtime ÷ window. repairHours holds one
// entry per CORRECTIVE or EMERGENCY work order in the window. With no failures
// MTBF and MTTR are reported as zero.
func Reliability(windowHours, downtimeHours float64, repairHours []float64) KPI {
	k := KPI{Failures: len(repairHours)}
	if windowHours > 0 {
		k.Availability = 1 - downtimeHours/windowHours
	}
	if k.Failures == 0 {
		return k
	}
	var repair float64
	for _, h := range repairHours {
		repair += h
	}
	k.MTBFHours = (windowHours - downtimeHours) / float64(k.Failures)
	k.MTTRHours = repair / float64(k.Failures)
	return k
}
