package domain

import "fmt"

const (
	LinePending  = "pending"
	LineSent     = "sent"
	LineAccepted = "accepted"
	LineRejected = "rejected"
	LineFailed   = "failed"

	ReleaseManual = "manual"
	ReleaseEco    = "eco"
	ReleaseJob    = "job"
)

// EngineeringOrganization flags a legal entity that owns engineering products (FR-9.14).
type EngineeringOrganization struct {
	ID            string
	LegalEntityID string
	Code          string
	Name          string
}

// EngineeringCategory is FR-9.18: track_versions products are owned by one engineering organization.
type EngineeringCategory struct {
	ID                 string
	Code               string
	Name               string
	ProductType        string
	TrackVersions      bool
	VersionAsDimension bool
	NomenclatureID     string
	ReadinessPolicyID  string
	ReleasePolicyID    string
	ChangeControlled   bool
}

// EngineeringVersion is one engineering revision of a product, maintained only in its organization.
type EngineeringVersion struct {
	ID              string
	ProductID       string
	VersionCode     string
	EngineeringLEID string
	BOMVersionID    string
	RouteVersionID  string
	LifecycleState  string
	CategoryID      string
}

// ReleasePolicy lists what a release session copies to an operational legal entity (FR-9.15).
type ReleasePolicy struct {
	ID                string
	Code              string
	CopyBOM           bool
	ActivateBOM       bool
	CopyRoute         bool
	ActivateRoute     bool
	CopyAttributes    bool
	CopyOrderSettings bool
	CopyDocuments     bool
	RequireAcceptance map[string]bool
}

// SiteMapping maps an engineering site onto a receiving entity's site.
type SiteMapping struct {
	EngineeringLEID string
	OperationalLEID string
	EngineeringSite string
	OperationalSite string
}

// ReleaseSession is one FR-9.15 workflow of product lines to receiving entities.
type ReleaseSession struct {
	ID        string
	Number    string
	CreatedBy string
	Source    string
	PolicyID  string
	EcoID     string

	EngineeringLEID   string
	ReadinessPolicyID string
}

// ReleaseLine is one (product, engineering version, legal entity) row (BR-9.13).
type ReleaseLine struct {
	ID                   string
	SessionID            string
	ProductID            string
	EngineeringVersion   string
	LegalEntityID        string
	Status               string
	Reason               string
	AcceptedBy           string
	Retries              int
	CopiedBOMVersionID   string
	CopiedRouteVersionID string
}

func (l *ReleaseLine) Send() error {
	if l.Status != LinePending {
		return errf(CodeReleaseState, "release line is %s, only pending can be sent", l.Status)
	}
	l.Status = LineSent
	return nil
}

func (l *ReleaseLine) Accept(actor, reason string) error {
	if l.Status != LineSent {
		return errf(CodeReleaseState, "release line is %s, only a sent line can be accepted", l.Status)
	}
	l.Status, l.AcceptedBy, l.Reason = LineAccepted, actor, reason
	return nil
}

func (l *ReleaseLine) Reject(actor, reason string) error {
	if l.Status != LineSent {
		return errf(CodeReleaseState, "release line is %s, only a sent line can be rejected", l.Status)
	}
	l.Status, l.AcceptedBy, l.Reason = LineRejected, actor, reason
	return nil
}

// ApplyRelease builds one line per receiving entity. Entities that do not require acceptance
// are accepted immediately (AC-9.11 DEMF); the others wait in Open product releases as sent.
func ApplyRelease(session ReleaseSession, policy ReleasePolicy, productID, versionCode string, legalEntities []string) []ReleaseLine {
	lines := make([]ReleaseLine, 0, len(legalEntities))
	for _, le := range legalEntities {
		line := ReleaseLine{SessionID: session.ID, ProductID: productID, EngineeringVersion: versionCode, LegalEntityID: le, Status: LinePending}
		_ = line.Send()
		if !policy.RequireAcceptance[le] {
			_ = line.Accept(session.CreatedBy, "")
		}
		lines = append(lines, line)
	}
	return lines
}

// CheckOwnedField refuses a local edit of engineering-owned data outside the owning organization (FR-9.14).
func CheckOwnedField(field, owningOrg, actingOrg string) error {
	if owningOrg != "" && actingOrg != owningOrg {
		return errf(CodeOwnedField, "%s is owned by engineering organization %s.", field, owningOrg)
	}
	return nil
}

// ReleaseNumber formats REL-{yyyy}-{#####} (FR-9.15).
func ReleaseNumber(year, seq int) string {
	return fmt.Sprintf("REL-%04d-%05d", year, seq)
}
