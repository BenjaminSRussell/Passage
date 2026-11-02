package domain

const (
	ReviewDraft         = "draft"
	ReviewSelfSubmitted = "self_submitted"
	ReviewSignedOff     = "signed_off"

	GoalOpen      = "OPEN"
	GoalAchieved  = "ACHIEVED"
	GoalCancelled = "CANCELLED"

	MaxReviewText = 4000
	MinRating     = 1
	MaxRating     = 5
)

func ValidGoalStatus(status string) bool {
	return status == GoalOpen || status == GoalAchieved || status == GoalCancelled
}
