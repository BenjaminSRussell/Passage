package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// Pipeline states (BR-46.6).
const (
	PipelineRunning      = "RUNNING"
	PipelinePausedSchema = "PAUSED_SCHEMA"
	PipelinePausedError  = "PAUSED_ERROR"

	// MaxConsecutiveFailures is the sink failure count that pauses a pipeline.
	MaxConsecutiveFailures = 5
)

// ChangeKind classifies one source DDL change (FR-46.3).
type ChangeKind string

const (
	AddColumn    ChangeKind = "add_column"
	DropColumn   ChangeKind = "drop_column"
	RenameColumn ChangeKind = "rename_column"
	WidenType    ChangeKind = "widen_type"
	NarrowType   ChangeKind = "narrow_type"
	ChangeType   ChangeKind = "change_type"
	SetNotNull   ChangeKind = "set_not_null"
	DropNotNull  ChangeKind = "drop_not_null"
)

// Verdict is what the sink does with a change.
type Verdict string

const (
	AutoApply Verdict = "AUTO"
	Pause     Verdict = "PAUSE"
)

// SQLType is the subset of column types the lakehouse maps: integers,
// decimals with precision and scale, and varchar (Length 0 = unbounded text).
type SQLType struct {
	Name      string
	Precision int
	Scale     int
	Length    int
}

var typeSyntax = regexp.MustCompile(`^(int|bigint|decimal\((\d+),(\d+)\)|varchar\((\d+)\)|text)$`)

// ParseType reads a type such as "decimal(18,4)" or "varchar(50)".
func ParseType(s string) (SQLType, error) {
	m := typeSyntax.FindStringSubmatch(s)
	if m == nil {
		return SQLType{}, fmt.Errorf("unsupported column type %q", s)
	}
	switch {
	case m[2] != "":
		p, _ := strconv.Atoi(m[2])
		sc, _ := strconv.Atoi(m[3])
		return SQLType{Name: "decimal", Precision: p, Scale: sc}, nil
	case m[4] != "":
		n, _ := strconv.Atoi(m[4])
		return SQLType{Name: "varchar", Length: n}, nil
	case m[1] == "text":
		return SQLType{Name: "varchar"}, nil
	}
	return SQLType{Name: m[1]}, nil
}

// widens reports whether a column of type from can be read as type to without
// losing values: integer ranks grow, decimals keep their integer digits and
// scale, and varchar lengths grow (unbounded text is the widest).
func widens(from, to SQLType) bool {
	switch {
	case from.Name == "int" && to.Name == "bigint":
		return true
	case from.Name == "decimal" && to.Name == "decimal":
		return to.Scale >= from.Scale && to.Precision-to.Scale >= from.Precision-from.Scale
	case from.Name == "varchar" && to.Name == "varchar":
		return to.Length == 0 || (from.Length != 0 && to.Length >= from.Length)
	}
	return false
}

// Change is one source column change. Type fields are used by the type kinds.
type Change struct {
	Kind       ChangeKind `json:"kind"`
	Nullable   bool       `json:"nullable,omitempty"`
	HasDefault bool       `json:"hasDefault,omitempty"`
	From       string     `json:"from,omitempty"`
	To         string     `json:"to,omitempty"`
}

// Classify decides whether the sink applies a change automatically or pauses
// for an operator to approve the mapping. The rules are additive-only: anything
// that can drop or truncate values pauses.
func Classify(c Change) (Verdict, error) {
	switch c.Kind {
	case AddColumn:
		if c.Nullable || c.HasDefault {
			return AutoApply, nil
		}
		return Pause, nil
	case DropColumn, RenameColumn, DropNotNull:
		return AutoApply, nil
	case SetNotNull, ChangeType, NarrowType:
		return Pause, nil
	case WidenType:
		from, to, err := parseChange(c)
		if err != nil {
			return Pause, err
		}
		if widens(from, to) {
			return AutoApply, nil
		}
		return Pause, nil
	}
	return Pause, fmt.Errorf("unknown change kind %q", c.Kind)
}

func parseChange(c Change) (SQLType, SQLType, error) {
	from, err := ParseType(c.From)
	if err != nil {
		return SQLType{}, SQLType{}, err
	}
	to, err := ParseType(c.To)
	if err != nil {
		return SQLType{}, SQLType{}, err
	}
	return from, to, nil
}

// Pipeline is the state of one CDC table pipeline (cdc_pipeline_state).
type Pipeline struct {
	Domain              string
	Table               string
	Status              string
	LastLSN             int64
	SchemaVersion       int
	ConsecutiveFailures int
}

var (
	ErrLSNRegression = errors.New("analytics: source LSN must not decrease")
	ErrWrongState    = errors.New("analytics: pipeline is not in a state that allows this action")
)

// NewPipeline starts a pipeline in RUNNING at LSN 0 and schema version 1.
func NewPipeline(domain, table string) Pipeline {
	return Pipeline{Domain: domain, Table: table, Status: PipelineRunning, SchemaVersion: 1}
}

// CommitLSN records a committed source position. Redelivery of the same LSN is
// fine; a lower LSN is refused, so last_lsn never decreases.
func (p *Pipeline) CommitLSN(lsn int64) error {
	if p.Status != PipelineRunning {
		return ErrWrongState
	}
	if lsn < p.LastLSN {
		return ErrLSNRegression
	}
	p.LastLSN, p.ConsecutiveFailures = lsn, 0
	return nil
}

// SinkFailed counts a failed sink commit and pauses with PAUSED_ERROR after
// MaxConsecutiveFailures in a row.
func (p *Pipeline) SinkFailed() {
	if p.Status != PipelineRunning {
		return
	}
	p.ConsecutiveFailures++
	if p.ConsecutiveFailures >= MaxConsecutiveFailures {
		p.Status = PipelinePausedError
	}
}

// ApplySchemaChange classifies the change. An AUTO change bumps the schema
// version; a PAUSE change moves the pipeline to PAUSED_SCHEMA.
func (p *Pipeline) ApplySchemaChange(c Change) (Verdict, error) {
	if p.Status != PipelineRunning {
		return "", ErrWrongState
	}
	v, err := Classify(c)
	if err != nil {
		return v, err
	}
	if v == Pause {
		p.Status = PipelinePausedSchema
		return v, nil
	}
	p.SchemaVersion++
	return v, nil
}

// ApproveMapping resumes a PAUSED_SCHEMA pipeline after an operator approves the
// mapping for the incompatible change.
func (p *Pipeline) ApproveMapping() error {
	if p.Status != PipelinePausedSchema {
		return ErrWrongState
	}
	p.Status = PipelineRunning
	p.SchemaVersion++
	return nil
}

// Replay resumes a PAUSED_ERROR pipeline from its stored LSN.
func (p *Pipeline) Replay() error {
	if p.Status != PipelinePausedError {
		return ErrWrongState
	}
	p.Status, p.ConsecutiveFailures = PipelineRunning, 0
	return nil
}
