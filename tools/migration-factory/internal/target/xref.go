package target

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

const (
	audMigration     = "migration"
	dutyXrefRead     = "migration.xref.read"
	pathXrefResolve  = "/v1/integration/xref:resolve"
	maxXrefKeysAsked = 500
)

// Xref resolves legacy keys through the migration service's cross-reference (services/migration, H-S8-6): the Passage ids the
// party migration loaded, so an open item's legacy customer needs no hand-made map.
type Xref struct{ c *Receivables }

// NewXref builds the client. A zero Config is valid and reports Configured false.
func NewXref(cfg Config) *Xref {
	c := NewReceivables(cfg)
	c.aud = audMigration
	return &Xref{c: c}
}

// Configured reports whether a lookup can be attempted: an address and a signing key.
func (x *Xref) Configured() bool { return x != nil && x.c.Configured() }

// Resolve returns the Passage id of each key of the entity from the source system that the cross-reference holds, within the
// legal entity's scope; keys it does not hold are left out. Keys are sent 500 to a request.
func (x *Xref) Resolve(ctx context.Context, tenantID, legalEntityID, system, entity string, keys []string) (map[string]string, error) {
	if !x.Configured() {
		return nil, ErrNotConfigured
	}
	out := map[string]string{}
	for start := 0; start < len(keys); start += maxXrefKeysAsked {
		chunk := keys[start:min(start+maxXrefKeysAsked, len(keys))]
		q := url.Values{"sourceSystem": {system}, "entity": {entity}, "keys": {strings.Join(chunk, ",")}}
		status, body, err := x.c.do(ctx, http.MethodGet, pathXrefResolve+"?"+q.Encode(), tenantID, legalEntityID, dutyXrefRead, "", nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, answerError(status, body)
		}
		var res struct {
			Items []struct {
				SourceKey string `json:"sourceKey"`
				TargetID  string `json:"targetId"`
			} `json:"items"`
		}
		if json.Unmarshal(body, &res) != nil {
			return nil, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the cross-reference answer is not readable"}
		}
		for _, it := range res.Items {
			out[it.SourceKey] = it.TargetID
		}
	}
	return out, nil
}

// MigrationSystem is the migration service's name of a factory source system (services/migration connection.go).
func MigrationSystem(factory string) string {
	switch factory {
	case "AX2012":
		return "ax2012"
	case "D365FO":
		return "d365fo"
	case "SAPECC":
		return "sap_ecc"
	case "S4HANA":
		return "sap_s4"
	}
	return strings.ToLower(factory)
}
