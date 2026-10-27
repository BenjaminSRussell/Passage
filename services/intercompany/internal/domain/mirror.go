package domain

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// OrderLine is one line of a sales order in the seller's document.
type OrderLine struct {
	Qty       decimal.Decimal
	UnitPrice decimal.Decimal
}

// Order is an intercompany sales order in document currency.
type Order struct {
	Currency string
	Lines    []OrderLine
}

// Total sums the rounded line amounts in document currency (BR-40.2).
func (o Order) Total() (money.Amount, error) {
	amounts := make([]money.Amount, 0, len(o.Lines))
	for _, l := range o.Lines {
		amounts = append(amounts, money.Amount{Value: l.Qty.Mul(l.UnitPrice), Currency: o.Currency}.Round())
	}
	return money.Sum(o.Currency, amounts...)
}

// SagaStatus is the BR-40.1 state of a mirror saga.
type SagaStatus string

const (
	StatusCreating    SagaStatus = "Creating"
	StatusLinked      SagaStatus = "Linked"
	StatusCompensated SagaStatus = "Compensated"
	StatusFailed      SagaStatus = "Failed"
)

// Saga steps in execution order (FR-40.2).
const (
	StepValidate     = "validate"
	StepReserve      = "reserve_numbers"
	StepCreateSource = "create_source"
	StepCreateMirror = "create_mirror"
	StepLink         = "link"
)

// SagaSteps lists the steps in execution order.
var SagaSteps = []string{StepValidate, StepReserve, StepCreateSource, StepCreateMirror, StepLink}

// Document types created by the saga.
const (
	DocSalesOrder    = "sales_order"
	DocPurchaseOrder = "purchase_order"
)

// Fault injects a failure for tests and the sweeper: Step fails, and when
// CompensationFails is set the cancellation of the first created document
// fails too, leaving it live for manual action.
type Fault struct {
	Step              string
	CompensationFails bool
}

// Doc is a document created by the saga.
type Doc struct {
	ID     string
	Entity string
	Type   string
	Amount money.Amount
	Live   bool
}

// Link is the IcDocumentLink created when both documents exist.
type Link struct {
	PairID      string
	SourceDocID string
	MirrorDocID string
}

// Saga is the in-memory result of one mirror run. The documents slice stands
// in for the two entities' databases; the Temporal workflow is a later slice.
type Saga struct {
	Status     SagaStatus
	FailedStep string
	Reason     string
	Docs       []Doc
	Link       *Link
}

// RunMirrorSaga runs FR-40.2 for one sales order against partner p. The mirror
// purchase order carries the same lines, so its amount equals the source in
// document currency. Any failure cancels every created document in reverse order.
func RunMirrorSaga(p Partner, src Order, pairID, sourceID, mirrorID string, f Fault) Saga {
	s := Saga{Status: StatusCreating}
	for _, step := range SagaSteps {
		if f.Step == step {
			s.fail(step, "injected failure at "+step, f)
			return s
		}
		if err := s.do(step, p, src, pairID, sourceID, mirrorID); err != nil {
			s.fail(step, err.Error(), f)
			return s
		}
	}
	s.Status = StatusLinked
	return s
}

func (s *Saga) do(step string, p Partner, src Order, pairID, sourceID, mirrorID string) error {
	switch step {
	case StepValidate:
		if !p.Active {
			return &Error{Code: CodeNoPolicy, Detail: fmt.Sprintf("IC partner policy %s to %s is inactive", p.SellerEntity, p.BuyerEntity)}
		}
		return ValidatePartner(p)
	case StepReserve:
		return nil // numbers are reserved by the caller; the sequence service is a later slice
	case StepCreateSource, StepCreateMirror:
		total, err := src.Total()
		if err != nil {
			return err
		}
		doc := Doc{ID: sourceID, Entity: p.SellerEntity, Type: DocSalesOrder, Amount: total, Live: true}
		if step == StepCreateMirror {
			doc = Doc{ID: mirrorID, Entity: p.BuyerEntity, Type: DocPurchaseOrder, Amount: total, Live: true}
		}
		s.Docs = append(s.Docs, doc)
		return nil
	case StepLink:
		if len(s.Docs) != 2 {
			return fmt.Errorf("link needs two documents, have %d", len(s.Docs))
		}
		s.Link = &Link{PairID: pairID, SourceDocID: s.Docs[0].ID, MirrorDocID: s.Docs[1].ID}
		return nil
	}
	return fmt.Errorf("unknown saga step %q", step)
}

// fail records the failed step and compensates. Compensation cancels live
// documents newest first. With CompensationFails the oldest document stays
// live and the saga ends Failed (BR-40.1, IC-0803).
func (s *Saga) fail(step, reason string, f Fault) {
	s.FailedStep, s.Reason = step, reason
	s.Link = nil
	for i := len(s.Docs) - 1; i >= 0; i-- {
		if f.CompensationFails && i == 0 {
			s.Status = StatusFailed
			s.Reason += "; compensation failed for " + s.Docs[i].ID
			return
		}
		s.Docs[i].Live = false
	}
	s.Status = StatusCompensated
}

// CheckInvariants verifies the FR-40.2 and BR-40.1 guarantees for a finished
// saga: Linked means two live documents with equal amounts, Compensated means
// no live document, Failed means a reported step with the orphan still live.
func (s Saga) CheckInvariants() error {
	live := 0
	for _, d := range s.Docs {
		if d.Live {
			live++
		}
	}
	switch s.Status {
	case StatusLinked:
		if s.Link == nil || len(s.Docs) != 2 || live != 2 {
			return fmt.Errorf("linked saga without two live documents")
		}
		a, b := s.Docs[0].Amount, s.Docs[1].Amount
		if a.Currency != b.Currency || !a.Value.Equal(b.Value) {
			return fmt.Errorf("linked saga amounts differ: %s vs %s", a, b)
		}
	case StatusCompensated:
		if live != 0 || s.Link != nil {
			return fmt.Errorf("compensated saga leaves %d live documents", live)
		}
	case StatusFailed:
		if live == 0 || s.FailedStep == "" {
			return fmt.Errorf("failed saga must report a step and keep the orphan")
		}
	default:
		return fmt.Errorf("saga ended in state %q", s.Status)
	}
	return nil
}
