package partydata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/identity"
)

const (
	Timeout         = 2 * time.Second
	maxResponseSize = 1 << 20

	partyAudience = "party"
	orgAudience   = "org"
	dutyPartyRead = "party.read"
	dutyOrgRead   = "org.read"
)

type Client struct {
	partyURL string
	orgURL   string
	http     *http.Client
	signer   *tenant.ServiceSigner
}

func New(partyURL, orgURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{partyURL: strings.TrimRight(partyURL, "/"), orgURL: strings.TrimRight(orgURL, "/"), http: httpClient}
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.signer = s
	return c
}

func (c *Client) get(ctx context.Context, base, audience, duty, legalEntityID, path string, out any) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("self-billing parties: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return fmt.Errorf("get %s: %w", path, err)
	}
	if c.signer == nil {
		identity.Forward(req, principal)
	} else if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{duty}); err != nil {
		return fmt.Errorf("get %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: unexpected status %d", path, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("get %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("get %s: unreadable response", path)
	}
	return nil
}

type addressBody struct {
	CountryCode string   `json:"countryCode"`
	Formatted   string   `json:"formatted"`
	Purposes    []string `json:"purposes"`
}

type addressList struct {
	Items []addressBody `json:"items"`
}

func (c *Client) partyAddress(ctx context.Context, legalEntityID, partyID string) (addressBody, error) {
	var list addressList
	path := "/v1/org/addresses?ownerType=party&ownerId=" + url.QueryEscape(partyID) + "&limit=100"
	if err := c.get(ctx, c.orgURL, orgAudience, dutyOrgRead, legalEntityID, path, &list); err != nil {
		return addressBody{}, err
	}
	if len(list.Items) == 0 {
		return addressBody{}, nil
	}
	for _, purpose := range []string{"invoice", "business"} {
		for _, item := range list.Items {
			if slices.Contains(item.Purposes, purpose) {
				return item, nil
			}
		}
	}
	return list.Items[0], nil
}

func (c *Client) SelfBillParties(ctx context.Context, legalEntityID, vendorID string) (domain.SelfBillParties, error) {
	var vendor struct {
		PartyID string `json:"partyId"`
	}
	if err := c.get(ctx, c.partyURL, partyAudience, dutyPartyRead, legalEntityID, "/v1/party/vendors/"+url.PathEscape(vendorID), &vendor); err != nil {
		return domain.SelfBillParties{}, err
	}
	var party struct {
		Name               string `json:"name"`
		LegalName          string `json:"legalName"`
		RegistrationNumber string `json:"registrationNumber"`
	}
	if err := c.get(ctx, c.partyURL, partyAudience, dutyPartyRead, legalEntityID, "/v1/party/parties/"+url.PathEscape(vendor.PartyID), &party); err != nil {
		return domain.SelfBillParties{}, err
	}
	supplierAddress, err := c.partyAddress(ctx, legalEntityID, vendor.PartyID)
	if err != nil {
		return domain.SelfBillParties{}, err
	}
	var entity struct {
		Name               string `json:"name"`
		Country            string `json:"country"`
		RegistrationNumber string `json:"registrationNumber"`
		PrimaryAddressID   string `json:"primaryAddressId"`
	}
	if err := c.get(ctx, c.orgURL, orgAudience, dutyOrgRead, legalEntityID, "/v1/org/legal-entities/"+url.PathEscape(legalEntityID), &entity); err != nil {
		return domain.SelfBillParties{}, err
	}
	var customerAddress addressBody
	if entity.PrimaryAddressID != "" {
		if err := c.get(ctx, c.orgURL, orgAudience, dutyOrgRead, legalEntityID, "/v1/org/addresses/"+url.PathEscape(entity.PrimaryAddressID), &customerAddress); err != nil {
			return domain.SelfBillParties{}, err
		}
	}
	supplierName := party.LegalName
	if supplierName == "" {
		supplierName = party.Name
	}
	out := domain.SelfBillParties{
		Supplier: domain.SelfBillParty{Name: supplierName, Address: supplierAddress.Formatted, Country: supplierAddress.CountryCode, RegistrationNumber: party.RegistrationNumber},
		Customer: domain.SelfBillParty{Name: entity.Name, Address: customerAddress.Formatted, Country: entity.Country, RegistrationNumber: entity.RegistrationNumber},
	}
	if out.Supplier.Address == "" || out.Supplier.Country == "" {
		return domain.SelfBillParties{}, errors.New("self-billing parties: the supplier has no address with a country in the org master")
	}
	if out.Supplier.Name == "" || out.Customer.Name == "" || out.Customer.Country == "" {
		return domain.SelfBillParties{}, errors.New("self-billing parties: the party or legal entity master has no name or country")
	}
	return out, nil
}
