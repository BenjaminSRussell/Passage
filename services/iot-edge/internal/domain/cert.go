package domain

import "time"

// Certificate policy (BR-25.14). The mTLS client certificate lasts 90 days and
// renewal starts when less than 30 days remain.
const (
	CertRenewWindow           = 30 * 24 * time.Hour
	CertAlertAfterFailingDays = 7
)

// CertAction is what the connector and the fleet do about its client certificate.
type CertAction string

const (
	// CertOK: the certificate is valid for more than the renewal window.
	CertOK CertAction = "ok"
	// CertRenew: renewal starts; less than 30 days remain.
	CertRenew CertAction = "renew"
	// CertAlert: renewal has failed for 7 consecutive days; page the on-call (SF-0018).
	CertAlert CertAction = "alert"
	// CertExpired: publishing stops. Buffering continues in the ring (24 h).
	CertExpired CertAction = "expired"
)

// DecideCert applies BR-25.14. An expired certificate takes precedence over every
// other state, then a renewal failing for 7 days or more, then the renewal window.
// renewFailingSince is nil while renewal is not failing.
func DecideCert(now, notAfter time.Time, renewFailingSince *time.Time) CertAction {
	switch {
	case !now.Before(notAfter):
		return CertExpired
	case renewFailingSince != nil && now.Sub(*renewFailingSince) >= CertAlertAfterFailingDays*24*time.Hour:
		return CertAlert
	case notAfter.Sub(now) < CertRenewWindow:
		return CertRenew
	default:
		return CertOK
	}
}

// PublishAllowed reports whether the connector may publish to Kafka under the
// given certificate action. Only an expired certificate stops publishing.
func PublishAllowed(a CertAction) bool { return a != CertExpired }
