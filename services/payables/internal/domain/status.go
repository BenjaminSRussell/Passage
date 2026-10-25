package domain

const (
	PaymentCreated       = "CREATED"
	PaymentPosted        = "POSTED"
	PaymentSent          = "SENT"
	PaymentAccepted      = "ACCEPTED"
	PaymentSettledAtBank = "SETTLED_AT_BANK"
	PaymentRejected      = "REJECTED"
	PaymentReversed      = "REVERSED"
	PaymentVoid          = "VOID"
	PrenoteNone          = "NONE"
	PrenotePending       = "PENDING"
	PrenoteConfirmed     = "CONFIRMED"
	PrenoteRejected      = "REJECTED"
)

var paymentEdges = map[string]map[string]bool{
	PaymentCreated:  {PaymentPosted: true},
	PaymentPosted:   {PaymentSent: true, PaymentReversed: true},
	PaymentSent:     {PaymentAccepted: true, PaymentRejected: true, PaymentReversed: true},
	PaymentAccepted: {PaymentSettledAtBank: true, PaymentRejected: true},
}

var prenoteEdges = map[string]map[string]bool{
	PrenoteNone:    {PrenotePending: true},
	PrenotePending: {PrenoteConfirmed: true, PrenoteRejected: true},
}

func TransitionPayment(from, to, method string) error {
	if paymentEdges[from][to] || (from == PaymentSent && to == PaymentVoid && method == MethodCheck) {
		return nil
	}
	return invalid("payment transition %q to %q is not allowed for method %q", from, to, method)
}

func TransitionPrenote(from, to string, accountChanged bool) error {
	if prenoteEdges[from][to] || (from == PrenoteConfirmed && to == PrenoteNone && accountChanged) {
		return nil
	}
	return invalid("prenote transition %q to %q is not allowed", from, to)
}
