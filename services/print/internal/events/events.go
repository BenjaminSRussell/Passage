// Package events holds the event type constants and payloads the print service
// publishes. Payload fields are snake_case, as in api/asyncapi.
package events

// JobCompleted is published when a job reaches PRINTED or FAILED.
const JobCompleted = "dev.passage.print.job.completed.v1"

// JobCompletedData is the data member of JobCompleted.
type JobCompletedData struct {
	JobID   string `json:"job_id"`
	Printer string `json:"printer"`
	Status  string `json:"status"`
}
