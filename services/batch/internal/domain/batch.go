package domain

const (
	JobActive = "active"
	JobHold   = "hold"

	RunWaiting   = "waiting"
	RunScheduled = "scheduled"
	RunExecuting = "executing"
	RunEnded     = "ended"
	RunError     = "error"
	RunFailed    = "failed"
	RunHold      = "hold"
	RunCanceling = "canceling"
	RunCanceled  = "canceled"

	PriorityA = "A"
	PriorityB = "B"
	PriorityC = "C"

	PartitionRange = "range"
	PartitionHash  = "hash"
	PartitionList  = "list"
)

// Job is a scheduled or on-demand partitioned handler.
type Job struct {
	ID                string
	Code              string
	Handler           string
	PartitionStrategy string
	PartitionCount    int
	Concurrency       int
	Priority          string
	Cron              string
	TimeZone          string
	Exclusive         bool
	Status            string
	CheckpointRows    int
}

// Run is one execution of a job.
type Run struct {
	ID        string
	JobID     string
	Status    string
	RowsTotal int64
	RowsDone  int64
	Error     string
}

// Task is one partition of a run.
type Task struct {
	ID          string
	RunID       string
	PartitionNo int
	Status      string
	RowsDone    int64
	Attempts    int
	LastError   string
}

// CanStartRun enforces hold and exclusive overlap (AC-4.8).
func CanStartRun(job Job, running bool) error {
	if job.Status == JobHold {
		return errf(CodeJobHold, "job is on hold")
	}
	if job.Exclusive && running {
		return errf(CodeRunOverlap, "job already has a running instance")
	}
	return nil
}

// ValidateJob reports field errors.
func ValidateJob(job Job) map[string]string {
	fields := map[string]string{}
	if job.Code == "" {
		fields["code"] = "required"
	}
	if job.Handler == "" {
		fields["handler"] = "required"
	}
	if job.PartitionCount < 1 || job.PartitionCount > 256 {
		fields["partitionCount"] = "must be 1 to 256"
	}
	if job.Concurrency < 1 || job.Concurrency > 32 {
		fields["concurrency"] = "must be 1 to 32"
	}
	switch job.PartitionStrategy {
	case PartitionRange, PartitionHash, PartitionList, "":
	default:
		fields["partitionStrategy"] = "must be range, hash or list"
	}
	switch job.Priority {
	case PriorityA, PriorityB, PriorityC, "":
	default:
		fields["priority"] = "must be A, B or C"
	}
	return fields
}

// RangeBounds returns the half-open row range for a partition.
func RangeBounds(rows int64, partitions, partition int) (start, end int64) {
	if partitions <= 0 || partition < 0 || partition >= partitions {
		return 0, 0
	}
	start = rows * int64(partition) / int64(partitions)
	end = rows * int64(partition+1) / int64(partitions)
	return start, end
}
