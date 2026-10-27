package domain

import "time"

// DefaultCooldown is the BR-44.4 default cooldown after a rule fires.
const DefaultCooldown = 60 * time.Minute

// Breached evaluates a threshold rule (BR-44.4). Supported operators are ">" and "<".
func Breached(op string, threshold, value float64) bool {
	switch op {
	case ">":
		return value > threshold
	case "<":
		return value < threshold
	}
	return false
}

// DedupeKey is the (asset, rule) key that allows one open request per rule.
func DedupeKey(assetID, ruleID string) string { return "iot:" + assetID + ":" + ruleID }

// ShouldRaise reports whether a breach creates a request: no NEW or TRIAGED
// request exists for the key and the rule is outside its cooldown (BR-44.4).
func ShouldRaise(openRequest bool, lastFired *time.Time, now time.Time, cooldown time.Duration) bool {
	if openRequest {
		return false
	}
	if lastFired != nil && now.Before(lastFired.Add(cooldown)) {
		return false
	}
	return true
}
