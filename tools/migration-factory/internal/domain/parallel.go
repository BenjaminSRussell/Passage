package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// ErrNotFound marks a reference to a record that the aggregate does not hold.
var ErrNotFound = errors.New("not found")

// ErrSoD is a review action by the person who started the run, or by nobody (9.49.17 MIG-001:
// MigrationRun and MigrationReview on the same variances is a HIGH conflict).
var ErrSoD = errors.New("segregation of duties")

// ParallelState is the parallel_run lifecycle (9.49.4).
type ParallelState string

const (
	ParallelReplaying ParallelState = "REPLAYING"
	ParallelDiffing   ParallelState = "DIFFING"
	ParallelReview    ParallelState = "REVIEW"
	ParallelSignedOff ParallelState = "SIGNED_OFF"
)

// Areas a variance belongs to (9.49.4 variance.area).
var Areas = []Area{"TB", "INV", "AP", "AR", "FA"}

// ParseArea validates a variance area.
func ParseArea(s string) (Area, error) {
	for _, a := range Areas {
		if string(a) == s {
			return a, nil
		}
	}
	return "", fmt.Errorf("%w: area %q is not TB, INV, AP, AR or FA", ErrInvalid, s)
}

// DefaultExplainedMaxPct is migration.parallel.explained_max_pct_revenue (9.49.13): 0.1 % of
// period revenue.
var DefaultExplainedMaxPct = decimal.RequireFromString("0.1")

// ParseExplainedMaxPct reads the explained-amount limit in percent of revenue: blank means the
// 0.1 default, and the value must lie in 0-1 (9.49.13).
func ParseExplainedMaxPct(s string) (decimal.Decimal, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultExplainedMaxPct, nil
	}
	v, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil || v.IsNegative() || v.GreaterThan(decimal.NewFromInt(1)) {
		return decimal.Decimal{}, fmt.Errorf("%w: explainedMaxPct %q must be a number from 0 to 1", ErrInvalid, s)
	}
	return v, nil
}

// ParallelVariance is a classified variance with the identity and audit fields the review needs.
type ParallelVariance struct {
	ID string
	Variance
	ExplainedBy string
	ExplainedAt *time.Time
}

// ParallelRun is one replayed legacy period compared with Passage (FR-49.5).
type ParallelRun struct {
	ID                string
	Period            string
	SourceSystem      SourceSystem
	DocumentsReplayed int
	Revenue           decimal.Decimal
	ExplainedMaxPct   decimal.Decimal
	InputHash         string
	State             ParallelState
	Variances         []ParallelVariance
	Summary           Summary
	StartedBy         string
	SignedOffBy       string
	SignedOffAt       *time.Time
	Version           int
	CreatedAt         time.Time
}

// ParallelInput is what a parallel run is started from: the legacy tuples, and the Passage
// tuples the replay produced (FR-49.5 step 1).
type ParallelInput struct {
	Period            string
	SourceSystem      SourceSystem
	DocumentsReplayed int
	Revenue           decimal.Decimal
	ExplainedMaxPct   decimal.Decimal
	Legacy            []Line
	Passage           []Line
}

// Validate checks the period (YYYY-MM), the counts and the tuples.
func (in ParallelInput) Validate() error {
	if _, err := time.Parse("2006-01", in.Period); err != nil || len(in.Period) != 7 {
		return fmt.Errorf("%w: period %q must be YYYY-MM", ErrInvalid, in.Period)
	}
	if in.DocumentsReplayed < 0 {
		return fmt.Errorf("%w: documentsReplayed must be >= 0", ErrInvalid)
	}
	if in.Revenue.IsNegative() {
		return fmt.Errorf("%w: revenue must be >= 0", ErrInvalid)
	}
	if len(in.Legacy)+len(in.Passage) == 0 {
		return fmt.Errorf("%w: a parallel run needs legacy or Passage tuples", ErrInvalid)
	}
	for _, side := range [][]Line{in.Legacy, in.Passage} {
		for _, l := range side {
			if strings.TrimSpace(l.Key) == "" {
				return fmt.Errorf("%w: %s tuple has a blank key", ErrInvalid, l.Area)
			}
		}
	}
	return nil
}

// Hash is the sha256 of the input in a canonical order, so the same tuples in any order hash
// the same. A repeated start for (period, system) with another hash is a different run.
func (in ParallelInput) Hash() string {
	lines := make([]string, 0, len(in.Legacy)+len(in.Passage))
	for _, l := range in.Legacy {
		lines = append(lines, "L|"+string(l.Area)+"|"+l.Key+"|"+l.Legacy.StringFixed(6))
	}
	for _, l := range in.Passage {
		lines = append(lines, "P|"+string(l.Area)+"|"+l.Key+"|"+l.Passage.StringFixed(6))
	}
	sort.Strings(lines)
	head := fmt.Sprintf("%s|%s|%d|%s|%s", in.Period, in.SourceSystem, in.DocumentsReplayed, in.Revenue.StringFixed(6), in.ExplainedMaxPct.StringFixed(6))
	sum := sha256.Sum256([]byte(head + "\n" + strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// StartParallelRun diffs and classifies the input with the tenant's variance rules and returns
// the run in REVIEW (FR-49.5 steps 2-5). newID names each variance.
func StartParallelRun(id string, in ParallelInput, rules []Rule, startedBy string, newID func() string) (ParallelRun, error) {
	if err := in.Validate(); err != nil {
		return ParallelRun{}, err
	}
	run := ParallelRun{ID: id, Period: in.Period, SourceSystem: in.SourceSystem, DocumentsReplayed: in.DocumentsReplayed,
		Revenue: in.Revenue, ExplainedMaxPct: in.ExplainedMaxPct, InputHash: in.Hash(), State: ParallelDiffing,
		StartedBy: startedBy, Version: 1}
	for _, v := range Diff(in.Legacy, in.Passage, rules, Explanations{}) {
		run.Variances = append(run.Variances, ParallelVariance{ID: newID(), Variance: v})
	}
	run.Summarize()
	run.State = ParallelReview
	return run, nil
}

// Summarize recomputes the run's aggregate from its variances.
func (p *ParallelRun) Summarize() {
	plain := make([]Variance, len(p.Variances))
	for i, v := range p.Variances {
		plain[i] = v.Variance
	}
	p.Summary = Summarize(plain)
}

func (p *ParallelRun) reviewer(by string) error {
	if strings.TrimSpace(by) == "" {
		return fmt.Errorf("%w: MIG-001 a review action needs a signed-in reviewer", ErrSoD)
	}
	if by == p.StartedBy {
		return fmt.Errorf("%w: MIG-001 %s started this parallel run and cannot review its variances", ErrSoD, by)
	}
	return nil
}

// Explain attaches a reason to an UNEXPLAINED variance, which becomes EXPLAINED (FR-49.5 step
// 3). Explaining a variance again with the same reason changes nothing and reports
// changed=false (idempotent by variance); another reason, or an EXPECTED variance, is refused.
func (p *ParallelRun) Explain(varianceID, reason, by string, at time.Time) (changed bool, err error) {
	if p.State != ParallelReview {
		return false, fmt.Errorf("%w: variances can be explained only in REVIEW, run is %s", ErrTransition, p.State)
	}
	if err := p.reviewer(by); err != nil {
		return false, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return false, fmt.Errorf("%w: an explanation needs a reason", ErrInvalid)
	}
	if utf8.RuneCountInString(reason) > 2000 {
		return false, fmt.Errorf("%w: the reason is longer than 2000 characters", ErrInvalid)
	}
	for i := range p.Variances {
		v := &p.Variances[i]
		if v.ID != varianceID {
			continue
		}
		switch v.Class {
		case ClassExplained:
			if v.Reason == reason {
				return false, nil
			}
			return false, fmt.Errorf("%w: variance %s is already explained", ErrTransition, varianceID)
		case ClassExpected:
			return false, fmt.Errorf("%w: variance %s is EXPECTED by rule %s and takes no explanation", ErrTransition, varianceID, v.RuleID)
		}
		v.Class, v.Reason, v.ExplainedBy, v.ExplainedAt = ClassExplained, reason, by, &at
		p.Summarize()
		p.Version++
		return true, nil
	}
	return false, fmt.Errorf("%w: variance %s is not in this parallel run", ErrNotFound, varianceID)
}

// SignOff moves a REVIEW run to SIGNED_OFF when the exit criterion holds: no unexplained
// variance and the explained amount within the limit (FR-49.5 step 6, MIG-1509).
func (p *ParallelRun) SignOff(by string, at time.Time) error {
	if p.State != ParallelReview {
		return fmt.Errorf("%w: sign-off needs REVIEW, run is %s", ErrTransition, p.State)
	}
	if err := p.reviewer(by); err != nil {
		return err
	}
	if err := SignOffGate(p.Summary, p.Revenue, p.ExplainedMaxPct); err != nil {
		return err
	}
	p.State, p.SignedOffBy, p.SignedOffAt = ParallelSignedOff, by, &at
	p.Version++
	return nil
}
