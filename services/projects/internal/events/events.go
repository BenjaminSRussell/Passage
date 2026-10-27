// Package events holds the CloudEvent types and data payloads the projects
// service publishes. The contract is events/asyncapi.yaml.
package events

// Event types (dev.passage.<context>.<entity>.<verb>.v<n>).
const (
	ProjectCreated = "dev.passage.projects.project.created.v1"
	ProjectUpdated = "dev.passage.projects.project.updated.v1"
	WBSScheduled   = "dev.passage.projects.wbs.scheduled.v1"
)

// WBSScheduledData is the data of WBSScheduled (PRD §8.41.5), written by each
// scheduler run over a non-empty plan. CriticalPath lists the critical WBS node
// ids in dependency order; FinishDate is the project finish (YYYY-MM-DD).
type WBSScheduledData struct {
	ProjectID     string   `json:"projectId"`
	PlanVersionID string   `json:"planVersionId"`
	CriticalPath  []string `json:"criticalPath"`
	FinishDate    string   `json:"finishDate"`
}

// ProjectCreatedData is the data of ProjectCreated.
type ProjectCreatedData struct {
	ID              string  `json:"id"`
	LegalEntityID   string  `json:"legalEntityId"`
	ParentProjectID *string `json:"parentProjectId,omitempty"`
	ProjectType     string  `json:"projectType"`
	Stage           string  `json:"stage"`
	Currency        string  `json:"currency"`
	Version         int     `json:"version"`
}

// ProjectUpdatedData is the data of ProjectUpdated. ChangedFields names the
// camelCase attributes that changed.
type ProjectUpdatedData struct {
	ID            string   `json:"id"`
	ChangedFields []string `json:"changedFields"`
	Stage         string   `json:"stage"`
	Version       int      `json:"version"`
}
