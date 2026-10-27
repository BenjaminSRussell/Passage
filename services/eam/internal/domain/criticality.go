package domain

import (
	"errors"
	"fmt"
)

// ErrCriticalityScale is returned when a consequence or likelihood score is outside 1-5.
var ErrCriticalityScale = errors.New("criticality scores must be 1-5")

// Consequences are the five consequence categories of the criticality matrix, each scored 1-5 (FR-44.22).
type Consequences struct {
	Safety      int `json:"safety"`
	Environment int `json:"environment"`
	Production  int `json:"production"`
	Quality     int `json:"quality"`
	Cost        int `json:"cost"`
}

// RiskScore is max(consequence categories) x likelihood, in the range 1-25 (BR-44.15).
func RiskScore(c Consequences, likelihood int) (int, error) {
	for _, v := range []int{c.Safety, c.Environment, c.Production, c.Quality, c.Cost, likelihood} {
		if v < 1 || v > 5 {
			return 0, fmt.Errorf("%w: got %d", ErrCriticalityScale, v)
		}
	}
	worst := max(c.Safety, c.Environment, c.Production, c.Quality, c.Cost)
	return worst * likelihood, nil
}

// CriticalityFromRisk maps a risk score and the safety consequence to asset criticality 1-5 (BR-44.15):
// 5 when risk >= 20 or safety = 5, 4 when >= 15, 3 when >= 10, 2 when >= 5, else 1.
func CriticalityFromRisk(risk, safety int) int {
	switch {
	case risk >= 20 || safety == 5:
		return 5
	case risk >= 15:
		return 4
	case risk >= 10:
		return 3
	case risk >= 5:
		return 2
	default:
		return 1
	}
}
