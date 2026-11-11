package domain

import "time"

// Game-day scorecard (PSG-50, AC-50.3): the same measures as deploy/multi-region/gameday/score.py, computed from the steps a
// failover recorded. RTO runs from the start of the fence step to the end of the DNS flip (fence to DNS verified); RPO is the
// replication lag at the fence, which is the data the target could be missing.
const (
	DefaultRTOTargetMinutes = 60
	DefaultRPOTargetSeconds = 300
)

// StepTiming is one recorded step.
type StepTiming struct {
	Action     string
	Status     string
	StartedAt  time.Time
	FinishedAt time.Time
}

// Scorecard is the result of a failover measured against the targets.
type Scorecard struct {
	Complete         bool     `json:"complete"`
	RTOSeconds       *float64 `json:"rtoSeconds,omitempty"`
	RTOTargetSeconds int      `json:"rtoTargetSeconds"`
	RTOMet           bool     `json:"rtoMet"`
	RPOSeconds       int      `json:"rpoSeconds"`
	RPOTargetSeconds int      `json:"rpoTargetSeconds"`
	RPOMet           bool     `json:"rpoMet"`
	DataLossAccepted bool     `json:"dataLossAccepted"`
	ChainVerified    bool     `json:"ledgerHashChainVerified"`
	// Passed is true when the failover is complete, both targets are met, the hash chain verified and no data loss was accepted.
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures"`
}

// ScoreFailover measures a failover. totalSteps is the length of the plan. A step counts only when DONE.
func ScoreFailover(plan Plan, steps []StepTiming, rtoTargetMinutes, rpoTargetSeconds int) Scorecard {
	if rtoTargetMinutes <= 0 {
		rtoTargetMinutes = DefaultRTOTargetMinutes
	}
	if rpoTargetSeconds <= 0 {
		rpoTargetSeconds = DefaultRPOTargetSeconds
	}
	by := map[string]StepTiming{}
	done := 0
	for _, s := range steps {
		if s.Status == "DONE" {
			by[s.Action] = s
			done++
		}
	}
	sc := Scorecard{
		RTOTargetSeconds: rtoTargetMinutes * 60, RPOSeconds: plan.LagSeconds, RPOTargetSeconds: rpoTargetSeconds,
		DataLossAccepted: plan.AcceptDataLoss, Failures: []string{},
	}
	sc.Complete = len(plan.Steps) > 0 && done >= len(plan.Steps)
	sc.RPOMet = sc.RPOSeconds <= rpoTargetSeconds
	_, sc.ChainVerified = by["verify-ledger-hash-chain"]
	fence, okF := by["fence-old-primary"]
	flip, okD := by["flip-dns"]
	if okF && okD {
		rto := flip.FinishedAt.Sub(fence.StartedAt).Seconds()
		sc.RTOSeconds = &rto
		sc.RTOMet = rto >= 0 && rto <= float64(sc.RTOTargetSeconds)
	}
	if !sc.Complete {
		sc.Failures = append(sc.Failures, "not every step of the plan is DONE")
	}
	if sc.RTOSeconds == nil {
		sc.Failures = append(sc.Failures, "RTO cannot be measured: the fence and DNS steps are not both DONE")
	} else if !sc.RTOMet {
		sc.Failures = append(sc.Failures, "RTO over the target")
	}
	if !sc.RPOMet {
		sc.Failures = append(sc.Failures, "RPO over the target")
	}
	if !sc.ChainVerified {
		sc.Failures = append(sc.Failures, "the ledger hash chain is not verified")
	}
	if sc.DataLossAccepted {
		sc.Failures = append(sc.Failures, "the failover accepted data loss (AC-50.3 needs zero data loss)")
	}
	sc.Passed = len(sc.Failures) == 0
	return sc
}
