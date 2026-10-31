package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

const (
	CodeReqState      = "HCM-REQ-STATE"
	CodeReqNoVacancy  = "HCM-REQ-002"
	CodeCandidateMove = "HCM-CAN-STATE"
	CodeOfferState    = "HCM-OFR-STATE"
)

var stageOrder = []string{"APPLIED", "SCREEN", "INTERVIEW", "OFFER", "HIRED"}

func CandidateStageMove(from, to string) error {
	if to == "HIRED" {
		return &Error{Code: CodeCandidateMove, Detail: "a candidate becomes HIRED only through the hire of an accepted offer"}
	}
	if from == "HIRED" || from == "REJECTED" {
		return &Error{Code: CodeCandidateMove, Detail: fmt.Sprintf("a %s candidate cannot move", from)}
	}
	if to == "REJECTED" {
		return nil
	}
	for i, s := range stageOrder {
		if s == from && i+1 < len(stageOrder) && stageOrder[i+1] == to {
			return nil
		}
	}
	return &Error{Code: CodeCandidateMove, Detail: fmt.Sprintf("a candidate moves from %s to the next stage only", from)}
}

var requisitionMoves = map[string][]string{
	"DRAFT":    {"APPROVED", "CANCELED"},
	"APPROVED": {"OPEN", "CANCELED"},
	"OPEN":     {"CANCELED"},
}

func RequisitionMove(from, to string) error {
	for _, t := range requisitionMoves[from] {
		if t == to {
			return nil
		}
	}
	return &Error{Code: CodeReqState, Detail: fmt.Sprintf("a %s requisition cannot become %s", from, to)}
}

func CheckVacancy(positionFTE, assigned, claimedByOthers decimal.Decimal, headcount int, allowOverfill bool) error {
	if allowOverfill {
		return nil
	}
	need := positionFTE.Mul(decimal.NewFromInt(int64(headcount)))
	free := positionFTE.Sub(assigned).Sub(claimedByOthers)
	if free.LessThan(need) {
		return &Error{Code: CodeReqNoVacancy, Detail: fmt.Sprintf("the position has %s FTE vacant on the planned start date; the requisition needs %s", free.StringFixed(2), need.StringFixed(2))}
	}
	return nil
}

var offerMoves = map[string][]string{
	"DRAFT":    {"SENT", "WITHDRAWN"},
	"SENT":     {"ACCEPTED", "DECLINED", "WITHDRAWN"},
	"ACCEPTED": {"WITHDRAWN"},
}

func OfferMove(from, to string) error {
	for _, t := range offerMoves[from] {
		if t == to {
			return nil
		}
	}
	return &Error{Code: CodeOfferState, Detail: fmt.Sprintf("a %s offer cannot become %s", from, to)}
}
