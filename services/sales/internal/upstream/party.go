package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	partyAudience = "party"
	dutyPartyRead = "party.read"
)

// ErrCustomerUnknown is the party service's answer that the customer does not
// exist in the legal entity.
var ErrCustomerUnknown = errors.New("the customer is unknown to the party service")

// WithParty sets the party service base URL; without it CustomerGroup answers
// ErrNotConfigured.
func (c *Client) WithParty(partyURL string) *Client {
	c.partyURL = strings.TrimRight(partyURL, "/")
	return c
}

// CustomerGroup reads the customer group of a customer from the party service
// (GET /v1/party/customers/{id}, duty party.read). The group is the customer's
// own customerGroupId; callers must not take it from a request.
func (c *Client) CustomerGroup(ctx context.Context, legalEntityID, customerID string) (string, error) {
	if c == nil || c.partyURL == "" {
		return "", ErrNotConfigured
	}
	status, raw, err := c.do(ctx, http.MethodGet, c.partyURL+"/v1/party/customers/"+url.PathEscape(customerID), partyAudience, dutyPartyRead, legalEntityID, "", nil)
	if err != nil {
		return "", err
	}
	return decodeCustomerGroup(status, raw, legalEntityID)
}

func decodeCustomerGroup(status int, raw []byte, legalEntityID string) (string, error) {
	switch {
	case status == http.StatusNotFound:
		return "", ErrCustomerUnknown
	case status != http.StatusOK:
		return "", &StatusError{Audience: partyAudience, Status: status}
	}
	var body struct {
		LegalEntityID   string `json:"legalEntityId"`
		CustomerGroupID string `json:"customerGroupId"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("decode party response: %w", err)
	}
	if !ids.Valid(body.CustomerGroupID) {
		return "", errors.New("party response carries no customerGroupId")
	}
	if body.LegalEntityID != legalEntityID {
		return "", ErrCustomerUnknown
	}
	return body.CustomerGroupID, nil
}
