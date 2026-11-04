package domain

// CanApplyHold reports whether a hold of holdType may be applied through the
// holds API (SalesOrderHold, PRD §5.27.17). CREDIT holds are placed only by the
// credit check and never by hand (BR-27.3, BR-27.4).
func CanApplyHold(holdType string) bool {
	return holdType == HoldFraud || holdType == HoldManual || holdType == HoldPriceReview
}

// CanRejectHold reports whether a credit manager can reject a hold (PRD
// §5.27.6, "release/reject with comment"): only an ACTIVE CREDIT hold of an
// order still awaiting confirmation. A rejection cancels that order. Other
// hold types are released or cleared by a cancel, and a CREDIT hold raised on a
// confirmed order by a total-raising change is answered by a change or a cancel
// through change management, which keep their own rules.
func CanRejectHold(holdType, holdStatus, orderStatus string) bool {
	return holdType == HoldCredit && holdStatus == HoldActive && CanConfirm(orderStatus)
}
