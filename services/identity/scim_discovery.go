package identity

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type schemaAttribute struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	MultiValued bool   `json:"multiValued"`
	Required    bool   `json:"required"`
	CaseExact   bool   `json:"caseExact"`
	Mutability  string `json:"mutability"`
	Returned    string `json:"returned"`
	Uniqueness  string `json:"uniqueness"`
}

func attribute(name, typ string, required bool, mutability, uniqueness string) schemaAttribute {
	return schemaAttribute{Name: name, Type: typ, Required: required, Mutability: mutability, Returned: "default", Uniqueness: uniqueness}
}

type scimDefinition struct {
	Schemas     []string          `json:"schemas,omitempty"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Endpoint    string            `json:"endpoint,omitempty"`
	Schema      string            `json:"schema,omitempty"`
	Attributes  []schemaAttribute `json:"attributes,omitempty"`
}

var schemaDefinitions = []scimDefinition{
	{ID: schemaUser, Name: "User", Description: "A person provisioned from the identity provider. Supported: userName, externalId, displayName, emails (one stored), active, name (used only to default displayName); groups is read-only.", Attributes: []schemaAttribute{
		attribute("userName", "string", true, "readWrite", "server"),
		attribute("externalId", "string", false, "readWrite", "none"),
		attribute("displayName", "string", false, "readWrite", "none"),
		{Name: "emails", Type: "complex", MultiValued: true, Mutability: "readWrite", Returned: "default", Uniqueness: "none"},
		attribute("active", "boolean", false, "readWrite", "none"),
		{Name: "groups", Type: "complex", MultiValued: true, Mutability: "readOnly", Returned: "default", Uniqueness: "none"},
	}},
	{ID: schemaGroup, Name: "Group", Description: "A provisioning group. Groups are records only and grant no roles.", Attributes: []schemaAttribute{
		attribute("displayName", "string", true, "readWrite", "server"),
		attribute("externalId", "string", false, "readWrite", "none"),
		{Name: "members", Type: "complex", MultiValued: true, Mutability: "readWrite", Returned: "default", Uniqueness: "none"},
	}},
}

var resourceTypes = []scimDefinition{
	{Schemas: []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, ID: "User", Name: "User", Description: "User account", Endpoint: "/Users", Schema: schemaUser},
	{Schemas: []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, ID: "Group", Name: "Group", Description: "Group", Endpoint: "/Groups", Schema: schemaGroup},
}

type supported struct {
	Supported bool `json:"supported"`
}

var serviceProviderConfig = struct {
	Schemas        []string          `json:"schemas"`
	Patch          supported         `json:"patch"`
	Bulk           map[string]any    `json:"bulk"`
	Filter         map[string]any    `json:"filter"`
	ChangePassword supported         `json:"changePassword"`
	Sort           supported         `json:"sort"`
	ETag           supported         `json:"etag"`
	AuthSchemes    []map[string]any  `json:"authenticationSchemes"`
	Meta           map[string]string `json:"meta"`
}{
	Schemas:        []string{schemaConfig},
	Patch:          supported{true},
	Bulk:           map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
	Filter:         map[string]any{"supported": true, "maxResults": scimMaxCount},
	ChangePassword: supported{false},
	Sort:           supported{false},
	ETag:           supported{false},
	AuthSchemes:    []map[string]any{{"type": "oauthbearertoken", "name": "OAuth Bearer Token", "description": "A tenant identity-provider access token carrying the tid claim", "primary": true}},
	Meta:           map[string]string{"resourceType": "ServiceProviderConfig", "location": scimBase + "/ServiceProviderConfig"},
}

func listResources(items []scimDefinition) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeSCIM(w, http.StatusOK, newList(len(items), 1, len(items), items))
	}
}

func getResource(items []scimDefinition) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		for _, item := range items {
			if item.ID == id {
				writeSCIM(w, http.StatusOK, item)
				return
			}
		}
		scimFail(w, r, scimNotFound("resource"))
	}
}
