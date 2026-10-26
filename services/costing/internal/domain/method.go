// Package domain holds the pure costing rules of the costing service: the
// settlement engine that values issues against receipts, the purchase price
// variance rules and the error type returned by both. It performs no I/O.
package domain

import "fmt"

// Method is the costing method of an item model group (FR-20.1).
type Method string

const (
	FIFO                Method = "FIFO"
	LIFO                Method = "LIFO"
	LIFODate            Method = "LIFO_DATE"
	WeightedAvgRunning  Method = "WEIGHTED_AVG_RUNNING"
	WeightedAvgPeriodic Method = "WEIGHTED_AVG_PERIODIC"
	MovingAvg           Method = "MOVING_AVG"
	Standard            Method = "STANDARD"
)

// Valid reports whether m is one of the seven costing methods.
func (m Method) Valid() bool {
	switch m {
	case FIFO, LIFO, LIFODate, WeightedAvgRunning, WeightedAvgPeriodic, MovingAvg, Standard:
		return true
	}
	return false
}

// Error is a domain rule violation carrying a stable message-catalog code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

// Message-catalog codes raised by the domain.
const (
	CodeInvalidTxn   = "CST-TXN-001"
	CodeDuplicateTxn = "CST-TXN-002"
	CodeInvalidPPV   = "CST-PPV-001"
)
