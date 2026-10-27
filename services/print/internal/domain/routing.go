package domain

// Document types that routing rules may match (FR-32.13, §6.32.15).
var documentTypes = map[string]bool{
	"LP_LABEL": true, "LOCATION_LABEL": true, "ITEM_LABEL": true, "GS1_CARTON_LABEL": true,
	"PICK_LIST": true, "COUNT_SHEET": true, "COA": true, "BOL": true,
}

// ValidDocumentType reports whether t is a routable document type.
func ValidDocumentType(t string) bool { return documentTypes[t] }

// RoutingRule sends one document type to a queue when its scope matches. At most one
// scope field may be set; a rule with no scope is the document-type default.
type RoutingRule struct {
	ID           string `json:"id"`
	DocumentType string `json:"documentType"`
	UserID       string `json:"userId,omitempty"`
	Zone         string `json:"zone,omitempty"`
	WarehouseID  string `json:"warehouseId,omitempty"`
	QueueCode    string `json:"queueCode"`
	Sequence     int    `json:"sequence"`
}

// Document is what a job is being printed for: its type and the context it was raised in.
type Document struct {
	Type        string
	UserID      string
	Zone        string
	WarehouseID string
}

// tier orders scopes by specificity: 1 user, 2 zone, 3 warehouse, 4 document-type default (BR-32.12).
func (r RoutingRule) tier() int {
	switch {
	case r.UserID != "":
		return 1
	case r.Zone != "":
		return 2
	case r.WarehouseID != "":
		return 3
	}
	return 4
}

func (r RoutingRule) matches(d Document) bool {
	switch r.tier() {
	case 1:
		return r.UserID == d.UserID
	case 2:
		return r.Zone == d.Zone
	case 3:
		return r.WarehouseID == d.WarehouseID
	}
	return true
}

// Validate returns field-level errors for a rule, or none.
func (r RoutingRule) Validate() map[string]string {
	fields := map[string]string{}
	if !ValidDocumentType(r.DocumentType) {
		fields["documentType"] = "must be a routable document type"
	}
	if !ValidCode(r.QueueCode) {
		fields["queueCode"] = "must be a valid queue code"
	}
	if r.Sequence < 1 {
		fields["sequence"] = "must be at least 1"
	}
	scopes := 0
	for _, s := range []string{r.UserID, r.Zone, r.WarehouseID} {
		if s != "" {
			scopes++
		}
	}
	if scopes > 1 {
		fields["scope"] = "set at most one of userId, zone or warehouseId"
	}
	return fields
}

// ResolveRoute returns the rule that prints doc. The most specific tier wins; within a
// tier the lowest sequence wins. ok is false when nothing matches (MOB-0112 for ZPL).
func ResolveRoute(rules []RoutingRule, doc Document) (RoutingRule, bool) {
	var best RoutingRule
	found := false
	for _, r := range rules {
		if r.DocumentType != doc.Type || !r.matches(doc) {
			continue
		}
		if !found || r.tier() < best.tier() || (r.tier() == best.tier() && r.Sequence < best.Sequence) {
			best, found = r, true
		}
	}
	return best, found
}
