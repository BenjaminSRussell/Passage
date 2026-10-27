package domain

import "fmt"

// Outcome reports where a job landed.
type Outcome struct {
	Printer  Printer
	Attempts int
	Failover bool // true when a device other than the first in rank order printed
}

// Dispatch tries the queue in failover-rank order, giving each device
// attemptsPerDevice tries before moving to the next (BR-32.12, FR-32.13).
// An empty queue fails with MOB-0112; exhausting every device fails with MOB-0111.
func Dispatch(queue []Printer, attemptsPerDevice int, send func(Printer) error) (Outcome, error) {
	if len(queue) == 0 {
		return Outcome{}, &Error{CodeNoPrinter, "no printer is routed to this queue"}
	}
	if attemptsPerDevice < 1 {
		attemptsPerDevice = 1
	}
	attempts := 0
	last := queue[0]
	for i, p := range queue {
		for a := 0; a < attemptsPerDevice; a++ {
			attempts++
			if err := send(p); err == nil {
				return Outcome{Printer: p, Attempts: attempts, Failover: i > 0}, nil
			}
			last = p
		}
	}
	return Outcome{Printer: last, Attempts: attempts},
		&Error{CodeUnreachable, fmt.Sprintf("printer %s unreachable after %d attempts", last.Code, attempts)}
}
