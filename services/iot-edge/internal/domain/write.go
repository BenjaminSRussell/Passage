package domain

import (
	"errors"
	"fmt"
	"math"
)

// ErrWriteRefused wraps every refused setpoint write (BR-25.15).
var ErrWriteRefused = errors.New("edge write refused")

// CodeWriteRefused is SF-0017: the write is not on the allowlist, is out of its
// limits, or no control recipe is in execution for it.
const CodeWriteRefused = "SF-0017"

// CategoryDeviation is the process message category raised when a readback does
// not match the value written (BR-25.15).
const CategoryDeviation = "deviation"

// WriteRequest is one setpoint write a control recipe asks the connector to make.
// RecipeInExecution is true when an active control recipe for the order on that
// machine is in-execution.
type WriteRequest struct {
	Node              string
	Value             float64
	RecipeInExecution bool
}

// CheckWrite applies BR-25.15 before the connector writes anything. The node must
// be on the document's allowlist, the value must lie within that node's limits, and
// an in-execution control recipe must be active. Every refusal wraps
// ErrWriteRefused and names SF-0017.
func (c EdgeConfig) CheckWrite(req WriteRequest) error {
	var rule *WriteRule
	for i := range c.WriteRules {
		if c.WriteRules[i].Node == req.Node {
			rule = &c.WriteRules[i]
			break
		}
	}
	if rule == nil {
		return refused(req.Node, "node is not on the write allowlist")
	}
	// Written as a positive test so NaN fails the limit check.
	if !(req.Value >= rule.Min && req.Value <= rule.Max) {
		return refused(req.Node, fmt.Sprintf("value %v is outside %v..%v", req.Value, rule.Min, rule.Max))
	}
	if !req.RecipeInExecution {
		return refused(req.Node, "no control recipe is in execution for this machine")
	}
	return nil
}

func refused(node, why string) error {
	return fmt.Errorf("%w: %s: write to node %s refused: %s", ErrWriteRefused, CodeWriteRefused, node, why)
}

// ReadbackMatches reports whether the value read back after a write equals the
// value written, within an absolute tolerance. A false result raises a deviation.
func ReadbackMatches(written, readback, tolerance float64) bool {
	return math.Abs(written-readback) <= tolerance
}
