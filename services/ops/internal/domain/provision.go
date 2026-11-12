package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/residency"
)

// Tenant provisioning (PSG-50, FR-50.8, AC-50.6; docs/ops/tenant-provisioning.md). A provision is recorded with its ordered
// plan; the provisioning runner executes each step and reports it, every step is idempotent so a retry resumes, and the
// provision is complete when every step is DONE. The residency check (OPS-1705) runs before anything is recorded.
const (
	CodeProvisionUnknown  = "OPS-1721"
	CodeProvisionInvalid  = "OPS-1722"
	CodeProvisionConflict = "OPS-1723"
	CodeProvisionLicense  = "OPS-1724"
	// CodeResidency is the residency refusal of platform/go/residency: the region is not deployed, or it lies outside the
	// tenant's residency.
	CodeResidency = residency.CodeRegionNotAllowed

	ProvisionRunning  = "PROVISIONING"
	ProvisionDone     = "PROVISIONED"
	ProvisionFailed   = "FAILED"
	DefaultProvisionT = 15 * time.Minute
)

// ProvisionSteps are the ordered steps of docs/ops/tenant-provisioning.md after the residency check (step 1, done before the
// provision is recorded) and before the event (step 7, written with the last DONE step).
var ProvisionSteps = []string{"realm", "databases", "kafka-acls", "lakehouse", "license"}

// Tiers are provisioning.default_tier values: a production tenant or a sandbox.
var Tiers = []string{"PROD", "DEV", "TEST", "UAT"}

// ProvisionRequest is a validated provision.
type ProvisionRequest struct {
	TenantID  string `json:"tenantId"`
	Name      string `json:"name"`
	Residency string `json:"residency"`
	Region    string `json:"region"`
	Tier      string `json:"tier"`
}

// ValidStep reports whether a is a provisioning step.
func ValidStep(a string) bool {
	for _, s := range ProvisionSteps {
		if s == a {
			return true
		}
	}
	return false
}

// ValidateProvision normalizes and checks a request against the deployed regions: the region must be one the platform runs
// (OPS-1705, "residency region is deployed") and must lie inside the residency (platform/go/residency, OPS-1705).
func ValidateProvision(req ProvisionRequest, deployed []Region) (ProvisionRequest, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Residency = residency.NormalizeCode(req.Residency)
	req.Region = residency.NormalizeRegion(req.Region)
	req.Tier = strings.ToUpper(strings.TrimSpace(req.Tier))
	if req.Tier == "" {
		req.Tier = "PROD"
	}
	switch {
	case req.Name == "" || len(req.Name) > 200:
		return req, fail(CodeProvisionInvalid, "name is required (at most 200 characters)")
	case !validTier(req.Tier):
		return req, fail(CodeProvisionInvalid, "tier must be one of %s", strings.Join(Tiers, ", "))
	case !residency.Valid(req.Residency):
		return req, fail(CodeProvisionInvalid, "residency must be one of %s", strings.Join(residency.Codes(), ", "))
	}
	if req.Region == "" {
		return req, fail(CodeResidency, "a tenant is placed in a named region: give the region")
	}
	known := false
	for _, r := range deployed {
		if r.Name == req.Region {
			known = true
			break
		}
	}
	if !known {
		return req, fail(CodeResidency, "region %s is not deployed on this platform; tenant not provisioned", req.Region)
	}
	if err := residency.Check(req.Residency, req.Region); err != nil {
		var refused *residency.Error
		if errors.As(err, &refused) {
			return req, &Error{Code: refused.Code, Detail: refused.Detail}
		}
		return req, err
	}
	return req, nil
}

func validTier(t string) bool {
	for _, x := range Tiers {
		if x == t {
			return true
		}
	}
	return false
}
