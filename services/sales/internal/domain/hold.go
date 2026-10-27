package domain

// CanApplyHold reports whether a hold of holdType may be applied through the
// holds API (SalesOrderHold, PRD §5.27.17). CREDIT holds are placed only by the
// credit check and never by hand (BR-27.3, BR-27.4).
func CanApplyHold(holdType string) bool {
	return holdType == HoldFraud || holdType == HoldManual || holdType == HoldPriceReview
}
