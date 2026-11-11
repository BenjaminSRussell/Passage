// Package failover executes the steps of a failover plan (FR-50.5). Each step is an interface (a port) so the runner is
// tested with fakes; the adapters in adapters.go talk to the Kubernetes API, the temporal CLI, a DNS webhook and a
// verification command. The runner records every step, stops at the first failure and can resume: a step already DONE is
// skipped, and every adapter call is idempotent, so a retry after a crash repeats nothing harmful.
package failover

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
)

// Step actions, the Action values of domain.PlanFailover.
const (
	ActionFence    = "fence-old-primary"
	ActionPromote  = "promote-cnpg"
	ActionMirror   = "switch-mirrormaker"
	ActionTemporal = "activate-temporal"
	ActionDNS      = "flip-dns"
	ActionChain    = "verify-ledger-hash-chain"
)

// Step statuses.
const (
	StatusDone    = "DONE"
	StatusFailed  = "FAILED"
	StatusDryRun  = "DRY_RUN"
	StatusSkipped = "SKIPPED"
)

// Fencer fences a region's Postgres so it accepts no connection and no write (BR-50.1).
type Fencer interface {
	FenceRegion(ctx context.Context, region string) error
}

// Promoter promotes a region's replica cluster to the writable primary and returns when it is.
type Promoter interface {
	Promote(ctx context.Context, region string) error
}

// Mirror stops replication into the new primary: only one MirrorMaker direction runs at a time. The old primary starts
// replicating from the new one when it rejoins (failback runbook).
type Mirror interface {
	StopInto(ctx context.Context, newPrimary string) error
}

// Temporal makes region the active cluster of the replicated namespaces.
type Temporal interface {
	ActivateIn(ctx context.Context, region string) error
}

// DNS moves traffic to region.
type DNS interface {
	Flip(ctx context.Context, region string) error
}

// ChainVerifier checks the ledger hash chain in region (BR-50.3). A break is an error (OPS-1704, Sev-1).
type ChainVerifier interface {
	Verify(ctx context.Context, region string) error
}

// Executors is the set of ports a Runner drives. Every port is required.
type Executors struct {
	Fence    Fencer
	Promote  Promoter
	Mirror   Mirror
	Temporal Temporal
	DNS      DNS
	Chain    ChainVerifier
}

// StepResult is the outcome of one step, what the CLI reports back to the ops service.
type StepResult struct {
	Order      int       `json:"order"`
	Action     string    `json:"action"`
	Region     string    `json:"region"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// Reporter receives each step result as soon as it is known. It may be nil.
type Reporter func(ctx context.Context, res StepResult) error

// Runner executes a plan.
type Runner struct {
	Exec Executors
	// DryRun describes each step and calls no port.
	DryRun bool
	Now    func() time.Time
	Report Reporter
}

// ErrStepFailed wraps the error of the step that stopped the run.
var ErrStepFailed = errors.New("failover step failed")

// Describe says what a step does to which region; the dry run prints it.
func Describe(s domain.Step, plan domain.Plan) string {
	switch s.Action {
	case ActionFence:
		return fmt.Sprintf("fence the Postgres cluster of %s (cnpg.io/fencedInstances=*)", s.Region)
	case ActionPromote:
		return fmt.Sprintf("promote the replica cluster of %s to primary (drop spec.replica) and wait until it is healthy", s.Region)
	case ActionMirror:
		return fmt.Sprintf("stop MirrorMaker 2 in %s so replication runs only from it", s.Region)
	case ActionTemporal:
		return fmt.Sprintf("make %s the active cluster of the replicated Temporal namespaces", s.Region)
	case ActionDNS:
		return fmt.Sprintf("flip DNS to %s", s.Region)
	case ActionChain:
		return fmt.Sprintf("verify the ledger hash chain in %s (a break is Sev-1, OPS-1704)", s.Region)
	}
	return "unknown step " + s.Action
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func (r Runner) check() error {
	e := r.Exec
	if e.Fence == nil || e.Promote == nil || e.Mirror == nil || e.Temporal == nil || e.DNS == nil || e.Chain == nil {
		return errors.New("every executor port is required")
	}
	return nil
}

func (r Runner) do(ctx context.Context, s domain.Step) error {
	switch s.Action {
	case ActionFence:
		return r.Exec.Fence.FenceRegion(ctx, s.Region)
	case ActionPromote:
		return r.Exec.Promote.Promote(ctx, s.Region)
	case ActionMirror:
		return r.Exec.Mirror.StopInto(ctx, s.Region)
	case ActionTemporal:
		return r.Exec.Temporal.ActivateIn(ctx, s.Region)
	case ActionDNS:
		return r.Exec.DNS.Flip(ctx, s.Region)
	case ActionChain:
		return r.Exec.Chain.Verify(ctx, s.Region)
	}
	return fmt.Errorf("unknown step action %q", s.Action)
}

// Run executes plan.Steps in order. done lists actions already completed (a resume); they are reported SKIPPED. The run
// stops at the first failed step, which is the last result and is wrapped in ErrStepFailed. A dry run reports every step
// as DRY_RUN with its description and never calls a port.
func (r Runner) Run(ctx context.Context, plan domain.Plan, done map[string]bool) ([]StepResult, error) {
	if !r.DryRun {
		if err := r.check(); err != nil {
			return nil, err
		}
	}
	var out []StepResult
	for _, s := range plan.Steps {
		res := StepResult{Order: s.Order, Action: s.Action, Region: s.Region, StartedAt: r.now()}
		var runErr error
		switch {
		case done[s.Action]:
			res.Status, res.Detail = StatusSkipped, "already done"
		case r.DryRun:
			res.Status, res.Detail = StatusDryRun, Describe(s, plan)
		default:
			if runErr = r.do(ctx, s); runErr != nil {
				res.Status, res.Detail = StatusFailed, runErr.Error()
			} else {
				res.Status, res.Detail = StatusDone, Describe(s, plan)
			}
		}
		res.FinishedAt = r.now()
		out = append(out, res)
		if r.Report != nil && !r.DryRun {
			if err := r.Report(ctx, res); err != nil {
				return out, fmt.Errorf("report step %s: %w", s.Action, err)
			}
		}
		if runErr != nil {
			return out, fmt.Errorf("%w: %s: %v", ErrStepFailed, s.Action, runErr)
		}
	}
	return out, nil
}
