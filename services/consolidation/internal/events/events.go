package events

import "github.com/BenjaminSRussell/passage/platform/events"

const (
	Context = "consol"
	Source  = "/passage/consolidation"
)

var RunCompleted = events.Type(Context, "run", "completed", 1)

// RunCompletedData is the payload of RunCompleted (PRD 7.40.5).
type RunCompletedData struct {
	RunID            string `json:"runId"`
	GroupID          string `json:"groupId"`
	Period           string `json:"period"`
	Version          int    `json:"version"`
	UnitCount        int    `json:"unitCount"`
	EliminationCount int    `json:"eliminationCount"`
	CTAAmount        string `json:"ctaAmount"`
}
