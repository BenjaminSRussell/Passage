// Package events holds the CloudEvent types and data payloads the projects
// service publishes. The contract is events/asyncapi.yaml.
package events

// Event types (dev.passage.<context>.<entity>.<verb>.v<n>).
const (
	ProjectCreated = "dev.passage.projects.project.created.v1"
	ProjectUpdated = "dev.passage.projects.project.updated.v1"
)

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
