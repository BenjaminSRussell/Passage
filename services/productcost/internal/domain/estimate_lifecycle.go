package domain

import (
	"fmt"
	"sort"
)

// Sales estimate version statuses (BR-39.14).
const (
	EstimateFrozen     = "Frozen"
	EstimateSuperseded = "Superseded"
)

// CodeEstimateSuperseded is the warning raised when a configuration change
// supersedes a current sales order estimate (CST-0751).
const CodeEstimateSuperseded = "CST-0751"

// CodeNoEstimate extends the catalog: a line has no current estimate to
// freeze, or its current estimate failed to roll (CST-0763). It is the
// "estimate missing at confirmation" alert of sales-order-estimate-refresh.
const CodeNoEstimate = "CST-0763"

// EstimateVersion is one version of the cost estimate of a sales order line.
// Pending versions follow the configuration; a confirmed version is Frozen; a
// version replaced by a later configuration is Superseded.
type EstimateVersion struct {
	LineID     string        `json:"lineId"`
	Version    int           `json:"version"`
	ConfigHash string        `json:"configHash"`
	Status     string        `json:"status"`
	Estimate   SalesEstimate `json:"estimate"`
}

// RefreshResult reports the outcome of recomputing a line's estimate: the
// current version after the refresh, the full version history in version order,
// whether anything changed, and warnings such as CST-0751.
type RefreshResult struct {
	Current     EstimateVersion   `json:"current"`
	Versions    []EstimateVersion `json:"versions"`
	Changed     bool              `json:"changed"`
	Diagnostics []Diagnostic      `json:"diagnostics"`
}

// RefreshEstimate records a freshly computed estimate for a line whose
// configuration hashes to configHash. The current version is the highest-numbered
// version that is not Superseded. When it already carries configHash nothing
// changes. Otherwise every Pending or Frozen version is superseded, a new Pending
// version is appended, and a warning is raised if a previous current version was
// replaced by a configuration change.
func RefreshEstimate(prior []EstimateVersion, lineID, configHash string, est SalesEstimate) (RefreshResult, error) {
	versions, current, err := currentOf(prior, lineID)
	if err != nil {
		return RefreshResult{}, err
	}
	if configHash == "" {
		return RefreshResult{}, fmt.Errorf("configuration hash is required")
	}
	if current != nil && current.ConfigHash == configHash {
		return RefreshResult{Current: *current, Versions: versions, Changed: false, Diagnostics: []Diagnostic{}}, nil
	}
	res := RefreshResult{Changed: true, Diagnostics: []Diagnostic{}}
	next := 1
	for i := range versions {
		if versions[i].Version >= next {
			next = versions[i].Version + 1
		}
		if versions[i].Status == StatusPending || versions[i].Status == EstimateFrozen {
			versions[i].Status = EstimateSuperseded
		}
	}
	if current != nil {
		res.Diagnostics = append(res.Diagnostics, warnDiag(CodeEstimateSuperseded,
			fmt.Sprintf("estimate version %d superseded by configuration change", current.Version), lineID))
	}
	fresh := EstimateVersion{LineID: lineID, Version: next, ConfigHash: configHash, Status: StatusPending, Estimate: est}
	versions = append(versions, fresh)
	res.Current = fresh
	res.Versions = versions
	return res, nil
}

// FreezeEstimate confirms the current estimate of a line (BR-39.14). A Pending
// current version becomes Frozen; a Frozen one stays as it is, so confirmation
// is idempotent. A line without a usable current estimate fails with CST-0763.
func FreezeEstimate(prior []EstimateVersion, lineID string) ([]EstimateVersion, EstimateVersion, error) {
	versions, current, err := currentOf(prior, lineID)
	if err != nil {
		return nil, EstimateVersion{}, err
	}
	if current == nil {
		return nil, EstimateVersion{}, &CodedError{Code: CodeNoEstimate, Detail: "line " + lineID + " has no estimate to freeze"}
	}
	if current.Estimate.Status != StatusPending {
		return nil, EstimateVersion{}, &CodedError{Code: CodeNoEstimate,
			Detail: fmt.Sprintf("estimate of line %s has status %s and cannot be frozen", lineID, current.Estimate.Status)}
	}
	for i := range versions {
		if versions[i].Version == current.Version {
			versions[i].Status = EstimateFrozen
			return versions, versions[i], nil
		}
	}
	return nil, EstimateVersion{}, fmt.Errorf("current estimate vanished")
}

// currentOf copies the versions of one line in version order and returns the
// highest-numbered version that is not Superseded, or nil when there is none.
// A version of another line is an error.
func currentOf(prior []EstimateVersion, lineID string) ([]EstimateVersion, *EstimateVersion, error) {
	if lineID == "" {
		return nil, nil, fmt.Errorf("line id is required")
	}
	versions := make([]EstimateVersion, 0, len(prior))
	seen := map[int]bool{}
	for _, v := range prior {
		if v.LineID != lineID {
			return nil, nil, fmt.Errorf("version %d belongs to line %s, not %s", v.Version, v.LineID, lineID)
		}
		if v.Version < 1 || seen[v.Version] {
			return nil, nil, fmt.Errorf("version numbers must be unique and positive, got %d", v.Version)
		}
		seen[v.Version] = true
		switch v.Status {
		case StatusPending, EstimateFrozen, EstimateSuperseded:
		default:
			return nil, nil, fmt.Errorf("version %d has status %q; must be Pending, Frozen or Superseded", v.Version, v.Status)
		}
		versions = append(versions, v)
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
	for i := len(versions) - 1; i >= 0; i-- {
		if versions[i].Status != EstimateSuperseded {
			cur := versions[i]
			return versions, &cur, nil
		}
	}
	return versions, nil, nil
}
