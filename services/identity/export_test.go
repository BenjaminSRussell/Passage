package identity

import "time"

// SetClock lets the external DB tests drive the worker's retry and backoff timing.
func (d *KeycloakDeprovisioner) SetClock(now func() time.Time) { d.now = now }
