package domain

import "strings"

const minReopenReason = 10

type ReopenApproval struct {
	UserID string `json:"userId"`
}

type ReopenRequest struct {
	RequestedBy string
	Required    int
	Reason      string
	Approvals   []ReopenApproval
}

func ValidateReopenReason(reason string) error {
	if len(strings.TrimSpace(reason)) < minReopenReason {
		return fail(CodeValidation, "reason must be at least 10 characters")
	}
	return nil
}

func ApproveReopen(req ReopenRequest, actor string) (ReopenRequest, bool, error) {
	if strings.TrimSpace(actor) == "" {
		return req, false, fail(CodeValidation, "approver is required")
	}
	if actor == req.RequestedBy {
		return req, false, fail(CodeInvalidTransition, "the requester cannot approve their own reopen")
	}
	for _, a := range req.Approvals {
		if a.UserID == actor {
			return req, false, fail(CodeInvalidTransition, "approver "+actor+" has already approved")
		}
	}
	needed := req.Required
	if needed < 2 {
		needed = 2
	}
	req.Approvals = append(req.Approvals, ReopenApproval{UserID: actor})
	return req, len(req.Approvals) >= needed, nil
}
