package domain

// Agreement statuses (FR-45.3). Only ACTIVE entitles cases and covers work orders.
const (
	AgreementDraft     = "DRAFT"
	AgreementSuspended = "SUSPENDED"
	AgreementExpired   = "EXPIRED"
	AgreementCanceled  = "CANCELED"
)

// Visit kinds recorded against an agreement line's includedVisits.
const (
	VisitPreventive = "PREVENTIVE"
	VisitCorrective = "CORRECTIVE"
)

// agreementMoves is the status machine a caller can drive. EXPIRED is reached
// only by the expiry pass once validTo is behind the day; CANCELED and EXPIRED
// are final.
var agreementMoves = map[string][]string{
	AgreementDraft:     {AgreementActive, AgreementCanceled},
	AgreementActive:    {AgreementSuspended, AgreementCanceled, AgreementExpired},
	AgreementSuspended: {AgreementActive, AgreementCanceled, AgreementExpired},
}

// AgreementCanMove reports whether an agreement may move from one status to another.
func AgreementCanMove(from, to string) bool {
	for _, t := range agreementMoves[from] {
		if t == to {
			return true
		}
	}
	return false
}

// AgreementEnded reports whether the agreement's validTo (YYYY-MM-DD, nil when
// open-ended) is before day (YYYY-MM-DD). Both are ISO dates, which order as text.
func AgreementEnded(validTo *string, day string) bool {
	return validTo != nil && *validTo < day
}

// AgreementCovers reports whether day lies inside [validFrom, validTo].
func AgreementCovers(validFrom string, validTo *string, day string) bool {
	return day >= validFrom && !AgreementEnded(validTo, day)
}

// VisitIncluded reports whether a line with used visits recorded still has an
// included visit left. A line with includedVisits 0 includes none.
func VisitIncluded(line AgreementLine, used int) bool {
	return used < line.IncludedVisits
}
