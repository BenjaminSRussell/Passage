package events

const (
	InstanceSubmitted = "dev.passage.workflow.instance.submitted.v1"
	WorkItemAssigned  = "dev.passage.workflow.workitem.assigned.v1"
	WorkItemEscalated = "dev.passage.workflow.workitem.escalated.v1"
	InstanceCompleted = "dev.passage.workflow.instance.completed.v1"
	BatchRunStarted   = "dev.passage.batch.run.started.v1"
	BatchRunCompleted = "dev.passage.batch.run.completed.v1"
	BatchRunFailed    = "dev.passage.batch.run.failed.v1"
	MessageSent       = "dev.passage.notify.message.sent.v1"
	MessageFailed     = "dev.passage.notify.message.failed.v1"
	MessageBounced    = "dev.passage.notify.message.bounced.v1"
	TemplatePublished = "dev.passage.notify.template.published.v1"
	EntityPublished   = "dev.passage.metadata.entity.published.v1"
)
